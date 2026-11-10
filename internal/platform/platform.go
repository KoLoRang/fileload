// Package platform declares the OS-specific capabilities FileLoad needs.
// Implementations live in platform/windows and platform/darwin (the only
// places with build tags); cmd/fileload picks one at compile time.
// Supporting another OS means adding one more implementation.
package platform

import (
	"context"
	"errors"
	"strings"
)

// ErrCanceled is returned by InstallDeps when the user dismissed the
// password prompt.
var ErrCanceled = errors.New("已取消安装")

// TargetKind tells the UI how mount targets look on this OS.
type TargetKind string

const (
	TargetDrive TargetKind = "drive" // Windows drive letter, e.g. "Z:"
	TargetDir   TargetKind = "dir"   // macOS directory, e.g. ~/FileLoad/work
)

// Target is one selectable mount point.
type Target struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Used  bool   `json:"used"`
}

// Platform is everything core needs from the OS.
type Platform interface {
	// Name is "windows" or "darwin".
	Name() string
	TargetKind() TargetKind
	// CheckDeps reports a missing file-system driver with install hints.
	CheckDeps() error
	// CanInstallDeps reports whether this build carries a driver installer
	// that InstallDeps can run (macOS app bundle with FUSE-T inside).
	CanInstallDeps() bool
	// InstallDeps installs the bundled driver; the OS asks for an
	// administrator password. Returns ErrCanceled if the user declines.
	InstallDeps(ctx context.Context) error
	// Targets lists mount points to offer; hint (a remote dir name) lets
	// directory-based platforms suggest a matching folder.
	Targets(hint string) []Target
	// PrepareTarget validates/normalises target and makes it ready to mount
	// (e.g. creates the directory).
	PrepareTarget(target string) (string, error)
	// ReleaseTarget undoes PrepareTarget after unmount.
	ReleaseTarget(target string)
	// MountOptions are the driver options for target.
	MountOptions(target, volname string) []string
	// Ready reports whether a mount on target is visible to other programs.
	Ready(target string) bool
	// Reveal opens target in Explorer/Finder.
	Reveal(target string) error
	// RelPath returns the slash-separated path of local below target
	// (ok=false when local is not inside target).
	RelPath(target, local string) (rel string, ok bool)
	// Translucent reports whether the window should use a system backdrop
	// (Mica on Windows 11).
	Translucent() bool
	Shims() ShimInstaller
}

// ShimSpec is one generated command wrapper.
type ShimSpec struct {
	Name       string // command name, e.g. "rcd"
	RemoteLine string // runs after the wrapper cd-ed into the remote dir
	Target     string // local mount point
	Root       string // remote root
	ProxyAddr  string // 127.0.0.1:port
	User       string // ssh user (the proxy accepts any)
}

// ShimInstaller puts command wrappers on the user's PATH.
type ShimInstaller interface {
	BinDir() string
	// Install (re)writes exactly specs (stale wrappers are removed) and
	// makes sure BinDir is on PATH for new terminals.
	Install(specs []ShimSpec) error
	// Remove deletes all wrappers.
	Remove() error
}

// BashQuote single-quotes s for a POSIX shell.
func BashQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
