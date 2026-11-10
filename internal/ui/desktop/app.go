// Package desktop binds core.Service to the Wails window, tray and
// frontend. The frontend calls the exported methods of App by name
// ("fileload/internal/ui/desktop.App.<Method>").
//
// Events pushed to the frontend:
//
//	"state"           core.Snapshot — after every change
//	"activity"        []string      — batched file sync messages
//	"close-requested" nothing       — window close clicked: ask tray or quit
package desktop

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"fileload/internal/core"
	"fileload/internal/model"
	"fileload/internal/platform"
	"fileload/internal/store"
	"fileload/internal/update"
)

type App struct {
	svc   *core.Service
	store *store.Store

	app      *application.App
	win      *application.WebviewWindow
	tray     *application.SystemTray
	menu     *application.Menu
	openItem *application.MenuItem
	quitting atomic.Bool

	updMu      sync.Mutex
	latest     *update.Info // result of the last CheckUpdate
	installing atomic.Bool
}

func New(svc *core.Service, st *store.Store) *App { return &App{svc: svc, store: st} }

// SetService completes construction (core needs the Publisher first).
func (a *App) SetService(svc *core.Service) { a.svc = svc }

// ---- core.Publisher ----

type publisher struct{ a *App }

func (p publisher) Changed() {
	if p.a.app == nil {
		return
	}
	p.a.app.Event.Emit("state", p.a.svc.Snapshot())
	p.a.updateTray()
}

func (p publisher) Activity(msgs []string) {
	if p.a.app != nil {
		p.a.app.Event.Emit("activity", msgs)
	}
}

// Publisher returns the core.Publisher backed by this window.
func (a *App) Publisher() core.Publisher { return publisher{a} }

// ServiceShutdown (Wails lifecycle) makes sure nothing stays mounted when
// the app exits by any path; after Quit it is a no-op.
func (a *App) ServiceShutdown() error {
	a.svc.Shutdown()
	return nil
}

// ---- state ----

func (a *App) Snapshot() core.Snapshot { return a.svc.Snapshot() }

// ProfileInput is the connection form.
type ProfileInput struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	KeyPath  string `json:"keyPath"`
}

func toInput(p model.Server) ProfileInput {
	return ProfileInput{Host: p.Host, Port: p.Port, User: p.User, Password: p.Password, KeyPath: p.KeyPath}
}

// LastProfile returns the server used last time (prefills the form).
func (a *App) LastProfile() *ProfileInput {
	p, ok := a.svc.LastServer()
	if !ok {
		return nil
	}
	in := toInput(p)
	return &in
}

func (a *App) GetServer(i int) (ProfileInput, error) {
	p, err := a.svc.Server(i)
	return toInput(p), err
}

func (a *App) DeleteServer(i int) error { return a.svc.DeleteServer(i) }

// ---- connect / browse / mount ----

func (a *App) Connect(ctx context.Context, in ProfileInput) error {
	return a.svc.Connect(ctx, model.Server{
		Host: in.Host, Port: in.Port, User: in.User, Password: in.Password, KeyPath: in.KeyPath,
	})
}

func (a *App) ListDir(ctx context.Context, dir string) (core.DirListing, error) {
	return a.svc.ListDir(ctx, dir)
}

// Targets lists mount points to offer for the chosen remote dir.
func (a *App) Targets(root string) []platform.Target { return a.svc.Targets(root) }

func (a *App) Mount(ctx context.Context, target, root string) error {
	return a.svc.Mount(ctx, target, root)
}

// InstallDeps installs the bundled file-system driver (macOS: FUSE-T).
func (a *App) InstallDeps(ctx context.Context) error { return a.svc.InstallDeps(ctx) }

func (a *App) Remount(ctx context.Context, i int) error { return a.svc.Remount(ctx, i) }
func (a *App) DeleteMount(i int) error                  { return a.svc.DeleteMount(i) }
func (a *App) Unmount() error                           { return a.svc.Unmount() }
func (a *App) OpenDrive() error                         { return a.svc.Reveal() }

// ---- commands ----

