// Package core orchestrates connections, mounts and commands. It depends
// only on interfaces (backend, mount, platform, Publisher) and never on
// Wails or an OS, so a CLI or a test can drive it directly.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fileload/internal/backend"
	"fileload/internal/model"
	"fileload/internal/mount"
	"fileload/internal/paths"
	"fileload/internal/platform"
	"fileload/internal/proxy"
	"fileload/internal/store"
)

// Conn is a leased remote connection.
type Conn interface {
	backend.Backend
	proxy.Upstream
	Release()
}

// Connector leases a connection for a profile (shared between users of the
// same profile, see sftp.Pool).
type Connector func(ctx context.Context, p model.Server) (Conn, error)

// Deps are the capabilities the service is assembled from.
type Deps struct {
	Store     *store.Store
	Connect   Connector
	Platform  platform.Platform
	Mounter   mount.Mounter
	Layout    paths.Layout
	Log       *slog.Logger
	Publisher Publisher
	ProxyPort int // first port tried for the command proxy
}

// Service is the application core. Safe for concurrent use.
type Service struct {
	d        Deps
	log      *slog.Logger
	activity *batcher

	installing atomic.Bool

	mu         sync.Mutex
	browse     Conn         // connection used by the wizard (may be shared with mounts)
	browseProf model.Server // its profile
	mounts     []*activeMount
	message    string // last mount message shown while idle (errors included)
}

func New(d Deps) *Service {
	d.Log = cmp.Or(d.Log, slog.Default())
	if d.Publisher == nil {
		d.Publisher = nopPublisher{}
	}
	d.ProxyPort = cmp.Or(d.ProxyPort, 12222)
	s := &Service{d: d, log: d.Log}
	s.activity = &batcher{every: 200 * time.Millisecond, flush: d.Publisher.Activity}
	return s
}

func (s *Service) changed() { s.d.Publisher.Changed() }

// ---- snapshot ----

