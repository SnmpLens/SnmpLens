//go:build gomibcorpus

package mib

// gosmi against gomib on a real corpus, every difference written to a report.
//
//	GOMIB_CORPUS=<dir of MIBs, searched recursively> GOMIB_REPORT=<file> \
//	  go test -tags gomibcorpus -run TestGomibCorpus -timeout 30m ./pkg/mib/
//
// gomib's own corpus is in the module: $(go list -m -f '{{.Dir}}' github.com/gosnmp/gomib)/testdata/corpus/primary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gomib"
	gm "github.com/gosnmp/gomib/mib"
	"github.com/sleepinggenius2/gosmi"
)

type corpusReport struct {
	b strings.Builder
}

func (r *corpusReport) f(format string, args ...any) { fmt.Fprintf(&r.b, format+"\n", args...) }

// group writes a section: lines grouped by key, counted, with every line or the first few.
func (r *corpusReport) group(title string, lines map[string][]string, all bool) {
	total := 0
	var keys []string
	for k, v := range lines {
		keys = append(keys, k)
		total += len(v)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(lines[keys[i]]) != len(lines[keys[j]]) {
			return len(lines[keys[i]]) > len(lines[keys[j]])
		}
		return keys[i] < keys[j]
	})
	r.f("\n## %s: %d", title, total)
	for _, k := range keys {
		v := lines[k]
		sort.Strings(v)
		r.f("- %s: %d", k, len(v))
		for i, l := range v {
			if !all && i == 4 {
				r.f("    ...")
				break
			}
			r.f("    %s", l)
		}
	}
}

