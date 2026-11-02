//go:build !linux

package tray

// hostAvailable: the notification area is part of Windows and of macOS.
func hostAvailable() bool { return true }
