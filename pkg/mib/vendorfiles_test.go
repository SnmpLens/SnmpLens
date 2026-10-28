package mib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepinggenius2/gosmi"
)

// The mistakes vendor MIBs actually make, each reproduced in a module written
// for the test. Every one of them used to end in a module that did not load, a
// module that "loaded" with half its objects untyped, or a message pointing
// somewhere other than the problem.

// vendorDir is a MIB directory holding the bundled MIBs and the given files.
func vendorDir(t *testing.T, files map[string]string) (*Service, string) {
	t.Helper()
	dir, _ := corpusCopy(t)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gosmi.Exit()
	gosmi.Init()
	return NewService(dir), dir
}

func loadAll(t *testing.T, s *Service, dir string) map[string]MibLoadResult {
	t.Helper()
	names, err := ListMibFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]MibLoadResult{}
	for _, r := range s.LoadWithDiagnostics(names).Diagnostics {
		out[r.FileName] = r
	}
	return out
}

const v1Module = `EXAMPLE-V1-MIB DEFINITIONS ::= BEGIN
IMPORTS
    enterprises, IpAddress FROM RFC1155-SMI
    OBJECT-TYPE FROM RFC-1212
    TRAP-TYPE FROM RFC-1215
    DisplayString FROM RFC-1213;

example OBJECT IDENTIFIER ::= { enterprises 99991 }

exampleName OBJECT-TYPE
    SYNTAX  DisplayString (SIZE (0..32))
    ACCESS  read-only
    STATUS  mandatory
    DESCRIPTION "The name of the box."
    ::= { example 1 }

examplePeer OBJECT-TYPE
    SYNTAX  IpAddress
    ACCESS  read-only
    STATUS  mandatory
    DESCRIPTION "Where the box reports to."
    ::= { example 2 }

exampleReboot TRAP-TYPE
    ENTERPRISE example
    VARIABLES { exampleName }
    DESCRIPTION "The box restarted."
    ::= 1
END
`

// An SMIv1 module names its SMIv1 sources, one of them by a spelling that is
// not a module name. It loaded "successfully" with its DisplayString objects
// untyped, and the editor reported three missing modules for a file the loader
// accepted.
func TestAnSMIv1ModuleLoadsWithEveryTypeResolved(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{"example-v1.mib": v1Module})
	r := loadAll(t, s, dir)["example-v1.mib"]
	if !r.Success {
		t.Fatalf("did not load: %s", r.Error)
	}
	codes := map[string]bool{}
	for _, w := range r.Warnings {
		codes[w.Code] = true
	}
	if codes[CodeNoType] || codes[CodeNoOid] {
		t.Errorf("objects left unresolved: %+v", r.Warnings)
	}
	if !codes[CodeImportAlias] {
		t.Errorf("the misspelt import is not reported: %+v", r.Warnings)
	}
	if got := s.ResolveOid("1.3.6.1.4.1.99991.1").Syntax; got != "DisplayString" {
		t.Errorf("exampleName's syntax = %q, want DisplayString", got)
	}

	for _, d := range Analyse(v1Module, Symbols()) {
		if d.Code == CodeUnknownModule || d.Code == CodeUnknownType {
			t.Errorf("the editor contradicts the loader: %+v", d)
		}
	}
}

