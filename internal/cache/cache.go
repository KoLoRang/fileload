// Package cache keeps local copies of remote files for one mount.
//
// Read path: the whole remote file is downloaded once and served locally;
// the copy is reused while its size and mtime still match the remote.
// Write path: writes land in the local copy, which is marked dirty (a
// "<file>.dirty" marker survives crashes) and uploaded by a small worker
// pool. A dirty copy always wins over the remote until it is uploaded.
package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"fileload/internal/backend"
)

const (
	dirtySuffix = ".dirty"
	partSuffix  = ".part"
	uploaders   = 2
)

// Store is the local cache of one mount: remote paths under Root map to
// files under Dir.
type Store struct {
	be   backend.Backend
	root string // remote root, clean absolute path
	dir  string // local cache dir
	log  *slog.Logger

	// OnStatus receives human readable progress ("downloading x (3MB)").
	OnStatus func(msg string)

	mu    sync.Mutex
	dirty map[string]uint64 // remote path -> generation, bumped on every write
	gen   uint64
	dl    map[string]*download // in-flight downloads (one per path)

	q *queue
}

type download struct {
	done  chan struct{}
	local string
	err   error
}

// Open prepares the cache for remote root in dir and schedules uploads of
// dirty files left over from a previous run (crash, kill, power loss).
func Open(be backend.Backend, root, dir string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建缓存目录失败: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Store{
		be: be, root: path.Clean(root), dir: dir, log: log,
		dirty: map[string]uint64{},
		dl:    map[string]*download{},
	}
	s.q = newQueue(uploaders, s.upload)
	s.recover()
	return s, nil
}

// Close stops accepting uploads and synchronously uploads everything that
// is still dirty. Dirty files that fail to upload keep their marker and are
// retried by the next Open.
func (s *Store) Close() {
	s.mu.Lock()
	pending := make([]string, 0, len(s.dirty))
	for r := range s.dirty {
		pending = append(pending, r)
	}
	s.mu.Unlock()
	for _, r := range pending {
		_ = s.q.schedule(r)
	}
	s.q.close()
}

func (s *Store) status(format string, args ...any) {
	if s.OnStatus != nil {
		s.OnStatus(fmt.Sprintf(format, args...))
	}
}

// Dir is the local cache directory.
func (s *Store) Dir() string { return s.dir }

// LocalPath maps a remote path under root to its cache file.
func (s *Store) LocalPath(remote string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(remote, s.root), "/")
	if rel == "" {
		return s.dir
	}
	return filepath.Join(s.dir, filepath.FromSlash(rel))
}

// remoteOf is the inverse of LocalPath.
func (s *Store) remoteOf(local string) (string, bool) {
	rel, err := filepath.Rel(s.dir, local)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return path.Join(s.root, filepath.ToSlash(rel)), true
}

// ---- dirty tracking ----

// IsDirty reports whether remote has local changes not uploaded yet.
func (s *Store) IsDirty(remote string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.dirty[remote]
	return ok
}

// MarkDirty records a local change. Only the clean->dirty transition
// touches the disk, so it is cheap to call on every write.
func (s *Store) MarkDirty(remote string) error {
	s.mu.Lock()
	_, was := s.dirty[remote]
	s.gen++
	s.dirty[remote] = s.gen
	s.mu.Unlock()
	if was {
		return nil
	}
	return os.WriteFile(s.LocalPath(remote)+dirtySuffix, []byte("1"), 0o600)
}

// Schedule queues remote for upload if it is dirty.
func (s *Store) Schedule(remote string) {
	if !s.IsDirty(remote) {
		return
	}
	if err := s.q.schedule(remote); err != nil {
		s.log.Warn("upload not scheduled", "path", remote, "err", err)
	}
}

func (s *Store) upload(ctx context.Context, remote string) {
	s.mu.Lock()
	gen, ok := s.dirty[remote]
	s.mu.Unlock()
	if !ok {
		return // renamed/removed/uploaded meanwhile
	}
	local := s.LocalPath(remote)
	f, err := os.Open(local)
	if err != nil {
		s.log.Warn("dirty file vanished", "path", remote, "err", err)
		s.clearDirty(remote, gen)
		return
	}
	s.status("uploading %s", remote)
	err = s.be.Upload(ctx, remote, f)
	_ = f.Close()
	if err != nil {
		s.log.Warn("upload failed", "path", remote, "err", err)
		s.status("upload FAILED %s: %v", remote, err)
		return // marker kept: retried on next flush or next mount
	}
	if s.clearDirty(remote, gen) {
		s.stampRemote(ctx, remote, local)
		s.status("synced %s", remote)
	}
}

// clearDirty drops the dirty state unless remote was written again after
// generation gen (that write needs another upload, already re-queued).
func (s *Store) clearDirty(remote string, gen uint64) bool {
	s.mu.Lock()
	cur, ok := s.dirty[remote]
	if !ok || cur != gen {
		s.mu.Unlock()
		return false
	}
	delete(s.dirty, remote)
	s.mu.Unlock()
	if err := os.Remove(s.LocalPath(remote) + dirtySuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("remove dirty marker", "path", remote, "err", err)
	}
	return true
}

// stampRemote copies the remote mtime onto the local copy so the next open
// sees the cache as current instead of downloading our own upload back.
func (s *Store) stampRemote(ctx context.Context, remote, local string) {
	fi, err := s.be.Stat(ctx, remote)
	if err != nil {
		return
	}
	_ = os.Chtimes(local, fi.ModTime(), fi.ModTime())
}

