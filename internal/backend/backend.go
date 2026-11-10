// Package backend defines the remote storage capability the file system is
// built on. SFTP is the only implementation today; WebDAV/S3/SMB can be
// added by implementing Backend without touching vfs, cache or the UI.
package backend

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

// ErrUnavailable wraps errors caused by the connection (not by the server's
// answer). Callers use it to fall back to cached data while offline.
var ErrUnavailable = errors.New("remote unavailable")

// Backend is a POSIX-like remote file tree addressed by absolute "/" paths.
//
// Errors the server answered with wrap fs.ErrNotExist, fs.ErrExist or
// fs.ErrPermission where the protocol allows it; connection failures wrap
// ErrUnavailable. Implementations must be safe for concurrent use.
type Backend interface {
	Stat(ctx context.Context, path string) (fs.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]fs.FileInfo, error)
	Mkdir(ctx context.Context, path string) error
	Remove(ctx context.Context, path string) error
	RemoveDir(ctx context.Context, path string) error
	// Rename replaces newPath atomically if it exists.
	Rename(ctx context.Context, oldPath, newPath string) error
	Truncate(ctx context.Context, path string, size int64) error
	Chmod(ctx context.Context, path string, mode fs.FileMode) error
	Chtimes(ctx context.Context, path string, atime, mtime time.Time) error
	// CreateEmpty creates (or truncates) path to an empty file.
	CreateEmpty(ctx context.Context, path string) error
	// Download streams the whole file at path into w.
	Download(ctx context.Context, path string, w io.Writer) error
	// Upload replaces the content of path with r (creating it if needed).
	Upload(ctx context.Context, path string, r io.Reader) error
}

// IsUnavailable reports whether err is a connection problem.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
