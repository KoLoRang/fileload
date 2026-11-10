package cache

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fileload/internal/backend/memfs"
)

func open(t *testing.T, be *memfs.FS) *Store {
	t.Helper()
	s, err := Open(be, "/root", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnsureDownloadsAndReusesCurrentCopy(t *testing.T) {
	be := memfs.New()
	be.Put("/root/a.txt", "hello")
	s := open(t, be)
	defer s.Close()

	local, err := s.Ensure(t.Context(), "/root/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(local); string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	// make the local copy differ without touching size/mtime: a current
	// cache must not be downloaded again
	fi, _ := os.Stat(local)
	_ = os.WriteFile(local, []byte("HELLO"), 0o600)
	_ = os.Chtimes(local, fi.ModTime(), fi.ModTime())
	if _, err := s.Ensure(t.Context(), "/root/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(local); string(got) != "HELLO" {
		t.Fatalf("current cache was re-downloaded: %q", got)
	}

	be.Put("/root/a.txt", "changed remotely") // new size+mtime
	if _, err := s.Ensure(t.Context(), "/root/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(local); string(got) != "changed remotely" {
		t.Fatalf("stale cache served: %q", got)
	}
}

func TestEnsureOfflineServesCachedCopy(t *testing.T) {
	be := memfs.New()
	be.Put("/root/a.txt", "v1")
	s := open(t, be)
	defer s.Close()
	if _, err := s.Ensure(t.Context(), "/root/a.txt"); err != nil {
		t.Fatal(err)
	}
	be.Offline = true
	local, err := s.Ensure(t.Context(), "/root/a.txt")
	if err != nil {
		t.Fatalf("offline with cache: %v", err)
	}
	if got, _ := os.ReadFile(local); string(got) != "v1" {
		t.Fatalf("got %q", got)
	}
	if _, err := s.Ensure(t.Context(), "/root/missing.txt"); err == nil {
		t.Fatal("offline without cache must fail")
	}
}

func TestDirtyWinsAndUploadsOnClose(t *testing.T) {
	be := memfs.New()
	be.Put("/root/a.txt", "remote")
	s := open(t, be)

	local, _ := s.Ensure(t.Context(), "/root/a.txt")
	_ = os.WriteFile(local, []byte("local edit"), 0o600)
	if err := s.MarkDirty("/root/a.txt"); err != nil {
		t.Fatal(err)
	}
	be.Put("/root/a.txt", "remote changed meanwhile")
	if l, _ := s.Ensure(t.Context(), "/root/a.txt"); l != local {
		t.Fatal("dirty copy must be served")
	}
	if got, _ := os.ReadFile(local); string(got) != "local edit" {
		t.Fatalf("dirty copy overwritten: %q", got)
	}
	s.Close() // flushes synchronously
	if got, _ := be.Get("/root/a.txt"); got != "local edit" {
		t.Fatalf("remote = %q", got)
	}
	if _, err := os.Stat(local + dirtySuffix); !os.IsNotExist(err) {
		t.Fatal("dirty marker left behind")
	}
}

func TestFailedUploadIsResumedByNextOpen(t *testing.T) {
	be := memfs.New()
	be.Put("/root/a.txt", "remote")
	dir := t.TempDir()
	s, _ := Open(be, "/root", dir, nil)
	local, _ := s.Ensure(t.Context(), "/root/a.txt")
	_ = os.WriteFile(local, []byte("edit"), 0o600)
	_ = s.MarkDirty("/root/a.txt")
	be.Offline = true
	s.Close() // upload fails, marker must survive
	if _, err := os.Stat(local + dirtySuffix); err != nil {
		t.Fatal("marker lost after failed upload")
	}

	be.Offline = false
	s2, _ := Open(be, "/root", dir, nil) // like the next app start
	s2.Close()
	if got, _ := be.Get("/root/a.txt"); got != "edit" {
		t.Fatalf("not resumed: remote = %q", got)
	}
}

func TestRenameCarriesDirtyState(t *testing.T) {
	be := memfs.New()
	s := open(t, be)
	// editor pattern: write temp file, rename over the target
	local, err := s.Created("/root/.a.txt.tmp")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(local, []byte("saved"), 0o600)
	_ = be.CreateEmpty(t.Context(), "/root/.a.txt.tmp")
	_ = be.Rename(t.Context(), "/root/.a.txt.tmp", "/root/a.txt")
	moved := s.Renamed("/root/.a.txt.tmp", "/root/a.txt")
	if len(moved) != 1 || moved[0] != "/root/a.txt" {
		t.Fatalf("moved = %v", moved)
	}
	if !s.IsDirty("/root/a.txt") || s.IsDirty("/root/.a.txt.tmp") {
		t.Fatal("dirty state not moved")
	}
	s.Close()
	if got, _ := be.Get("/root/a.txt"); got != "saved" {
		t.Fatalf("remote = %q", got)
	}
	if _, ok := be.Get("/root/.a.txt.tmp"); ok {
		t.Fatal("temp file re-created remotely")
	}
}

func TestLocalOnlyListsUnuploadedFiles(t *testing.T) {
	be := memfs.New()
	be.Put("/root/sub/x", "1")
	s := open(t, be)
	defer s.Close()
	if _, err := s.Created("/root/sub/new.txt"); err != nil {
		t.Fatal(err)
	}
	got := s.LocalOnly("/root/sub")
	if _, ok := got["new.txt"]; !ok || len(got) != 1 {
		t.Fatalf("LocalOnly = %v", got)
	}
}

func TestConcurrentEnsureDownloadsOnce(t *testing.T) {
	be := &countingFS{FS: memfs.New()}
	be.Put("/root/big", string(make([]byte, 1<<20)))
	s, _ := Open(be, "/root", t.TempDir(), nil)
	defer s.Close()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := s.Ensure(context.Background(), "/root/big"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := be.downloads.Load(); n != 1 {
		t.Fatalf("downloads = %d, want 1", n)
	}
}

func TestLocalPathMapping(t *testing.T) {
	s := &Store{root: "/root", dir: filepath.FromSlash("/c")}
	if got := s.LocalPath("/root"); got != s.dir {
		t.Fatalf("root -> %q", got)
	}
	if got, want := s.LocalPath("/root/a/b.txt"), filepath.Join(s.dir, "a", "b.txt"); got != want {
		t.Fatalf("%q != %q", got, want)
	}
	if r, ok := s.remoteOf(filepath.Join(s.dir, "a", "b.txt")); !ok || r != "/root/a/b.txt" {
		t.Fatalf("remoteOf = %q %v", r, ok)
	}
}

// ---- queue ----

func TestQueueCoalescesAndNeverRunsKeyConcurrently(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	release := make(chan struct{})
	q := newQueue(4, func(ctx context.Context, key string) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		<-release
		running.Add(-1)
	})
	_ = q.schedule("a")
	time.Sleep(20 * time.Millisecond) // first run started
	for range 10 {
		_ = q.schedule("a") // while running: one re-run only
	}
	close(release)
	q.close()
	if maxRunning.Load() != 1 {
		t.Fatalf("same key ran concurrently (%d)", maxRunning.Load())
	}
	if runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2 (initial + one coalesced re-run)", runs.Load())
	}
	if err := q.schedule("a"); err != ErrClosed {
		t.Fatalf("schedule after close = %v", err)
	}
}

type countingFS struct {
	*memfs.FS
	downloads atomic.Int32
}

func (c *countingFS) Download(ctx context.Context, p string, w io.Writer) error {
	c.downloads.Add(1)
	time.Sleep(20 * time.Millisecond) // widen the race window
	return c.FS.Download(ctx, p, w)
}
