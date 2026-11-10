//go:build windows

package updater

import (
	"time"

	"golang.org/x/sys/windows"
)

// WaitForExit waits until the process pid has exited, or timeout has passed.
// It reports whether the process is gone; one that cannot be opened is taken
// to be gone already.
func WaitForExit(pid int, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}