// gosmi reads some imports from RFC1213-MIB out of the modules that replaced
// it. Those are not missing; the others are.
func TestOnlyWhatGosmiDoesNotRewriteIsMissing(t *testing.T) {
	cat := loadedCatalogue(t)
	mod := func(imports string) string {
		return `EXAMPLE-OLD-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM RFC1155-SMI
    OBJECT-TYPE FROM RFC-1212
    ` + imports + ` FROM RFC1213-MIB;
example OBJECT IDENTIFIER ::= { enterprises 99992 }
exampleText OBJECT-TYPE
    SYNTAX  DisplayString
    ACCESS  read-only
    STATUS  mandatory
    DESCRIPTION "Some text."
    ::= { example 1 }
END
`
	}
	for _, d := range Analyse(mod("DisplayString"), cat) {
		if d.Code == CodeUnknownModule {
			t.Errorf("DisplayString is read from SNMPv2-TC, yet: %s", d.Message)
		}
	}
	var found bool
	for _, d := range Analyse(mod("DisplayString, ifInOctets"), cat) {
		if d.Code == CodeUnknownModule {
			found = true
			if !strings.Contains(d.Message, "ifInOctets") || strings.Contains(d.Message, "DisplayString needs") {
				t.Errorf("the message should name only what needs the module: %s", d.Message)
			}
		}
	}
	if !found {
		t.Error("ifInOctets from RFC1213-MIB is not reported")
	}

	// ipRouteDest is rewritten too, to a module without it: not missing, lost.
	var lost bool
	for _, d := range Analyse(mod("DisplayString, ipRouteDest"), cat) {
		switch d.Code {
		case CodeUnknownModule:
			t.Errorf("adding RFC1213-MIB would change nothing for ipRouteDest, yet: %s", d.Message)
		case CodeImportLost:
			lost = d.Symbol == "ipRouteDest"
		}
	}
	if !lost {
		t.Error("ipRouteDest from RFC1213-MIB resolves to nothing and is not reported")
	}
}

const quotedMib = `EXAMPLE-QUOTE-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI;
example OBJECT IDENTIFIER ::= { enterprises 99993 }
exampleMode OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Set by the \"fast start\" option of the box."
    ::= { example 1 }
END
`

// SMI strings have no escape, so \" ends the DESCRIPTION and the module was
// refused at "fast".
func TestAnEscapedQuoteIsReadAsAnApostrophe(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{"EXAMPLE-QUOTE-MIB": quotedMib})
	r := loadAll(t, s, dir)["EXAMPLE-QUOTE-MIB"]
	if !r.Success {
		t.Fatalf("did not load: %s", r.Error)
	}
	// Two quotes on one line are one thing to fix.
	if len(r.Warnings) != 1 || r.Warnings[0].Line != 8 || r.Warnings[0].Code != CodeEscapedQuote {
		t.Errorf("warnings = %+v, want one for the quotes on line 8", r.Warnings)
	}

	fixed := Repair(quotedMib)
	if len(fixed.Fixes) != 2 || fixed.Fixes[0].Code != CodeEscapedQuote {
		t.Fatalf("fixes = %+v", fixed.Fixes)
	}
	if strings.Contains(fixed.Content, `\"`) {
		t.Error("the repaired text still holds an escaped quote")
	}
	if _, err := parseOnce(fixed.Content); err != nil {
		t.Errorf("the repaired text does not parse strictly: %v", err)
	}
}

// A column found in the repaired text is reported in the text on screen: an
// error after an escaped quote on its line was underlined one character to the
// left of what it was about.
func TestPositionsAreReportedInTheTextAsRead(t *testing.T) {
	// One escaped quote, then, on the same line, a type nothing defines.
	mod := `EXAMPLE-SHIFT-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, enterprises FROM SNMPv2-SMI;
example OBJECT IDENTIFIER ::= { enterprises 99988 }
exampleThing OBJECT-TYPE
    SYNTAX Integer32 MAX-ACCESS read-only STATUS current DESCRIPTION "a \"b\" c" ::= { example 1 }
exampleOther OBJECT-TYPE
    SYNTAX NoSuchType
    MAX-ACCESS read-only
    STATUS current
    DESCRIPTION "x"
    ::= { example 2 }
END
`
	r := repairSource(mod)
	if len(r.Fixes) != 2 {
		t.Fatalf("fixes = %+v", r.Fixes)
	}
	line := strings.Split(mod, "\n")[4]
	// "::=" sits after both quotes: two characters shorter in the repaired text.
	repairedLine := strings.Split(r.Text, "\n")[4]
	inRepaired := strings.Index(repairedLine, "::=") + 1
	if got, want := r.original(5, inRepaired), strings.Index(line, "::=")+1; got != want {
		t.Errorf("column %d in the repaired text maps to %d, want %d", inRepaired, got, want)
	}
	// Before the first change, nothing moves; inside a replacement, its start.
	if got := r.original(5, 3); got != 3 {
		t.Errorf("a column before any change moved: %d", got)
	}
	quote := strings.Index(line, `\"`) + 1
	if got := r.original(5, quote); got != quote {
		t.Errorf("the replacement maps to %d, want %d", got, quote)
	}
}

