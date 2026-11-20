package mib

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/gosnmp/gomib"
	gm "github.com/gosnmp/gomib/mib"
)

// The migration's measure for naming: Translate, ResolveOid and NameOIDs from
// gosmi, live, against their gomib counterparts, on every node of the tree, an
// instance under each, an OID no MIB defines under each, and OIDs that are not
// OIDs. It reports while the migration is under way.
func TestGomibNamesAgainstGosmi(t *testing.T) {
	s, dir := vendorDir(t, map[string]string{
		"EXAMPLE-CONSTRUCTS-MIB": constructsMib,
		"example-v1.mib":         v1Module,
	})
	names, err := ListMibFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.LoadWithDiagnostics(names)
	src, err := gomib.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := gomib.Load(context.Background(), gomib.WithSource(src), gomib.WithResolverStrictness(gm.ResolverPermissive))
	if err != nil {
		t.Fatalf("gomib: %v", err)
	}

	oids := []string{"", "abc", "1..3", "-1", "0", "1", "2", "1.0.99999.5", "9.9.9", "1.3.6.1.4.1.99999.1",
		".1.3.6.1.2.1.1.1.0", "1.3.6.1.2.1.2.2.1.2.5.6.7"}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			oids = append(oids, n.Oid, n.Oid+".0", n.Oid+".1.2", n.Oid+".4294967295")
			walk(n.Children)
		}
	}
	walk(resp.Tree)

	diffs := map[string][]string{}
	add := func(kind, oid string, ref, now any) {
		diffs[kind] = append(diffs[kind], fmt.Sprintf("%q: gosmi %v, gomib %v", oid, ref, now))
	}
	flat := func(s string) string {
		for strings.Contains(s, "\n\n") {
			s = strings.ReplaceAll(s, "\n\n", "\n")
		}
		return s
	}
	gosmiNames := s.NameOIDs(oids)
	for _, oid := range oids {
		a, b := s.Translate(oid), gomibTranslate(m, oid)
		if a.Name != b.Name {
			add("Translate name", oid, a.Name, b.Name)
		} else if a.Description != b.Description {
			if flat(a.Description) == flat(b.Description) {
				diffs["Translate description, paragraphs kept"] = append(diffs["Translate description, paragraphs kept"], oid)
			} else {
				add("Translate description", oid, fmt.Sprintf("%.60q", a.Description), fmt.Sprintf("%.60q", b.Description))
			}
		}

		x, y := s.ResolveOid(oid), gomibResolveOid(m, oid)
		if x.Name != y.Name {
			add("ResolveOid name", oid, x.Name, y.Name)
		}
		if x.Syntax != y.Syntax {
			add("ResolveOid syntax", oid, x.Syntax, y.Syntax)
		}
		if fmt.Sprint(x.EnumValues) != fmt.Sprint(y.EnumValues) && (len(x.EnumValues) > 0 || len(y.EnumValues) > 0) {
			add("ResolveOid enums", oid, len(x.EnumValues), len(y.EnumValues))
		}

		p, inGosmi := gosmiNames[oid]
		q, inGomib := gomibNameOID(m, oid)
		if inGosmi != inGomib || p != q {
			add("NameOIDs", oid, p, q)
		}
	}

	total := 0
	var kinds []string
	for k, d := range diffs {
		kinds = append(kinds, k)
		total += len(d)
	}
	sort.Strings(kinds)
	t.Logf("%d OIDs, %d differences", len(oids), total)
	for _, k := range kinds {
		d := diffs[k]
		sort.Strings(d)
		t.Logf("%s: %d", k, len(d))
		for i, line := range d {
			if i == 6 {
				t.Logf("   ...")
				break
			}
			t.Logf("   %s", line)
		}
	}
}
