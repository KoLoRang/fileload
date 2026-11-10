// Package paths resolves the per-user directories FileLoad uses.
//
//	Windows: config %APPDATA%\fileload, cache %LOCALAPPDATA%\fileload
//	macOS:   config ~/Library/Application Support/fileload, cache ~/Library/Caches/fileload
package paths

import (
	"os"
	"path/filepath"
)

const appDir = "fileload"

// Layout is the set of directories of one installation. Tests build their
// own Layout under t.TempDir() instead of touching the real user dirs.
type Layout struct {
	Config string // json settings
	Cache  string // file cache root (one sub dir per mount)
	Bin    string // generated command wrappers
	Logs   string
}

// Default returns the layout under the OS user directories.
func Default() Layout {
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = "."
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	c := filepath.Join(cache, appDir)
	return Layout{
		Config: filepath.Join(cfg, appDir),
		Cache:  filepath.Join(c, "cache"),
		Bin:    filepath.Join(c, "bin"),
		Logs:   filepath.Join(c, "logs"),
	}
}

// Under returns a layout rooted at dir (tests, portable mode).
func Under(dir string) Layout {
	return Layout{
		Config: filepath.Join(dir, "config"),
		Cache:  filepath.Join(dir, "cache"),
		Bin:    filepath.Join(dir, "bin"),
		Logs:   filepath.Join(dir, "logs"),
	}
}
