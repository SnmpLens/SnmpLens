package mib

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sleepinggenius2/gosmi"
)

// What pkg/mib assumes about gosmi, asserted against gosmi itself.
//
// Several pieces of this package work AROUND gosmi by mirroring a rule of its
// own: which file names it finds a module by (gosmiExtensions), which SMIv1
// imports it rewrites to their successors (gosmiConverted), that it lexes the
// whole file before parsing (parseSMI), which dates its grammar accepts
// (badDateEdits). A gosmi upgrade that changes one of them changes nothing in
// a build and nothing in a diff: the copy goes on describing the old rule and
// the results go quietly wrong. Each test here fails instead, at the upgrade —
// the moment to re-read gosmi and move the copy.

// gosmiDir is a fresh gosmi over dir, reached by plain path: these tests are
// about gosmi, not about mibFS.
func gosmiDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gosmi.Exit()
	gosmi.Init()
	gosmi.SetPath(dir)
	return dir
}

const contractBase = `CONTRACT-BASE-MIB DEFINITIONS ::= BEGIN
IMPORTS enterprises FROM SNMPv2-SMI;
contractRoot OBJECT IDENTIFIER ::= { enterprises 99970 }
END
`

// gosmiExtensions: gosmi finds CONTRACT-BASE-MIB under these file names and no
// others. mibFS adds an entry for every file gosmi would NOT find by this rule.
func TestGosmiFindsAModuleOnlyUnderTheseFileNames(t *testing.T) {
	smi, err := os.ReadFile("../../mibs/SNMPv2-SMI")
	if err != nil {
		t.Skip("no corpus")
	}
	for _, name := range []string{
		"CONTRACT-BASE-MIB", "CONTRACT-BASE-MIB.mib", "CONTRACT-BASE-MIB.my",
		"CONTRACT-BASE-MIB.mi2", "CONTRACT-BASE-MIB.txt",
		"CONTRACT-BASE-MIB.MIB", "CONTRACT-BASE-MIB.smi", "CONTRACT-BASE-MIB.asn1",
		"contract-base-mib.mib", "CONTRACT-BASE-MIB.v2.mib",
	} {
		gosmiDir(t, map[string]string{name: contractBase, "SNMPv2-SMI": string(smi)})
		var loaded string
		captureStdout(func() { loaded, _ = gosmi.LoadModule("CONTRACT-BASE-MIB") })
		found := loaded == "CONTRACT-BASE-MIB"
		if want := gosmiFinds(name, "CONTRACT-BASE-MIB"); found != want {
			t.Errorf("%s: gosmi finds it: %v; gosmiFinds says %v", name, found, want)
		}
	}
}

// gosmiConverted: every name listed is resolved by gosmi with the module named
// absent, and a name not listed is not — so the list is neither too long, which
// would hide a missing module in the editor, nor too short, which would report
// one the loader does not need. Except gosmiLost, which gosmi rewrites to a
// module that does not define it: those must NOT resolve, or the warning the
// editor gives about them has become false.
func TestGosmiRewritesExactlyTheImportsWeList(t *testing.T) {
	dir, names := corpusCopy(t)

	type probe struct{ module, name string }
	var probes []probe
	for module, symbols := range gosmiConverted {
		for name := range symbols {
			if name == "OBJECT-TYPE" || name == "TRAP-TYPE" {
				continue // keywords of the grammar, never looked up
			}
			probes = append(probes, probe{module, name})
		}
	}
	sort.Slice(probes, func(i, j int) bool {
		return probes[i].module+probes[i].name < probes[j].module+probes[j].name
	})
	// Controls, which must NOT resolve: the same modules, names gosmi does not
	// rewrite. Without them a test that resolved everything would pass.
	controls := []probe{{"RFC1213-MIB", "ifInOctets"}, {"RFC1155-SMI", "NetworkAddress"}}

	resolves := func(p probe) bool {
		user := "CONTRACT-USER-MIB DEFINITIONS ::= BEGIN\nIMPORTS OBJECT-TYPE, enterprises FROM SNMPv2-SMI\n    " +
			p.name + " FROM " + p.module + ";\n"
		if p.name[0] >= 'A' && p.name[0] <= 'Z' {
			// A type: an object whose SYNTAX it is.
			user += "contractObj OBJECT-TYPE\n    SYNTAX " + p.name +
				"\n    MAX-ACCESS read-only\n    STATUS current\n    DESCRIPTION \"x\"\n    ::= { enterprises 99971 }\nEND\n"
		} else {
			// A node: something placed under it.
			user += "contractObj OBJECT IDENTIFIER ::= { " + p.name + " 99 }\nEND\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "CONTRACT-USER-MIB"), []byte(user), 0o600); err != nil {
			t.Fatal(err)
		}
		gosmi.Exit()
		gosmi.Init()
		gosmi.SetPath(dir)
		captureStdout(func() {
			for _, n := range names {
				_, _ = gosmi.LoadModule(n)
			}
			_, _ = gosmi.LoadModule("CONTRACT-USER-MIB")
		})
		node, err := gosmi.GetNode("contractObj")
		if err != nil {
			return false
		}
		if p.name[0] >= 'A' && p.name[0] <= 'Z' {
			return node.Type != nil
		}
		return node.Oid != nil && node.OidLen > 1
	}

	for _, p := range probes {
		got, lost := resolves(p), gosmiLost[p.module][p.name]
		switch {
		case lost && got:
			t.Errorf("%s FROM %s is in gosmiLost, and gosmi now resolves it: take it out", p.name, p.module)
		case !lost && !got:
			t.Errorf("%s FROM %s is in gosmiConverted, and gosmi does not resolve it without %s",
				p.name, p.module, p.module)
		}
	}
	for module, names := range gosmiLost {
		for name := range names {
			if !gosmiConverted[module][name] {
				t.Errorf("%s FROM %s is in gosmiLost and not in gosmiConverted: gosmi still reads it from %s",
					name, module, module)
			}
		}
	}
	for _, p := range controls {
		if resolves(p) {
			t.Errorf("control %s FROM %s resolved without %s: the test cannot tell a listed name from any other",
				p.name, p.module, p.module)
		}
	}
}

