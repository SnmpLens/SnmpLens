package mib

// Conceptual tables described from a gomib load, as Service.Table describes
// them from gosmi's: the third step of the migration.

import (
	"fmt"

	gm "github.com/gosnmp/gomib/mib"
)

// gomibWireType is the ASN.1 type a SET must carry for a column of this type,
// from its base type — which gomib keeps through every textual convention, so a
// TimeStamp is TimeTicks on the wire where gosmi, which has no such base, said
// Gauge32.
func gomibWireType(t *gm.Type) string {
	if t == nil {
		return ""
	}
	switch t.EffectiveBase() {
	case gm.BaseInteger32:
		return "Integer"
	case gm.BaseUnsigned32, gm.BaseGauge32:
		return "Gauge32"
	case gm.BaseCounter32:
		return "Counter32"
	case gm.BaseCounter64:
		return "Counter64"
	case gm.BaseTimeTicks:
		return "TimeTicks"
	case gm.BaseIPAddress:
		return "IpAddress"
	case gm.BaseOpaque:
		return "Opaque"
	case gm.BaseObjectIdentifier:
		return "ObjectIdentifier"
	case gm.BaseOctetString, gm.BaseBits:
		return "OctetString"
	}
	return ""
}

// gomibTableInfo is Service.Table's answer from gomib: the table holding oid,
// which may name the table, its row, a column or an instance.
func gomibTableInfo(m *gm.Mib, oid string) (*TableInfo, error) {
	n, _, err := gomibLookup(m, oid)
	if err != nil {
		return nil, fmt.Errorf("%q is not an OID", oid)
	}
	if n == nil {
		return nil, fmt.Errorf("%s is not in any loaded MIB", oid)
	}
	switch n.Kind() {
	case gm.KindColumn:
		n = n.Parent().Parent()
	case gm.KindRow:
		n = n.Parent()
	case gm.KindTable:
	default:
		return nil, fmt.Errorf("%s is a %s, not a table", oid, gomibKind(n.Kind()))
	}
	table := n.Object()
	if table == nil || table.Entry() == nil {
		return nil, fmt.Errorf("%s has no row", oid)
	}
	row := table.Entry()
	info := &TableInfo{Oid: n.OID().String(), Name: n.Name(), RowOid: row.OID().String(), RowName: row.Name()}
	if aug := row.Augments(); aug != nil {
		info.Augments = aug.Name()
	}

	// EffectiveIndexes follows AUGMENTS, IMPLIED included: the flag gosmi lost
	// on an augmenting row (effectiveImplied) is not lost here.
	indexes := row.EffectiveIndexes()
	indexNames := make(map[string]bool, len(indexes))
	for i, idx := range indexes {
		part := IndexPart{}
		if idx.Object != nil {
			part.Name = idx.Object.Name()
			part.Oid = idx.Object.OID().String()
			part.Syntax = gomibSyntax(idx.Object)
		} else {
			part.Name, part.Syntax = idx.TypeName, idx.TypeName
		}
		part.Implied = idx.Implied && i == len(indexes)-1
		info.Implied = info.Implied || part.Implied
		info.Index = append(info.Index, part)
		indexNames[part.Name] = true
	}

	for _, col := range row.Columns() {
		c := TableColumn{
			Name: col.Name(), Oid: col.OID().String(), Access: gomibAccess(col.Access()),
			IsIndex: indexNames[col.Name()], Syntax: gomibSyntax(col), WireType: gomibWireType(col.Type()),
			EnumValues: gomibEnums(col),
		}
		switch col.Access() {
		case gm.AccessReadWrite, gm.AccessReadCreate, gm.AccessWriteOnly:
			c.Writable = true
		}
		if t := col.Type(); t != nil && t.Name() == "RowStatus" {
			info.RowStatusOid = c.Oid
		}
		info.Columns = append(info.Columns, c)
	}
	if len(info.Columns) == 0 {
		return nil, fmt.Errorf("%s has no columns", oid)
	}
	return info, nil
}