// recover re-registers dirty markers found on disk and queues them.
func (s *Store) recover() {
	_ = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, dirtySuffix) {
			return nil
		}
		local := strings.TrimSuffix(p, dirtySuffix)
		remote, ok := s.remoteOf(local)
		if !ok {
			return nil
		}
		if _, err := os.Stat(local); err != nil {
			_ = os.Remove(p)
			return nil
		}
		s.mu.Lock()
		s.gen++
		s.dirty[remote] = s.gen
		s.mu.Unlock()
		s.log.Info("resuming upload left from last run", "path", remote)
		_ = s.q.schedule(remote)
		return nil
	})
}

// ---- reads ----

// Ensure returns a local copy of remote that matches the server, or the
// local version if it has un-uploaded changes, or a stale copy while the
// server is unreachable. Concurrent calls for one path share one download.
func (s *Store) Ensure(ctx context.Context, remote string) (string, error) {
	if s.IsDirty(remote) {
		local := s.LocalPath(remote)
		if _, err := os.Stat(local); err == nil {
			return local, nil
		}
	}
	s.mu.Lock()
	if d, ok := s.dl[remote]; ok {
		s.mu.Unlock()
		select {
		case <-d.done:
			return d.local, d.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	d := &download{done: make(chan struct{})}
	s.dl[remote] = d
	s.mu.Unlock()

	d.local, d.err = s.ensure(ctx, remote)
	s.mu.Lock()
	delete(s.dl, remote)
	s.mu.Unlock()
	close(d.done)
	return d.local, d.err
}

func (s *Store) ensure(ctx context.Context, remote string) (string, error) {
	local := s.LocalPath(remote)
	li, lerr := os.Stat(local)
	if lerr == nil && li.IsDir() {
		return "", fmt.Errorf("cache path %s is a directory: %w", local, fs.ErrInvalid)
	}
	fi, err := s.be.Stat(ctx, remote)
	if err != nil {
		if lerr == nil && backend.IsUnavailable(err) {
			return local, nil // offline: serve the cached copy
		}
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s: %w", remote, errIsDir)
	}
	if lerr == nil && li.Size() == fi.Size() && li.ModTime().Equal(fi.ModTime()) {
		return local, nil // cache is current
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", err
	}
	s.status("downloading %s (%s)", remote, HumanSize(fi.Size()))
	tmp := local + partSuffix
	if err := s.fetch(ctx, remote, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp, local); err != nil {
		_ = os.Remove(tmp)
		if lerr == nil {
			return local, nil // old copy still open elsewhere (Windows): serve it
		}
		return "", err
	}
	return local, nil
}

func (s *Store) fetch(ctx context.Context, remote, dst string) error {
	f, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = s.be.Download(ctx, remote, f)
	return errors.Join(err, f.Close())
}

// errIsDir is returned by Ensure when the remote path is a directory.
var errIsDir = errors.New("is a directory")

// IsDirError reports whether err came from Ensure on a directory.
func IsDirError(err error) bool { return errors.Is(err, errIsDir) }

// ---- namespace changes ----

// Created prepares an empty local file for a newly created remote file and
// marks it dirty so it is uploaded even if never written.
func (s *Store) Created(remote string) (string, error) {
	local := s.LocalPath(remote)
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(local, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return local, s.MarkDirty(remote)
}

// Removed forgets the local copy of remote (file or directory tree).
func (s *Store) Removed(remote string) {
	s.mu.Lock()
	for r := range s.dirty {
		if r == remote || strings.HasPrefix(r, remote+"/") {
			delete(s.dirty, r)
		}
	}
	s.mu.Unlock()
	local := s.LocalPath(remote)
	if local == s.dir {
		return
	}
	_ = os.RemoveAll(local)
	_ = os.Remove(local + dirtySuffix)
}

// Renamed moves the local copy (and pending dirty state, so edits saved via
// "write temp, rename over target" are not lost) and returns the remote
// paths that must be uploaded under their new name.
func (s *Store) Renamed(oldRemote, newRemote string) []string {
	oldLocal, newLocal := s.LocalPath(oldRemote), s.LocalPath(newRemote)
	s.Removed(newRemote) // replaced target: its old cache is obsolete

	s.mu.Lock()
	var moved []string
	for r, g := range s.dirty {
		var nr string
		switch {
		case r == oldRemote:
			nr = newRemote
		case strings.HasPrefix(r, oldRemote+"/"):
			nr = newRemote + strings.TrimPrefix(r, oldRemote)
		default:
			continue
		}
		delete(s.dirty, r)
		s.dirty[nr] = g
		moved = append(moved, nr)
	}
	s.mu.Unlock()

	if _, err := os.Stat(oldLocal); err == nil {
		if err := os.MkdirAll(filepath.Dir(newLocal), 0o700); err == nil {
			if err := os.Rename(oldLocal, newLocal); err != nil {
				s.log.Warn("move cache", "from", oldLocal, "to", newLocal, "err", err)
			}
		}
	}
	if _, err := os.Stat(oldLocal + dirtySuffix); err == nil {
		_ = os.Rename(oldLocal+dirtySuffix, newLocal+dirtySuffix)
	}
	// markers of files inside a renamed directory moved with the directory
	return moved
}

// LocalOnly lists names in the cache dir of remoteDir that are dirty but may
// not exist remotely yet (created, not uploaded).
func (s *Store) LocalOnly(remoteDir string) map[string]fs.FileInfo {
	out := map[string]fs.FileInfo{}
	s.mu.Lock()
	var names []string
	for r := range s.dirty {
		if path.Dir(r) == remoteDir {
			names = append(names, path.Base(r))
		}
	}
	s.mu.Unlock()
	for _, n := range names {
		if fi, err := os.Stat(s.LocalPath(path.Join(remoteDir, n))); err == nil && !fi.IsDir() {
			out[n] = fi
		}
	}
	return out
}

// HumanSize formats n bytes as "1.5MB".
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
