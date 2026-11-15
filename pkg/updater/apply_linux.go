//go:build linux

package updater

import (
	"os"

	"SnmpLens/pkg/flatpak"
)

const (
	linuxDebAsset     = "SnmpLens-linux-amd64.deb"
	linuxRPMAsset     = "SnmpLens-linux-amd64.rpm"
	linuxFlatpakAsset = "SnmpLens-linux-amd64.flatpak"
	linuxBinaryAsset  = "SnmpLens-linux-amd64"
	// packageInstallPath is where the .deb and the .rpm both install the
	// binary (see the release workflow). A copy running from there is
	// system-managed.
	packageInstallPath = "/usr/local/bin/snmplens"
	// dpkgRecord is the file list dpkg keeps for the installed package. The two
	// packages install the same path, so the path cannot say which one put it
	// there, and sending an rpm system to the .deb is a download that installs
	// nothing.
	dpkgRecord = "/var/lib/dpkg/info/snmplens.list"
)

// target self-replaces a portable binary, but defers package installs to the
// browser since replacing a system-managed file needs root, and a Flatpak's,
// since /app is read-only.
func target() (string, applyMode) {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	_, statErr := os.Stat(dpkgRecord)
	return targetFor(exe, flatpak.Sandboxed(), statErr == nil)
}

func targetFor(exe string, sandboxed, dpkgOwned bool) (string, applyMode) {
	switch {
	case sandboxed:
		return linuxFlatpakAsset, applyBrowser
	case exe == packageInstallPath && dpkgOwned:
		return linuxDebAsset, applyBrowser
	case exe == packageInstallPath:
		return linuxRPMAsset, applyBrowser
	}
	return linuxBinaryAsset, applyReplace
}