// A string that genuinely ends with a backslash is valid SMI, and a valid file
// is never touched.
func TestAValidFileIsNeverRepaired(t *testing.T) {
	valid := strings.Replace(quotedMib, `"Set by the \"fast start\" option of the box."`, `"Stored under C:\"`, 1)
	if _, err := parseOnce(valid); err != nil {
		t.Fatalf("the control does not parse: %v", err)
	}
	if r := Repair(valid); len(r.Fixes) != 0 || r.Content != "" {
		t.Errorf("a valid file was repaired: %+v", r.Fixes)
	}
}

// The day without the time: the grammar wants ten or twelve digits and a Z,
// and refused the whole module for it.
func TestADateWithoutTheTimeIsReadAsMidnight(t *testing.T) {
	mod := `EXAMPLE-DATE-MIB DEFINITIONS ::= BEGIN
IMPORTS MODULE-IDENTITY, enterprises FROM SNMPv2-SMI;
exampleDate MODULE-IDENTITY
    LAST-UPDATED "20190415Z"
    ORGANIZATION "Example"
    CONTACT-INFO "nobody"
    DESCRIPTION  "A module dated by its day alone."
    REVISION     "201904150000Z"
    DESCRIPTION  "First."
    ::= { enterprises 99994 }
END
`
	s, dir := vendorDir(t, map[string]string{"EXAMPLE-DATE-MIB": mod})
	r := loadAll(t, s, dir)["EXAMPLE-DATE-MIB"]
	if !r.Success {
		t.Fatalf("did not load: %s", r.Error)
	}
	fixed := Repair(mod)
	if len(fixed.Fixes) != 1 || fixed.Fixes[0].Code != CodeBadDate || fixed.Fixes[0].Line != 4 {
		t.Fatalf("fixes = %+v", fixed.Fixes)
	}
	if !strings.Contains(fixed.Content, `LAST-UPDATED "201904150000Z"`) {
		t.Errorf("not repaired: %s", fixed.Content)
	}
}

// One quote too many ends a DESCRIPTION early. gosmi's lexer reads the whole
// file first, so it used to report the first typographic dash in the prose
// that followed — several lines below — and the editor's outline was empty.
func TestAStrayQuoteIsReportedAtTheQuote(t *testing.T) {
	mod := `EXAMPLE-STRAY-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI;
example OBJECT IDENTIFIER ::= { enterprises 99995 }
exampleOffset OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "The offset (input – reference)."
        Only on boxes with a second input"
    ::= { example 1 }
exampleDrift OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "The drift (input – reference)."
    ::= { example 2 }
END
`
	var syntax []Diagnostic
	for _, d := range Validate(mod) {
		if d.Code == CodeSyntax {
			syntax = append(syntax, d)
		}
	}
	if len(syntax) != 1 {
		t.Fatalf("syntax diagnostics = %+v", syntax)
	}
	d := syntax[0]
	if d.Line != 8 || !strings.Contains(d.Message, "this quote ends the string") || !strings.Contains(d.Message, `"Only" on line 9`) {
		t.Errorf("reported at %d:%d: %s — want the quote ending line 8", d.Line, d.Column, d.Message)
	}

	outline := AnalyseAll(mod, Catalogue{}).Outline
	if len(outline) == 0 {
		t.Error("the outline is empty: nothing above the error is shown")
	}
}

// A character the lexer cannot read, outside any string, is named.
func TestAnUnreadableCharacterIsNamed(t *testing.T) {
	mod := strings.Replace(quotedMib, "::= { enterprises 99993 }", "::= { enterprises 99993 } –", 1)
	var d Diagnostic
	for _, x := range Validate(mod) {
		if x.Code == CodeSyntax {
			d = x
		}
	}
	if d.Line != 3 || !strings.Contains(d.Message, "U+2013") || !strings.Contains(d.Message, "inside a string or a comment") {
		t.Errorf("got %d:%d %s", d.Line, d.Column, d.Message)
	}
}

