package main

import (
	"fileload/internal/paths"
	"fileload/internal/platform"
	"fileload/internal/platform/windows"
)

func newPlatform(l paths.Layout) platform.Platform { return windows.New(l.Bin) }
