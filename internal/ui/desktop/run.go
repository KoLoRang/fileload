package desktop

import (
	"io/fs"
	"net/http"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Options configure the window.
type Options struct {
	Icon        []byte
	TrayIcon    []byte // macOS template image for the menu bar
	Frontend    fs.FS
	Translucent bool // Mica on Windows 11
}

// Run creates the window and tray and blocks until the app quits.
func (a *App) Run(o Options) error {
	files := application.AssetFileServerFS(o.Frontend)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/icon.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(o.Icon)
			return
		}
		files.ServeHTTP(w, r)
	})

	a.app = application.New(application.Options{
		Name:        "FileLoad",
		Description: "SSH 云端目录映射本地",
		Icon:        o.Icon,
		Services:    []application.Service{application.NewService(a)},
		Assets:      application.AssetOptions{Handler: handler},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		Windows:     application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
	})

	opts := application.WebviewWindowOptions{
		Name:            "main",
		Title:           "FileLoad",
		Width:           1040,
		Height:          720,
		MinWidth:        860,
		MinHeight:       580,
		InitialPosition: application.WindowCentered,
		URL:             "/",
		BackgroundType:  application.BackgroundTypeSolid,
		Windows:         application.WindowsWindow{Theme: application.SystemDefault},
		Mac:             application.MacWindow{TitleBar: application.MacTitleBarDefault},
	}
	if o.Translucent {
		opts.BackgroundType = application.BackgroundTypeTranslucent
		opts.Windows.BackdropType = application.Mica
	}
	a.win = a.app.Window.NewWithOptions(opts)

	// closing the window asks (or follows the saved choice): tray or quit
	a.win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if a.quitting.Load() {
			return
		}
		e.Cancel()
		a.onCloseRequested()
	})

	a.tray = a.app.SystemTray.New()
	a.tray.SetTooltip("FileLoad — 未挂载")
	if runtime.GOOS == "darwin" && len(o.TrayIcon) > 0 {
		a.tray.SetTemplateIcon(o.TrayIcon)
	} else {
		a.tray.SetIcon(o.Icon)
	}
	a.menu = a.app.NewMenu()
	a.menu.Add("显示主窗口").OnClick(func(*application.Context) { a.showWindow() })
	a.openItem = a.menu.Add("打开挂载目录").OnClick(func(*application.Context) { _ = a.svc.Reveal() })
	a.openItem.SetEnabled(false)
	a.menu.AddSeparator()
	a.menu.Add("退出（先上传再卸载）").OnClick(func(*application.Context) { go a.Quit() })
	a.tray.SetMenu(a.menu)
	if runtime.GOOS != "darwin" { // macOS: a click opens the menu
		a.tray.OnClick(a.showWindow)
	}
	return a.app.Run()
}