// Windows-1252 is how most non-UTF-8 vendor MIBs are written. It loads, its
// prose reads as written, and the gate does not turn it away.
func TestAWindows1252MibLoadsAndReads(t *testing.T) {
	text := strings.Replace(quotedMib, `"Set by the \"fast start\" option of the box."`, "\"R\xe9glage \x96 voir le manuel.\"", 1)
	raw := []byte(text)

	content, _, enc := DecodeSource(raw)
	if enc != EncodingWindows1252 || !strings.Contains(content, "Réglage – voir") {
		t.Fatalf("decoded as %s: %q", enc, content)
	}
	if _, _, reject := ImportRejection(append([]byte("-- \xe9t\xe9\n"), raw...)); reject {
		t.Error("the import gate refused a Windows-1252 MIB")
	}
	if _, _, reject := ImportRejection([]byte("DEFINITIONS\x01\x02\x03\x04\x05\x06\x07\x08\x0e\x0f\x10\x11\x12\x13")); !reject {
		t.Error("control bytes passed the gate as text")
	}

	s, dir := vendorDir(t, map[string]string{"EXAMPLE-QUOTE-MIB": text})
	if r := loadAll(t, s, dir)["EXAMPLE-QUOTE-MIB"]; !r.Success {
		t.Fatalf("did not load: %s", r.Error)
	}
	if got := s.ResolveOid("1.3.6.1.4.1.99993.1").Description; !strings.Contains(got, "Réglage – voir") {
		t.Errorf("description = %q", got)
	}
}

// gosmi finds an imported module by file name. A vendor file named after its
// product was invisible to everything importing it, and a module importing it
// "loaded" with no OIDs.
func TestAModuleIsFoundByTheNameItDeclares(t *testing.T) {
	base := `EXAMPLE-BASE-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
exampleRoot OBJECT IDENTIFIER ::= { enterprises 99996 }
END
`
	user := `EXAMPLE-USER-MIB DEFINITIONS ::= BEGIN
IMPORTS OBJECT-TYPE, Integer32 FROM SNMPv2-SMI
    exampleRoot FROM EXAMPLE-BASE-MIB;
exampleCount OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A count."
    ::= { exampleRoot 1 }
END
`
	// Sorted so the importer comes first: the base module cannot have been
	// loaded before it is needed.
	s, dir := vendorDir(t, map[string]string{"a-user.mib": user, "zz example base 2.mib": base})
	r := loadAll(t, s, dir)["a-user.mib"]
	if !r.Success || len(r.Warnings) > 0 {
		t.Fatalf("success=%v warnings=%v error=%s", r.Success, r.Warnings, r.Error)
	}
	if got := s.ResolveOid("1.3.6.1.4.1.99996.1").Name; got != "exampleCount" {
		t.Errorf("1.3.6.1.4.1.99996.1 resolves to %q", got)
	}
}

// Two files declaring one module both loaded, and the second replaced the
// first in silence. Now one is loaded, the other says which and why, and
// switching the newer off makes the older the module.
func TestTwoFilesDeclaringOneModuleLoadOnce(t *testing.T) {
	version := func(date, extra string) string {
		return `EXAMPLE-TWICE-MIB DEFINITIONS ::= BEGIN
IMPORTS MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI;
exampleTwice MODULE-IDENTITY
    LAST-UPDATED "` + date + `"
    ORGANIZATION "Example"
    CONTACT-INFO "nobody"
    DESCRIPTION  "Twice."
    ::= { enterprises 99997 }
exampleOne OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "One."
    ::= { exampleTwice 1 }
` + extra + `END
`
	}
	newer := version("202106010000Z", `exampleTwo OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Two."
    ::= { exampleTwice 2 }
`)
	s, dir := vendorDir(t, map[string]string{"a-old.mib": version("201901010000Z", ""), "b-new.mib": newer})

	r := loadAll(t, s, dir)
	if !r["b-new.mib"].Success {
		t.Fatalf("the newer did not load: %s", r["b-new.mib"].Error)
	}
	old := r["a-old.mib"]
	if old.Success || old.Diagnosis == nil || old.Diagnosis.Stage != StageDuplicate ||
		!strings.Contains(old.Error, "b-new.mib") || !strings.Contains(old.Error, "newer") {
		t.Errorf("the older: success=%v error=%q", old.Success, old.Error)
	}
	if got := s.ResolveOid("1.3.6.1.4.1.99997.2").Name; got != "exampleTwo" {
		t.Errorf("the newer's object resolves to %q", got)
	}

	names, _ := ListMibFiles(dir)
	var enabled []string
	for _, n := range names {
		if n != "b-new.mib" {
			enabled = append(enabled, n)
		}
	}
	gosmi.Exit()
	gosmi.Init()
	for _, res := range s.LoadWithDiagnostics(enabled).Diagnostics {
		if res.FileName == "a-old.mib" && !res.Success {
			t.Errorf("with the newer switched off, the older was refused: %s", res.Error)
		}
	}
}

