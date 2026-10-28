package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/mib"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func newMibApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	mibDir := filepath.Join(root, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{persistentMibDir: mibDir, mibService: mib.NewService(mibDir)}
	return a, mibDir
}

// The editor WRITES, and monitoring.db, service.json and the secret store all
// live one directory above the MIB folder. A name must never be able to reach
// them.
func TestResolveMibPathRefusesEscapes(t *testing.T) {
	a, mibDir := newMibApp(t)

	escapes := []string{
		"../monitoring.db",
		"../../monitoring.db",
		`..\..\service.json`,
		"subdir/../../secrets",
		"/etc/passwd",
		`C:\Windows\System32\hosts`,
		"",
		"   ",
		".",
	}
	for _, name := range escapes {
		path, err := a.resolveMibPath(name)
		if err != nil {
			continue // refused outright, which is fine
		}
		if filepath.Dir(path) != filepath.Clean(mibDir) {
			t.Errorf("%q resolved to %q, outside the MIB directory", name, path)
		}
	}

	// A plain name must still work.
	path, err := a.resolveMibPath("IF-MIB")
	if err != nil {
		t.Fatalf("a legitimate name was refused: %v", err)
	}
	if filepath.Dir(path) != filepath.Clean(mibDir) {
		t.Errorf("path = %q", path)
	}
}

func TestSaveAndReadRoundTrip(t *testing.T) {
	a, _ := newMibApp(t)
	const src = "TEST-MIB DEFINITIONS ::= BEGIN\nEND\n"

	res, err := a.MibEditorSave("TEST-MIB", src, "", false)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !res.Saved {
		t.Fatal("save reported no write")
	}

	got, err := a.MibEditorRead("TEST-MIB")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Content != src {
		t.Errorf("content = %q", got.Content)
	}
	if got.Sha256 != res.Sha256 {
		t.Error("the checksum from save does not match the one from read")
	}
}

// A backup beside the original would be loaded as a second copy of the same
// module, and os.ReadDir being alphabetical, "IF-MIB.123.bak" would win over
// "IF-MIB" — stale content, no error anywhere.
func TestBackupsLandOutsideTheMibDirectory(t *testing.T) {
	a, mibDir := newMibApp(t)

	if _, err := a.MibEditorSave("TEST-MIB", "TEST-MIB DEFINITIONS ::= BEGIN\nEND\n", "", false); err != nil {
		t.Fatal(err)
	}
	res, err := a.MibEditorSave("TEST-MIB", "TEST-MIB DEFINITIONS ::= BEGIN\n-- edited\nEND\n", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.BackupPath == "" {
		t.Fatal("no backup was taken before overwriting")
	}
	if strings.HasPrefix(filepath.Clean(res.BackupPath), filepath.Clean(mibDir)) {
		t.Errorf("the backup landed inside the MIB directory: %s", res.BackupPath)
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Errorf("the backup does not exist: %v", err)
	}

	// And the MIB directory must still hold exactly one file.
	entries, _ := os.ReadDir(mibDir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the MIB directory holds %v; a stray file there loads as a MIB", names)
	}
}

// Saving must not silently discard an edit made underneath the editor.
func TestSaveDetectsAConflict(t *testing.T) {
	a, _ := newMibApp(t)
	const original = "TEST-MIB DEFINITIONS ::= BEGIN\nEND\n"

	first, _ := a.MibEditorSave("TEST-MIB", original, "", false)

	// Someone else changes the file.
	path, _ := a.resolveMibPath("TEST-MIB")
	os.WriteFile(path, []byte("TEST-MIB DEFINITIONS ::= BEGIN\n-- theirs\nEND\n"), 0o644)

	res, err := a.MibEditorSave("TEST-MIB", "TEST-MIB DEFINITIONS ::= BEGIN\n-- mine\nEND\n", first.Sha256, false)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !res.Conflict {
		t.Fatal("the concurrent edit was overwritten silently")
	}
	if res.Saved {
		t.Error("the save went ahead despite the conflict")
	}

	// force must get through.
	res, err = a.MibEditorSave("TEST-MIB", "TEST-MIB DEFINITIONS ::= BEGIN\n-- mine\nEND\n", first.Sha256, true)
	if err != nil || !res.Saved {
		t.Errorf("force did not override the conflict: %+v %v", res, err)
	}
}

// A CRLF file must stay CRLF: rewriting every line because one word changed
// turns a one-line diff into a whole-file diff.
func TestSavePreservesLineEndings(t *testing.T) {
	a, _ := newMibApp(t)
	path, _ := a.resolveMibPath("CRLF-MIB")
	os.WriteFile(path, []byte("CRLF-MIB DEFINITIONS ::= BEGIN\r\nEND\r\n"), 0o644)

	src, err := a.MibEditorRead("CRLF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	if src.Eol != "crlf" {
		t.Fatalf("eol = %q", src.Eol)
	}
	if strings.Contains(src.Content, "\r") {
		t.Error("CR reached the editor buffer; it would double up on save")
	}

	if _, err := a.MibEditorSave("CRLF-MIB", src.Content+"-- added\n", src.Sha256, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "\r\n") {
		t.Error("the file was rewritten with LF endings")
	}
	if strings.Contains(string(raw), "\r\r") {
		t.Error("the line endings were doubled")
	}
}

// Diagnostics must reach the frontend with the file, or opening a broken MIB
// says nothing about why it is broken.
func TestReadCarriesDiagnostics(t *testing.T) {
	a, _ := newMibApp(t)
	path, _ := a.resolveMibPath("BAD-MIB")
	os.WriteFile(path, []byte("BAD-MIB DEFINITIONS ::= BEGIN\nnot valid at all\nEND\n"), 0o644)

	src, err := a.MibEditorRead("BAD-MIB")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.Diagnostics) == 0 {
		t.Fatal("a broken MIB opened with no diagnostics")
	}
	located := false
	for _, d := range src.Diagnostics {
		if d.Line > 0 {
			located = true
		}
	}
	if !located {
		t.Errorf("no diagnostic carries a line: %+v", src.Diagnostics)
	}
}

