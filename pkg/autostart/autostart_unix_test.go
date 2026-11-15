//go:build !windows && !darwin

package autostart

import (
	"os"
	"path/filepath"
	"testing"

	"SnmpLens/pkg/flatpak"
	"SnmpLens/pkg/portal"
)

// In the sandbox, $XDG_CONFIG_HOME is the sandbox's own and no desktop reads
// it. What is read back is the entry the portal wrote on the host, under the
// application ID — and a desktop that switched it off is believed, as it is
// for the entry written outside a sandbox.
func TestInAFlatpakTheEntryIsThePortalsOnTheHost(t *testing.T) {
	saved := sandboxed
	t.Cleanup(func() { sandboxed = saved })
	sandboxed = func() bool { return true }

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".var", "app", flatpak.AppID, "config"))
	t.Setenv("FLATPAK_ID", flatpak.AppID)

	want := filepath.Join(home, ".config", "autostart", flatpak.AppID+".desktop")
	st := Get()
	if st.Location != want {
		t.Fatalf("Location = %q, want the portal's entry %q", st.Location, want)
	}
	if st.Enabled {
		t.Fatal("no entry on the host, and autostart reads as enabled")
	}

	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	const portalExec = "flatpak run --command=snmplens " + flatpak.AppID
	entry := "[Desktop Entry]\nType=Application\nName=" + flatpak.AppID + "\nExec=" + portalExec + "\nX-Flatpak=" + flatpak.AppID + "\n"
	if err := os.WriteFile(want, []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Get(); !st.Enabled || st.Command != portalExec {
		t.Fatalf("the portal's entry is there and Get() = %+v", st)
	}

	if err := os.WriteFile(want, []byte(entry+"Hidden=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Get().Enabled {
		t.Fatal("the desktop switched the entry off and it still reads as enabled")
	}
}

// The portal's own codes are pkg/portal's; what is left here is that an
// answer is not a login entry until the desktop says it wrote one.
func TestAnAnswerWithoutALoginEntryIsNotEnabled(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		got     portal.Background
		wantErr bool
	}{
		{"granted", true, portal.Background{Background: true, Autostart: true}, false},
		{"background only", true, portal.Background{Background: true}, true},
		{"nothing", true, portal.Background{}, true},
		{"removed", false, portal.Background{Background: true}, false},
	}
	for _, c := range cases {
		if err := autostartGranted(c.enabled, c.got); (err != nil) != c.wantErr {
			t.Errorf("%s: autostartGranted = %v, want error %v", c.name, err, c.wantErr)
		}
	}
}
