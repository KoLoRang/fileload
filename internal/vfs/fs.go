// Package vfs exposes a backend.Backend as a local file system through
// cgofuse (WinFsp on Windows, FUSE-T on macOS). All caching lives in
// internal/cache; this package only translates FUSE calls.
package vfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"fileload/internal/backend"
	"fileload/internal/cache"
)

// FS implements fuse.FileSystemInterface.
type FS struct {
	fuse.FileSystemBase

	ctx   context.Context
	be    backend.Backend
	cache *cache.Store
	root  string
	log   *slog.Logger
	uid   int // owner reported for every entry (-1 = leave to the driver)
	gid   int

	mu      sync.Mutex
	handles map[uint64]*handle
	next    uint64

	ready     chan struct{}
	markReady func()
}

type handle struct {
	f      *os.File
	remote string // updated on rename
	write  bool
	local  bool // local-only metadata file: never uploaded
}

// isLocalOnly reports macOS metadata files that are kept in the local cache
// and never sent to the server: AppleDouble "._name" companions (macOS
// stores xattrs, Finder tags and quarantine flags in them on NFS-backed
// mounts like FUSE-T) and Finder's .DS_Store.
func isLocalOnly(p string) bool {
	b := path.Base(p)
	return strings.HasPrefix(b, "._") || b == ".DS_Store"
}

func newFS(ctx context.Context, be backend.Backend, c *cache.Store, root string, log *slog.Logger) *FS {
	f := &FS{
		ctx: ctx, be: be, cache: c, root: path.Clean(root), log: log,
		uid: os.Getuid(), gid: os.Getgid(),
		handles: map[uint64]*handle{},
		ready:   make(chan struct{}),
	}
	f.markReady = sync.OnceFunc(func() { close(f.ready) })
	return f
}

// guard turns a panic in a FUSE callback into EIO (a panic on a cgo
// callback thread would kill the whole process) and logs it with a stack.
func (f *FS) guard(op, p string, errc *int) {
	if r := recover(); r != nil {
		f.log.Error("panic in fuse op", "op", op, "path", p, "panic", r, "stack", string(debug.Stack()))
		*errc = -fuse.EIO
	}
}

func (f *FS) remotePath(p string) string {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if p == "/" {
		return f.root
	}
	return path.Join(f.root, p)
}

// errno maps backend/cache errors to negative FUSE error codes.
func (f *FS) errno(op, p string, err error) int {
	if err != nil {
		f.log.Debug("fuse op error", "op", op, "path", p, "err", err)
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, fs.ErrNotExist):
		return -fuse.ENOENT
	case errors.Is(err, fs.ErrExist):
		return -fuse.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return -fuse.EACCES
	case cache.IsDirError(err):
		return -fuse.EISDIR
	}
	f.log.Warn("fuse op failed", "op", op, "path", p, "err", err)
	return -fuse.EIO
}

func (f *FS) handle(fh uint64) *handle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handles[fh]
}

func (f *FS) addHandle(h *handle) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.handles[f.next] = h
	return f.next
}

// ---- lifecycle ----

func (f *FS) Init() { f.markReady() }

// ---- metadata ----

func (f *FS) fillStat(st *fuse.Stat_t, fi fs.FileInfo) {
	perm := uint32(fi.Mode().Perm())
	if fi.IsDir() {
		st.Mode = fuse.S_IFDIR | perm | 0o700
		st.Nlink = 2
		st.Size = 4096
	} else {
		st.Mode = fuse.S_IFREG | perm | 0o600
		st.Nlink = 1
		st.Size = fi.Size()
	}
	if f.uid >= 0 {
		st.Uid, st.Gid = uint32(f.uid), uint32(f.gid)
	}
	t := fuse.NewTimespec(fi.ModTime())
	st.Mtim, st.Ctim, st.Birthtim, st.Atim = t, t, t, t
}

