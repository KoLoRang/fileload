package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"

	"fileload/internal/cache"
	"fileload/internal/model"
	"fileload/internal/mount"
	"fileload/internal/proxy"
)

// activeMount is one mount and everything it owns. Teardown order is fixed
// (see Service.teardown) so nothing outlives it.
type activeMount struct {
	rec   model.MountRecord
	conn  Conn
	cache *cache.Store

	ctx     context.Context // cancelled when teardown starts (aborts a pending mount)
	cancel  context.CancelFunc
	once    sync.Once
	settled chan struct{} // closed when the driver call returned (handle known)
	done    chan struct{} // closed when teardown finished

	mu      sync.Mutex
	phase   Phase
	message string
	handle  mount.Handle
	proxy   *proxy.Server
	cmd     CmdPhase
	cmdErr  string
}

func (m *activeMount) set(fn func(m *activeMount)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

func (m *activeMount) view() (MountView, CmdView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mv := MountView{
		Phase: m.phase, Target: m.rec.Target, Root: m.rec.Root,
		Server: m.rec.Label(), CacheDir: m.cache.Dir(), Message: m.message,
	}
	cv := CmdView{Phase: m.cmd, Error: m.cmdErr, Target: m.rec.Target, Root: m.rec.Root}
	if m.proxy != nil {
		cv.ProxyAddr = m.proxy.Addr()
	}
	return mv, cv
}

func (m *activeMount) proxyAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proxy == nil || m.cmd != CmdReady {
		return ""
	}
	return m.proxy.Addr()
}

// ---- public API ----

// Mount mounts root of the browse connection on target. It returns once the
// request is validated; mounting continues in the background and progress
// is published through Changed.
func (s *Service) Mount(ctx context.Context, target, root string) error {
	c, prof := s.browseConn()
	if c == nil {
		return errors.New("请先连接服务器")
	}
	// the mount holds its own lease so the wizard can switch servers freely
	lease, err := s.d.Connect(ctx, prof)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	rec := model.MountRecord{Server: prof, Target: target, Root: cleanRemote(root)}
	if err := s.start(ctx, lease, rec); err != nil {
		lease.Release()
		return err
	}
	return nil
}

// Remount restores saved record i, connecting silently.
func (s *Service) Remount(ctx context.Context, i int) error {
	rec, err := s.d.Store.Mount(i)
	if err != nil {
		return err
	}
	lease, err := s.d.Connect(ctx, rec.Server)
	if err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	if err := s.start(ctx, lease, rec); err != nil {
		lease.Release()
		return err
	}
	// like v1: the restored server becomes the wizard's connection too
	if b, err := s.d.Connect(ctx, rec.Server); err == nil {
		s.setBrowse(b, rec.Server)
		s.changed()
	}
	return nil
}

func (s *Service) DeleteMount(i int) error {
	defer s.changed()
	return s.d.Store.DeleteMount(i)
}

// Unmount unmounts every active mount (M0 has at most one), uploading
// pending changes first. It blocks until done.
func (s *Service) Unmount() error {
	s.mu.Lock()
	ms := slices.Clone(s.mounts)
	s.mu.Unlock()
	if len(ms) == 0 {
		return nil
	}
	for _, m := range ms {
		s.teardown(m, "已卸载，挂载命令已移除")
	}
	return nil
}

// Reveal opens the active mount in Explorer/Finder.
func (s *Service) Reveal() error {
	s.mu.Lock()
	var target string
	if len(s.mounts) > 0 {
		target = s.mounts[0].rec.Target
	}
	s.mu.Unlock()
	if target == "" {
		return errors.New("当前没有挂载")
	}
	return s.d.Platform.Reveal(target)
}

// Mounted reports whether a mount is active and ready.
func (s *Service) Mounted() (target string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.mounts {
		m.mu.Lock()
		ready := m.phase == PhaseMounted
		m.mu.Unlock()
		if ready {
			return m.rec.Target, true
		}
	}
	return "", false
}

// ---- lifecycle ----

