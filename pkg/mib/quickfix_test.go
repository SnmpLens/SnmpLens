package mib

import (
	"strings"
	"testing"
)

const tableMib = `EXAMPLE-TABLE-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, Counter32,
    Gauge32, enterprises                    -- the base types
        FROM SNMPv2-SMI
    DisplayString, TruthValue
        FROM SNMPv2-TC
    MODULE-COMPLIANCE
        FROM SNMPv2-CONF;

exampleTable MODULE-IDENTITY
    LAST-UPDATED "202401010000Z"
    ORGANIZATION "Example"
    CONTACT-INFO "nobody"
    DESCRIPTION  "Rows of things."
    ::= { enterprises 99980 }

exampleThingTable OBJECT-TYPE
    SYNTAX      SEQUENCE OF ExampleThingEntry
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "The things."
    ::= { exampleTable 1 }

exampleThingEntry OBJECT-TYPE
    SYNTAX      ExampleThingEntry
    MAX-ACCESS  read-create -- as the vendor wrote it
    STATUS      current
    DESCRIPTION "One thing."
    INDEX       { exampleThingIndex }
    ::= { exampleThingTable 1 }

ExampleThingEntry ::= SEQUENCE { exampleThingIndex Integer32, exampleThingName DisplayString }

exampleThingIndex OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "Which thing."
    ::= { exampleThingEntry 1 }

exampleThingName OBJECT-TYPE
    SYNTAX      DisplayString
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Its name."
    ::= { exampleThingEntry 2 }
END
`

func codes(diags []Diagnostic, code string) []string {
	var out []string
	for _, d := range diags {
		if d.Code == code {
			out = append(out, d.Symbol)
		}
	}
	return out
}

// A table and its row given read access: both get not-accessible, the word and
// nothing else changes, and the comment beside it stays.
func TestTheRowAccessFixWritesNotAccessible(t *testing.T) {
	cat := loadedCatalogue(t)
	if got := codes(Analyse(tableMib, cat), CodeRowAccess); len(got) != 2 {
		t.Fatalf("the fixture should have two row-access findings, has %v", got)
	}

	one := QuickFix(tableMib, CodeRowAccess, []string{"exampleThingEntry"}, cat)
	if strings.Join(one.Fixed, ",") != "exampleThingEntry" {
		t.Fatalf("fixed %v", one.Fixed)
	}
	if !strings.Contains(one.Content, "MAX-ACCESS  not-accessible -- as the vendor wrote it") ||
		!strings.Contains(one.Content, "SEQUENCE OF ExampleThingEntry\n    MAX-ACCESS  read-only") {
		t.Errorf("only the row's access should change:\n%s", one.Content)
	}

	all := QuickFix(tableMib, CodeRowAccess, nil, cat)
	if len(all.Fixed) != 2 {
		t.Fatalf("every finding: %v", all.Fixed)
	}
	if got := codes(Analyse(all.Content, cat), CodeRowAccess); len(got) != 0 {
		t.Errorf("still reported after the fix: %v", got)
	}
	if strings.Count(all.Content, "\n") != strings.Count(tableMib, "\n") {
		t.Error("the fix moved lines")
	}
	if _, err := parseOnce(all.Content); err != nil {
		t.Errorf("the fixed file does not parse: %v", err)
	}
}