// parseSMI looks behind a lexer error because participle lexes the WHOLE file
// before the grammar sees a token: a character it cannot lex anywhere is
// reported instead of a grammar error that comes before it.
func TestGosmiReportsALexerErrorBeforeAnEarlierGrammarError(t *testing.T) {
	mod := "CONTRACT-LEX-MIB DEFINITIONS ::= BEGIN\nIMPORTS FROM;\n" + strings.Repeat("\n", 5) + "x – y\nEND\n"
	_, err := parseOnce(mod)
	if err == nil {
		t.Fatal("the control parses")
	}
	if !strings.HasPrefix(err.Error(), "8:") || !strings.Contains(err.Error(), "no match found") {
		t.Errorf("gosmi now reports %q: it no longer lexes ahead, and parseSMI's look-behind can go", err)
	}
}

// neutralise blanks everything the lexer has no token for. What it leaves must
// lex: a lone full stop, a non-ASCII letter, a control character.
func TestNeutralisedProseLexes(t *testing.T) {
	prose := "Stray prose. With – dashes, é accents, 温度 and a\x01control. And a range 1..2"
	mod := "CONTRACT-PROSE-MIB DEFINITIONS ::= BEGIN\n" + prose + "\nEND\n"
	if _, err := parseOnce(mod); err == nil || !strings.Contains(err.Error(), "no match found") {
		t.Fatalf("the control is not a lexer error: %v", err)
	}
	if _, err := parseOnce(neutralise(mod)); err != nil && strings.Contains(err.Error(), "no match found") {
		t.Errorf("neutralise left something the lexer refuses: %v", err)
	}
}

// badDateEdits repairs a date to twelve digits and a Z because the grammar
// wants ten or twelve and refuses the rest.
func TestGosmiAcceptsOnlyTheseDates(t *testing.T) {
	mod := func(date string) string {
		return `CONTRACT-DATE-MIB DEFINITIONS ::= BEGIN
IMPORTS MODULE-IDENTITY, enterprises FROM SNMPv2-SMI;
contractDate MODULE-IDENTITY
    LAST-UPDATED "` + date + `"
    ORGANIZATION "x"
    CONTACT-INFO "x"
    DESCRIPTION  "x"
    ::= { enterprises 99972 }
END
`
	}
	for date, ok := range map[string]bool{
		"1904150000Z": true, "201904150000Z": true, "201904150000z": true,
		"20190415Z": false, "201904150000": false, "2019041500Z0": false,
	} {
		if _, err := parseOnce(mod(date)); (err == nil) != ok {
			t.Errorf("LAST-UPDATED %q: parses=%v, want %v", date, err == nil, ok)
		}
	}
}

// LoadWithDiagnostics recovers the load error from stdout: smi.LoadModule
// prints it and returns an empty name.
func TestGosmiPrintsTheLoadErrorItDiscards(t *testing.T) {
	gosmiDir(t, map[string]string{"CONTRACT-BROKEN-MIB": "CONTRACT-BROKEN-MIB DEFINITIONS ::= BEGIN\nIMPORTS FROM;\nEND\n"})
	var err error
	printed := captureStdout(func() { _, err = gosmi.LoadModule("CONTRACT-BROKEN-MIB") })
	if err == nil {
		t.Fatal("a broken module loaded")
	}
	if !strings.Contains(printed, "Parse module") || !strings.Contains(printed, "2:") {
		t.Errorf("gosmi no longer prints the positioned error: %q", printed)
	}
}