func (a *App) SaveCommand(oldName, name, command, desc string) error {
	return a.svc.SaveCommand(oldName, model.Command{Name: name, Command: command, Desc: desc})
}

func (a *App) DeleteCommand(name string) error { return a.svc.DeleteCommand(name) }

// ---- about / update ----

type AboutInfo struct {
	Version     string `json:"version"`
	ReleasesURL string `json:"releasesUrl"`
	HasToken    bool   `json:"hasToken"`    // token saved in the About page
	BuiltinAuth bool   `json:"builtinAuth"` // read-only token baked into this build
}

func (a *App) About() AboutInfo {
	return AboutInfo{
		Version: update.Version, ReleasesURL: update.ReleasesWebURL,
		HasToken: a.store.UpdateToken() != "", BuiltinAuth: update.EmbeddedToken != "",
	}
}

func (a *App) SetUpdateToken(token string) error { return a.store.SetUpdateToken(token) }

// updateToken prefers the user's token over the one built in.
func (a *App) updateToken() string { return cmp.Or(a.store.UpdateToken(), update.EmbeddedToken) }

func (a *App) CheckUpdate(ctx context.Context) (update.Info, error) {
	info, err := update.Check(ctx, a.updateToken())
	if err == nil {
		a.updMu.Lock()
		a.latest = &info
		a.updMu.Unlock()
	}
	return info, err
}

// InstallUpdate downloads the installer of the release found by the last
// CheckUpdate (emitting "update-progress" {done,total}), starts it and
// quits so it can replace the app; the new version starts by itself.
func (a *App) InstallUpdate(ctx context.Context) error {
	if !a.installing.CompareAndSwap(false, true) {
		return errors.New("正在更新中")
	}
	defer a.installing.Store(false)
	a.updMu.Lock()
	info := a.latest
	a.updMu.Unlock()
	if info == nil || !info.HasUpdate {
		return errors.New("请先检查更新")
	}
	path, err := update.Download(ctx, *info, a.updateToken(), func(done, total int64) {
		a.app.Event.Emit("update-progress", map[string]int64{"done": done, "total": total})
	})
	if err != nil {
		return err
	}
	if err := update.Apply(path); err != nil {
		return err
	}
	go a.Quit() // uploads pending changes and unmounts first
	return nil
}

// OpenURL opens an http(s) link in the default browser.
func (a *App) OpenURL(link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("无效链接")
	}
	return a.app.Browser.OpenURL(link)
}

// ---- window / tray ----

func (a *App) GetCloseAction() string { return string(a.svc.UI().CloseAction) }

func (a *App) SetCloseAction(action string) error {
	return a.svc.SetCloseAction(model.CloseAction(strings.TrimSpace(action)))
}

// CloseChoice handles the answer of the close dialog: "tray" or "quit".
func (a *App) CloseChoice(action string, remember bool) error {
	act := model.CloseAction(action)
	if act != model.CloseTray && act != model.CloseQuit {
		return errors.New("unknown close action")
	}
	if remember {
		if err := a.svc.SetCloseAction(act); err != nil {
			return err
		}
	}
	if act == model.CloseTray {
		a.win.Hide()
		return nil
	}
	go a.Quit()
	return nil
}

func (a *App) onCloseRequested() {
	switch a.svc.UI().CloseAction {
	case model.CloseTray:
		a.win.Hide()
	case model.CloseQuit:
		go a.Quit()
	default:
		a.showWindow()
		a.app.Event.Emit("close-requested")
	}
}

func (a *App) showWindow() {
	if a.win == nil {
		return
	}
	a.win.Show()
	a.win.UnMinimise()
	a.win.Focus()
}

// Quit uploads pending changes, unmounts, removes the commands and exits.
func (a *App) Quit() {
	if a.quitting.Swap(true) {
		return
	}
	a.svc.Shutdown()
	a.app.Quit()
}

func (a *App) updateTray() {
	if a.tray == nil {
		return
	}
	tip := "FileLoad — 未挂载"
	target, ok := a.svc.Mounted()
	if ok {
		tip = "FileLoad — 已挂载 " + target
	}
	a.tray.SetTooltip(tip)
	a.openItem.SetEnabled(ok)
	a.menu.Update()
}
