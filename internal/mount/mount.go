// Package mount is the contract between core and the file-system driver
// (internal/vfs, cgofuse-based). Keeping it separate lets core compile and
// be tested without cgo or a FUSE installation.
package mount

import (
	"context"
	"log/slog"

	"fileload/internal/backend"
	"fileload/internal/cache"
)

// Request describes one mount.
type Request struct {
	Target  string   // drive letter (Windows) or directory (macOS)
	Options []string // driver options, e.g. "-o", "volname=x"
	Root    string   // remote root directory
	Backend backend.Backend
	Cache   *cache.Store
	// Ready reports whether the target is visible to other programs yet.
	// Optional; polled after the driver reports it is initialised.
	Ready func() bool
	Log   *slog.Logger
}

// Mounter mounts file systems. Mount blocks until the target is usable,
// the mount failed, or ctx is done.
type Mounter interface {
	Mount(ctx context.Context, req Request) (Handle, error)
}

// Handle is a live mount.
type Handle interface {
	// Unmount detaches the file system and waits until the driver stopped.
	Unmount() error
	// Done is closed when the file system went away, also when that was not
	// requested (e.g. the volume was ejected in Finder).
	Done() <-chan struct{}
}
