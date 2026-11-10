//go:build darwin

package vfs

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"fileload/internal/backend"
	"fileload/internal/backend/memfs"
	"fileload/internal/backend/sftp"
	"fileload/internal/cache"
	"fileload/internal/model"
	"fileload/internal/mount"
)

// These tests really mount through the installed FUSE driver (FUSE-T on
// macOS). Opt in with FILELOAD_FUSE=1; the sftp variant also needs the
// FILELOAD_IT_* variables (see backend/sftp/client_test.go).

func ready(target string) func() bool {
	return func() bool {
		var a, b syscall.Stat_t
		if syscall.Stat(target, &a) != nil || syscall.Stat(filepath.Dir(target), &b) != nil {
			return false
		}
		return a.Dev != b.Dev
	}
}

// testOptions mirrors platform/darwin MountOptions; FILELOAD_FUSE_OPTS
// ("a,b") overrides it for experiments.
func testOptions() []string {
	list := []string{"volname=fltest", "noattrcache"}
	if v := os.Getenv("FILELOAD_FUSE_OPTS"); v != "" {
		list = strings.Split(v, ",")
	}
	var out []string
	for _, o := range list {
		out = append(out, "-o", o)
	}
	return out
}

func mountFor(t *testing.T, be backend.Backend, root string) (string, *cache.Store, mount.Handle) {
	t.Helper()
	if os.Getenv("FILELOAD_FUSE") == "" || runtime.GOOS != "darwin" {
		t.Skip("set FILELOAD_FUSE=1 on macOS with FUSE-T installed")
	}
	base := t.TempDir()
	target := filepath.Join(base, "mnt")
	_ = os.Mkdir(target, 0o755)
	c, err := cache.Open(be, root, filepath.Join(base, "cache"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var log *slog.Logger
	if os.Getenv("FILELOAD_DEBUG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	h, err := Mounter{}.Mount(ctx, mount.Request{
		Target: target, Root: root, Backend: be, Cache: c, Log: log,
		Options: testOptions(),
		Ready:   ready(target),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Unmount(); err != nil {
			t.Error(err)
		}
		c.Close()
		if ready(target)() {
			_ = exec.Command("umount", "-f", target).Run()
			t.Error("still mounted after Unmount")
		}
	})
	return target, c, h
}

func exercise(t *testing.T, mnt string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// read existing
	got, err := os.ReadFile(filepath.Join(mnt, "hello.txt"))
	must(err)
	if string(got) != "hello from remote" {
		t.Fatalf("read = %q", got)
	}
	// create + write + read back
	must(os.WriteFile(filepath.Join(mnt, "new.txt"), []byte("written locally"), 0o644))
	got, err = os.ReadFile(filepath.Join(mnt, "new.txt"))
	must(err)
	if string(got) != "written locally" {
		t.Fatalf("read back = %q", got)
	}
	// append (open existing for write)
	f, err := os.OpenFile(filepath.Join(mnt, "new.txt"), os.O_WRONLY|os.O_APPEND, 0)
	must(err)
	_, err = f.WriteString(" + more")
	must(err)
	must(f.Close())
	// mkdir + nested file + rename (editor "save via temp file")
	must(os.Mkdir(filepath.Join(mnt, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(mnt, "sub", ".tmp~"), []byte("v2"), 0o644))
	must(os.Rename(filepath.Join(mnt, "sub", ".tmp~"), filepath.Join(mnt, "sub", "doc.txt")))
	// truncate
	must(os.WriteFile(filepath.Join(mnt, "trunc.txt"), []byte("0123456789"), 0o644))
	must(os.Truncate(filepath.Join(mnt, "trunc.txt"), 4))
	// listing
	ents, err := os.ReadDir(mnt)
	must(err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	for _, want := range []string{"hello.txt", "new.txt", "sub", "trunc.txt"} {
		if !slices.Contains(names, want) {
			t.Fatalf("listing %v lacks %s", names, want)
		}
	}
	// macOS metadata files are accepted but stay local (never uploaded).
	// "._name" AppleDouble files are created by the kernel itself (for
	// xattrs); user processes cannot create that name directly on FUSE-T.
	must(os.WriteFile(filepath.Join(mnt, ".DS_Store"), []byte("finder"), 0o644))
	if got, err := os.ReadFile(filepath.Join(mnt, ".DS_Store")); err != nil || string(got) != "finder" {
		t.Fatalf("local-only read back = %q %v", got, err)
	}
	if out, err := exec.Command("xattr", "-w", "com.example.tag", "blue", filepath.Join(mnt, "new.txt")).CombinedOutput(); err != nil {
		// informational: depends on the macOS NFS client used by FUSE-T
		t.Logf("xattr -w: %v %s", err, bytes.TrimSpace(out))
	}
	// delete
	must(os.WriteFile(filepath.Join(mnt, "gone.txt"), []byte("x"), 0o644))
	must(os.Remove(filepath.Join(mnt, "gone.txt")))
	if _, err := os.Stat(filepath.Join(mnt, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still there: %v", err)
	}
	// a 4 MB file through the page cache
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<18)
	must(os.WriteFile(filepath.Join(mnt, "big.bin"), big, 0o644))
	got, err = os.ReadFile(filepath.Join(mnt, "big.bin"))
	must(err)
	if !bytes.Equal(got, big) {
		t.Fatalf("big file mismatch (%d bytes)", len(got))
	}
}

func waitRemote(t *testing.T, get func(string) (string, bool), p, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got, _ = get(p)
		if got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("remote %s = %q, want %q", p, got, want)
}

func TestFUSEWithMemoryBackend(t *testing.T) {
	be := memfs.New()
	be.Put("/r/hello.txt", "hello from remote")
	mnt, _, _ := mountFor(t, be, "/r")
	exercise(t, mnt)

	// uploads happen in the background after close
	waitRemote(t, be.Get, "/r/new.txt", "written locally + more")
	waitRemote(t, be.Get, "/r/sub/doc.txt", "v2")
	waitRemote(t, be.Get, "/r/trunc.txt", "0123")
	list, _ := be.ReadDir(t.Context(), "/r")
	for _, fi := range list {
		if strings.HasPrefix(fi.Name(), "._") || fi.Name() == ".DS_Store" {
			t.Errorf("macOS metadata file reached the backend: %s", fi.Name())
		}
	}
	if _, ok := be.Get("/r/sub/.tmp~"); ok {
		t.Fatal("temp file left remotely")
	}
	// remote change becomes visible locally
	be.Put("/r/hello.txt", "edited on server")
	time.Sleep(200 * time.Millisecond)
	got, _ := os.ReadFile(filepath.Join(mnt, "hello.txt"))
	if string(got) != "edited on server" {
		t.Fatalf("remote edit not visible: %q", got)
	}
}

func TestFUSEWithSFTP(t *testing.T) {
	host := os.Getenv("FILELOAD_IT_HOST")
	if host == "" {
		t.Skip("FILELOAD_IT_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("FILELOAD_IT_PORT"))
	prof := model.Server{Host: host, Port: max(port, 22), User: os.Getenv("FILELOAD_IT_USER"),
		KeyPath: os.Getenv("FILELOAD_IT_KEY"), Password: os.Getenv("FILELOAD_IT_PASSWORD")}
	ctx := t.Context()
	cl, err := sftp.Dial(ctx, prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	root := os.Getenv("FILELOAD_IT_DIR") + "/fuse-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_ = cl.Mkdir(ctx, os.Getenv("FILELOAD_IT_DIR"))
	if err := cl.Mkdir(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := cl.Upload(ctx, root+"/hello.txt", bytes.NewReader([]byte("hello from remote"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // after the unmount cleanup (LIFO)
		_ = exec.Command("ssh", "-i", prof.KeyPath, "-o", "BatchMode=yes", prof.User+"@"+prof.Host, "rm -rf "+root).Run()
	})
	get := func(p string) (string, bool) {
		var b bytes.Buffer
		if err := cl.Download(ctx, p, &b); err != nil {
			return "", false
		}
		return b.String(), true
	}

	mnt, _, _ := mountFor(t, cl, root)
	start := time.Now()
	exercise(t, mnt)
	t.Logf("file ops over sftp took %v", time.Since(start))
	waitRemote(t, get, root+"/new.txt", "written locally + more")
	waitRemote(t, get, root+"/sub/doc.txt", "v2")
	waitRemote(t, get, root+"/trunc.txt", "0123")
	list, err := cl.ReadDir(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range list {
		if strings.HasPrefix(fi.Name(), "._") || fi.Name() == ".DS_Store" {
			t.Errorf("macOS metadata file uploaded: %s", fi.Name())
		}
	}
}

// TestFUSEHold keeps a memory-backed mount open for manual poking:
// FILELOAD_FUSE=1 FILELOAD_FUSE_HOLD=60s go test -run Hold -v ./internal/vfs/
func TestFUSEHold(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("FILELOAD_FUSE_HOLD"))
	if err != nil {
		t.Skip("FILELOAD_FUSE_HOLD not set")
	}
	be := memfs.New()
	be.Put("/r/hello.txt", "hello from remote")
	mnt, _, _ := mountFor(t, be, "/r")
	t.Logf("MOUNT=%s", mnt)
	time.Sleep(d)
}
