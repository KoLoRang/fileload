package core

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"fileload/internal/backend/memfs"
	"fileload/internal/model"
	"fileload/internal/mount"
	"fileload/internal/paths"
	"fileload/internal/platform"
	"fileload/internal/store"
)

// ---- fakes ----

type fakeConn struct {
	*memfs.FS
	released atomic.Int32
}

func (c *fakeConn) Release() { c.released.Add(1) }
func (c *fakeConn) OpenSession(context.Context) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, errors.New("no ssh in tests")
}

type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) add(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }
func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

type fakeShims struct{ r *recorder }

func (f fakeShims) BinDir() string { return "/bin" }
func (f fakeShims) Install(specs []platform.ShimSpec) error {
	f.r.add("shims.install")
	return nil
}
func (f fakeShims) Remove() error { f.r.add("shims.remove"); return nil }

type fakePlatform struct{ r *recorder }

func (fakePlatform) Name() string                           { return "test" }
func (fakePlatform) TargetKind() platform.TargetKind        { return platform.TargetDir }
func (fakePlatform) CheckDeps() error                       { return nil }
func (fakePlatform) CanInstallDeps() bool                   { return false }
func (fakePlatform) InstallDeps(context.Context) error      { return nil }
func (fakePlatform) Targets(string) []platform.Target       { return nil }
func (fakePlatform) PrepareTarget(t string) (string, error) { return t, nil }
func (p fakePlatform) ReleaseTarget(string)                 { p.r.add("target.release") }
func (fakePlatform) MountOptions(string, string) []string   { return nil }
func (fakePlatform) Ready(string) bool                      { return true }
func (fakePlatform) Reveal(string) error                    { return nil }
func (fakePlatform) RelPath(string, string) (string, bool)  { return "", false }
func (fakePlatform) Translucent() bool                      { return false }
func (p fakePlatform) Shims() platform.ShimInstaller        { return fakeShims{p.r} }

type fakeHandle struct {
	r    *recorder
	done chan struct{}
	once sync.Once
}

func (h *fakeHandle) Unmount() error {
	h.once.Do(func() { h.r.add("unmount"); close(h.done) })
	return nil
}
func (h *fakeHandle) Done() <-chan struct{} { return h.done }

type fakeMounter struct {
	r    *recorder
	fail error
	last chan *fakeHandle
	req  chan mount.Request
}

func (m *fakeMounter) Mount(ctx context.Context, req mount.Request) (mount.Handle, error) {
	m.req <- req
	if m.fail != nil {
		return nil, m.fail
	}
	h := &fakeHandle{r: m.r, done: make(chan struct{})}
	m.last <- h
	return h, nil
}

type nopPub struct{ changed atomic.Int32 }

func (p *nopPub) Changed()          { p.changed.Add(1) }
func (p *nopPub) Activity([]string) {}

type env struct {
	svc  *Service
	fs   *memfs.FS
	conn *fakeConn
	mnt  *fakeMounter
	rec  *recorder
	st   *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	rec := &recorder{}
	fs := memfs.New()
	fs.Put("/srv/proj/readme", "hi")
	conn := &fakeConn{FS: fs}
	mnt := &fakeMounter{r: rec, last: make(chan *fakeHandle, 4), req: make(chan mount.Request, 4)}
	dir := t.TempDir()
	st := store.Open(dir)
	svc := New(Deps{
		Store:     st,
		Connect:   func(context.Context, model.Server) (Conn, error) { return conn, nil },
		Platform:  fakePlatform{rec},
		Mounter:   mnt,
		Layout:    paths.Under(dir),
		Publisher: &nopPub{},
		ProxyPort: 24000,
	})
	return &env{svc: svc, fs: fs, conn: conn, mnt: mnt, rec: rec, st: st}
}

var prof = model.Server{Host: "h", Port: 22, User: "u", Password: "p"}

func waitPhase(t *testing.T, s *Service, want Phase) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := s.Snapshot()
		if snap.Mount.Phase == want && (want != PhaseMounted || snap.Cmd.Phase != CmdStarting) {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("phase never became %v (now %+v)", want, s.Snapshot().Mount)
	return Snapshot{}
}

// ---- tests ----

