package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OpenFolder is reachable from the renderer, and the file managers it calls
// OPEN a file they are handed — run it, for an executable. Only a directory
// gets through.
func TestOpenFolderAcceptsOnlyADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(file, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := folderToOpen(dir + string(filepath.Separator)); err != nil || got != filepath.Clean(dir) {
		t.Errorf("a directory was refused: %q %v", got, err)
	}
	for name, path := range map[string]string{
		"a file":          file,
		"a relative path": "mibs",
		"nothing":         "  ",
		"a missing path":  filepath.Join(dir, "gone"),
	} {
		if _, err := folderToOpen(path); err == nil {
			t.Errorf("%s was accepted: %q", name, path)
		}
	}
}

func TestFolderCommandNeverLaunchesAMacBundle(t *testing.T) {
	if got := strings.Join(folderCommand("darwin", "/Users/x/.config/SnmpLens/mibs"), " "); got != "open /Users/x/.config/SnmpLens/mibs" {
		t.Errorf("a plain folder: %s", got)
	}
	for _, bundle := range []string{"/Applications/Evil.app", "/tmp/x.App/", "/Library/Thing.prefPane"} {
		if argv := folderCommand("darwin", bundle); len(argv) != 3 || argv[1] != "-R" {
			t.Errorf("%s would be launched: %v", bundle, argv)
		}
	}
	if argv := folderCommand("windows", `C:\mibs`); argv[0] != "explorer.exe" || argv[1] != `C:\mibs` {
		t.Errorf("windows: %v", argv)
	}
	if argv := folderCommand("linux", "/home/x/mibs"); argv[0] != "xdg-open" || len(argv) != 2 {
		t.Errorf("linux: %v", argv)
	}
}