func (f *FS) Getattr(p string, st *fuse.Stat_t, fh uint64) (errc int) {
	defer f.guard("getattr", p, &errc)
	remote := f.remotePath(p)
	if isLocalOnly(remote) {
		li, err := os.Stat(f.cache.LocalPath(remote))
		if err != nil {
			return f.errno("getattr", p, err)
		}
		f.fillStat(st, li)
		return 0
	}
	if f.cache.IsDirty(remote) { // local edits not uploaded yet win
		if li, err := os.Stat(f.cache.LocalPath(remote)); err == nil && !li.IsDir() {
			f.fillStat(st, li)
			return 0
		}
	}
	fi, err := f.be.Stat(f.ctx, remote)
	if err != nil {
		return f.errno("getattr", p, err)
	}
	f.fillStat(st, fi)
	return 0
}

// Statfs reports a large virtual capacity so programs that check free
// space before writing see a healthy disk instead of 0 bytes.
func (f *FS) Statfs(p string, st *fuse.Statfs_t) int {
	const bsize = 4096
	const total, free = 1 << 40, 1 << 39
	st.Bsize, st.Frsize = bsize, bsize
	st.Blocks, st.Bfree, st.Bavail = total/bsize, free/bsize, free/bsize
	st.Files, st.Ffree, st.Favail = 1<<24, 1<<23, 1<<23
	st.Namemax = 255
	return 0
}

