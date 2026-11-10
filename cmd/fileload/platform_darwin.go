package main

import (
	"os"
	"path/filepath"

	"fileload/internal/paths"
	"fileload/internal/platform"
	"fileload/internal/platform/darwin"
)

func newPlatform(l paths.Layout) platform.Platform {
	var profiles []string // nil = the user's real shell profiles
	if dir := os.Getenv("FILELOAD_HOME"); dir != "" {
		profiles = []string{filepath.Join(dir, "zprofile")} // test mode: never touch ~
	}
	return darwin.New(l.Bin, profiles)
}