func TestPreferredSaysWhy(t *testing.T) {
	dated := func(date string) string {
		return `EXAMPLE-P-MIB DEFINITIONS ::= BEGIN
IMPORTS MODULE-IDENTITY, enterprises FROM SNMPv2-SMI;
exampleP MODULE-IDENTITY
    LAST-UPDATED "` + date + `"
    ORGANIZATION "Example"
    CONTACT-INFO "nobody"
    DESCRIPTION  "P."
    ::= { enterprises 99998 }
END
`
	}
	broken := strings.Replace(dated("209901010000Z"), "ORGANIZATION", "ORGANISATION", 1)
	for _, tc := range []struct {
		cands []string
		best  int
		why   string // why the other one lost
	}{
		{[]string{dated("201901010000Z"), dated("202101010000Z")}, 1, "newer (last updated 2021-01-01)"},
		{[]string{broken, dated("201901010000Z")}, 1, "does not parse"},
		{[]string{dated("202101010000Z"), dated("202101010000Z")}, 0, "identical"},
		{[]string{v1Module, strings.Replace(v1Module, "box", "unit", 1)}, 0, "comes first"},
	} {
		var cands []Candidate
		for _, c := range tc.cands {
			cands = append(cands, Candidate{Content: c})
		}
		best, reasons := Preferred(cands)
		if best != tc.best || reasons[best].Code != "" || !strings.Contains(reasons[1-best].Why, tc.why) {
			t.Errorf("best=%d %+v, want %d %q", best, reasons, tc.best, tc.why)
		}
	}
}

func TestNaturalLessReadsNumbersAsNumbers(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		less bool
	}{
		{"1.9 example", "1.10 example", true},
		{"1.10 example", "1.9 example", false},
		{"v1.9/example.mib", "v1.10/example.mib", true},
		{"example", "example 2", true},
		{"example 007", "example 7", false},
	} {
		if got := NaturalLess(tc.a, tc.b); got != tc.less {
			t.Errorf("NaturalLess(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

// One name defined twice used to read "assigned to x and x".
func TestAnObjectDefinedTwiceSaysSo(t *testing.T) {
	twice := strings.Replace(quotedMib, "END\n", `exampleMode OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Again."
    ::= { example 1 }
END
`, 1)
	var dup []Diagnostic
	for _, d := range Analyse(twice, Catalogue{}) {
		if d.Code == CodeDuplicateName || d.Code == CodeDuplicateOid {
			dup = append(dup, d)
		}
	}
	if len(dup) != 1 || dup[0].Code != CodeDuplicateName || !strings.Contains(dup[0].Message, "lines 4 and 10") {
		t.Errorf("got %+v", dup)
	}
}

// What ships must load clean: the warnings exist to single out a module that
// is wrong, and a bundled one carrying them would teach everybody to ignore
// them.
func TestEveryBundledMibLoadsWithoutWarnings(t *testing.T) {
	s, dir := vendorDir(t, nil)
	for name, r := range loadAll(t, s, dir) {
		if !r.Success || len(r.Warnings) > 0 {
			t.Errorf("%s: success=%v warnings=%v error=%s", name, r.Success, r.Warnings, r.Error)
		}
	}
}
