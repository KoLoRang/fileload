package sftp

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"fileload/internal/model"
)

// Integration test against a real server. Opt in with:
//
//	FILELOAD_IT_HOST=1.2.3.4 FILELOAD_IT_USER=me FILELOAD_IT_KEY=~/.ssh/id \
//	FILELOAD_IT_DIR=/home/me/fileload-test go test ./internal/backend/sftp/
//
// Everything happens below FILELOAD_IT_DIR/<random>, removed afterwards.
func itServer(t *testing.T) (model.Server, string) {
	host := os.Getenv("FILELOAD_IT_HOST")
	if host == "" {
		t.Skip("FILELOAD_IT_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("FILELOAD_IT_PORT"))
	return model.Server{
		Host: host, Port: max(port, 22), User: os.Getenv("FILELOAD_IT_USER"),
		KeyPath: os.Getenv("FILELOAD_IT_KEY"), Password: os.Getenv("FILELOAD_IT_PASSWORD"),
	}, os.Getenv("FILELOAD_IT_DIR")
}

func TestIntegrationRoundTrip(t *testing.T) {
	prof, base := itServer(t)
	ctx := t.Context()
	pool := NewPool(nil)
	defer pool.Close()
	c, err := pool.Acquire(ctx, prof)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()

	dir := path.Join(base, "it-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := c.Mkdir(ctx, base); err != nil && !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		list, _ := c.ReadDir(ctx, dir)
		for _, fi := range list {
			_ = c.Remove(ctx, path.Join(dir, fi.Name()))
		}
		_ = c.RemoveDir(ctx, dir)
	})

	if err := c.Mkdir(ctx, dir); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("mkdir existing = %v, want ErrExist", err)
	}
	if _, err := c.Stat(ctx, path.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat missing = %v, want ErrNotExist", err)
	}

	payload := bytes.Repeat([]byte("fileload-"), 200_000) // ~1.8 MB, exercises concurrent IO
	f := path.Join(dir, "a.bin")
	if err := c.Upload(ctx, f, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := c.Download(ctx, f, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("round trip mismatch: %d vs %d bytes", got.Len(), len(payload))
	}
	if err := c.Rename(ctx, f, path.Join(dir, "b.bin")); err != nil {
		t.Fatal(err)
	}
	list, err := c.ReadDir(ctx, dir)
	if err != nil || len(list) != 1 || list[0].Name() != "b.bin" {
		t.Fatalf("readdir = %v %v", list, err)
	}

	// a dropped connection is healed transparently
	c.invalidate(c.cur.Load())
	if _, err := c.Stat(ctx, dir); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}

	// the same profile shares one connection
	c2, err := pool.Acquire(ctx, prof)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Client != c.Client {
		t.Fatal("pool did not share the client")
	}
	c2.Release()
	if !strings.HasPrefix(c.Config().Label(), prof.User+"@") {
		t.Fatal(c.Config().Label())
	}
}
