package app

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"SnmpLens/pkg/flatpak"
)

// What would give a desktop a tray when none answered. Only GNOME is worth
// naming: it is the desktop with none by default, and the remedy is one
// extension — which Ubuntu installs, and a user can switch off without knowing
// that background mode stands on it.
const (
	trayHintGnomeOff     = "gnome-extension-off"
	trayHintGnomeMissing = "gnome-extension-missing"
)

// appIndicatorExtensions are the extensions that give GNOME a tray, by UUID,
// with the name the Extensions application shows.
var appIndicatorExtensions = []struct{ uuid, name string }{
	{"ubuntu-appindicators@ubuntu.com", "Ubuntu AppIndicators"},
	{"appindicatorsupport@rgcjonas.gmail.com", "AppIndicator and KStatusNotifierItem Support"},
}

// trayHint says what would give this desktop a tray, and the extension's name
// when one is installed and off. Called when no tray answered; with one
// installed, it is switched off — or, rarely, does not run on this GNOME,
// which the Extensions application shows as well.
func trayHint(goos, desktop string, extensionDirs []string) (hint, extension string) {
	if goos != "linux" || !strings.Contains(strings.ToUpper(desktop), "GNOME") {
		return "", ""
	}
	for _, dir := range extensionDirs {
		for _, ext := range appIndicatorExtensions {
			if fi, err := os.Stat(filepath.Join(dir, ext.uuid)); err == nil && fi.IsDir() {
				return trayHintGnomeOff, ext.name
			}
		}
	}
	return trayHintGnomeMissing, ""
}

// gnomeExtensionDirs are where GNOME Shell looks for extensions: the system's
// and the user's. A Flatpak sees the system's under /run/host
// (--filesystem=host-os) and the user's in its home.
func gnomeExtensionDirs() []string {
	system := "/usr/share/gnome-shell/extensions"
	if flatpak.Sandboxed() {
		system = "/run/host" + system
	}
	dirs := []string{system}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "gnome-shell", "extensions"))
	}
	return dirs
}

// currentTrayHint is trayHint for this process, or nothing when a tray answered.
func (a *App) currentTrayHint() (string, string) {
	if a.trayLive {
		return "", ""
	}
	return trayHint(goruntime.GOOS, os.Getenv("XDG_CURRENT_DESKTOP"), gnomeExtensionDirs())
}
