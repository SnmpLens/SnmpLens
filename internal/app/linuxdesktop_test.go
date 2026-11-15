package app

import (
	"os"
	"path/filepath"
	"testing"

	"SnmpLens/pkg/portal"
	"SnmpLens/pkg/service"
)

func TestTheDesktopDecidesTheVariantOnlyWhenTheSettingsLetIt(t *testing.T) {
	cases := []struct {
		theme  string
		scheme portal.ColorScheme
		dark   bool
	}{
		{"system", portal.PreferDark, true},
		{"system", portal.PreferLight, false},
		// No preference stated: GTK keeps its theme's own.
		{"system", portal.NoPreference, false},
		// Before the renderer has said anything, it is "system".
		{"", portal.PreferDark, true},
		// An explicit theme wins, for the title bar as for the content.
		{"light", portal.PreferDark, false},
		{"dark", portal.PreferLight, true},
	}
	for _, c := range cases {
		if got := preferDark(c.theme, c.scheme); got != c.dark {
			t.Errorf("theme %q, desktop %d: dark = %v, want %v", c.theme, c.scheme, got, c.dark)
		}
	}
}

func TestOnGnomeTheMissingTrayIsNamed(t *testing.T) {
	system, user := t.TempDir(), t.TempDir()
	dirs := []string{system, user}

	if hint, _ := trayHint("linux", "KDE", dirs); hint != "" {
		t.Errorf("KDE has a tray of its own, and was given hint %q", hint)
	}
	if hint, _ := trayHint("windows", "GNOME", dirs); hint != "" {
		t.Errorf("not Linux, and given hint %q", hint)
	}
	if hint, _ := trayHint("linux", "ubuntu:GNOME", dirs); hint != trayHintGnomeMissing {
		t.Errorf("GNOME with no extension: hint %q, want %q", hint, trayHintGnomeMissing)
	}

	// Installed — by Ubuntu, in the system's folder — and no tray answered:
	// it is switched off, and its name is what the Extensions app shows.
	if err := os.MkdirAll(filepath.Join(system, "ubuntu-appindicators@ubuntu.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	hint, name := trayHint("linux", "ubuntu:GNOME", dirs)
	if hint != trayHintGnomeOff || name != "Ubuntu AppIndicators" {
		t.Errorf("installed and off: got (%q, %q)", hint, name)
	}
}

func TestOnlyFoldersHoldingFilesAreOffered(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	full := filepath.Join(root, "usr", "share", "snmp", "mibs")
	empty := filepath.Join(root, "usr", "share", "snmp", "mibs", "ietf")
	user := filepath.Join(home, ".snmp", "mibs")
	for _, dir := range []string{full, empty, user} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(full, "IF-MIB.txt"), filepath.Join(full, "NET-SNMP-MIB.txt"), filepath.Join(user, "VENDOR-MIB")} {
		if err := os.WriteFile(f, []byte("X DEFINITIONS ::= BEGIN END"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A dot file is not a MIB, here as in the list the settings show.
	if err := os.WriteFile(filepath.Join(empty, ".keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := findMibDirectories([]string{full, empty, filepath.Join(root, "missing"), "~/.snmp/mibs"}, home, false)
	want := []SystemMibDirectory{{Path: full, Files: 2}, {Path: user, Files: 1}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %+v, want %+v", got[i], want[i])
		}
	}
}

// In a Flatpak the host's /usr is under /run/host, and that is the path that
// loads; the user's folder is the same path inside and out.
func TestInAFlatpakTheHostsUsrIsUnderRunHost(t *testing.T) {
	got := findMibDirectories([]string{"/usr/share/snmp/mibs"}, "", true)
	for _, d := range got {
		if d.Path != "/run/host/usr/share/snmp/mibs" {
			t.Errorf("offered %q from inside a sandbox", d.Path)
		}
	}
}

// The window closes to somewhere only where there is a way back: a tray, or a
// Flatpak the desktop lets run in the background. Anywhere else closing quits.
func TestTheWindowHidesOnlyWhereItCanBeFoundAgain(t *testing.T) {
	cases := []struct {
		name              string
		background        bool
		tray, portalAllow bool
		hide              bool
	}{
		{"tray", true, true, false, true},
		{"flatpak, no tray, allowed", true, false, true, true},
		{"no tray, no permission", true, false, false, false},
		{"background mode off", false, true, true, false},
	}
	for _, c := range cases {
		a := &App{serviceCfg: service.Config{RunInBackground: c.background}, trayLive: c.tray, backgroundPortal: c.portalAllow}
		if got := a.hideInsteadOfClosing(); got != c.hide {
			t.Errorf("%s: hideInsteadOfClosing = %v, want %v", c.name, got, c.hide)
		}
	}
}
