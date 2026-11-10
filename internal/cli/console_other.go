//go:build !windows

package cli

// attachConsole has nothing to do where a program started from a terminal
// writes to it.
func attachConsole() {}
