package mib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sleepinggenius2/gosmi"
)

const userOfAddedMib = `EXAMPLE-USER-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, Integer32 FROM SNMPv2-SMI
    exampleAdded FROM EXAMPLE-ADDED-MIB;
exampleUserValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A value under a node an added folder defines."
    ::= { exampleAdded 1 }
END
`

const addedMib = `EXAMPLE-ADDED-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
exampleAdded OBJECT IDENTIFIER ::= { enterprises 99978 }
END
`

// libraryDirs is a MIB folder holding the bundled set, and an added folder.
func libraryDirs(t *testing.T) (primary, added string) {
	t.Helper()
	root := t.TempDir()
	primary, added = filepath.Join(root, "mibs"), filepath.Join(root, "vendor folder")
	for _, d := range []string{primary, added} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir("../../mibs")
	if err != nil {
		t.Skip("no corpus")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join("../../mibs", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(primary, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return primary, added
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file in a folder added in the settings loads, by its name, and a module in
// the MIB folder importing what it defines resolves. It loaded nothing: the
// names were looked up in the MIB folder.
func TestAnAddedFolderLoads(t *testing.T) {
	primary, added := libraryDirs(t)
	write(t, primary, "EXAMPLE-USER-MIB", userOfAddedMib)
	write(t, added, "product-added.txt", addedMib)

	gosmi.Exit()
	InitPath(primary)
	s := NewService(primary)
	if got := s.SetDirectories([]string{added}); len(got) != 1 {
		t.Fatalf("the added folder was not taken: %v", got)
	}
	res := s.LoadWithDiagnostics([]string{"SNMPv2-SMI", "product-added.txt", "EXAMPLE-USER-MIB"})
	for _, r := range res.Diagnostics {
		if !r.Success {
			t.Errorf("%s: %s", r.FileName, r.Error)
		}
	}
	if got := s.ResolveOid("1.3.6.1.4.1.99978.1").Name; got != "exampleUserValue" {
		t.Errorf("the value under the added module resolves to %q", got)
	}
	if d := s.Diagnose("product-added.txt"); d.Stage != StageLoaded && d.Stage != "" {
		t.Errorf("a file of an added folder is diagnosed where it is: %+v", d)
	}
}

// Of two files of one name, the MIB folder's is the one loaded, and the added
// folder's is listed as shadowed. What is not a MIB in an added folder is not
// shown to gosmi at all.
func TestAnAddedFolderIsGated(t *testing.T) {
	primary, added := libraryDirs(t)
	write(t, primary, "EXAMPLE-ADDED-MIB", addedMib)
	write(t, added, "EXAMPLE-ADDED-MIB", "EXAMPLE-ADDED-MIB DEFINITIONS ::= BEGIN\nEND\n")
	write(t, added, "manual.pdf", "%PDF-1.7\n%binary")
	write(t, added, "id_ed25519", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n")

	s := NewService(primary)
	s.SetDirectories([]string{added})
	lib := s.library()
	if p, _ := lib.path("EXAMPLE-ADDED-MIB"); filepath.Dir(p) != filepath.Clean(primary) {
		t.Errorf("the MIB folder's file should win, got %s", p)
	}
	if first := s.Shadowed()[filepath.Join(added, "EXAMPLE-ADDED-MIB")]; filepath.Dir(first) != filepath.Clean(primary) {
		t.Errorf("the shadowed file is not listed with the one loaded: %v", s.Shadowed())
	}
	for _, name := range []string{"manual.pdf", "id_ed25519"} {
		if _, ok := lib.path(name); ok {
			t.Errorf("%s is not a MIB and was shown to gosmi", name)
		}
	}
}

func TestOnlyFoldersAreAdded(t *testing.T) {
	primary, added := libraryDirs(t)
	write(t, added, "x.mib", addedMib)
	s := NewService(primary)
	got := s.SetDirectories([]string{"relative/dir", filepath.Join(added, "x.mib"), filepath.Join(added, "nope"),
		primary, added, added + string(filepath.Separator), ""})
	if len(got) != 1 || got[0] != filepath.Clean(added) {
		t.Errorf("taken: %v", got)
	}
}
