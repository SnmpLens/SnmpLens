//go:build !windows && !darwin

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"SnmpLens/pkg/flatpak"
)

// desktopFile is the XDG autostart entry name.
const desktopFile = "snmplens.desktop"

var errUnsupported = errors.New("autostart: not supported on this platform")

// sandboxed is flatpak.Sandboxed, a variable so a test can be either.
var sandboxed = flatpak.Sandboxed

func supported() bool { return true }

// autostartDir follows the XDG base directory specification: $XDG_CONFIG_HOME
// when set, ~/.config otherwise. Every mainstream desktop reads it.
//
// Not in a Flatpak: the sandbox points $XDG_CONFIG_HOME at ~/.var/app, where
// no desktop looks, so an entry written there is a login item that never runs.
// The Background portal writes the entry instead (autostart_portal.go), into
// the host's ~/.config/autostart under the application ID, and that file is
// what is read back.
func autostartDir() string {
	if sandboxed() {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "autostart")
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "autostart")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "autostart")
}

func entryPath() string {
	dir := autostartDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, entryName())
}

// entryName is the file the entry is in: ours, or the portal's, which names it
// after the application ID.
func entryName() string {
	if sandboxed() {
		return flatpak.ID() + ".desktop"
	}
	return desktopFile
}

func location() string {
	if p := entryPath(); p != "" {
		return p
	}
	return "~/.config/autostart/" + entryName()
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func isEnabled() (bool, string, error) {
	path := entryPath()
	if path == "" {
		return false, "", fmt.Errorf("cannot determine the configuration directory")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("read %s: %w", path, err)
	}
	// Every line is read before deciding: a key's place in the file means
	// nothing, and a desktop that appends Hidden=true after Exec= — as one
	// rewriting the entry with its own keys last does — was read as enabled.
	exec, hidden := "", false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		// A desktop entry can be present but switched off by the desktop's own
		// startup-applications UI, which writes this key rather than deleting
		// the file. Reporting it as enabled would contradict what the user
		// sees in their own settings.
		if strings.EqualFold(line, "Hidden=true") {
			hidden = true
		}
		if after, ok := strings.CutPrefix(line, "Exec="); ok && exec == "" {
			exec = after
		}
	}
	if hidden {
		return false, "", nil
	}
	return true, exec, nil
}

func enable() error {
	if sandboxed() {
		return requestAutostart(true)
	}
	path := entryPath()
	if path == "" {
		return fmt.Errorf("cannot determine the configuration directory")
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	// Exec is not quoted the way a shell would quote it: the desktop entry
	// specification uses its own escaping, where a literal space inside an
	// argument is written as a double-quoted field.
	execLine := exe
	if strings.ContainsAny(exe, " \t") {
		execLine = `"` + strings.ReplaceAll(exe, `"`, `\"`) + `"`
	}

	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=SnmpLens
Comment=SNMP MIB browser and network monitoring
Exec=%s
Terminal=false
X-GNOME-Autostart-enabled=true
`, execLine)

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(entry), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

func disable() error {
	if sandboxed() {
		return requestAutostart(false)
	}
	path := entryPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
