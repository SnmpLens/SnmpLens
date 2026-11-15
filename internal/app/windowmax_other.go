//go:build !linux

package app

// Windows and macOS read 0 as no limit; windowmax_linux.go says why Linux
// cannot.
const windowMaxWidth, windowMaxHeight = 0, 0
