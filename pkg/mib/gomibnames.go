package mib

// OIDs named from a gomib load, as Translate, ResolveOid and NameOIDs name them
// from gosmi's: the second step of the migration (gomibtree.go has the first).

import (
	"strconv"
	"strings"

	gm "github.com/gosnmp/gomib/mib"
)

// gomibLookup is the node an OID is named after: the node at it or, as
// gosmi.GetNodeByOID answered, the closest one above it that has a name — an
// instance is named after its column, an OID no MIB defines after the last
// node on its way that one does.
func gomibLookup(m *gm.Mib, oid string) (*gm.Node, gm.OID, error) {
	id, err := gm.ParseOID(oid)
	if err != nil {
		return nil, nil, err
	}
	n := m.LongestPrefixByOID(id)
	for n != nil && n.Name() == "" {
		n = n.Parent()
	}
	return n, id, nil
}

// rootArcs names the three arcs at the root of every OID, which gomib leaves
// unnamed under ccitt and joint-iso-ccitt when no module defines anything there.
var rootArcs = map[uint32]string{0: "ccitt", 1: "iso", 2: "joint-iso-ccitt"}

// gomibTranslate is Translate's answer from gomib.
func gomibTranslate(m *gm.Mib, oid string) OidDetails {
	n, id, err := gomibLookup(m, oid)
	if err != nil {
		return OidDetails{Name: oid, Description: "Invalid OID format"}
	}
	if n == nil {
		if root, ok := rootArcs[id[0]]; ok {
			return OidDetails{Name: root}
		}
		return OidDetails{Name: oid, Description: "OID not found in loaded MIBs"}
	}
	return OidDetails{Name: n.Name(), Description: gomibDescription(n)}
}

// gomibResolveOid is ResolveOid's answer from gomib.
func gomibResolveOid(m *gm.Mib, oid string) OidInfo {
	n, id, err := gomibLookup(m, oid)
	if err != nil {
		return OidInfo{Name: oid}
	}
	if n == nil {
		if root, ok := rootArcs[id[0]]; ok {
			return OidInfo{Name: root}
		}
		return OidInfo{Name: oid}
	}
	info := OidInfo{Name: n.Name(), Description: gomibDescription(n)}
	if o := n.Object(); o != nil {
		info.Syntax = gomibSyntax(o)
		info.EnumValues = gomibEnums(o)
	}
	return info
}

// gomibEnums is an object's labels, an enumeration's or its BITS', as the
// panel and the value formatter read them.
func gomibEnums(o *gm.Object) map[string]int64 {
	values := o.EffectiveEnums()
	if len(values) == 0 {
		values = o.EffectiveBits()
	}
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int64, len(values))
	for _, v := range values {
		out[v.Label] = v.Value
	}
	return out
}

// gomibNameOID is NameOIDs' answer for one OID from gomib: an instance of a
// scalar or a column, with its module, object and instance arcs.
func gomibNameOID(m *gm.Mib, oid string) (OidName, bool) {
	n, id, err := gomibLookup(m, strings.TrimPrefix(oid, "."))
	if err != nil || n == nil || n.Object() == nil {
		return OidName{}, false
	}
	if k := n.Kind(); k != gm.KindScalar && k != gm.KindColumn {
		return OidName{}, false
	}
	at := n.OID()
	if len(at) > len(id) {
		return OidName{}, false
	}
	arcs := make([]string, 0, len(id)-len(at))
	for _, a := range id[len(at):] {
		arcs = append(arcs, strconv.FormatUint(uint64(a), 10))
	}
	module := ""
	if mod := n.Module(); mod != nil {
		module = mod.Name()
	}
	return OidName{Module: module, Object: n.Name(), Instance: strings.Join(arcs, ".")}, true
}
