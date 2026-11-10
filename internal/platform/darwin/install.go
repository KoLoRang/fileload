//go:build darwin

package darwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fileload/internal/platform"
)

// bundledPkgName is the FUSE-T installer that scripts/build_macos.sh puts
// into FileLoad.app/Contents/Resources.
const bundledPkgName = "fuse-t.pkg"

// bundledPkg locates the installer next to the running executable
// (Contents/MacOS/FileLoad -> Contents/Resources/fuse-t.pkg).
func bundledPkg() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	p := filepath.Join(filepath.Dir(exe), "..", "Resources", bundledPkgName)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return filepath.Clean(p), true
	}
	return "", false
}

func (*Platform) CanInstallDeps() bool {
	_, ok := bundledPkg()
	return ok
}

// installScript runs Apple's installer with the standard macOS password
// prompt. The package path is passed as an argument (never spliced into the
// script) and quoted by AppleScript itself.
const installScript = `on run argv
	do shell script "/usr/sbin/installer -pkg " & quoted form of (item 1 of argv) & " -target /" ¬
		with prompt "FileLoad 需要安装 FUSE-T 文件系统驱动，才能把远程目录挂载到本地。" ¬
		with administrator privileges
end run`

func (p *Platform) InstallDeps(ctx context.Context) error {
	pkg, ok := bundledPkg()
	if !ok {
		return errors.New("此版本未内置 FUSE-T 安装包，请从 https://www.fuse-t.org 下载安装")
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", installScript, pkg).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") { // errAEUserCanceled
			return platform.ErrCanceled
		}
		return fmt.Errorf("FUSE-T 安装失败: %s", msg)
	}
	if err := p.CheckDeps(); err != nil {
		return fmt.Errorf("安装程序已运行，但仍未检测到 FUSE-T: %w", err)
	}
	return nil
}