func (f *FS) Readdir(p string, fill func(name string, st *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) (errc int) {
	defer f.guard("readdir", p, &errc)
	remote := f.remotePath(p)
	infos, err := f.be.ReadDir(f.ctx, remote)
	if err != nil {
		return f.errno("readdir", p, err)
	}
	fill(".", nil, 0)
	fill("..", nil, 0)
	localOnly := f.cache.LocalOnly(remote) // created here, not uploaded yet
	for _, fi := range infos {
		name := fi.Name()
		if li, ok := localOnly[name]; ok {
			fi = li
			delete(localOnly, name)
		}
		var st fuse.Stat_t
		f.fillStat(&st, fi)
		if !fill(name, &st, 0) {
			return 0
		}
	}
	for name, li := range localOnly {
		var st fuse.Stat_t
		f.fillStat(&st, li)
		if !fill(name, &st, 0) {
			break
		}
	}
	return 0
}

func (f *FS) Chmod(p string, mode uint32) (errc int) {
	defer f.guard("chmod", p, &errc)
	if isLocalOnly(p) {
		return f.errno("chmod", p, os.Chmod(f.cache.LocalPath(f.remotePath(p)), fs.FileMode(mode).Perm()))
	}
	err := f.be.Chmod(f.ctx, f.remotePath(p), fs.FileMode(mode).Perm())
	if errors.Is(err, fs.ErrNotExist) && f.cache.IsDirty(f.remotePath(p)) {
		return 0 // not uploaded yet
	}
	return f.errno("chmod", p, err)
}

// Chown is accepted and ignored: remote ownership is the login user's.
func (f *FS) Chown(p string, uid, gid uint32) int { return 0 }

func (f *FS) Utimens(p string, tmsp []fuse.Timespec) (errc int) {
	defer f.guard("utimens", p, &errc)
	remote := f.remotePath(p)
	now := time.Now()
	atime, mtime := now, now
	if len(tmsp) == 2 {
		atime, mtime = tmsp[0].Time(), tmsp[1].Time()
	}
	if isLocalOnly(remote) {
		return f.errno("utimens", p, os.Chtimes(f.cache.LocalPath(remote), atime, mtime))
	}
	if f.cache.IsDirty(remote) {
		_ = os.Chtimes(f.cache.LocalPath(remote), atime, mtime)
		return 0 // the upload sets the remote mtime anyway
	}
	return f.errno("utimens", p, f.be.Chtimes(f.ctx, remote, atime, mtime))
}

// ---- namespace ----

func (f *FS) Mkdir(p string, mode uint32) (errc int) {
	defer f.guard("mkdir", p, &errc)
	remote := f.remotePath(p)
	if err := f.be.Mkdir(f.ctx, remote); err != nil {
		return f.errno("mkdir", p, err)
	}
	return 0
}

func (f *FS) Rmdir(p string) (errc int) {
	defer f.guard("rmdir", p, &errc)
	remote := f.remotePath(p)
	if remote == f.root {
		return -fuse.EPERM
	}
	if err := f.be.RemoveDir(f.ctx, remote); err != nil {
		return f.errno("rmdir", p, err)
	}
	f.cache.Removed(remote)
	return 0
}

func (f *FS) Unlink(p string) (errc int) {
	defer f.guard("unlink", p, &errc)
	remote := f.remotePath(p)
	if isLocalOnly(remote) {
		return f.errno("unlink", p, os.Remove(f.cache.LocalPath(remote)))
	}
	err := f.be.Remove(f.ctx, remote)
	if err != nil && !(errors.Is(err, fs.ErrNotExist) && f.cache.IsDirty(remote)) {
		return f.errno("unlink", p, err)
	}
	f.cache.Removed(remote)
	return 0
}

func (f *FS) Rename(oldPath, newPath string) (errc int) {
	defer f.guard("rename", oldPath, &errc)
	oldRemote, newRemote := f.remotePath(oldPath), f.remotePath(newPath)
	switch lo, ln := isLocalOnly(oldRemote), isLocalOnly(newRemote); {
	case lo && ln:
		return f.errno("rename", oldPath, os.Rename(f.cache.LocalPath(oldRemote), f.cache.LocalPath(newRemote)))
	case lo || ln:
		return -fuse.EXDEV // across the local/remote boundary: callers fall back to copy
	}
	if err := f.be.Rename(f.ctx, oldRemote, newRemote); err != nil {
		return f.errno("rename", oldPath, err)
	}
	moved := f.cache.Renamed(oldRemote, newRemote)
	f.mu.Lock()
	for _, h := range f.handles {
		switch {
		case h.remote == oldRemote:
			h.remote = newRemote
		case strings.HasPrefix(h.remote, oldRemote+"/"):
			h.remote = newRemote + strings.TrimPrefix(h.remote, oldRemote)
		}
	}
	f.mu.Unlock()
	for _, r := range moved {
		f.cache.Schedule(r)
	}
	return 0
}

// ---- files ----

func (f *FS) Create(p string, flags int, mode uint32) (errc int, fh uint64) {
	defer f.guard("create", p, &errc)
	remote := f.remotePath(p)
	if isLocalOnly(remote) {
		return f.openLocal(p, remote, os.O_RDWR|os.O_CREATE|os.O_TRUNC, true)
	}
	// create remotely right away so the file exists for other clients; an
	// offline create still works locally and is uploaded later
	if err := f.be.CreateEmpty(f.ctx, remote); err != nil && !backend.IsUnavailable(err) {
		return f.errno("create", p, err), ^uint64(0)
	}
	local, err := f.cache.Created(remote)
	if err != nil {
		return f.errno("create", p, err), ^uint64(0)
	}
	file, err := os.OpenFile(local, os.O_RDWR, 0)
	if err != nil {
		return f.errno("create", p, err), ^uint64(0)
	}
	return 0, f.addHandle(&handle{f: file, remote: remote, write: true})
}

func (f *FS) Open(p string, flags int) (errc int, fh uint64) {
	defer f.guard("open", p, &errc)
	remote := f.remotePath(p)
	write := flags&fuse.O_ACCMODE != fuse.O_RDONLY
	if isLocalOnly(remote) {
		mode := os.O_RDONLY
		if write {
			mode = os.O_RDWR
		}
		if flags&fuse.O_TRUNC != 0 && write {
			mode |= os.O_TRUNC
		}
		return f.openLocal(p, remote, mode, write)
	}

	local, err := f.cache.Ensure(f.ctx, remote)
	if err != nil {
		if !write || !errors.Is(err, fs.ErrNotExist) {
			return f.errno("open", p, err), ^uint64(0)
		}
		// Windows often opens-for-write without O_CREAT: create it
		if err := f.be.CreateEmpty(f.ctx, remote); err != nil && !backend.IsUnavailable(err) {
			return f.errno("open", p, err), ^uint64(0)
		}
		if local, err = f.cache.Created(remote); err != nil {
			return f.errno("open", p, err), ^uint64(0)
		}
	}
	mode := os.O_RDONLY
	if write {
		mode = os.O_RDWR
	}
	file, err := os.OpenFile(local, mode, 0)
	if err != nil {
		return f.errno("open", p, err), ^uint64(0)
	}
	if write && flags&fuse.O_TRUNC != 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return f.errno("open", p, err), ^uint64(0)
		}
		if err := f.cache.MarkDirty(remote); err != nil {
			f.log.Warn("mark dirty", "path", remote, "err", err)
		}
	}
	return 0, f.addHandle(&handle{f: file, remote: remote, write: write})
}

