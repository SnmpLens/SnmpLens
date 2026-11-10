//go:build windows

package sysvc

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Account is the virtual account the service runs as: its own identity, with no
// password to manage and no rights beyond what is granted to it.
const Account = `NT SERVICE\` + Name

// FirewallRule names the inbound rule installing adds. A service has no desktop
// to show Windows' "allow this program" prompt on, so without a rule the trap
// listener would hear nothing from the network at all.
const FirewallRule = "SnmpLens (service)"

// Supported reports whether a system service can be installed here.
func Supported() bool { return true }

// DataDir is where the service keeps its database and credentials:
// %ProgramData%\SnmpLens, readable by administrators and the service only.
func DataDir() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Name), nil
}

// StatusDir is where the service publishes its status, the one part of its
// directory any local user may read: whether it runs, its version, the last
// error — nothing it supervises.
func StatusDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "status"), nil
}

// LogPath is the service's own log file.
func LogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "service.log"), nil
}

// ErrLocation explains why a copy outside Program Files is refused.
var ErrLocation = errors.New("SnmpLens must be installed for all users, in Program Files, to run as a service: " +
	"the service runs under a system account, and a copy an ordinary user can replace " +
	"would hand that account to whoever replaces it")

// CheckLocation reports whether exe may be installed as a service: it must sit
// where only administrators can write. A portable copy, or one installed for the
// current user only, may not.
func CheckLocation(exe string) error {
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86} {
		dir, err := windows.KnownFolderPath(id, 0)
		if err == nil && isUnder(exe, dir) {
			return nil
		}
	}
	return ErrLocation
}

func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
