package update

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Apply starts the downloaded Inno Setup installer silently. ShellExecute
// (not CreateProcess) is used so Windows shows the UAC prompt the
// installer needs. The caller must quit right after so the installer can
// replace FileLoad.exe; the installer relaunches the app when done.
func Apply(installer string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(installer)
	if err != nil {
		return err
	}
	args, _ := windows.UTF16PtrFromString("/SILENT /SP- /SUPPRESSMSGBOXES /NORESTART /CLOSEAPPLICATIONS")
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_SHOWNORMAL); err != nil {
		if err == windows.ERROR_CANCELLED {
			return fmt.Errorf("已取消安装（需要允许管理员权限）")
		}
		return fmt.Errorf("启动安装程序失败：%w", err)
	}
	return nil
}
