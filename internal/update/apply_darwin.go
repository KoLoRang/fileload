package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Apply mounts the downloaded dmg, replaces the running FileLoad.app with
// the one inside it and schedules a relaunch once this process exits. The
// caller must quit right after.
func Apply(dmg string) error {
	app, err := currentBundle()
	if err != nil {
		return err
	}
	mnt, err := os.MkdirTemp("", "fileload-dmg-")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	if out, err := exec.Command("/usr/bin/hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen",
		"-mountpoint", mnt, dmg).CombinedOutput(); err != nil {
		return fmt.Errorf("打开安装镜像失败：%s", strings.TrimSpace(string(out)))
	}
	defer exec.Command("/usr/bin/hdiutil", "detach", "-force", mnt).Run()

	src := filepath.Join(mnt, "FileLoad.app")
	if _, err := os.Stat(filepath.Join(src, "Contents", "Info.plist")); err != nil {
		return errors.New("安装镜像里没有 FileLoad.app")
	}

	// copy next to the current bundle first (same volume), then swap with
	// two renames so a failure never leaves the app half-replaced
	parent := filepath.Dir(app)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	staged := filepath.Join(parent, ".FileLoad-update-"+stamp+".app")
	old := filepath.Join(parent, ".FileLoad-old-"+stamp+".app")
	if out, err := exec.Command("/usr/bin/ditto", src, staged).CombinedOutput(); err != nil {
		_ = os.RemoveAll(staged)
		if errors.Is(err, os.ErrPermission) || strings.Contains(string(out), "Permission denied") {
			return fmt.Errorf("没有写入 %s 的权限，请打开安装镜像手动拖入「应用程序」", parent)
		}
		return fmt.Errorf("复制新版本失败：%s", strings.TrimSpace(string(out)))
	}
	if err := os.Rename(app, old); err != nil {
		_ = os.RemoveAll(staged)
		return fmt.Errorf("替换旧版本失败：%w", err)
	}
	if err := os.Rename(staged, app); err != nil {
		_ = os.Rename(old, app) // roll back
		_ = os.RemoveAll(staged)
		return fmt.Errorf("替换旧版本失败：%w", err)
	}
	_ = os.RemoveAll(old)
	_ = os.Remove(dmg)

	// relaunch after this process has exited
	script := `while kill -0 "$1" 2>/dev/null; do sleep 0.3; done; /usr/bin/open "$2"`
	cmd := exec.Command("/bin/sh", "-c", script, "sh", strconv.Itoa(os.Getpid()), app)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("新版本已安装，但无法自动重启：%w", err)
	}
	return cmd.Process.Release()
}

// currentBundle returns the path of the running .app bundle.
func currentBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	// <bundle>.app/Contents/MacOS/<exe>
	app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(app, ".app") {
		return "", errors.New("当前不是从 FileLoad.app 运行，无法自动更新，请手动安装")
	}
	if strings.HasPrefix(app, "/Volumes/") {
		return "", errors.New("请先把 FileLoad.app 拖到「应用程序」文件夹再运行，才能自动更新")
	}
	return app, nil
}
