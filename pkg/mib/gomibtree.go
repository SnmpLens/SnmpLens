package mib

// The tree from gomib (github.com/gosnmp/gomib), in the vocabulary the rest of
// SnmpLens reads: the migration from gosmi starts here, behind the API pkg/mib
// already has. testdata/reference.jsonl is what it must reproduce
// (TestGomibTreeAgainstTheReference), and frontend/tests/mibcontract.test.mjs
// what the renderer needs of the strings below.

import (
	"sort"
	"strings"

	gm "github.com/gosnmp/gomib/mib"
)

// gomibKind is gomib's kind as the renderer names it (utils/mibNode.js).
func gomibKind(k gm.Kind) string {
	switch k {
	case gm.KindScalar:
		return "Scalar"
	case gm.KindTable:
		return "Table"
	case gm.KindRow:
		return "Row"
	case gm.KindColumn:
		return "Column"
	case gm.KindNotification:
		return "Notification"
	case gm.KindGroup:
		return "Group"
	case gm.KindCompliance:
		return "Compliance"
	case gm.KindCapability:
		return "Capabilities"
	case gm.KindNode, gm.KindInternal:
		return "Node"
	}
	return "Unknown"
}

// gomibAccess is an object's access as the tree has always given it. read-create
// is reported as ReadWrite, as gosmi did: the renderer offers SET on what reads
// as writable, and "read-create" does not.
func gomibAccess(a gm.Access) string {
	switch a {
	case gm.AccessNotAccessible:
		return "NotAccessible"
	case gm.AccessAccessibleForNotify:
		return "Notify"
	case gm.AccessReadOnly:
		return "ReadOnly"
	case gm.AccessReadWrite, gm.AccessReadCreate, gm.AccessWriteOnly:
		return "ReadWrite"
	case gm.AccessNotImplemented:
		return "NotImplemented"
	}
	return "Unknown"
}

func gomibStatus(s gm.Status) string {
	switch s {
	case gm.StatusCurrent:
		return "Current"
	case gm.StatusDeprecated:
		return "Deprecated"
	case gm.StatusObsolete:
		return "Obsolete"
	case gm.StatusMandatory:
		return "Mandatory"
	case gm.StatusOptional:
		return "Optional"
	}
	return ""
}

// gomibSyntax names an object's syntax: its type's name when the type has one
// of its own — a textual convention, an SMI type — and otherwise its base, in
// the spelling the tree has always used.
func gomibSyntax(o *gm.Object) string {
	t := o.Type()
	if t == nil {
		return ""
	}
	if name := t.Name(); name != "" && !strings.ContainsAny(name, " ") && name != "INTEGER" && name != "BITS" {
		return name
	}
	switch t.EffectiveBase() {
	case gm.BaseObjectIdentifier:
		return "ObjectIdentifier"
	case gm.BaseOctetString:
		return "OctetString"
	case gm.BaseBits:
		return "Bits"
	case gm.BaseInteger32:
		if len(o.EffectiveEnums()) > 0 {
			return "Enumeration"
		}
		return "Integer32"
	}
	return t.EffectiveBase().String()
}

// gomibDescription is the description of whatever defines the node, for the
// panel: gomib keeps the text as written, with the indentation of every line
// after the first and the file's line endings, which gosmi took out. gosmi also
// took out the blank lines, and with them every paragraph an author wrote; they
// stay, one blank line however many separated them.
func gomibDescription(n *gm.Node) string {
	return dedent(rawDescription(n))
}

func dedent(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for i, line := range lines {
		if i > 0 {
			line = strings.TrimLeft(line, " \t")
		}
		if line == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \t\n")
}

func rawDescription(n *gm.Node) string {
	switch {
	case n.Object() != nil:
		return n.Object().Description()
	case n.Notification() != nil:
		return n.Notification().Description()
	case n.Group() != nil:
		return n.Group().Description()
	case n.Compliance() != nil:
		return n.Compliance().Description()
	case n.Capability() != nil:
		return n.Capability().Description()
	}
	// A MODULE-IDENTITY's description is the module's.
	if mod := n.Module(); mod != nil && n.Description() == "" && mod.OID().String() == n.OID().String() {
		return mod.Description()
	}
	return n.Description()
}

// gomibStatusOf is the status of whatever defines the node, "" for a plain
// OBJECT IDENTIFIER assignment, which declares none.
func gomibStatusOf(n *gm.Node) string {
	switch {
	case n.Object() != nil:
		return gomibStatus(n.Object().Status())
	case n.Notification() != nil:
		return gomibStatus(n.Notification().Status())
	case n.Group() != nil:
		return gomibStatus(n.Group().Status())
	case n.Compliance() != nil:
		return gomibStatus(n.Compliance().Status())
	case n.Capability() != nil:
		return gomibStatus(n.Capability().Status())
	}
	if st, ok := n.ObjectIdentityStatus(); ok {
		return gomibStatus(st)
	}
	return ""
}

// treeFromGomib is buildTree's answer, from a gomib load: one Node per named
// node, nested by OID, roots sorted.
func treeFromGomib(m *gm.Mib) []*Node {
	nodes := map[string]*Node{}
	var order []*gm.Node
	for n := range m.Nodes() {
		// The arcs at the root (iso, ccitt, joint-iso-ccitt) and the nameless
		// ones in between were never in the tree.
		if n.Name() == "" || n.Parent() == nil || n.Parent().Parent() == nil {
			continue
		}
		oid := n.OID().String()
		out := &Node{
			Name: n.Name(), Oid: oid, Description: gomibDescription(n), Children: []*Node{},
			MibType: gomibKind(n.Kind()), Access: "Unknown", Status: gomibStatusOf(n),
		}
		if o := n.Object(); o != nil {
			out.Access = gomibAccess(o.Access())
			out.Syntax = gomibSyntax(o)
			out.Units = o.Units()
			values := o.EffectiveEnums()
			if len(values) == 0 {
				values = o.EffectiveBits()
			}
			if len(values) > 0 {
				out.EnumValues = make(map[string]int64, len(values))
				for _, v := range values {
					out.EnumValues[v.Label] = v.Value
				}
			}
		}
		nodes[oid] = out
		order = append(order, n)
	}
	var roots []*Node
	for _, n := range order {
		out := nodes[n.OID().String()]
		// Under the nearest named ancestor: an SMIv1 TRAP-TYPE sits at
		// enterprise.0.specific, and nothing names the .0 between them.
		attached := false
		for p := n.Parent(); p != nil; p = p.Parent() {
			if parent, ok := nodes[p.OID().String()]; ok {
				parent.Children = append(parent.Children, out)
				out.Parent = parent.Name
				attached = true
				break
			}
		}
		if !attached {
			roots = append(roots, out)
		}
	}
	for _, r := range roots {
		sortNodes(r)
	}
	sort.Slice(roots, func(i, j int) bool { return isOidLess(roots[i].Oid, roots[j].Oid) })
	return roots
}
