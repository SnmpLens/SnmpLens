package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/mib"
)

// importSingleFile is one file through the whole import, for the tests that
// are about what happens to one file.
func (a *App) importSingleFile(src string) MibImportResult {
	return a.ImportMibFiles([]string{src})[0]
}

// A vendor MIB archive must not be able to replace a standard MIB.
//
// Vendor zips routinely ship their own SNMPv2-SMI, SNMPv2-TC and SNMPv2-CONF,
// and nearly every other MIB imports from those three. The import wrote with
// os.WriteFile unconditionally, and ensureStandardMibs restores only files
// that are ABSENT — so a replaced one is never repaired and the tree stays
// broken across restarts, with nothing saying why.
func TestImportRefusesToReplaceABundledMib(t *testing.T) {
	root := t.TempDir()
	mibDir := filepath.Join(root, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// mibs is the real embed.FS from main.go: the bundled list the check
	// consults is the one that actually ships.
	a := &App{persistentMibDir: mibDir, mibService: mib.NewService(mibDir), mibs: mibs}

	// The file an operator already has, and the copy the archive would put
	// over it. Different bytes, so the identical-file skip does not apply.
	good := []byte("SNMPv2-SMI DEFINITIONS ::= BEGIN\n-- the bundled copy\nEND\n")
	dst := filepath.Join(mibDir, "SNMPv2-SMI")
	if err := os.WriteFile(dst, good, 0o644); err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	hostile := filepath.Join(srcDir, "SNMPv2-SMI")
	if err := os.WriteFile(hostile, []byte("SNMPv2-SMI DEFINITIONS ::= BEGIN\n-- the vendor copy, truncated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := a.importSingleFile(hostile)
	if res.Success {
		t.Fatalf("the bundled SNMPv2-SMI was replaced: %+v", res)
	}
	// The refusal has to say what happened and what to do; this result goes
	// straight into the per-file list the import dialog shows.
	if !strings.Contains(res.Error, "SNMPv2-SMI") || !strings.Contains(res.Error, "MIB editor") {
		t.Errorf("the refusal does not explain itself: %q", res.Error)
	}

	// And the file on disk is untouched.
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(good) {
		t.Fatalf("the file was written anyway:\n%s", after)
	}
}

// Every bundled name is covered, not only the three obvious ones. The list is
// read from the embed rather than repeated here, so a MIB added to mibs/ is
// protected the day it is added.
func TestEveryBundledNameIsRefused(t *testing.T) {
	root := t.TempDir()
	mibDir := filepath.Join(root, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{persistentMibDir: mibDir, mibService: mib.NewService(mibDir), mibs: mibs}

	entries, err := fs.ReadDir(mibs, "mibs")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no bundled MIBs; this test proves nothing")
	}
	srcDir := t.TempDir()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(srcDir, e.Name())
		if err := os.WriteFile(p, []byte("REPLACEMENT DEFINITIONS ::= BEGIN\nEND\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if res := a.importSingleFile(p); res.Success {
			t.Errorf("%s was importable over the bundled copy", e.Name())
		}
	}
}

// The detector: a MIB that is NOT bundled must still import, or the fix would
// have broken the feature it is protecting.
func TestAVendorMibStillImports(t *testing.T) {
	root := t.TempDir()
	mibDir := filepath.Join(root, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{persistentMibDir: mibDir, mibService: mib.NewService(mibDir), mibs: mibs}

	srcDir := t.TempDir()
	p := filepath.Join(srcDir, "ACME-POE-MIB")
	if err := os.WriteFile(p, []byte("ACME-POE-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := a.importSingleFile(p)
	if !res.Success || res.Skipped {
		t.Fatalf("a vendor MIB was refused: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(mibDir, "ACME-POE-MIB")); err != nil {
		t.Fatalf("it was not written: %v", err)
	}
}

// writeTree writes files under root, creating their folders.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func boxMib(date, extra string) string {
	return `EXAMPLE-BOX-MIB DEFINITIONS ::= BEGIN
IMPORTS MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI;
exampleBox MODULE-IDENTITY
    LAST-UPDATED "` + date + `"
    ORGANIZATION "Example"
    CONTACT-INFO "nobody"
    DESCRIPTION  "A box."
    ::= { enterprises 99990 }
exampleUp OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Seconds up."
    ::= { exampleBox 1 }
` + extra + `END
`
}

func resultsByName(results []MibImportResult) map[string]MibImportResult {
	out := map[string]MibImportResult{}
	for _, r := range results {
		out[r.FileName] = r
	}
	return out
}

// A folder holding a release per sub-folder, the same file name in each.
// The import kept whichever was written last and reported every one of them
// as imported.
func TestAFolderOfReleasesKeepsOneVersionAndSaysWhich(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	newest := boxMib("202304010000Z", "")
	writeTree(t, src, map[string]string{
		"vendor/1.0/box.mib": boxMib("201901010000Z", ""),
		"vendor/2.0/box.mib": newest,
		"vendor/box.mib":     newest,
	})

	r := resultsByName(a.ImportMibFiles([]string{filepath.Join(src, "vendor")}))
	kept := r["vendor/box.mib"]
	if !kept.Success || kept.Skipped || kept.StoredAs != "EXAMPLE-BOX-MIB.mib" {
		t.Fatalf("the top-level copy: %+v", kept)
	}
	if kept.Outcome != ImportOutcomeImported || kept.Date != "2023-04-01" {
		t.Errorf("the kept one: outcome=%q date=%q", kept.Outcome, kept.Date)
	}
	if old := r["vendor/1.0/box.mib"]; !old.Skipped || old.Outcome != ImportOutcomeSetAside || old.Reason != mib.LostOlder ||
		old.KeptFrom != "vendor/box.mib" || old.Date != "2019-01-01" || !strings.Contains(old.Note, "newer") {
		t.Errorf("the older release: %+v", old)
	}
	if same := r["vendor/2.0/box.mib"]; !same.Skipped || same.Reason != mib.LostIdentical || !strings.Contains(same.Note, "identical") {
		t.Errorf("the identical copy: %+v", same)
	}

	names, _ := mib.ListMibFiles(mibDir)
	if len(names) != 1 || names[0] != "EXAMPLE-BOX-MIB.mib" {
		t.Fatalf("the directory holds %v", names)
	}
	if got, _ := os.ReadFile(filepath.Join(mibDir, names[0])); string(got) != newest {
		t.Error("the stored version is not the newest")
	}
}

// Two different modules shipped under one file name, in two folders: the
// second overwrote the first.
func TestTwoModulesUnderOneFileNameAreBothKept(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	module := func(name string) string {
		return name + " DEFINITIONS ::= BEGIN\nIMPORTS enterprises FROM SNMPv2-SMI;\nroot OBJECT IDENTIFIER ::= { enterprises 99989 }\nEND\n"
	}
	writeTree(t, src, map[string]string{
		"pack/a/mib.txt": module("EXAMPLE-A-MIB"),
		"pack/b/mib.txt": module("EXAMPLE-B-MIB"),
	})
	for _, r := range a.ImportMibFiles([]string{filepath.Join(src, "pack")}) {
		if !r.Success || r.Skipped {
			t.Errorf("%s: %+v", r.FileName, r)
		}
	}
	for _, want := range []string{"EXAMPLE-A-MIB.txt", "EXAMPLE-B-MIB.txt"} {
		if _, err := os.Stat(filepath.Join(mibDir, want)); err != nil {
			t.Errorf("%s is missing: %v", want, err)
		}
	}
}

// A bundled module arriving under another file name went in beside the
// bundled one, and the module map being last-wins, replaced it.
func TestABundledModuleIsRefusedWhateverTheFileIsCalled(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"vendor-smi.txt": "SNMPv2-SMI DEFINITIONS ::= BEGIN\nEND\n",
	})
	res := a.importSingleFile(filepath.Join(src, "vendor-smi.txt"))
	if res.Success || res.Outcome != ImportOutcomeError || res.Reason != ImportErrorBundled ||
		!strings.Contains(res.Error, "SNMPv2-SMI ships with SnmpLens") {
		t.Fatalf("got %+v", res)
	}
	if names, _ := mib.ListMibFiles(mibDir); len(names) != 0 {
		t.Errorf("written anyway: %v", names)
	}
}

// Importing a module the directory already holds replaces it — under whatever
// name it was kept — and keeps the previous file in mib-backups.
func TestImportingAModuleAgainReplacesItWithABackup(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	older := boxMib("202001010000Z", "")
	// Kept under a product name, as the import used to keep everything.
	writeTree(t, mibDir, map[string]string{"example box 1.mib": older})
	writeTree(t, src, map[string]string{"box.mib": boxMib("202201010000Z", "")})

	res := a.importSingleFile(filepath.Join(src, "box.mib"))
	if !res.Success || res.Skipped || res.Outcome != ImportOutcomeReplaced || res.Replaced != "example box 1.mib" || res.StoredAs != "EXAMPLE-BOX-MIB.mib" {
		t.Fatalf("got %+v", res)
	}
	if names, _ := mib.ListMibFiles(mibDir); len(names) != 1 || names[0] != "EXAMPLE-BOX-MIB.mib" {
		t.Errorf("the directory holds %v", names)
	}
	backups, _ := os.ReadDir(filepath.Join(filepath.Dir(mibDir), mibBackupDirName))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	if got, _ := os.ReadFile(filepath.Join(filepath.Dir(mibDir), mibBackupDirName, backups[0].Name())); string(got) != older {
		t.Error("the backup is not the previous file")
	}

	// The same file again is a no-op, said as such.
	if again := a.importSingleFile(filepath.Join(src, "box.mib")); !again.Skipped || again.Outcome != ImportOutcomePresent {
		t.Errorf("a second identical import: %+v", again)
	}
}

// A Windows-1252 MIB whose first accented letter comes early was refused at
// the gate as "not valid UTF-8"; it loads, so it imports.
func TestAWindows1252MibImports(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	p := filepath.Join(src, "box.mib")
	body := "-- Bo\xeete \x96 version fran\xe7aise\n" + boxMib("202001010000Z", "")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := a.importSingleFile(p); !res.Success {
		t.Fatalf("refused: %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(mibDir, "EXAMPLE-BOX-MIB.mib")); string(got) != body {
		t.Error("the bytes were not kept as they were")
	}
}

// "Use this version" in the import report imports the version set aside, by
// the path the result gives back, in place of the one kept — which goes to
// mib-backups like any version replaced.
func TestAVersionSetAsideCanBeChosenInstead(t *testing.T) {
	a, mibDir := newImportApp(t)
	src := t.TempDir()
	older := boxMib("201901010000Z", "")
	writeTree(t, src, map[string]string{
		"pack/1.0/box.mib": older,
		"pack/2.0/box.mib": boxMib("202304010000Z", ""),
	})

	r := resultsByName(a.ImportMibFiles([]string{filepath.Join(src, "pack")}))
	kept, aside := r["pack/2.0/box.mib"], r["pack/1.0/box.mib"]
	if kept.Source != filepath.Join(src, "pack", "2.0", "box.mib") || aside.Source != filepath.Join(src, "pack", "1.0", "box.mib") {
		t.Fatalf("sources: kept %q, set aside %q", kept.Source, aside.Source)
	}
	if aside.Outcome != ImportOutcomeSetAside {
		t.Fatalf("the older one should be set aside: %+v", aside)
	}

	chosen := a.ImportMibFiles([]string{aside.Source})
	if len(chosen) != 1 || chosen[0].Outcome != ImportOutcomeReplaced || chosen[0].StoredAs != kept.StoredAs {
		t.Fatalf("choosing it: %+v", chosen)
	}
	if raw, _ := os.ReadFile(filepath.Join(mibDir, kept.StoredAs)); string(raw) != older {
		t.Error("the directory does not hold the version chosen")
	}
	copies, _ := a.MibEditorBackups(kept.StoredAs)
	if len(copies) != 1 {
		t.Errorf("the version it replaced should be kept: %+v", copies)
	}
}
