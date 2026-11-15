package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"SnmpLens/pkg/flatpak"
)

// The desktop matches a window to its launcher and icon by the name the window
// goes by against the desktop file's name or its StartupWMClass. A rename on
// either side matches nothing, with no error anywhere: the window gets a
// generic icon and its own entry in the dock.
func TestEachDesktopFileNamesTheWindowItLaunches(t *testing.T) {
	cases := []struct {
		file, name string
	}{
		{filepath.Join("build", "linux", "snmplens.desktop"), linuxProgramName},
		{filepath.Join("build", "linux", "flatpak", flatpak.AppID+".desktop"), flatpak.AppID},
	}
	for _, c := range cases {
		entry := readDesktopEntry(t, c.file)
		if got := entry["StartupWMClass"]; got != c.name {
			t.Errorf("%s: StartupWMClass=%q, but the window goes by %q", c.file, got, c.name)
		}
	}
	// The .deb's file is named after the program; the Flatpak's after the ID.
	if base := filepath.Base(cases[0].file); base != linuxProgramName+".desktop" {
		t.Errorf("the .deb's desktop file is %s, not %s.desktop", base, linuxProgramName)
	}
}

// The two files say the same thing in every language, and only where to find
// the program differs: a translation added to one and not the other is a
// launcher that reads differently depending on how SnmpLens was installed.
func TestTheDesktopFilesDifferOnlyInWhereTheProgramIs(t *testing.T) {
	deb := readDesktopEntry(t, filepath.Join("build", "linux", "snmplens.desktop"))
	pak := readDesktopEntry(t, filepath.Join("build", "linux", "flatpak", flatpak.AppID+".desktop"))
	where := map[string]bool{"Exec": true, "Icon": true, "StartupWMClass": true}

	keys := map[string]bool{}
	for k := range deb {
		keys[k] = true
	}
	for k := range pak {
		keys[k] = true
	}
	var differ []string
	for k := range keys {
		if !where[k] && deb[k] != pak[k] {
			differ = append(differ, k)
		}
	}
	sort.Strings(differ)
	if len(differ) > 0 {
		t.Errorf("the two desktop files disagree on %v", differ)
	}
}

func readDesktopEntry(t *testing.T, rel string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	entry := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			entry[k] = v
		}
	}
	return entry
}
