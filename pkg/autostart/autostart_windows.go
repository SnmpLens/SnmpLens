//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey is the per-user startup list. HKCU rather than HKLM on purpose: the
// machine-wide equivalent needs elevation, and an SNMP browser has no business
// asking for administrator rights to tick a checkbox.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

var errUnsupported = errors.New("autostart: not supported on this platform")

func supported() bool { return true }

// keepRunning: only a Flatpak asks the desktop to keep running with no window.
func keepRunning() (bool, error) { return false, errUnsupported }

func location() string { return `HKEY_CURRENT_USER\` + runKey }

// command is the value written to the registry.
//
// The path is quoted because Program Files contains a space: unquoted, Windows
// would try to run "C:\Program" and pass the rest as arguments.
func command() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the executable: %w", err)
	}
	// Resolve symlinks so the entry survives the original link being replaced.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return `"` + exe + `"`, nil
}

func isEnabled() (bool, string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("open %s: %w", location(), err)
	}
	defer k.Close()

	value, _, err := k.GetStringValue(AppName)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("read the %s value: %w", AppName, err)
	}
	return strings.TrimSpace(value) != "", value, nil
}

func enable() error {
	cmd, err := command()
	if err != nil {
		return err
	}
	// CreateKey rather than OpenKey: the Run key exists on every normal
	// installation, but a hardened or freshly-provisioned profile may not have
	// it yet, and failing there would be a confusing dead end.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open %s for writing: %w", location(), err)
	}
	defer k.Close()

	if err := k.SetStringValue(AppName, cmd); err != nil {
		return fmt.Errorf("write the %s value: %w", AppName, err)
	}
	return nil
}

func disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil // nothing registered; already the desired state
		}
		return fmt.Errorf("open %s for writing: %w", location(), err)
	}
	defer k.Close()

	if err := k.DeleteValue(AppName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("remove the %s value: %w", AppName, err)
	}
	return nil
}
