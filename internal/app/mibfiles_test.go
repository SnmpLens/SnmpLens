package app

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeMib(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const vendorMib = `EXAMPLE-VENDOR-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI
    DisplayString FROM SNMPv2-TC
    exampleBase FROM EXAMPLE-BASE-MIB
    exampleGone FROM EXAMPLE-GONE-MIB;
exampleVendor OBJECT IDENTIFIER ::= { exampleBase 1 }
END
`

const baseMib = `EXAMPLE-BASE-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
exampleBase OBJECT IDENTIFIER ::= { enterprises 99979 }
END
`

// Renaming moves the draft with the file, and refuses what would lose
// something: an existing name, a standard MIB's name, a path out of the
// directory.
func TestRenamingAMib(t *testing.T) {
	a, dir := newImportApp(t)
	writeMib(t, dir, "vendor.txt", vendorMib)
	writeMib(t, dir, "other.txt", baseMib)
	if err := a.MibEditorSaveDraft("vendor.txt", "unsaved work"); err != nil {
		t.Fatal(err)
	}

	if err := a.MibEditorRenameFile("vendor.txt", "EXAMPLE-VENDOR-MIB"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "EXAMPLE-VENDOR-MIB")); err != nil {
		t.Error("not renamed")
	}
	if d, _ := a.MibEditorReadDraft("EXAMPLE-VENDOR-MIB"); d != "unsaved work" {
		t.Errorf("the draft did not follow: %q", d)
	}
	if d, _ := a.MibEditorReadDraft("vendor.txt"); d != "" {
		t.Error("the old draft is left behind")
	}

	for _, tc := range []struct{ from, to, why string }{
		{"EXAMPLE-VENDOR-MIB", "other.txt", "an existing file"},
		{"EXAMPLE-VENDOR-MIB", "IF-MIB", "a standard MIB's name"},
		{"EXAMPLE-VENDOR-MIB", "../monitoring.db", "a path out of the directory"},
		{"EXAMPLE-VENDOR-MIB", `sub\x.txt`, "a path inside it"},
		{"EXAMPLE-VENDOR-MIB", ".hidden", "a name the list never shows"},
		{"EXAMPLE-VENDOR-MIB", "  ", "no name"},
		{"missing.txt", "x.txt", "a file that is not there"},
	} {
		if err := a.MibEditorRenameFile(tc.from, tc.to); err == nil {
			t.Errorf("renaming to %s was allowed", tc.why)
		}
	}
	writeMib(t, dir, "IF-MIB", "IF-MIB DEFINITIONS ::= BEGIN\nEND\n")
	if err := a.MibEditorRenameFile("IF-MIB", "renamed"); err == nil {
		t.Error("a standard MIB was renamed")
	}
	// Only the case: the same file on a case-insensitive disk, and allowed.
	if err := a.MibEditorRenameFile("other.txt", "OTHER.txt"); err != nil {
		t.Errorf("a change of case: %v", err)
	}
}

func TestDuplicatingAMib(t *testing.T) {
	a, dir := newImportApp(t)
	writeMib(t, dir, "base.txt", baseMib)
	if err := a.MibEditorDuplicateFile("base.txt", "base copy.txt"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "base copy.txt"))
	if err != nil || string(raw) != baseMib {
		t.Errorf("the copy: %q, %v", raw, err)
	}
	if err := a.MibEditorDuplicateFile("base.txt", "base copy.txt"); err == nil {
		t.Error("a copy over an existing file")
	}
	if err := a.MibEditorDuplicateFile("base.txt", "SNMPv2-SMI"); err == nil {
		t.Error("a copy under a standard MIB's name")
	}
}

