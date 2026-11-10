//go:build windows

package updater

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	windowsSetupAsset  = "SnmpLens-windows-amd64-setup.exe"
	windowsBinaryAsset = "SnmpLens-windows-amd64.exe"
)

// target picks the asset and apply strategy for Windows: the NSIS installer for
// installed copies, an in-place self-replace for the portable executable.
//
// The installer is what updates an all-users copy safely: it stops the Windows
// service that runs the same executable, replaces it, and starts the service
// again.
func target() (string, applyMode) {
	if isInstalled() {
		return windowsSetupAsset, applyInstaller
	}
	return windowsBinaryAsset, applyReplace
}

// isInstalled reports whether the RUNNING executable is the NSIS-installed copy,
// not merely that an install exists somewhere: a portable executable on a
// machine that also has an installed copy must replace itself, not launch the
// installer against the other copy — and an all-users installer, against the
// copy the service runs.
func isInstalled() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = real
	}
	return installedAt(filepath.Dir(exe), installLocations(), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"))
}

// installedAt is isInstalled's decision for the directory of the running
// executable. A per-user and an all-users copy can both be registered, so the
// executable is installed when it lives under any registered location; with
// none registered, a copy under Program Files is taken for installed.
func installedAt(dir string, registered []string, programFiles ...string) bool {
	if len(registered) > 0 {
		for _, loc := range registered {
			if pathUnder(dir, loc) {
				return true
			}
		}
		return false
	}
	for _, pf := range programFiles {
		if pf != "" && pathUnder(dir, pf) {
			return true
		}
	}
	return false
}

// installLocations returns the install directories the NSIS installer recorded:
// the per-user one (HKCU) and the all-users one (HKLM), whichever exist. Both
// can.
func installLocations() []string {
	const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\SnmpLens`
	var locs []string
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, uninstallKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if loc, _, err := k.GetStringValue("InstallLocation"); err == nil && loc != "" {
			locs = append(locs, loc)
		}
		k.Close()
	}
	return locs
}
