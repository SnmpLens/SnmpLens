package mib

import (
	"strings"
	"testing"
)

// longOidModule is a module registered at oid, with one object under it.
func longOidModule(module, identity, oid, object string) string {
	return strings.NewReplacer("@MODULE@", module, "@IDENTITY@", identity, "@OID@", oid, "@OBJECT@", object).Replace(
		`@MODULE@ DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32 FROM SNMPv2-SMI;

@IDENTITY@ MODULE-IDENTITY
    LAST-UPDATED "202611200000Z"
    ORGANIZATION "Example"
    CONTACT-INFO "Example"
    DESCRIPTION  "A module registered with an OID written arc by arc."
    ::= @OID@

@OBJECT@ OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A value."
    ::= { @IDENTITY@ 1 }
END
`)
}

// An OID written with several arcs after its parent creates a node for each arc
// in between, and gosmi v0.4.4 carried on from a node it had just dropped
// whenever one was already in the tree: everything after it had no OID and the
// module's objects were missing from the tree, with the load reported as a
// success. Two modules registered under `iso std(0) 62439` were enough (issue
// #200), and so was any OID spelt out through nodes SNMPv2-SMI defines.
// third_party/README.md has the fix; this fails against unpatched v0.4.4.
func TestALongOidCrossingANodeInTheTreeStaysInIt(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{
		// The deeper one sorts first, and loads first, as in the issue.
		"EXAMPLE-ARC-100-MIB.mib": longOidModule("EXAMPLE-ARC-100-MIB", "arcHundred", "{ iso std(0) 99991 100 }", "hundredValue"),
		"EXAMPLE-ARC-3-MIB.mib":   longOidModule("EXAMPLE-ARC-3-MIB", "arcThree", "{ iso std(0) 99991 }", "threeValue"),
		"EXAMPLE-ARC-SMI-MIB.mib": longOidModule("EXAMPLE-ARC-SMI-MIB", "arcSmi",
			"{ iso org(3) dod(6) internet(1) private(4) enterprises(1) 99991 }", "smiValue"),
	})
	results := loadAll(t, s, dir)

	for file, r := range results {
		if !strings.HasPrefix(file, "EXAMPLE-ARC-") {
			continue
		}
		if !r.Success {
			t.Errorf("%s did not load: %s", file, r.Error)
		}
		for _, w := range r.Warnings {
			if w.Code == CodeNoOid {
				t.Errorf("%s: %s", file, w.Message)
			}
		}
	}
	for oid, want := range map[string]string{
		"1.0.99991.1":         "threeValue",
		"1.0.99991.100":       "arcHundred",
		"1.0.99991.100.1":     "hundredValue",
		"1.3.6.1.4.1.99991.1": "smiValue",
		"1.3.6.1.4.1.99991":   "arcSmi",
		"1.3.6.1.2.1.1.1":     "sysDescr", // what the arcs crossed is still there
		"1.3.6.1.4.1":         "enterprises",
	} {
		if got := s.ResolveOid(oid).Name; got != want {
			t.Errorf("%s resolves to %q, want %q", oid, got, want)
		}
	}
}