func TestGomibCorpus(t *testing.T) {
	root, out := os.Getenv("GOMIB_CORPUS"), os.Getenv("GOMIB_REPORT")
	if root == "" || out == "" {
		t.Skip("GOMIB_CORPUS and GOMIB_REPORT unset")
	}
	dir := t.TempDir()
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(p)
			os.WriteFile(filepath.Join(dir, d.Name()), raw, 0o600)
			files = append(files, d.Name())
		}
		return nil
	})
	r := &corpusReport{}
	r.f("# gosmi against gomib: %d files from %s", len(files), root)

	// gosmi, file by file first: what panics is left out of the joint load.
	names, _ := ListMibFiles(dir)
	panics := map[string][]string{}
	for _, n := range names {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					panics[fmt.Sprint(rec)] = append(panics[fmt.Sprint(rec)], n)
				}
			}()
			gosmi.Exit()
			gosmi.Init()
			NewService(dir).LoadWithDiagnostics([]string{n})
		}()
	}
	r.group("gosmi panics, loading each file alone", panics, true)
	crashed := map[string]bool{}
	for _, v := range panics {
		for _, n := range v {
			crashed[n] = true
		}
	}
	var loadable []string
	for _, n := range names {
		if !crashed[n] {
			loadable = append(loadable, n)
		}
	}

	gosmi.Exit()
	gosmi.Init()
	s := NewService(dir)
	t0 := time.Now()
	var resp MibLoadResponse
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.f("\ngosmi PANICKED on the joint load: %v", rec)
			}
		}()
		resp = s.LoadWithDiagnostics(loadable)
	}()
	gosmiTime := time.Since(t0)

	src, err := gomib.Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	m, err := gomib.Load(context.Background(), gomib.WithSource(src), gomib.WithResolverStrictness(gm.ResolverPermissive))
	gomibTime := time.Since(t0)
	r.f("\ngosmi: %v for %d files (the panicking ones left out). gomib: %v for all, error %v", gosmiTime.Round(time.Millisecond), len(loadable), gomibTime.Round(time.Millisecond), err)

	moduleOf := func(oid string) string {
		o, err := gm.ParseOID(oid)
		if err != nil {
			return "?"
		}
		if n := m.LongestPrefixByOID(o); n != nil && n.Module() != nil {
			return n.Module().Name()
		}
		return "?"
	}

	// Loads.
	refused := map[string][]string{}
	warned := map[string][]string{}
	for _, d := range resp.Diagnostics {
		if !d.Success {
			refused["refused"] = append(refused["refused"], fmt.Sprintf("%s: %.160s", d.FileName, d.Error))
		}
		for _, w := range d.Warnings {
			warned[w.Code] = append(warned[w.Code], fmt.Sprintf("%s: %.160s", d.FileName, w.Message))
		}
	}
	r.group("gosmi refused", refused, true)
	r.group("gosmi warnings, by code", warned, false)
	diags := map[string][]string{}
	for _, d := range m.Diagnostics() {
		key := d.Severity.String() + " " + d.Code
		diags[key] = append(diags[key], fmt.Sprintf("%s:%d: %.140s", d.Module, d.Line, d.Message))
	}
	r.group("gomib diagnostics, by severity and code", diags, false)

	// Trees.
	a, b := map[string]*Node{}, map[string]*Node{}
	var into map[string]*Node
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			into[n.Oid] = n
			walk(n.Children)
		}
	}
	into = a
	walk(resp.Tree)
	into = b
	walk(treeFromGomib(m))
	r.f("\nnodes: gosmi %d, gomib %d", len(a), len(b))

	onlyGosmi, onlyGomib := map[string][]string{}, map[string][]string{}
	field := map[string]map[string][]string{}
	for _, k := range []string{"name", "kind", "access", "syntax", "status", "units", "enums", "description"} {
		field[k] = map[string][]string{}
	}
	flat := func(s string) string {
		for strings.Contains(s, "\n\n") {
			s = strings.ReplaceAll(s, "\n\n", "\n")
		}
		return s
	}
	for oid, x := range a {
		y, ok := b[oid]
		if !ok {
			onlyGosmi[moduleOf(oid)] = append(onlyGosmi[moduleOf(oid)], oid+" "+x.Name+" ("+x.MibType+")")
			continue
		}
		mod := moduleOf(oid)
		add := func(f, key, line string) { field[f][key] = append(field[f][key], line) }
		if x.Name != y.Name {
			add("name", mod, fmt.Sprintf("%s: gosmi %s, gomib %s", oid, x.Name, y.Name))
		}
		if x.MibType != y.MibType {
			add("kind", x.MibType+" -> "+y.MibType+" in "+mod, oid+" "+x.Name)
		}
		if x.Access != y.Access {
			add("access", x.Access+" -> "+y.Access, mod+" "+oid+" "+x.Name)
		}
		if x.Syntax != y.Syntax {
			add("syntax", x.Syntax+" -> "+y.Syntax, mod+" "+x.Name)
		}
		if x.Status != y.Status {
			add("status", x.Status+" -> "+y.Status, mod+" "+x.Name)
		}
		if x.Units != y.Units {
			add("units", fmt.Sprintf("%q -> %q", x.Units, y.Units), mod+" "+x.Name)
		}
		if fmt.Sprint(x.EnumValues) != fmt.Sprint(y.EnumValues) && (len(x.EnumValues) > 0 || len(y.EnumValues) > 0) {
			add("enums", mod, fmt.Sprintf("%s: gosmi %v, gomib %v", x.Name, x.EnumValues, y.EnumValues))
		}
		if x.Description != y.Description {
			if flat(x.Description) == flat(y.Description) {
				add("description", "paragraphs kept", mod+" "+x.Name)
			} else {
				add("description", "text differs in "+mod, fmt.Sprintf("%s: gosmi %.80q, gomib %.80q", x.Name, x.Description, y.Description))
			}
		}
	}
	for oid, y := range b {
		if _, ok := a[oid]; !ok {
			onlyGomib[moduleOf(oid)] = append(onlyGomib[moduleOf(oid)], oid+" "+y.Name+" ("+y.MibType+")")
		}
	}
	r.group("nodes only gosmi has, by module", onlyGosmi, true)
	r.group("nodes only gomib has, by module", onlyGomib, false)
	for _, k := range []string{"name", "kind", "access", "syntax", "status", "units", "enums", "description"} {
		r.group(k+" differences", field[k], k == "name" || k == "access")
	}

	// Tables, as Service.Table describes them.
	tables := map[string][]string{}
	tableCount := 0
	for oid, x := range a {
		if x.MibType != "Table" {
			continue
		}
		tableCount++
		want, werr := s.Table(oid)
		got, gerr := gomibTableInfo(m, oid)
		mod := moduleOf(oid)
		switch {
		case werr != nil && gerr != nil:
			continue
		case werr != nil:
			tables["only gomib describes it"] = append(tables["only gomib describes it"], mod+" "+x.Name+": gosmi "+werr.Error())
			continue
		case gerr != nil:
			tables["only gosmi describes it"] = append(tables["only gosmi describes it"], mod+" "+x.Name+": gomib "+gerr.Error())
			continue
		}
		cmp := func(k string, p, q any) {
			if fmt.Sprint(p) != fmt.Sprint(q) {
				tables[k] = append(tables[k], fmt.Sprintf("%s %s: gosmi %v, gomib %v", mod, x.Name, p, q))
			}
		}
		cmp("row", want.RowName, got.RowName)
		cmp("implied", want.Implied, got.Implied)
		cmp("augments", want.Augments, got.Augments)
		cmp("rowStatus", want.RowStatusOid, got.RowStatusOid)
		var wi, gi []string
		for _, p := range want.Index {
			wi = append(wi, p.Name)
		}
		for _, p := range got.Index {
			gi = append(gi, p.Name)
		}
		cmp("index objects", wi, gi)
		cmp("column count", len(want.Columns), len(got.Columns))
		if len(want.Columns) == len(got.Columns) {
			for i := range want.Columns {
				w, g := want.Columns[i], got.Columns[i]
				cmp("column writable", w.Name+" "+fmt.Sprint(w.Writable), g.Name+" "+fmt.Sprint(g.Writable))
				cmp("column wire type", w.Name+" "+w.WireType, g.Name+" "+g.WireType)
			}
		}
	}
	r.f("\ntables gosmi describes: %d", tableCount)
	r.group("table differences", tables, false)

	// Names.
	names2 := map[string][]string{}
	var oids []string
	for oid := range a {
		oids = append(oids, oid, oid+".7")
	}
	gosmiNames := s.NameOIDs(oids)
	for _, oid := range oids {
		x, y := s.Translate(oid), gomibTranslate(m, oid)
		if x.Name != y.Name {
			names2["Translate name"] = append(names2["Translate name"], fmt.Sprintf("%s %s: gosmi %s, gomib %s", moduleOf(oid), oid, x.Name, y.Name))
		}
		p, ok1 := gosmiNames[oid]
		q, ok2 := gomibNameOID(m, oid)
		if ok1 != ok2 || p != q {
			names2["NameOIDs"] = append(names2["NameOIDs"], fmt.Sprintf("%s %s: gosmi %v %v, gomib %v %v", moduleOf(oid), oid, ok1, p, ok2, q))
		}
	}
	r.group("naming differences ("+fmt.Sprint(len(oids))+" OIDs)", names2, false)

	if err := os.WriteFile(out, []byte(r.b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %s", out)
}