func (s *Service) Snapshot() Snapshot {
	p := s.d.Platform
	snap := Snapshot{
		Platform:    p.Name(),
		TargetKind:  p.TargetKind(),
		Translucent: p.Translucent(),
		Servers:     []ServerItem{},
		Mounts:      []MountItem{},
		Commands:    []CommandItem{},
		Cmd:         CmdView{BinDir: p.Shims().BinDir()},
	}
	if err := p.CheckDeps(); err != nil {
		snap.DepsError = err.Error()
		snap.DepsInstallable = p.CanInstallDeps()
	}
	for i, sv := range s.d.Store.Servers() {
		snap.Servers = append(snap.Servers, ServerItem{Index: i, Name: sv.Name, Host: sv.Host})
	}
	for i, r := range s.d.Store.Mounts() {
		snap.Mounts = append(snap.Mounts, MountItem{Index: i, User: r.User, Host: r.Host, Port: r.Port, Root: r.Root, Target: r.Target})
	}
	for _, c := range s.d.Store.Commands() {
		snap.Commands = append(snap.Commands, CommandItem(c))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	snap.Connected = s.browse != nil
	if s.browse != nil {
		snap.Server = s.browseProf.Label()
	}
	snap.Mount = MountView{Phase: PhaseIdle, Message: s.message}
	if len(s.mounts) > 0 {
		m := s.mounts[0]
		snap.Mount, snap.Cmd = m.view()
		snap.Cmd.BinDir = p.Shims().BinDir()
	}
	return snap
}

// ---- servers ----

func (s *Service) LastServer() (model.Server, bool) { return s.d.Store.LastServer() }
func (s *Service) Server(i int) (model.Server, error) {
	return s.d.Store.Server(i)
}

func (s *Service) DeleteServer(i int) error {
	defer s.changed()
	return s.d.Store.DeleteServer(i)
}

func normalizeProfile(p model.Server) (model.Server, error) {
	p.Host, p.User, p.KeyPath = strings.TrimSpace(p.Host), strings.TrimSpace(p.User), strings.TrimSpace(p.KeyPath)
	p.Port = cmp.Or(p.Port, 22)
	if p.Host == "" || p.User == "" {
		return p, errors.New("请填写主机和用户名")
	}
	if p.Password == "" && p.KeyPath == "" {
		return p, errors.New("请填写密码或私钥路径")
	}
	p.Name = cmp.Or(strings.TrimSpace(p.Name), p.User+"@"+p.Host)
	return p, nil
}

// Connect opens the browse connection and saves the profile.
func (s *Service) Connect(ctx context.Context, p model.Server) error {
	p, err := normalizeProfile(p)
	if err != nil {
		return err
	}
	c, err := s.d.Connect(ctx, p)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	s.setBrowse(c, p)
	err = errors.Join(s.d.Store.UpsertServer(p), s.d.Store.SetLastServer(p))
	s.changed()
	if err != nil {
		return fmt.Errorf("已连接，但配置保存失败: %w", err)
	}
	return nil
}

func (s *Service) setBrowse(c Conn, p model.Server) {
	s.mu.Lock()
	old := s.browse
	s.browse, s.browseProf = c, p
	s.mu.Unlock()
	if old != nil {
		old.Release()
	}
}

func (s *Service) browseConn() (Conn, model.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browse, s.browseProf
}

// ListDir lists a remote directory, folders first.
func (s *Service) ListDir(ctx context.Context, dir string) (DirListing, error) {
	c, _ := s.browseConn()
	if c == nil {
		return DirListing{}, errors.New("请先连接服务器")
	}
	p := cleanRemote(dir)
	infos, err := c.ReadDir(ctx, p)
	if err != nil {
		return DirListing{Path: p}, fmt.Errorf("无法读取 %s: %w", p, err)
	}
	out := DirListing{Path: p, Entries: make([]Entry, 0, len(infos))}
	for _, fi := range infos {
		if n := fi.Name(); n != "." && n != ".." {
			out.Entries = append(out.Entries, Entry{Name: n, Path: path.Join(p, n), IsDir: fi.IsDir()})
		}
	}
	slices.SortFunc(out.Entries, func(a, b Entry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out, nil
}

func cleanRemote(p string) string {
	return path.Clean("/" + strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"))
}

// Targets lists mount points to offer for remote dir root.
func (s *Service) Targets(root string) []platform.Target { return s.d.Platform.Targets(root) }

// ---- commands ----

func (s *Service) SaveCommand(oldName string, c model.Command) error {
	if err := s.d.Store.SaveCommand(oldName, c); err != nil {
		return err
	}
	return s.reinstallShims()
}

func (s *Service) DeleteCommand(name string) error {
	if err := s.d.Store.DeleteCommand(name); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return s.reinstallShims()
}

// reinstallShims rewrites the wrappers of live mounts so command edits work
// in new terminals right away.
func (s *Service) reinstallShims() error {
	defer s.changed()
	s.mu.Lock()
	ms := slices.Clone(s.mounts)
	s.mu.Unlock()
	var errs []error
	for _, m := range ms {
		if addr := m.proxyAddr(); addr != "" {
			errs = append(errs, s.installShims(m.rec, addr))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("命令注入失败: %w", err)
	}
	return nil
}

// remoteLine is what runs on the server after the wrapper's cd.
func remoteLine(c model.Command) string {
	if strings.TrimSpace(c.Command) == "" {
		return "exec bash -l"
	}
	return "exec bash -lc " + platform.BashQuote(c.Command)
}

func (s *Service) installShims(rec model.MountRecord, proxyAddr string) error {
	var specs []platform.ShimSpec
	for _, c := range s.d.Store.Commands() {
		specs = append(specs, platform.ShimSpec{
			Name: c.Name, RemoteLine: remoteLine(c), Target: rec.Target,
			Root: rec.Root, ProxyAddr: proxyAddr, User: rec.User,
		})
	}
	return s.d.Platform.Shims().Install(specs)
}

// InstallDeps installs the bundled file-system driver (macOS: FUSE-T; the
// OS shows its own password prompt). Concurrent calls are rejected.
func (s *Service) InstallDeps(ctx context.Context) error {
	if !s.installing.CompareAndSwap(false, true) {
		return errors.New("正在安装，请稍候")
	}
	defer s.installing.Store(false)
	defer s.changed()
	if s.d.Platform.CheckDeps() == nil {
		return nil // already there
	}
	s.log.Info("installing file-system driver")
	if err := s.d.Platform.InstallDeps(ctx); err != nil {
		s.log.Warn("driver install", "err", err)
		return err
	}
	s.log.Info("file-system driver installed")
	return nil
}

// ---- settings ----

func (s *Service) UI() model.UISettings { return s.d.Store.UI() }
func (s *Service) SetCloseAction(a model.CloseAction) error {
	if !a.Valid() {
		return fmt.Errorf("unknown close action %q", a)
	}
	return s.d.Store.SetUI(model.UISettings{CloseAction: a})
}

// ---- shutdown ----

// Shutdown unmounts everything (uploading pending changes first) and
// drops the browse connection. Safe to call more than once.
func (s *Service) Shutdown() {
	s.mu.Lock()
	ms := slices.Clone(s.mounts)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, m := range ms {
		wg.Go(func() { s.teardown(m, "") })
	}
	wg.Wait()
	s.setBrowse(nil, model.Server{})
	s.activity.fire()
}