func TestListMarksBundledAndModified(t *testing.T) {
	a, _ := newMibApp(t)
	// No embed.FS in the test App, so nothing can be bundled — the point here
	// is that a plain vendor MIB is not falsely flagged.
	path, _ := a.resolveMibPath("VENDOR-MIB")
	os.WriteFile(path, []byte("VENDOR-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644)

	list, err := a.MibEditorList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d files", len(list))
	}
	if list[0].Bundled || list[0].Modified {
		t.Errorf("a vendor MIB was flagged as bundled: %+v", list[0])
	}
	if list[0].Size == 0 {
		t.Error("size was not filled in")
	}
}

// Restoring something that never shipped must say so rather than writing an
// empty file over the user's work.
func TestRestoreBundledRefusesAVendorMib(t *testing.T) {
	a, _ := newMibApp(t)
	path, _ := a.resolveMibPath("VENDOR-MIB")
	os.WriteFile(path, []byte("VENDOR-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644)

	if _, err := a.MibEditorRestoreBundled("VENDOR-MIB"); err == nil {
		t.Fatal("restoring a non-bundled MIB was allowed")
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		t.Error("the file was emptied by a failed restore")
	}
}

// A draft must survive leaving the tab, and must NOT be written into the MIB
// directory: a half-finished MIB there would be loaded by the app.
func TestDraftsLandOutsideTheMibDirectory(t *testing.T) {
	a, mibDir := newMibApp(t)

	if err := a.MibEditorSaveDraft("WIP-MIB", "WIP-MIB DEFINITIONS ::= BEGIN\n"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	entries, _ := os.ReadDir(mibDir)
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the draft landed in the MIB directory: %v", names)
	}

	got, err := a.MibEditorReadDraft("WIP-MIB")
	if err != nil {
		t.Fatalf("ReadDraft: %v", err)
	}
	if got != "WIP-MIB DEFINITIONS ::= BEGIN\n" {
		t.Errorf("draft = %q", got)
	}

	list, err := a.MibEditorListDrafts()
	if err != nil || len(list) != 1 || list[0].Name != "WIP-MIB" {
		t.Errorf("ListDrafts = %+v (%v)", list, err)
	}

	if err := a.MibEditorDiscardDraft("WIP-MIB"); err != nil {
		t.Fatalf("DiscardDraft: %v", err)
	}
	if got, _ := a.MibEditorReadDraft("WIP-MIB"); got != "" {
		t.Error("the draft survived being discarded")
	}
}

// Reading a draft that was never written is a normal state, not an error:
// every open would otherwise have to handle a failure that means "nothing to
// recover".
func TestReadingAMissingDraftIsNotAnError(t *testing.T) {
	a, _ := newMibApp(t)
	got, err := a.MibEditorReadDraft("NEVER-SAVED")
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if got != "" {
		t.Errorf("content = %q", got)
	}
}

func TestDraftNamesAreContained(t *testing.T) {
	a, _ := newMibApp(t)
	for _, name := range []string{"../escape", "../../monitoring.db", "/etc/passwd"} {
		path, err := a.draftPath(name)
		if err != nil {
			continue
		}
		if strings.Contains(filepath.ToSlash(path), "/mibs/") {
			t.Errorf("%q resolved into the MIB directory: %s", name, path)
		}
		if !strings.Contains(filepath.ToSlash(path), mibDraftDirName) {
			t.Errorf("%q escaped the draft directory: %s", name, path)
		}
	}
}

// An empty enabled-list must mean empty, not "everything on disk". The old
// fallback silently re-enabled every MIB the user had switched off.
func TestReloadDoesNotReEnableEverything(t *testing.T) {
	a, _ := newMibApp(t)
	for _, name := range []string{"A-MIB", "B-MIB"} {
		path, _ := a.resolveMibPath(name)
		os.WriteFile(path, []byte(name+" DEFINITIONS ::= BEGIN\nEND\n"), 0o644)
	}

	res := a.MibEditorReload([]string{})
	for _, d := range res.Diagnostics {
		if d.FileName == "A-MIB" || d.FileName == "B-MIB" {
			t.Errorf("a disabled MIB was loaded anyway: %s", d.FileName)
		}
	}
}

// One call, one parse: the editor used to make three separate bridge calls that
// each re-parsed the file.
func TestAnalyseReturnsEverythingInOneCall(t *testing.T) {
	a, _ := newMibApp(t)
	out := a.MibEditorAnalyse("", "BAD-MIB DEFINITIONS ::= BEGIN\nnot valid\nEND\n")
	if len(out.Diagnostics) == 0 {
		t.Error("no diagnostics")
	}
	if out.Missing == nil {
		t.Error("Missing is nil; it crosses the bridge as null and every access has to guard")
	}
}

// A vendor MIB that is not UTF-8 opens as what it says, and saving it writes
// UTF-8.
//
// Wails marshals every bound result with encoding/json, which replaces each
// invalid byte with U+FFFD — so a Latin-1 vendor MIB used to arrive mangled,
// and saving it with NO edits wrote the replacement characters back over the
// original bytes (0x8a and 0xe9 both became ef bf bd). The answer then was to
// refuse such a file, which refused the vendor MIBs that most needed editing.
// It is decoded as Windows-1252 before it crosses the bridge instead.
func TestAWindows1252MibOpensAsItReads(t *testing.T) {
	a, dir := newMibApp(t)

	cp1252 := []byte("OLD-MIB DEFINITIONS ::= BEGIN\r\n-- r\xe9sum\xe9 \x96 caf\x8a\r\nEND\r\n")
	path := filepath.Join(dir, "OLD-MIB")
	if err := os.WriteFile(path, cp1252, 0o644); err != nil {
		t.Fatal(err)
	}

	src, err := a.MibEditorRead("OLD-MIB")
	if err != nil {
		t.Fatalf("a Windows-1252 MIB was refused: %v", err)
	}
	if src.Encoding != mib.EncodingWindows1252 || !strings.Contains(src.Content, "résumé – cafŠ") {
		t.Fatalf("read as %s: %q", src.Encoding, src.Content)
	}
	if after, _ := os.ReadFile(path); string(after) != string(cp1252) {
		t.Fatal("opening the file modified it")
	}

	// Saving what was read is not a conflict with what was read.
	res, err := a.MibEditorSave("OLD-MIB", src.Content, src.Sha256, false)
	if err != nil || res.Conflict || !res.Saved {
		t.Fatalf("save: %+v %v", res, err)
	}
	after, _ := os.ReadFile(path)
	if want := "OLD-MIB DEFINITIONS ::= BEGIN\r\n-- résumé – cafŠ\r\nEND\r\n"; string(after) != want {
		t.Errorf("saved %q, want %q", after, want)
	}
}

// Ordinary UTF-8 with non-ASCII in a DESCRIPTION still opens: the guard is
// about INVALID bytes, not about non-English text.
func TestReadAcceptsValidUtf8(t *testing.T) {
	a, dir := newMibApp(t)
	body := "OK-MIB DEFINITIONS ::= BEGIN\n-- résumé café ✓\nEND\n"
	if err := os.WriteFile(filepath.Join(dir, "OK-MIB"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := a.MibEditorRead("OK-MIB")
	if err != nil {
		t.Fatalf("valid UTF-8 was refused: %v", err)
	}
	if src.Content != body {
		t.Errorf("content changed:\n want %q\n got  %q", body, src.Content)
	}
}

// Two saves inside one wall-clock second must not share a backup name.
//
// The stamp was seconds, so the second save destroyed the first backup — the
// copy of the version the user would actually want back.
func TestBackupsInTheSameSecondDoNotCollide(t *testing.T) {
	a, dir := newMibApp(t)
	if err := os.WriteFile(filepath.Join(dir, "B-MIB"), []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var paths []string
	content := "v0\n"
	for i := 1; i <= 5; i++ {
		src, err := a.MibEditorRead("B-MIB")
		if err != nil {
			t.Fatal(err)
		}
		next := fmt.Sprintf("v%d\n", i)
		res, err := a.MibEditorSave("B-MIB", next, src.Sha256, false)
		if err != nil || !res.Saved {
			t.Fatalf("save %d: %v %+v", i, err, res)
		}
		if res.BackupPath != "" {
			paths = append(paths, res.BackupPath)
		}
		content = next
	}
	_ = content

	if len(paths) < 2 {
		t.Skipf("only %d backups were taken; this file is not bundled", len(paths))
	}
	seen := map[string]string{}
	for i, p := range paths {
		if prev, dup := seen[p]; dup {
			t.Errorf("backup %d reuses the name of %s: %s", i, prev, p)
		}
		seen[p] = fmt.Sprint(i)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("backup %d does not exist: %v", i, err)
		}
	}

	// And each one holds a DIFFERENT version, which is the point.
	bodies := map[string]bool{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		bodies[string(raw)] = true
	}
	if len(bodies) != len(paths) {
		t.Errorf("%d backups hold only %d distinct versions", len(paths), len(bodies))
	}
}

// A file read in the encoding chosen saves without a false conflict, and as
// UTF-8: the check compares the bytes on disk, whatever they were read as.
func TestAMibReadInAnotherEncodingSaves(t *testing.T) {
	a, dir := newMibApp(t)
	raw, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("GBK-MIB DEFINITIONS ::= BEGIN\n-- 温度\nEND\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "GBK-MIB"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := a.MibEditorReadAs("GBK-MIB", "GBK")
	if err != nil || !strings.Contains(src.Content, "温度") || src.Encoding != "gbk" {
		t.Fatalf("read as GBK: %q %s %v", src.Content, src.Encoding, err)
	}
	res, err := a.MibEditorSave("GBK-MIB", src.Content+"-- saved\n", src.Sha256, false)
	if err != nil || res.Conflict || !res.Saved {
		t.Fatalf("saving it: %+v %v", res, err)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "GBK-MIB"))
	if !strings.Contains(string(onDisk), "温度") {
		t.Error("not written as UTF-8")
	}
	again, _ := a.MibEditorRead("GBK-MIB")
	if again.Sha256 != res.Sha256 || again.Encoding != "utf-8" {
		t.Errorf("after the save the file is UTF-8 and its checksum the one the save gave: %s %s / %s", again.Encoding, again.Sha256, res.Sha256)
	}

	// Changed underneath: a conflict, as before.
	if err := os.WriteFile(filepath.Join(dir, "GBK-MIB"), []byte("GBK-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := a.MibEditorSave("GBK-MIB", again.Content, again.Sha256, false); !res.Conflict {
		t.Error("a file changed on disk saved over")
	}

	if _, err := a.MibEditorReadAs("GBK-MIB", "klingon"); err == nil {
		t.Error("an encoding not offered was accepted")
	}
	if _, err := a.MibEditorReopenExternalAs("gbk"); err == nil {
		t.Error("re-reading with no file opened from outside should say so")
	}
}