// Unused imports leave IMPORTS: from the middle of a group, at the end of a
// group, a whole group, and the clause still parses.
func TestTheUnusedImportFixTakesThemOut(t *testing.T) {
	cat := loadedCatalogue(t)
	unused := codes(Analyse(tableMib, cat), CodeUnusedImport)
	want := "Counter32,Gauge32,MODULE-COMPLIANCE,TruthValue"
	if strings.Join(sortedCopy(unused), ",") != want {
		t.Fatalf("the fixture's unused imports: %v", unused)
	}

	fix := QuickFix(tableMib, CodeUnusedImport, nil, cat)
	if strings.Join(fix.Fixed, ",") != want {
		t.Fatalf("fixed %v", fix.Fixed)
	}
	if _, err := parseOnce(fix.Content); err != nil {
		t.Fatalf("the fixed file does not parse: %v\n%s", err, fix.Content)
	}
	if got := codes(Analyse(fix.Content, cat), CodeUnusedImport); len(got) != 0 {
		t.Errorf("still unused: %v", got)
	}
	for _, gone := range []string{"Counter32", "Gauge32", "TruthValue", "MODULE-COMPLIANCE", "SNMPv2-CONF"} {
		if strings.Contains(fix.Content[:strings.Index(fix.Content, ";")], gone) {
			t.Errorf("%s is still imported:\n%s", gone, fix.Content)
		}
	}
	// What is kept is kept as it was written, comment included.
	if !strings.Contains(fix.Content, "-- the base types") || !strings.Contains(fix.Content, "DisplayString\n        FROM SNMPv2-TC;") {
		t.Errorf("the clause was rewritten rather than edited:\n%s", fix.Content)
	}

	// One name only, when one is asked for.
	one := QuickFix(tableMib, CodeUnusedImport, []string{"TruthValue"}, cat)
	if strings.Join(one.Fixed, ",") != "TruthValue" || !strings.Contains(one.Content, "Counter32") {
		t.Errorf("one name: %v", one.Fixed)
	}
	if _, err := parseOnce(one.Content); err != nil {
		t.Errorf("does not parse: %v", err)
	}
}

// When nothing imported is used, the clause goes: `IMPORTS ;` does not parse.
func TestNoImportLeftNoClauseLeft(t *testing.T) {
	cat := loadedCatalogue(t)
	mod := "EXAMPLE-BARE-MIB DEFINITIONS ::= BEGIN\nIMPORTS Counter32, Gauge32 FROM SNMPv2-SMI;\nexampleBare OBJECT IDENTIFIER ::= { iso 3 }\nEND\n"
	fix := QuickFix(mod, CodeUnusedImport, nil, cat)
	if strings.Contains(fix.Content, "IMPORTS") {
		t.Errorf("the clause is left:\n%s", fix.Content)
	}
	if _, err := parseOnce(fix.Content); err != nil {
		t.Errorf("does not parse: %v\n%s", err, fix.Content)
	}
}

func TestOnlyWhatQuickFixKnows(t *testing.T) {
	if r := QuickFix(tableMib, CodeTabs, nil, Catalogue{}); r.Content != "" || len(r.Fixed) != 0 {
		t.Errorf("a code with no fix: %+v", r)
	}
}

// Muting takes advice out of the verdict and never an error.
func TestMutingLeavesErrorsAlone(t *testing.T) {
	tabs := Diagnostic{Severity: SevWarning, Code: CodeTabs, Message: "tabs"}
	row := Diagnostic{Severity: SevWarning, Code: CodeRowAccess, Message: "row"}
	noType := Diagnostic{Severity: SevWarning, Code: CodeNoType, Message: "no type"}
	twice := Diagnostic{Severity: SevError, Code: CodeDuplicateName, Message: "twice"}
	loaded := &MibLoadResult{Success: true, Warnings: []Diagnostic{
		{Severity: SevWarning, Code: CodeBadDate, Message: "date"}, noType,
	}}
	muted := map[string]bool{CodeTabs: true, CodeBadDate: true, CodeNoType: true, CodeDuplicateName: true}

	if v := JudgeMuted(nil, []Diagnostic{tabs}, muted); v.Level != VerdictOK {
		t.Errorf("a muted rule alone: %+v", v)
	}
	if v := JudgeMuted(nil, []Diagnostic{tabs, row}, muted); v.Level != VerdictWarning || v.Count != 1 || v.Detail != "row" {
		t.Errorf("the rest still counts: %+v", v)
	}
	if v := JudgeMuted(nil, []Diagnostic{tabs, twice}, muted); v.Level != VerdictError {
		t.Errorf("an error is never muted: %+v", v)
	}
	if v := JudgeMuted(loaded, nil, muted); v.Level != VerdictWarning || v.Detail != "no type" {
		t.Errorf("the load's muted warnings go, what resolves to nothing stays: %+v", v)
	}
	if loaded.Warnings[0].Code != CodeBadDate {
		t.Error("the load result itself was changed")
	}
	for _, c := range MutableRules() {
		if c == CodeNoType || c == CodeNoOid || c == CodeImportLost || c == CodeSyntax {
			t.Errorf("%s is not advice and must not be mutable", c)
		}
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