func (s *Service) start(ctx context.Context, conn Conn, rec model.MountRecord) error {
	if err := s.d.Platform.CheckDeps(); err != nil {
		return err
	}
	s.mu.Lock()
	busy := len(s.mounts) > 0
	s.mu.Unlock()
	if busy {
		return errors.New("当前已有挂载，请先卸载后再挂载")
	}
	target, err := s.d.Platform.PrepareTarget(rec.Target)
	if err != nil {
		return err
	}
	rec.Target = target
	if fi, err := conn.Stat(ctx, rec.Root); err != nil || !fi.IsDir() {
		s.d.Platform.ReleaseTarget(target)
		return fmt.Errorf("远程目录不可用: %s", rec.Root)
	}
	store, err := cache.Open(conn, rec.Root, s.cacheDir(rec), s.log.With("mount", rec.Target))
	if err != nil {
		s.d.Platform.ReleaseTarget(target)
		return err
	}
	store.OnStatus = s.activity.add

	mctx, cancel := context.WithCancel(context.Background())
	m := &activeMount{
		rec: rec, conn: conn, cache: store, ctx: mctx, cancel: cancel,
		settled: make(chan struct{}), done: make(chan struct{}), phase: PhaseMounting,
		message: fmt.Sprintf("正在挂载 %s → %s ...", rec.Root, rec.Target),
	}
	s.mu.Lock()
	if len(s.mounts) > 0 { // lost a race with another Mount call
		s.mu.Unlock()
		cancel()
		store.Close()
		s.d.Platform.ReleaseTarget(target)
		return errors.New("当前已有挂载，请先卸载后再挂载")
	}
	s.mounts = append(s.mounts, m)
	s.message = ""
	s.mu.Unlock()
	s.changed()

	go s.run(m)
	return nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (s *Service) cacheDir(r model.MountRecord) string {
	name := unsafeChars.ReplaceAllString(r.Host, "_") + "_" + strconv.Itoa(r.Port) + "_" +
		unsafeChars.ReplaceAllString(r.User, "_") + "_" + unsafeChars.ReplaceAllString(r.Root, "_")
	return filepath.Join(s.d.Layout.Cache, name)
}

func volName(r model.MountRecord) string {
	return "fileload " + filepath.Base(r.Root)
}

// run drives one mount from "mounting" to "mounted" and starts the
// commands; failures roll everything back.
func (s *Service) run(m *activeMount) {
	rec, p := m.rec, s.d.Platform
	h, err := s.d.Mounter.Mount(m.ctx, mount.Request{
		Target:  rec.Target,
		Options: p.MountOptions(rec.Target, volName(rec)),
		Root:    rec.Root,
		Backend: m.conn,
		Cache:   m.cache,
		Ready:   func() bool { return p.Ready(rec.Target) },
		Log:     s.log.With("mount", rec.Target),
	})
	if err == nil {
		m.set(func(m *activeMount) { m.handle = h })
	}
	close(m.settled) // teardown may now read the handle
	if err != nil {
		if m.ctx.Err() == nil {
			s.log.Error("mount failed", "target", rec.Target, "err", err)
			s.teardown(m, err.Error())
		}
		return
	}
	if m.ctx.Err() != nil {
		return // unmounted while mounting: teardown unmounts the handle
	}
	go s.watch(m, h)

	if err := s.d.Store.UpsertMount(rec); err != nil {
		s.log.Warn("save mount record", "err", err)
	}
	m.set(func(m *activeMount) { m.phase, m.message, m.cmd = PhaseMounted, "", CmdStarting })
	s.changed()

	px, err := proxy.Start(m.conn, s.d.ProxyPort, s.log)
	if err == nil {
		// teardown cancels before it takes the proxy under m.mu, so either
		// it sees the proxy or we see the cancellation
		m.mu.Lock()
		gone := m.ctx.Err() != nil
		if !gone {
			m.proxy = px
		}
		m.mu.Unlock()
		if gone {
			_ = px.Close()
			return
		}
		err = s.installShims(rec, px.Addr())
		if m.ctx.Err() != nil {
			_ = s.d.Platform.Shims().Remove() // raced with teardown
			return
		}
	}
	m.set(func(m *activeMount) {
		if err != nil {
			m.cmd, m.cmdErr = CmdError, "命令启用失败: "+err.Error()
		} else {
			m.cmd = CmdReady
		}
	})
	s.changed()
}

// watch tears the mount down when the file system goes away on its own
// (e.g. ejected in Finder).
func (s *Service) watch(m *activeMount, h mount.Handle) {
	select {
	case <-h.Done():
		if m.ctx.Err() == nil {
			s.log.Warn("file system went away", "target", m.rec.Target)
			s.teardown(m, "挂载已被系统卸载："+m.rec.Target)
		}
	case <-m.ctx.Done():
	}
}

// teardown releases everything m owns exactly once, in dependency order:
// stop new command sessions → remove wrappers → unmount (no new file ops)
// → upload pending changes → release the connection → remove the target.
// Concurrent callers wait for the first one to finish.
func (s *Service) teardown(m *activeMount, msg string) {
	m.once.Do(func() {
		defer close(m.done)
		m.cancel()
		m.set(func(m *activeMount) {
			m.phase = PhaseUnmounting
			m.message = "卸载中，正在上传未同步的文件..."
		})
		s.changed()
		<-m.settled // the driver call returned: the handle (if any) is known

		m.mu.Lock()
		px, h := m.proxy, m.handle
		m.proxy = nil
		m.mu.Unlock()
		if px != nil {
			_ = px.Close()
		}
		if err := s.d.Platform.Shims().Remove(); err != nil {
			s.log.Warn("remove command wrappers", "err", err)
		}
		if h != nil {
			if err := h.Unmount(); err != nil {
				s.log.Warn("unmount", "err", err)
			}
		}
		m.cache.Close()
		m.conn.Release()
		s.d.Platform.ReleaseTarget(m.rec.Target)

		s.mu.Lock()
		s.mounts = slices.DeleteFunc(s.mounts, func(x *activeMount) bool { return x == m })
		s.message = msg
		s.mu.Unlock()
		s.changed()
	})
	<-m.done
}