func TestMountLifecycleAndTeardownOrder(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	if err := e.svc.Connect(ctx, prof); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Mount(ctx, "/tmp/x", "/srv/proj"); err != nil {
		t.Fatal(err)
	}
	req := <-e.mnt.req
	snap := waitPhase(t, e.svc, PhaseMounted)
	if snap.Cmd.Phase != CmdReady {
		t.Fatalf("commands = %+v", snap.Cmd)
	}
	if r := e.st.Mounts(); len(r) != 1 || r[0].Root != "/srv/proj" {
		t.Fatalf("record not saved: %+v", r)
	}

	// a write that is still dirty at unmount must be uploaded
	local := req.Cache.LocalPath("/srv/proj/new.txt")
	if _, err := req.Cache.Created("/srv/proj/new.txt"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(local, []byte("pending"), 0o600)

	if err := e.svc.Unmount(); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.fs.Get("/srv/proj/new.txt"); got != "pending" {
		t.Fatalf("pending change not uploaded: %q", got)
	}
	if got := e.svc.Snapshot().Mount.Phase; got != PhaseIdle {
		t.Fatalf("phase = %v", got)
	}
	want := []string{"shims.install", "shims.remove", "unmount", "target.release"}
	if got := e.rec.list(); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// browse lease + mount lease; only the mount lease is released
	if n := e.conn.released.Load(); n != 1 {
		t.Fatalf("released = %d", n)
	}
	e.svc.Shutdown()
	if n := e.conn.released.Load(); n != 2 {
		t.Fatalf("after shutdown released = %d", n)
	}
}

func TestMountFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.mnt.fail = errors.New("driver says no")
	_ = e.svc.Connect(t.Context(), prof)
	if err := e.svc.Mount(t.Context(), "/tmp/x", "/srv/proj"); err != nil {
		t.Fatal(err)
	}
	<-e.mnt.req
	snap := waitPhase(t, e.svc, PhaseIdle)
	if snap.Mount.Message != "driver says no" {
		t.Fatalf("message = %q", snap.Mount.Message)
	}
	if len(e.st.Mounts()) != 0 {
		t.Fatal("failed mount was recorded")
	}
	if e.conn.released.Load() != 1 {
		t.Fatal("mount lease not released")
	}
}

func TestExternalUnmountTearsDown(t *testing.T) {
	e := newEnv(t)
	_ = e.svc.Connect(t.Context(), prof)
	_ = e.svc.Mount(t.Context(), "/tmp/x", "/srv/proj")
	<-e.mnt.req
	h := <-e.mnt.last
	waitPhase(t, e.svc, PhaseMounted)
	_ = h.Unmount() // e.g. ejected in Finder
	snap := waitPhase(t, e.svc, PhaseIdle)
	if snap.Mount.Message == "" {
		t.Fatal("no message after external unmount")
	}
}

func TestSecondMountRejectedAndBadRoot(t *testing.T) {
	e := newEnv(t)
	_ = e.svc.Connect(t.Context(), prof)
	if err := e.svc.Mount(t.Context(), "/tmp/x", "/nope"); err == nil {
		t.Fatal("missing remote dir accepted")
	}
	_ = e.svc.Mount(t.Context(), "/tmp/x", "/srv/proj")
	<-e.mnt.req
	if err := e.svc.Mount(t.Context(), "/tmp/y", "/srv/proj"); err == nil {
		t.Fatal("second mount accepted")
	}
	waitPhase(t, e.svc, PhaseMounted)
	e.svc.Shutdown()
}

func TestListDirSortsFoldersFirst(t *testing.T) {
	e := newEnv(t)
	e.fs.Put("/srv/b.txt", "")
	e.fs.Put("/srv/A/x", "")
	e.fs.Put("/srv/a.txt", "")
	_ = e.svc.Connect(t.Context(), prof)
	l, err := e.svc.ListDir(t.Context(), "/srv/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range l.Entries {
		names = append(names, x.Name)
	}
	if want := []string{"A", "proj", "a.txt", "b.txt"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestRemoteLine(t *testing.T) {
	if got := remoteLine(model.Command{}); got != "exec bash -l" {
		t.Fatal(got)
	}
	if got := remoteLine(model.Command{Command: "echo 'hi'"}); got != `exec bash -lc 'echo '\''hi'\'''` {
		t.Fatal(got)
	}
}