// openLocal opens a local-only metadata file in the cache.
func (f *FS) openLocal(p, remote string, mode int, write bool) (int, uint64) {
	local := f.cache.LocalPath(remote)
	if mode&os.O_CREATE != 0 {
		if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
			return f.errno("open", p, err), ^uint64(0)
		}
	}
	file, err := os.OpenFile(local, mode, 0o600)
	if err != nil {
		return f.errno("open", p, err), ^uint64(0)
	}
	return 0, f.addHandle(&handle{f: file, remote: remote, write: write, local: true})
}

func (f *FS) Read(p string, buf []byte, ofst int64, fh uint64) (n int) {
	defer f.guard("read", p, &n)
	h := f.handle(fh)
	if h == nil {
		return -fuse.EBADF
	}
	n, err := h.f.ReadAt(buf, ofst)
	if err != nil && !errors.Is(err, io.EOF) {
		return f.errno("read", p, err)
	}
	return n
}

func (f *FS) Write(p string, buf []byte, ofst int64, fh uint64) (n int) {
	defer f.guard("write", p, &n)
	h := f.handle(fh)
	if h == nil {
		return -fuse.EBADF
	}
	n, err := h.f.WriteAt(buf, ofst)
	if err != nil && n == 0 {
		return f.errno("write", p, err)
	}
	if h.local {
		return n
	}
	f.mu.Lock()
	remote := h.remote
	f.mu.Unlock()
	if err := f.cache.MarkDirty(remote); err != nil {
		f.log.Warn("mark dirty", "path", remote, "err", err)
	}
	return n
}

func (f *FS) Truncate(p string, size int64, fh uint64) (errc int) {
	defer f.guard("truncate", p, &errc)
	remote := f.remotePath(p)
	if h := f.handle(fh); h != nil && h.write {
		if err := h.f.Truncate(size); err != nil {
			return f.errno("truncate", p, err)
		}
		if h.local {
			return 0
		}
		return f.errno("truncate", p, f.cache.MarkDirty(remote))
	}
	if isLocalOnly(remote) {
		return f.errno("truncate", p, os.Truncate(f.cache.LocalPath(remote), size))
	}
	local, err := f.cache.Ensure(f.ctx, remote)
	if err != nil {
		return f.errno("truncate", p, err)
	}
	if err := os.Truncate(local, size); err != nil {
		return f.errno("truncate", p, err)
	}
	if err := f.cache.MarkDirty(remote); err != nil {
		return f.errno("truncate", p, err)
	}
	f.cache.Schedule(remote)
	return 0
}

func (f *FS) Fsync(p string, datasync bool, fh uint64) (errc int) {
	defer f.guard("fsync", p, &errc)
	if h := f.handle(fh); h != nil {
		return f.errno("fsync", p, h.f.Sync())
	}
	return 0
}

// Flush runs on every close() of a descriptor: queue the upload.
func (f *FS) Flush(p string, fh uint64) (errc int) {
	defer f.guard("flush", p, &errc)
	h := f.handle(fh)
	if h == nil || !h.write || h.local {
		return 0
	}
	f.mu.Lock()
	remote := h.remote
	f.mu.Unlock()
	f.cache.Schedule(remote)
	return 0
}

func (f *FS) Release(p string, fh uint64) (errc int) {
	defer f.guard("release", p, &errc)
	f.mu.Lock()
	h := f.handles[fh]
	delete(f.handles, fh)
	f.mu.Unlock()
	if h == nil {
		return 0
	}
	err := h.f.Close()
	if h.write && !h.local {
		f.cache.Schedule(h.remote)
	}
	return f.errno("release", p, err)
}

// closeAll releases handles the driver never released (forced unmount).
func (f *FS) closeAll() {
	f.mu.Lock()
	hs := f.handles
	f.handles = map[uint64]*handle{}
	f.mu.Unlock()
	for _, h := range hs {
		_ = h.f.Close()
	}
}
