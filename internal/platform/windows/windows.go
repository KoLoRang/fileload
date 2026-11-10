//go:build windows

// Package windows implements platform.Platform with WinFsp drive letters,
// .cmd/.ps1 wrappers and the HKCU user PATH.
package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"

	"fileload/internal/platform"
)

type Platform struct {
	shims *shims
}

var _ platform.Platform = (*Platform)(nil)

// New returns the Windows platform; generated wrappers go to binDir.
func New(binDir string) *Platform { return &Platform{shims: &shims{bin: binDir}} }

func (*Platform) Name() string                    { return "windows" }
func (*Platform) TargetKind() platform.TargetKind { return platform.TargetDrive }
func (p *Platform) Shims() platform.ShimInstaller { return p.shims }

func (*Platform) CheckDeps() error {
	dll := filepath.Join(os.Getenv("ProgramFiles(x86)"), "WinFsp", "bin", "winfsp-x64.dll")
	if _, err := os.Stat(dll); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("ProgramFiles"), "WinFsp", "bin", "winfsp-x64.dll")); err == nil {
		return nil
	}
	return errors.New("未检测到 WinFsp：请先安装 WinFsp（https://winfsp.dev/rel/）")
}

// The Windows installer already bundles WinFsp; nothing to install here.
func (*Platform) CanInstallDeps() bool { return false }
func (*Platform) InstallDeps(context.Context) error {
	return errors.New("请重新运行 FileLoad 安装包以安装 WinFsp")
}

// Targets lists Z: down to D: and whether each is taken.
func (*Platform) Targets(string) []platform.Target {
	var out []platform.Target
	for c := 'Z'; c >= 'D'; c-- {
		d := string(c) + ":"
		out = append(out, platform.Target{Value: d, Label: d, Used: driveInUse(d)})
	}
	return out
}

var driveRe = regexp.MustCompile(`^[D-Z]:$`)

func driveInUse(drive string) bool {
	_, err := os.Stat(drive + `\`)
	return err == nil
}

func (*Platform) PrepareTarget(t string) (string, error) {
	t = strings.ToUpper(strings.TrimSpace(t))
	if !driveRe.MatchString(t) {
		return "", errors.New("请选择盘符")
	}
	if driveInUse(t) {
		return "", fmt.Errorf("盘符 %s 已被占用，请换一个盘符", t)
	}
	return t, nil
}

func (*Platform) ReleaseTarget(string) {}

func (*Platform) MountOptions(_, volname string) []string {
	return []string{
		"-o", "volname=" + volname,
		"-o", "uid=-1", // owner = mounting user (not Administrators)
		"-o", "gid=-1",
		"-o", "umask=000", // normal users can create/write files
		"-o", "create_umask=000",
		"-o", "dothidden", // dot files shown as hidden
	}
}

func (*Platform) Ready(t string) bool {
	st, err := os.Stat(t + `\`)
	return err == nil && st.IsDir()
}

func (*Platform) Reveal(t string) error { return exec.Command("explorer.exe", t+`\`).Start() }

func (*Platform) RelPath(target, local string) (string, bool) {
	if len(local) < 2 || !strings.EqualFold(local[:2], target) {
		return "", false
	}
	return strings.Trim(strings.ReplaceAll(local[2:], `\`, "/"), "/"), true
}

// Translucent: Mica needs Windows 11 22H2+ (build 22621).
func (*Platform) Translucent() bool { return windows.RtlGetVersion().BuildNumber >= 22621 }
