// Package logx sets up structured logging to a size-rotated file.
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const maxSize = 5 << 20 // rotate at 5 MB, keep one previous file

// Open returns a logger writing to dir/fileload.log (and stderr when
// FILELOAD_DEBUG is set) plus a closer.
func Open(dir string) (*slog.Logger, func() error) {
	level := slog.LevelInfo
	var out io.Writer = io.Discard
	closer := func() error { return nil }
	if err := os.MkdirAll(dir, 0o700); err == nil {
		w := &rotating{path: filepath.Join(dir, "fileload.log")}
		out, closer = w, w.Close
	}
	if os.Getenv("FILELOAD_DEBUG") != "" {
		level = slog.LevelDebug
		out = io.MultiWriter(out, os.Stderr)
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})), closer
}

type rotating struct {
	path string
	mu   sync.Mutex
	f    *os.File
	size int64
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil || r.size+int64(len(p)) > maxSize {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotating) rotateLocked() error {
	if r.f != nil {
		_ = r.f.Close()
		_ = os.Rename(r.path, r.path+".1")
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f, r.size = f, 0
	if st != nil {
		r.size = st.Size()
	}
	return nil
}

func (r *rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
