// Package memfs is an in-memory backend.Backend for tests.
package memfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"fileload/internal/backend"
)

type node struct {
	dir   bool
	data  []byte
	mode  fs.FileMode
	mtime time.Time
}

// FS is a remote tree held in memory. Set Offline to simulate a dropped
// connection; Uploads counts Upload calls per path.
type FS struct {
	mu      sync.Mutex
	nodes   map[string]*node
	clock   time.Time
	Offline bool
	Uploads map[string]int
}

var _ backend.Backend = (*FS)(nil)

func New() *FS {
	return &FS{
		nodes:   map[string]*node{"/": {dir: true, mode: 0o755}},
		clock:   time.Now().Truncate(time.Second),
		Uploads: map[string]int{},
	}
}

// tick returns a strictly increasing mtime (whole seconds, like SFTP).
func (f *FS) tick() time.Time { f.clock = f.clock.Add(time.Second); return f.clock }

func (f *FS) check(p string) (string, error) {
	if f.Offline {
		return "", fmt.Errorf("network down: %w", backend.ErrUnavailable)
	}
	return path.Clean(p), nil
}

// Put writes a file directly (test setup), creating parents.
func (f *FS) Put(p, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	for d := path.Dir(p); d != "/"; d = path.Dir(d) {
		if _, ok := f.nodes[d]; !ok {
			f.nodes[d] = &node{dir: true, mode: 0o755, mtime: f.tick()}
		}
	}
	f.nodes[p] = &node{data: []byte(data), mode: 0o644, mtime: f.tick()}
}

// Get returns a file's content (test assertions).
func (f *FS) Get(p string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[path.Clean(p)]
	if !ok || n.dir {
		return "", false
	}
	return string(n.data), true
}

type info struct {
	name string
	n    node
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return int64(len(i.n.data)) }
func (i info) ModTime() time.Time { return i.n.mtime }
func (i info) IsDir() bool        { return i.n.dir }
func (i info) Sys() any           { return nil }
func (i info) Mode() fs.FileMode {
	if i.n.dir {
		return fs.ModeDir | i.n.mode
	}
	return i.n.mode
}

func notExist(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

func (f *FS) Stat(_ context.Context, p string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return nil, err
	}
	n, ok := f.nodes[p]
	if !ok {
		return nil, notExist("stat", p)
	}
	return info{path.Base(p), *n}, nil
}

func (f *FS) ReadDir(_ context.Context, p string) ([]fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return nil, err
	}
	if n, ok := f.nodes[p]; !ok || !n.dir {
		return nil, notExist("readdir", p)
	}
	var out []fs.FileInfo
	for k, n := range f.nodes {
		if k != p && path.Dir(k) == p {
			out = append(out, info{path.Base(k), *n})
		}
	}
	slices.SortFunc(out, func(a, b fs.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}

func (f *FS) Mkdir(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return err
	}
	if _, ok := f.nodes[p]; ok {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	f.nodes[p] = &node{dir: true, mode: 0o755, mtime: f.tick()}
	return nil
}

func (f *FS) remove(p string, dir bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return err
	}
	n, ok := f.nodes[p]
	if !ok || n.dir != dir {
		return notExist("remove", p)
	}
	delete(f.nodes, p)
	return nil
}

func (f *FS) Remove(_ context.Context, p string) error    { return f.remove(p, false) }
func (f *FS) RemoveDir(_ context.Context, p string) error { return f.remove(p, true) }

func (f *FS) Rename(_ context.Context, o, n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, err := f.check(o)
	if err != nil {
		return err
	}
	n = path.Clean(n)
	if _, ok := f.nodes[o]; !ok {
		return notExist("rename", o)
	}
	for k, v := range f.nodes {
		if k == o || strings.HasPrefix(k, o+"/") {
			delete(f.nodes, k)
			f.nodes[n+strings.TrimPrefix(k, o)] = v
		}
	}
	return nil
}

func (f *FS) withFile(p string, fn func(n *node)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return err
	}
	n, ok := f.nodes[p]
	if !ok {
		return notExist("open", p)
	}
	fn(n)
	return nil
}

func (f *FS) Truncate(_ context.Context, p string, size int64) error {
	return f.withFile(p, func(n *node) {
		n.data = append(n.data[:0:0], n.data[:min(int64(len(n.data)), size)]...)
		n.mtime = f.tick()
	})
}

func (f *FS) Chmod(_ context.Context, p string, m fs.FileMode) error {
	return f.withFile(p, func(n *node) { n.mode = m })
}

func (f *FS) Chtimes(_ context.Context, p string, _, mt time.Time) error {
	return f.withFile(p, func(n *node) { n.mtime = mt })
}

func (f *FS) CreateEmpty(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.check(p)
	if err != nil {
		return err
	}
	f.nodes[p] = &node{mode: 0o644, mtime: f.tick()}
	return nil
}

func (f *FS) Download(_ context.Context, p string, w io.Writer) error {
	var data []byte
	if err := f.withFile(p, func(n *node) { data = bytes.Clone(n.data) }); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func (f *FS) Upload(_ context.Context, p string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, err = f.check(p); err != nil {
		return err
	}
	f.Uploads[p]++
	f.nodes[p] = &node{data: data, mode: 0o644, mtime: f.tick()}
	return nil
}
