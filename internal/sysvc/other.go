//go:build !windows

package sysvc

import (
	"net"
	"time"
)

// Supported reports whether a system service can be installed here.
func Supported() bool { return false }

// IsService reports whether this process was started by the service manager.
func IsService() bool { return false }

// Run is the service's main where there is no service.
func Run(string) error { return ErrUnsupported }

// Query reports no service.
func Query() (Info, error) { return Info{}, nil }

// DataDir has no service's directory to give.
func DataDir() (string, error) { return "", ErrUnsupported }

// StatusDir has no service's directory to give.
func StatusDir() (string, error) { return "", ErrUnsupported }

// CheckLocation refuses: there is no service to install.
func CheckLocation(string) error { return ErrUnsupported }

// Install is refused where there is no service.
func Install(string, string, string, string) (Result, error) { return Result{}, ErrUnsupported }

// Uninstall is refused where there is no service.
func Uninstall(bool, string) error { return ErrUnsupported }

// Start is refused where there is no service.
func Start() error { return ErrUnsupported }

// Stop is refused where there is no service.
func Stop() error { return ErrUnsupported }

// Allow is refused where there is no service.
func Allow(string) error { return ErrUnsupported }

// IsElevated reports whether this process runs as root.
func IsElevated() bool { return false }

// CurrentUserSID has no SID to give.
func CurrentUserSID() (string, error) { return "", ErrUnsupported }

// RunElevated is refused where there is no service.
func RunElevated(string, []string, time.Duration) (uint32, error) { return 0, ErrUnsupported }

// DialControl finds no service.
func DialControl(time.Duration) (net.Conn, error) { return nil, ErrUnsupported }

// Export file names in the window's directory.
const (
	ExportedDatabase = "monitoring.db"
	ExportedBundle   = "bundle.json"
)
