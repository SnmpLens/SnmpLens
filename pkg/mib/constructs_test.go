package mib

import (
	"strings"
	"testing"
)

// constructsMib holds one of each construct the bundled MIBs leave out: BITS,
// Opaque, tables indexed by an OBJECT IDENTIFIER (length-prefixed and IMPLIED)
// and by an enumeration, an OBJECT-GROUP, AGENT-CAPABILITIES, and an SMIv2
// notification beside an SMIv1 one's place.
const constructsMib = `EXAMPLE-CONSTRUCTS-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, NOTIFICATION-TYPE, Integer32, Opaque, enterprises
        FROM SNMPv2-SMI
    OBJECT-GROUP, AGENT-CAPABILITIES
        FROM SNMPv2-CONF;

exampleConstructs MODULE-IDENTITY
    LAST-UPDATED "202611200000Z"
    ORGANIZATION "Example"
    CONTACT-INFO "Example"
    DESCRIPTION  "One of each construct the bundled MIBs do not use."
    ::= { enterprises 99980 }

exampleFlags OBJECT-TYPE
    SYNTAX      BITS { power(0), fan(1), door(2) }
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "What is wrong with the box."
    ::= { exampleConstructs 1 }

exampleBlob OBJECT-TYPE
    SYNTAX      Opaque
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "Something only the vendor reads."
    ::= { exampleConstructs 2 }

exampleOidTable OBJECT-TYPE
    SYNTAX      SEQUENCE OF ExampleOidEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "Rows keyed by an OBJECT IDENTIFIER."
    ::= { exampleConstructs 3 }

exampleOidEntry OBJECT-TYPE
    SYNTAX      ExampleOidEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "A row."
    INDEX       { exampleOidKey }
    ::= { exampleOidTable 1 }

ExampleOidEntry ::= SEQUENCE { exampleOidKey OBJECT IDENTIFIER, exampleOidValue Integer32 }

exampleOidKey OBJECT-TYPE
    SYNTAX      OBJECT IDENTIFIER
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "The key."
    ::= { exampleOidEntry 1 }

exampleOidValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A value."
    ::= { exampleOidEntry 2 }

exampleImpTable OBJECT-TYPE
    SYNTAX      SEQUENCE OF ExampleImpEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "Rows keyed by an IMPLIED OBJECT IDENTIFIER."
    ::= { exampleConstructs 4 }

exampleImpEntry OBJECT-TYPE
    SYNTAX      ExampleImpEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "A row."
    INDEX       { IMPLIED exampleImpKey }
    ::= { exampleImpTable 1 }

ExampleImpEntry ::= SEQUENCE { exampleImpKey OBJECT IDENTIFIER, exampleImpValue Integer32 }

exampleImpKey OBJECT-TYPE
    SYNTAX      OBJECT IDENTIFIER
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "The key."
    ::= { exampleImpEntry 1 }

exampleImpValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A value."
    ::= { exampleImpEntry 2 }

exampleEnumTable OBJECT-TYPE
    SYNTAX      SEQUENCE OF ExampleEnumEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "Rows keyed by an enumeration."
    ::= { exampleConstructs 5 }

exampleEnumEntry OBJECT-TYPE
    SYNTAX      ExampleEnumEntry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "A row."
    INDEX       { exampleEnumKey }
    ::= { exampleEnumTable 1 }

ExampleEnumEntry ::= SEQUENCE { exampleEnumKey INTEGER, exampleEnumValue Integer32 }

exampleEnumKey OBJECT-TYPE
    SYNTAX      INTEGER { red(1), green(2) }
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION "The key."
    ::= { exampleEnumEntry 1 }

exampleEnumValue OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION "A value."
    ::= { exampleEnumEntry 2 }

exampleCaps AGENT-CAPABILITIES
    PRODUCT-RELEASE "Example 1.0"
    STATUS          current
    DESCRIPTION     "What the example agent implements."
    SUPPORTS        EXAMPLE-CONSTRUCTS-MIB
    INCLUDES        { exampleGroup }
    ::= { exampleConstructs 6 }

exampleGroup OBJECT-GROUP
    OBJECTS     { exampleFlags, exampleBlob, exampleOidValue, exampleImpValue, exampleEnumValue }
    STATUS      current
    DESCRIPTION "Everything readable."
    ::= { exampleConstructs 7 }

exampleTraps OBJECT IDENTIFIER ::= { exampleConstructs 0 }

exampleAlarm NOTIFICATION-TYPE
    OBJECTS     { exampleFlags }
    STATUS      current
    DESCRIPTION "Something went wrong."
    ::= { exampleTraps 1 }
END
`

