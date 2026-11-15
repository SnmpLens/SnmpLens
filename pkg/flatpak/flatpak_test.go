package flatpak

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheSandboxIsTheInfoFileNotTheEnvironment(t *testing.T) {
	saved := infoFile
	t.Cleanup(func() { infoFile = saved })

	infoFile = filepath.Join(t.TempDir(), ".flatpak-info")
	t.Setenv("FLATPAK_ID", AppID)
	if Sandboxed() {
		t.Fatal("FLATPAK_ID alone made this a sandbox; an inherited variable must not")
	}

	if err := os.WriteFile(infoFile, []byte("[Application]\nname="+AppID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Sandboxed() {
		t.Fatal("the info file is there and this is not a sandbox")
	}
}

func TestTheIDIsWhatTheSandboxWasStartedAs(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	if got := ID(); got != AppID {
		t.Errorf("with no FLATPAK_ID, ID() = %q, want %q", got, AppID)
	}
	t.Setenv("FLATPAK_ID", "io.example.Other")
	if got := ID(); got != "io.example.Other" {
		t.Errorf("ID() = %q, want the FLATPAK_ID it was started as", got)
	}
}
