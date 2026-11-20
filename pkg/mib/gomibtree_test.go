package mib

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/gosnmp/gomib"
	gm "github.com/gosnmp/gomib/mib"
)

// The migration's measure: the tree treeFromGomib builds, against the
// reference gosmi's tree was recorded as, field by field. It reports while the
// migration is under way; once the differences are each fixed or accepted into
// the reference, it becomes an assertion.
func TestGomibTreeAgainstTheReference(t *testing.T) {
	_, dir := vendorDir(t, map[string]string{
		"EXAMPLE-CONSTRUCTS-MIB": constructsMib,
		"example-v1.mib":         v1Module,
	})
	src, err := gomib.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := gomib.Load(context.Background(), gomib.WithSource(src), gomib.WithResolverStrictness(gm.ResolverPermissive))
	if err != nil {
		t.Fatalf("gomib: %v", err)
	}
	got := map[string]*Node{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			got[n.Oid] = n
			walk(n.Children)
		}
	}
	walk(treeFromGomib(m))

	raw, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]referenceNode{}
	for line := range strings.SplitSeq(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		var n referenceNode
		if line == "" || json.Unmarshal([]byte(line), &n) != nil || n.Section != "node" {
			continue
		}
		want[n.Oid] = n
	}

	diffs := map[string][]string{}
	note := func(field, oid, name string, ref, now any) {
		diffs[field] = append(diffs[field], fmt.Sprintf("%s %s: reference %v, gomib %v", oid, name, ref, now))
	}
	for oid, w := range want {
		n, ok := got[oid]
		if !ok {
			diffs["missing"] = append(diffs["missing"], oid+" "+w.Name)
			continue
		}
		if n.Name != w.Name {
			note("name", oid, w.Name, w.Name, n.Name)
		}
		if n.MibType != w.Kind {
			note("kind", oid, w.Name, w.Kind, n.MibType)
		}
		if n.Syntax != w.Syntax {
			note("syntax", oid, w.Name, w.Syntax, n.Syntax)
		}
		if n.Access != w.Access {
			note("access", oid, w.Name, w.Access, n.Access)
		}
		if n.Status != w.Status {
			note("status", oid, w.Name, w.Status, n.Status)
		}
		if n.Units != w.Units {
			note("units", oid, w.Name, w.Units, n.Units)
		}
		if n.Parent != w.Parent {
			note("parent", oid, w.Name, w.Parent, n.Parent)
		}
		if fmt.Sprint(n.EnumValues) != fmt.Sprint(w.Enums) && (len(n.EnumValues) > 0 || len(w.Enums) > 0) {
			note("enums", oid, w.Name, len(w.Enums), len(n.EnumValues))
		}
		if shortHash(n.Description) != w.Description {
			// gosmi took the blank lines out; taken out here too, is the text the same?
			flat := strings.ReplaceAll(n.Description, "\n\n", "\n")
			if shortHash(flat) == w.Description {
				diffs["description, paragraphs kept"] = append(diffs["description, paragraphs kept"], oid+" "+w.Name)
			} else {
				note("description", oid, w.Name, w.Description, shortHash(n.Description))
			}
		}
	}
	for oid, n := range got {
		if _, ok := want[oid]; !ok {
			diffs["extra"] = append(diffs["extra"], oid+" "+n.Name)
		}
	}

	var fields []string
	total := 0
	for f, d := range diffs {
		fields = append(fields, f)
		total += len(d)
	}
	sort.Strings(fields)
	t.Logf("%d reference nodes, %d from gomib, %d differences", len(want), len(got), total)
	for _, f := range fields {
		d := diffs[f]
		sort.Strings(d)
		t.Logf("%s: %d", f, len(d))
		for i, line := range d {
			if i == 5 {
				t.Logf("   ...")
				break
			}
			t.Logf("   %s", line)
		}
	}
}
