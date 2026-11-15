package app

import "SnmpLens/pkg/flatpak"

// linuxProgramName is the name the window goes by on Linux: GTK hands it to the
// compositor as the Wayland app_id and to X11 as the WM_CLASS, and the desktop
// matches it against a .desktop file to know which icon and launcher the
// window belongs to. Without it GTK used the executable's FILE name, so the
// .deb's window matched snmplens.desktop only because the binary happens to be
// called snmplens, and the tarball's SnmpLens matched nothing.
//
// It is the .deb's and the .rpm's desktop file (build/linux/snmplens.desktop),
// whose StartupWMClass says the same, and windowid_test.go holds the two
// together.
const linuxProgramName = "snmplens"

// programName is linuxProgramName, or in a Flatpak the application ID, which
// is what the sandbox's desktop file is named after.
func programName() string {
	if flatpak.Sandboxed() {
		return flatpak.ID()
	}
	return linuxProgramName
}
