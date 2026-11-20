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

// The migration's measure for tables: every table of the reference, described
// from gomib, field by field. It reports while the migration is under way.
func TestGomibTablesAgainstTheReference(t *testing.T) {
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
	raw, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatal(err)
	}

	diffs := map[string][]string{}
	tables := 0
	for line := range strings.SplitSeq(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		var ref referenceTable
		if line == "" || json.Unmarshal([]byte(line), &ref) != nil || ref.Section != "table" {
			continue
		}
		tables++
		want := ref.Table
		got, err := gomibTableInfo(m, want.Oid)
		if err != nil {
			diffs["error"] = append(diffs["error"], fmt.Sprintf("%s: %v", want.Name, err))
			continue
		}
		note := func(field string, a, b any) {
			if fmt.Sprint(a) != fmt.Sprint(b) {
				diffs[field] = append(diffs[field], fmt.Sprintf("%s: gosmi %v, gomib %v", want.Name, a, b))
			}
		}
		note("row", want.RowName+" "+want.RowOid, got.RowName+" "+got.RowOid)
		note("implied", want.Implied, got.Implied)
		note("augments", want.Augments, got.Augments)
		note("rowStatus", want.RowStatusOid, got.RowStatusOid)
		note("index", want.Index, got.Index)
		if len(want.Columns) != len(got.Columns) {
			note("columns", len(want.Columns), len(got.Columns))
			continue
		}
		for i, w := range want.Columns {
			g := got.Columns[i]
			col := want.Name + "." + w.Name
			for _, f := range []struct {
				field string
				a, b  any
			}{
				{"column name", w.Name, g.Name}, {"column access", w.Access, g.Access}, {"column syntax", w.Syntax, g.Syntax},
				{"column wire type", w.WireType, g.WireType}, {"column isIndex", w.IsIndex, g.IsIndex},
				{"column writable", w.Writable, g.Writable}, {"column enums", len(w.EnumValues), len(g.EnumValues)},
			} {
				if fmt.Sprint(f.a) != fmt.Sprint(f.b) {
					diffs[f.field] = append(diffs[f.field], fmt.Sprintf("%s: gosmi %v, gomib %v", col, f.a, f.b))
				}
			}
		}
	}

	total := 0
	var fields []string
	for f, d := range diffs {
		fields = append(fields, f)
		total += len(d)
	}
	sort.Strings(fields)
	t.Logf("%d tables, %d differences", tables, total)
	for _, f := range fields {
		d := diffs[f]
		sort.Strings(d)
		t.Logf("%s: %d", f, len(d))
		for i, line := range d {
			if i == 8 {
				t.Logf("   ...")
				break
			}
			t.Logf("   %s", line)
		}
	}
}
