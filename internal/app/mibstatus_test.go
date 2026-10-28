package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"SnmpLens/pkg/mib"

	"github.com/sleepinggenius2/gosmi"
)

const twiceMib = `TWICE-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI;
twice OBJECT IDENTIFIER ::= { enterprises 99987 }
twiceA OBJECT-TYPE
    SYNTAX Integer32
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "a"
    ::= { twice 1 }
twiceA OBJECT-TYPE
    SYNTAX Integer32
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "a"
    ::= { twice 2 }
END
`

const fineMib = `FINE-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
fine OBJECT IDENTIFIER ::= { enterprises 99986 }
END
`

// verdictApp is a MIB directory holding the bundled MIBs and files, loaded
// through the App so the verdicts know what the load did. The verdict depends
// on what is loaded — an import is satisfied or not — so the test loads the
// bundled set itself rather than inheriting whatever the tests before it left
// in gosmi.
func verdictApp(t *testing.T, files map[string]string) (*App, string, func(string, string)) {
	t.Helper()
	a, dir := newMibApp(t)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "mibs"))
	if err != nil {
		t.Skip("no bundled MIBs")
	}
	var names []string
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "mibs", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		write(e.Name(), string(raw))
		names = append(names, e.Name())
	}
	for name, content := range files {
		write(name, content)
		names = append(names, name)
	}
	gosmi.Exit()
	mib.InitPath(dir)
	a.LoadMibsWithDiagnostics(names)
	return a, dir, write
}

func statusesByName(a *App) map[string]MibFileStatus {
	out := map[string]MibFileStatus{}
	for _, s := range a.MibFileStatuses() {
		out[s.Name] = s
	}
	return out
}

// One verdict per file, joining the load and the analysis: a module that
// loads with a duplicate definition is an ERROR, not "loaded" on one screen
// and "error" on another.
func TestEveryFileHasOneVerdict(t *testing.T) {
	a, dir, write := verdictApp(t, map[string]string{
		"BROKEN-MIB": "BROKEN-MIB DEFINITIONS ::= BEGIN\nIMPORTS FROM;\nEND\n",
		"TWICE-MIB":  twiceMib,
		"FINE-MIB":   fineMib,
	})

	got := statusesByName(a)
	if s := got["BROKEN-MIB"]; s.Verdict.Level != mib.VerdictFailed || s.Verdict.Detail == "" || !s.Loaded {
		t.Errorf("a file gosmi refuses: %+v", s)
	}
	if s := got["TWICE-MIB"]; s.Verdict.Level != mib.VerdictError || s.Verdict.Count == 0 {
		t.Errorf("a duplicate definition is an error in a file that loads: %+v", s)
	}
	if s := got["FINE-MIB"]; s.Verdict.Level != mib.VerdictOK {
		t.Errorf("a clean file: %+v", s)
	}

	// A file in the directory that was not loaded is judged by its text, and
	// says it was not loaded.
	write("OFF-MIB", "OFF-MIB DEFINITIONS ::= BEGIN\nIMPORTS FROM;\nEND\n")
	if s := statusesByName(a)["OFF-MIB"]; s.Loaded || s.Verdict.Level != mib.VerdictFailed {
		t.Errorf("a file switched off: %+v", s)
	}

	// Fixed on disk, the analysis follows — the cache is keyed by the file —
	// and the load still says what it said until the next load.
	write("BROKEN-MIB", "BROKEN-MIB DEFINITIONS ::= BEGIN\nIMPORTS enterprises FROM SNMPv2-SMI;\nb OBJECT IDENTIFIER ::= { enterprises 99985 }\nEND\n")
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(dir, "BROKEN-MIB"), future, future)
	if s := statusesByName(a)["BROKEN-MIB"]; s.Verdict.Level != mib.VerdictFailed {
		t.Errorf("not reloaded yet, the load still stands: %+v", s)
	}
	a.LoadMibsWithDiagnostics([]string{"SNMPv2-SMI", "BROKEN-MIB"})
	if s := statusesByName(a)["BROKEN-MIB"]; s.Verdict.Level != mib.VerdictOK {
		t.Errorf("after the fix and a load: %+v", s)
	}

	// A file that is gone is gone from the answer.
	_ = os.Remove(filepath.Join(dir, "FINE-MIB"))
	if _, ok := statusesByName(a)["FINE-MIB"]; ok {
		t.Error("a deleted file is still reported")
	}
}

// A file opens with its complete analysis, and the editor's verdict on it is
// the list's: it used to open with the syntax check alone, marked "warning",
// and turn "error" when the full analysis answered a moment later.
func TestAFileOpensJudgedAsTheListJudgesIt(t *testing.T) {
	a, _, _ := verdictApp(t, map[string]string{"TWICE-MIB": twiceMib})

	src, err := a.MibEditorRead("TWICE-MIB")
	if err != nil {
		t.Fatal(err)
	}
	if src.Verdict != statusesByName(a)["TWICE-MIB"].Verdict {
		t.Errorf("opened: %+v, listed: %+v", src.Verdict, statusesByName(a)["TWICE-MIB"].Verdict)
	}
	if len(src.Outline) == 0 {
		t.Error("the outline does not come with the file")
	}
}

// The load speaks for the file as it is on disk. A buffer that IS that text is
// failed when gosmi refused it, whatever the analysis says; an edited buffer is
// judged by its analysis — except when another file holds its module, which no
// edit of the text undoes.
func TestTheBufferIsJudgedByTheLoadOnlyWhileItIsTheFile(t *testing.T) {
	a, _, _ := verdictApp(t, map[string]string{"FINE-MIB": fineMib})

	// A load refusing what the analysis accepts: say so, as gosmi would.
	refused := mib.MibLoadResult{FileName: "FINE-MIB", Success: false, Error: "refused by gosmi"}
	a.mibStatuses.rememberLoads([]mib.MibLoadResult{refused})
	if v := a.MibEditorAnalyse("FINE-MIB", fineMib).Verdict; v.Level != mib.VerdictFailed {
		t.Errorf("the file as loaded: %+v", v)
	}
	edited := fineMib + "\n"
	if v := a.MibEditorAnalyse("FINE-MIB", edited).Verdict; v.Level != mib.VerdictOK {
		t.Errorf("an edited buffer is judged by its text: %+v", v)
	}
	if v := a.MibEditorAnalyse("", fineMib).Verdict; v.Level != mib.VerdictOK {
		t.Errorf("a file from elsewhere has no load: %+v", v)
	}

	duplicate := mib.MibLoadResult{FileName: "FINE-MIB", Success: false, Error: "held by another file",
		Diagnosis: &mib.LoadDiagnosis{Stage: mib.StageDuplicate}}
	a.mibStatuses.rememberLoads([]mib.MibLoadResult{duplicate})
	if v := a.MibEditorAnalyse("FINE-MIB", edited).Verdict; v.Level != mib.VerdictFailed {
		t.Errorf("a module another file holds stays refused: %+v", v)
	}
}