// constructsService loads the bundled MIBs, constructsMib and the SMIv1 module.
func constructsService(t *testing.T) (*Service, map[string]*Node) {
	t.Helper()
	s, dir := vendorDir(t, map[string]string{
		"EXAMPLE-CONSTRUCTS-MIB": constructsMib,
		"example-v1.mib":         v1Module,
	})
	names, err := ListMibFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.LoadWithDiagnostics(names)
	for _, d := range resp.Diagnostics {
		if !d.Success {
			t.Fatalf("%s did not load: %s", d.FileName, d.Error)
		}
	}
	nodes := map[string]*Node{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			nodes[n.Oid] = n
			walk(n.Children)
		}
	}
	walk(resp.Tree)
	return s, nodes
}

// Every kind of definition SMI has, as the tree describes it to the renderer.
// These strings are not cosmetic: MibPanel.svelte offers GET and SET on a
// Scalar or a Column, a walk on a Table, a Row, a Node or a Group, "walk as
// table" on a Table or a Row, and SET only when the access reads as writable.
// A read-create column is folded into ReadWrite (TestGosmiFoldsReadCreateIntoReadWrite),
// and an engine that spelt it "read-create" would turn SET off on every one.
func TestEveryKindOfDefinitionIsInTheTree(t *testing.T) {
	_, nodes := constructsService(t)
	cases := []struct {
		oid, name, kind, syntax, access, status string
	}{
		{"1.3.6.1.2.1.2", "interfaces", "Node", "", "Unknown", ""},
		{"0.0", "zeroDotZero", "Node", "", "Unknown", "Current"}, // OBJECT-IDENTITY
		{"1.3.6.1.2.1.2.1", "ifNumber", "Scalar", "Integer32", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.2.2", "ifTable", "Table", "", "NotAccessible", "Current"},
		{"1.3.6.1.2.1.2.2.1", "ifEntry", "Row", "", "NotAccessible", "Current"},
		{"1.3.6.1.2.1.2.2.1.1", "ifIndex", "Column", "InterfaceIndex", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.2.2.1.6", "ifPhysAddress", "Column", "PhysAddress", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.2.2.1.7", "ifAdminStatus", "Column", "Enumeration", "ReadWrite", "Current"},
		{"1.3.6.1.2.1.2.2.1.5", "ifSpeed", "Column", "Gauge32", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.2.2.1.22", "ifSpecific", "Column", "ObjectIdentifier", "ReadOnly", "Deprecated"},
		{"1.3.6.1.2.1.11.2", "snmpOutPkts", "Scalar", "Counter32", "ReadOnly", "Obsolete"},
		{"1.3.6.1.2.1.31.1.1.1.6", "ifHCInOctets", "Column", "Counter64", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.31.1.4.1.2", "ifRcvAddressStatus", "Column", "RowStatus", "ReadWrite", "Current"}, // read-create
		{"1.3.6.1.2.1.1.1", "sysDescr", "Scalar", "DisplayString", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.1.3", "sysUpTime", "Scalar", "TimeTicks", "ReadOnly", "Current"},
		{"1.3.6.1.2.1.4.20.1.1", "ipAdEntAddr", "Column", "IpAddress", "ReadOnly", "Deprecated"},
		{"1.3.6.1.6.3.1.1.4.1", "snmpTrapOID", "Scalar", "ObjectIdentifier", "Unknown", "Current"}, // accessible-for-notify
		{"1.3.6.1.6.3.1.1.5.3", "linkDown", "Notification", "", "Unknown", "Current"},
		{"1.3.6.1.2.1.31.2.1.10", "ifGeneralInformationGroup", "Group", "", "Unknown", "Current"},
		{"1.3.6.1.2.1.31.2.2.3", "ifCompliance3", "Compliance", "", "Unknown", "Current"},
		{"1.3.6.1.4.1.99980.1", "exampleFlags", "Scalar", "Bits", "ReadOnly", "Current"},
		{"1.3.6.1.4.1.99980.2", "exampleBlob", "Scalar", "Opaque", "ReadOnly", "Current"},
		{"1.3.6.1.4.1.99980.6", "exampleCaps", "Capabilities", "", "Unknown", "Current"},
		{"1.3.6.1.4.1.99980.7", "exampleGroup", "Group", "", "Unknown", "Current"},
		{"1.3.6.1.4.1.99980.0.1", "exampleAlarm", "Notification", "", "Unknown", "Current"},
	}
	for _, c := range cases {
		n := nodes[c.oid]
		if n == nil {
			t.Errorf("%s (%s) is not in the tree", c.oid, c.name)
			continue
		}
		got := []string{n.Name, n.MibType, n.Syntax, n.Access, n.Status}
		want := []string{c.name, c.kind, c.syntax, c.access, c.status}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got name/kind/syntax/access/status %q, want %q", c.oid, got, want)
				break
			}
		}
	}

	// Enumerations reach the renderer as a map, a textual convention's too, and
	// BITS name their bit positions the same way.
	for oid, want := range map[string]map[string]int64{
		"1.3.6.1.2.1.2.2.1.7":    {"up": 1, "down": 2, "testing": 3},
		"1.3.6.1.4.1.99980.1":    {"power": 0, "fan": 1, "door": 2},
		"1.3.6.1.2.1.31.1.4.1.2": {"active": 1, "createAndGo": 4, "destroy": 6},
	} {
		for label, v := range want {
			if got, ok := nodes[oid].EnumValues[label]; !ok || got != v {
				t.Errorf("%s: %s = %d, %v; want %d", oid, label, got, ok, v)
			}
		}
	}
	if got := nodes["1.3.6.1.2.1.2.2.1.3"].EnumValues["ethernetCsmacd"]; got != 6 {
		t.Errorf("ifType's ethernetCsmacd = %d, want 6 (IANAifType, through its textual convention)", got)
	}
}

// What the Traps tab and the walk results put in the Name column. A
// notification is named whether it is SMIv2's NOTIFICATION-TYPE or SMIv1's
// TRAP-TYPE, read at enterprise.0.specific as RFC 3584 maps it; an instance is
// named after its column; an OID no MIB defines after the closest node above it.
func TestNotificationsAndInstancesAreNamed(t *testing.T) {
	s, _ := constructsService(t)
	for oid, want := range map[string]string{
		"1.3.6.1.6.3.1.1.5.3":   "linkDown",
		".1.3.6.1.6.3.1.1.5.3":  "linkDown",
		"1.3.6.1.6.3.1.1.5.1":   "coldStart",
		"1.3.6.1.4.1.99980.0.1": "exampleAlarm",  // NOTIFICATION-TYPE
		"1.3.6.1.4.1.99991.0.1": "exampleReboot", // TRAP-TYPE, ENTERPRISE example ::= 1
		"1.3.6.1.2.1.2.2.1.2.5": "ifDescr",
		"1.3.6.1.2.1.1.1.0":     "sysDescr",
		"1.3.6.1.4.1.99999.1":   "enterprises",
	} {
		if got := s.Translate(oid).Name; got != want {
			t.Errorf("Translate(%s) = %q, want %q", oid, got, want)
		}
	}
}

// Indexes of every shape a table can be keyed by, decoded and encoded back:
// an OBJECT IDENTIFIER takes a length unless it is IMPLIED (RFC 2578 7.7), an
// enumeration shows its label, and a sub-identifier past 255 is not an octet.
func TestIndexesOfEveryShape(t *testing.T) {
	s, _ := constructsService(t)
	cases := []struct {
		table, instance string
		values          []string
		display         string
	}{
		{"1.3.6.1.4.1.99980.3", "3.1.3.6", []string{"1.3.6"}, "1.3.6"},
		{"1.3.6.1.4.1.99980.4", "1.3.6", []string{"1.3.6"}, "1.3.6"},
		{"1.3.6.1.4.1.99980.5", "2", []string{"2"}, "green(2)"},
	}
	for _, c := range cases {
		d := s.DecodeIndexes(c.table, []string{c.instance})[0]
		if d.Error != "" || len(d.Parts) != 1 || d.Parts[0].Display != c.display {
			t.Errorf("%s: %s decoded as %+v, want %q", c.table, c.instance, d, c.display)
		}
		got, err := s.EncodeIndex(c.table, c.values)
		if err != nil || got != c.instance {
			t.Errorf("%s: %v encoded as %q, %v; want %q", c.table, c.values, got, err, c.instance)
		}
	}
	d := s.DecodeIndexes("1.3.6.1.2.1.4.22", []string{"3.10.0.0.300"})[0]
	if !strings.Contains(d.Error, "not an octet") {
		t.Errorf("an address with 300 in it decoded as %+v", d)
	}
}
