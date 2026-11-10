// Command fileload is the desktop app: it only assembles the components
// (dependency injection) and hands control to the UI.
package main

import (
	"context"
	"log"
	"os"

	"fileload"
	"fileload/internal/backend/sftp"
	"fileload/internal/core"
	"fileload/internal/logx"
	"fileload/internal/model"
	"fileload/internal/paths"
	"fileload/internal/store"
	"fileload/internal/ui/desktop"
	"fileload/internal/update"
	"fileload/internal/vfs"
)

func main() {
	layout := paths.Default()
	if dir := os.Getenv("FILELOAD_HOME"); dir != "" { // portable/test mode
		layout = paths.Under(dir)
	}
	logger, closeLog := logx.Open(layout.Logs)
	defer closeLog()
	logger.Info("starting", "version", update.Version)

	st := store.Open(layout.Config)
	pool := sftp.NewPool(logger)
	defer pool.Close()
	plat := newPlatform(layout)

	ui := desktop.New(nil, st)
	svc := core.New(core.Deps{
		Store: st,
		Connect: func(ctx context.Context, p model.Server) (core.Conn, error) {
			l, err := pool.Acquire(ctx, p)
			if err != nil {
				return nil, err // keep the interface nil on error
			}
			return l, nil
		},
		Platform:  plat,
		Mounter:   vfs.Mounter{},
		Layout:    layout,
		Log:       logger,
		Publisher: ui.Publisher(),
	})
	ui.SetService(svc)

	if err := ui.Run(desktop.Options{
		Icon:        fileload.IconPNG,
		TrayIcon:    fileload.TrayTemplatePNG,
		Frontend:    fileload.Frontend(),
		Translucent: plat.Translucent(),
	}); err != nil {
		log.Fatal(err)
	}
}
