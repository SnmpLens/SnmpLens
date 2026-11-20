package mib

import (
	"testing"
)

// An enumerated type restricted by a value — SYNTAX InetAddressType(1), as an
// Alcatel-Lucent MIB writes it — holds a range where gosmi expects a named
// number, and reading its labels was a panic: the whole load failed, every
// MIB with it. third_party/README.md has the fix; this panics against the
// original.
const restrictedEnumMib = `EXAMPLE-RESTRICTED-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI
    InetAddressType FROM INET-ADDRESS-MIB;

exampleRestricted MODULE-IDENTITY
    LAST-UPDATED "202611200000Z"
    ORGANIZATION "Example"
    CONTACT-INFO "Example"
    DESCRIPTION  "An enumeration restricted by a value."
    ::= { enterprises 99961 }

exampleAddressType OBJECT-TYPE
    SYNTAX      InetAddressType(1)
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Always ipv4."
    ::= { exampleRestricted 1 }

exampleCount OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Something after it."
    ::= { exampleRestricted 2 }
END
`

func TestAnEnumerationRestrictedByAValueLoads(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{"EXAMPLE-RESTRICTED-MIB": restrictedEnumMib})
	r := loadAll(t, s, dir)["EXAMPLE-RESTRICTED-MIB"]
	if !r.Success {
		t.Fatalf("did not load: %s", r.Error)
	}
	for oid, want := range map[string]string{
		"1.3.6.1.4.1.99961.1": "exampleAddressType",
		"1.3.6.1.4.1.99961.2": "exampleCount",
	} {
		if got := s.ResolveOid(oid).Name; got != want {
			t.Errorf("%s = %q, want %q", oid, got, want)
		}
	}
}

// gosmi read a module's types in order and stopped at the first it could not
// place — a textual convention whose SYNTAX is another one, which RFC 2579
// forbids and vendor MIBs are full of, or one naming a type nothing defines.
// Every type after it was undefined, the SEQUENCEs of the tables among them:
// their columns were read as scalars, so SnmpLens asked for column.0 and
// offered no table view, objects of the later types had none, and SET carried
// no wire type. On a corpus of vendor MIBs, 12 567 columns. Now such a type is
// skipped alone, and a textual convention of another one takes its parent's
// base type and labels.
const vendorTypesMib = `EXAMPLE-VENDOR-TYPES-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, enterprises FROM SNMPv2-SMI
    TEXTUAL-CONVENTION, RowStatus FROM SNMPv2-TC;

exampleVendor MODULE-IDENTITY
    LAST-UPDATED "202611200000Z"
    ORGANIZATION "Example"
    CONTACT-INFO "Example"
    DESCRIPTION  "Types as vendors write them."
    ::= { enterprises 99962 }

ExampleState ::= TEXTUAL-CONVENTION
    STATUS      current
    DESCRIPTION "A state."
    SYNTAX      INTEGER { up(1), down(2) }

ExampleAdminState ::= TEXTUAL-CONVENTION
    STATUS      current
    DESCRIPTION "A textual convention of another one."
    SYNTAX      ExampleState

ExampleLost ::= TEXTUAL-CONVENTION
    STATUS      current
    DESCRIPTION "One naming a type nothing defines."
    SYNTAX      NoSuchType

ExampleName ::= TEXTUAL-CONVENTION
    DISPLAY-HINT "255a"
    STATUS      current
    DESCRIPTION "Defined after both."
    SYNTAX      OCTET STRING (SIZE (0..32))

exampleTable OBJECT-TYPE
    SYNTAX      SEQUENCE OF ExampleEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "A table whose SEQUENCE comes after them."
    ::= { exampleVendor 1 }

exampleEntry OBJECT-TYPE
    SYNTAX      ExampleEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "A row."
    INDEX       { exampleIndex }
    ::= { exampleTable 1 }

ExampleEntry ::= SEQUENCE {
    exampleIndex     Integer32,
    exampleAdmin     ExampleAdminState,
    exampleName      ExampleName,
    exampleRowStatus RowStatus
}

exampleIndex OBJECT-TYPE
    SYNTAX      Integer32 (1..100)
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "The index."
    ::= { exampleEntry 1 }

exampleAdmin OBJECT-TYPE
    SYNTAX      ExampleAdminState
    MAX-ACCESS  read-create
    STATUS      current
    DESCRIPTION "Up or down."
    ::= { exampleEntry 2 }

exampleName OBJECT-TYPE
    SYNTAX      ExampleName
    MAX-ACCESS  read-create
    STATUS      current
    DESCRIPTION "A name."
    ::= { exampleEntry 3 }

exampleRowStatus OBJECT-TYPE
    SYNTAX      RowStatus
    MAX-ACCESS  read-create
    STATUS      current
    DESCRIPTION "Creates and destroys rows."
    ::= { exampleEntry 4 }
END
`

func TestTypesAfterOneGosmiCannotPlaceAreStillRead(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{"EXAMPLE-VENDOR-TYPES-MIB": vendorTypesMib})
	names, err := ListMibFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.LoadWithDiagnostics(names)
	nodes := map[string]*Node{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			nodes[n.Oid] = n
			walk(n.Children)
		}
	}
	walk(resp.Tree)

	for oid, want := range map[string][2]string{
		"1.3.6.1.4.1.99962.1.1.1": {"Column", "Integer32"},
		"1.3.6.1.4.1.99962.1.1.2": {"Column", "ExampleAdminState"},
		"1.3.6.1.4.1.99962.1.1.3": {"Column", "ExampleName"},
		"1.3.6.1.4.1.99962.1.1.4": {"Column", "RowStatus"},
	} {
		n := nodes[oid]
		if n == nil {
			t.Errorf("%s is not in the tree", oid)
			continue
		}
		if n.MibType != want[0] || n.Syntax != want[1] {
			t.Errorf("%s %s: kind %s, syntax %q; want %s, %q", oid, n.Name, n.MibType, n.Syntax, want[0], want[1])
		}
	}
	// The textual convention of another one has its parent's labels.
	if admin := nodes["1.3.6.1.4.1.99962.1.1.2"]; admin != nil {
		if admin.EnumValues["up"] != 1 || admin.EnumValues["down"] != 2 {
			t.Errorf("exampleAdmin's labels = %v, want up(1) down(2)", admin.EnumValues)
		}
	}

	info, err := s.Table("1.3.6.1.4.1.99962.1")
	if err != nil {
		t.Fatalf("the table view: %v", err)
	}
	if info.RowStatusOid != "1.3.6.1.4.1.99962.1.1.4" {
		t.Errorf("RowStatus column = %q, so rows cannot be created", info.RowStatusOid)
	}
	wire := map[string]string{}
	for _, c := range info.Columns {
		wire[c.Name] = c.WireType
		if c.Name != "exampleIndex" && !c.Writable {
			t.Errorf("%s is read-create and not writable", c.Name)
		}
	}
	if wire["exampleAdmin"] != "Integer" || wire["exampleName"] != "OctetString" {
		t.Errorf("wire types = %v", wire)
	}
}