// Deleting moves the file to mib-backups/; it is listed as deleted until it
// is restored, and comes back as it was.
func TestADeletedMibComesBack(t *testing.T) {
	a, dir := newImportApp(t)
	writeMib(t, dir, "base.txt", baseMib)
	writeMib(t, dir, "SNMPv2-SMI", "SNMPv2-SMI DEFINITIONS ::= BEGIN\nEND\n")
	if err := a.MibEditorSaveDraft("base.txt", "draft"); err != nil {
		t.Fatal(err)
	}

	moved, err := a.MibEditorDeleteFiles([]string{"base.txt", "SNMPv2-SMI"})
	if err == nil || !strings.Contains(err.Error(), "SNMPv2-SMI") {
		t.Errorf("a standard MIB should be refused, by name: %v", err)
	}
	if len(moved) != 1 || moved[0] != "base.txt" {
		t.Errorf("moved %v", moved)
	}
	if _, err := os.Stat(filepath.Join(dir, "base.txt")); !os.IsNotExist(err) {
		t.Error("still in the directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "SNMPv2-SMI")); err != nil {
		t.Error("the standard MIB went")
	}
	if d, _ := a.MibEditorReadDraft("base.txt"); d != "" {
		t.Error("the draft of a deleted file is left")
	}

	deleted, err := a.MibEditorDeletedFiles()
	if err != nil || len(deleted) != 1 || deleted[0].Name != "base.txt" || !deleted[0].Deleted {
		t.Fatalf("deleted: %+v, %v", deleted, err)
	}
	if content, err := a.MibEditorReadBackup(deleted[0].ID); err != nil || content != baseMib {
		t.Errorf("the kept copy: %q, %v", content, err)
	}
	name, err := a.MibEditorRestoreDeleted(deleted[0].ID)
	if err != nil || name != "base.txt" {
		t.Fatalf("restored %q, %v", name, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "base.txt")); string(raw) != baseMib {
		t.Error("not restored as it was")
	}
	if left, _ := a.MibEditorDeletedFiles(); len(left) != 0 {
		t.Errorf("still listed as deleted: %+v", left)
	}
}

// A save's backup and a deletion are both copies of one file, newest first.
func TestTheCopiesOfAMib(t *testing.T) {
	a, dir := newImportApp(t)
	writeMib(t, dir, "base.txt", baseMib)
	if _, err := a.MibEditorSave("base.txt", baseMib+"-- edited\n", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.MibEditorDeleteFiles([]string{"base.txt"}); err != nil {
		t.Fatal(err)
	}
	copies, err := a.MibEditorBackups("base.txt")
	if err != nil || len(copies) != 2 {
		t.Fatalf("copies: %+v, %v", copies, err)
	}
	if !copies[0].Deleted || copies[1].Deleted || copies[0].SavedAt < copies[1].SavedAt {
		t.Errorf("newest first, the deletion last made: %+v", copies)
	}
	if content, _ := a.MibEditorReadBackup(copies[1].ID); content != baseMib {
		t.Errorf("the save's backup holds what it replaced: %q", content)
	}
}

// A copy is asked for by its own name and nothing else: the bridge must not
// read a file because the renderer named it.
func TestOnlyAKeptCopyIsRead(t *testing.T) {
	a, _ := newImportApp(t)
	for _, id := range []string{"../monitoring.db", "x.123.bak/../../service.json", "base.txt", "base.txt.12.bak", ""} {
		if _, err := a.MibEditorReadBackup(id); err == nil {
			t.Errorf("%q was read", id)
		}
		if _, err := a.MibEditorRestoreDeleted(id); err == nil {
			t.Errorf("%q was restored", id)
		}
	}
}

// An export carries what the MIB imports, transitively, and says what the
// directory does not hold.
func TestAnExportCarriesItsDependencies(t *testing.T) {
	a, dir := newImportApp(t)
	writeMib(t, dir, "vendor.txt", vendorMib)
	writeMib(t, dir, "base.mib", baseMib)
	for _, name := range []string{"SNMPv2-SMI", "SNMPv2-TC"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "mibs", name))
		if err != nil {
			t.Skip("no bundled MIBs")
		}
		writeMib(t, dir, name, string(raw))
	}

	files, missing, err := a.exportClosure([]string{"vendor.txt"})
	if err != nil {
		t.Fatal(err)
	}
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	if files[0] != "vendor.txt" || strings.Join(sorted, ",") != "SNMPv2-SMI,SNMPv2-TC,base.mib,vendor.txt" {
		t.Errorf("files %v", files)
	}
	if strings.Join(missing, ",") != "EXAMPLE-GONE-MIB" {
		t.Errorf("missing %v", missing)
	}
	if _, _, err := a.exportClosure([]string{"../monitoring.db"}); err == nil {
		t.Error("a path out of the directory was exported")
	}

	var buf bytes.Buffer
	if err := a.writeMibZip(&buf, files); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(sorted, ",") {
		t.Errorf("the archive holds %v", names)
	}
}

// A file is shown selected in its folder, never opened.
func TestRevealNeverOpensTheFile(t *testing.T) {
	path := filepath.Join("C:", "x", "IF-MIB")
	for goos, want := range map[string]string{
		"windows": "explorer.exe /select," + path,
		"darwin":  "open -R " + path,
		"linux":   "xdg-open " + filepath.Dir(path),
	} {
		if got := strings.Join(revealCommand(goos, path), " "); got != want {
			t.Errorf("%s: %s", goos, got)
		}
	}
}
