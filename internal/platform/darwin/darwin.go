//go:build darwin

// Package darwin implements platform.Platform for macOS: FUSE-T directory
// mounts (no kernel extension), POSIX sh wrappers and a marked PATH block
// in the user's shell profiles.
package darwin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"fileload/internal/platform"
)

// FUSE-T installs this library; cgofuse dlopens it at mount time.
var fuseLibs = []string{
	"/usr/local/lib/libfuse-t.dylib",
	"/usr/local/lib/libfuse.2.dylib", // macFUSE also works
}

type Platform struct {
	home  string // user home: default targets live in ~/FileLoad
	shims *shims
}

var _ platform.Platform = (*Platform)(nil)

// New returns the macOS platform. Wrappers go to binDir; profiles are the
// shell startup files that get the PATH block (nil = ~/.zprofile and, if
// it exists, ~/.bash_profile).
func New(binDir string, profiles []string) *Platform {
	home, _ := os.UserHomeDir()
	if profiles == nil {
		profiles = []string{filepath.Join(home, ".zprofile")}
		if _, err := os.Stat(filepath.Join(home, ".bash_profile")); err == nil {
			profiles = append(profiles, filepath.Join(home, ".bash_profile"))
		}
	}
	return &Platform{home: home, shims: &shims{bin: binDir, profiles: profiles}}
}

func (*Platform) Name() string                    { return "darwin" }
func (*Platform) TargetKind() platform.TargetKind { return platform.TargetDir }
func (p *Platform) Shims() platform.ShimInstaller { return p.shims }
func (*Platform) Translucent() bool               { return false }

func (*Platform) CheckDeps() error {
	for _, l := range fuseLibs {
		if _, err := os.Stat(l); err == nil {
			return nil
		}
	}
	return errors.New("未检测到 FUSE-T：请先安装（brew install --cask macos-fuse-t/cask/fuse-t，或从 https://www.fuse-t.org 下载安装包）")
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._ -]+`)

// Targets suggests ~/FileLoad/<last remote dir name>.
func (p *Platform) Targets(hint string) []platform.Target {
	name := strings.TrimSpace(unsafeName.ReplaceAllString(filepath.Base(strings.TrimRight(hint, "/")), "_"))
	if name == "" || name == "." || name == "/" {
		name = "remote"
	}
	base := filepath.Join(p.home, "FileLoad", name)
	t := base
	for i := 2; p.busy(t); i++ {
		t = fmt.Sprintf("%s-%d", base, i)
	}
	return []platform.Target{{Value: t, Label: p.pretty(t)}}
}

func (p *Platform) pretty(t string) string {
	if rel, err := filepath.Rel(p.home, t); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return t
}

// busy: exists and is either a mount point or a non-empty directory.
func (p *Platform) busy(t string) bool {
	st, err := os.Stat(t)
	if err != nil {
		return false
	}
	if !st.IsDir() || p.Ready(t) {
		return true
	}
	ents, err := os.ReadDir(t)
	return err != nil || len(ents) > 0
}

func (p *Platform) expand(t string) string {
	t = strings.TrimSpace(t)
	if t == "~" {
		return p.home
	}
	if rest, ok := strings.CutPrefix(t, "~/"); ok {
		return filepath.Join(p.home, rest)
	}
	return t
}

func (p *Platform) PrepareTarget(t string) (string, error) {
	t = p.expand(t)
	if t == "" || !filepath.IsAbs(t) {
		return "", errors.New("请填写挂载目录的完整路径（例如 ~/FileLoad/work）")
	}
	t = filepath.Clean(t)
	if st, err := os.Stat(t); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("%s 不是目录", t)
		}
		if p.busy(t) {
			return "", fmt.Errorf("%s 已被占用或不是空目录，请换一个目录", p.pretty(t))
		}
		return t, nil
	}
	if err := os.MkdirAll(t, 0o755); err != nil {
		return "", fmt.Errorf("创建挂载目录失败: %w", err)
	}
	return t, nil
}

// ReleaseTarget removes the mount directory if it is empty again.
func (*Platform) ReleaseTarget(t string) { _ = syscall.Rmdir(t) }

func (*Platform) MountOptions(_, volname string) []string {
	return []string{
		"-o", "volname=" + volname,
		"-o", "noattrcache", // see remote edits (e.g. by rcd commands) promptly
	}
}

// Ready: the target is a mount point once its device differs from its
// parent's.
func (*Platform) Ready(t string) bool {
	var a, b syscall.Stat_t
	if syscall.Stat(t, &a) != nil || syscall.Stat(filepath.Dir(t), &b) != nil {
		return false
	}
	return a.Dev != b.Dev
}

func (*Platform) Reveal(t string) error { return exec.Command("/usr/bin/open", t).Start() }

func (*Platform) RelPath(target, local string) (string, bool) {
	rel, err := filepath.Rel(target, local)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if rel == "." {
		rel = ""
	}
	return filepath.ToSlash(rel), true
}
