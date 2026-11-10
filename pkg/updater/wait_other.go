//go:build !windows

package updater

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"time"
)

// WaitForExit waits until the process pid has exited, or timeout has passed.
// It reports whether the process is gone.
func WaitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if gone(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// gone reports whether pid no longer runs. An exited process its parent has
// not reaped yet still answers signal 0 on Linux, so a zombie counts as gone.
func gone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil || p.Signal(syscall.Signal(0)) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses and may
	// itself contain spaces or parentheses.
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] == 'Z'
	}
	return false
}
