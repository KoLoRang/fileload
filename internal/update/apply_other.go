//go:build !windows && !darwin

package update

import "errors"

// Apply is not supported on this OS.
func Apply(string) error { return errors.New("当前系统不支持自动更新") }
