// Package fileload embeds the UI and icon so cmd/fileload can serve them
// (go:embed cannot reach parent directories, hence this root package).
package fileload

import (
	"embed"
	"io/fs"
)

//go:embed assets/icon.png
var IconPNG []byte

// TrayTemplatePNG is the macOS menu-bar icon: a black-on-transparent
// template image that macOS tints for light/dark menu bars (a full-color
// opaque icon would render as a solid square). Source: assets/tray_template.svg.
//
//go:embed assets/tray_template.png
var TrayTemplatePNG []byte

//go:embed all:frontend
var frontendFS embed.FS

// Frontend returns the UI files (plain HTML/CSS/JS, no build step).
func Frontend() fs.FS {
	sub, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		panic(err) // embedded path is fixed at compile time
	}
	return sub
}
