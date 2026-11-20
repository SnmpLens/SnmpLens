package mib

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The reference: everything pkg/mib tells the rest of SnmpLens about the
// bundled MIBs, constructsMib and an SMIv1 module, one JSON object a line in
// testdata/reference.jsonl. Every node of the tree with what the renderer reads
// of it, every table as the table view and row creation see it, the name every
// node's instances are shown under, and what each file's load said.
//
// It exists for whoever changes the MIB engine — gosmi now, gomib being
// studied: the new one has to give the same answers, and where it does not,
// the difference is a line to read and decide on, not a behaviour that changed
// unseen. frontend/tests/mibcontract.test.mjs reads the same file and holds the
// renderer to it.
//
// After a deliberate change:
//
//	go test ./pkg/mib -run TestTheMibEngineMatchesItsReference -update-reference
var updateReference = flag.Bool("update-reference", false, "rewrite testdata/reference.jsonl")

const referenceFile = "testdata/reference.jsonl"

type referenceNode struct {
	Section     string           `json:"section"`
	Oid         string           `json:"oid"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind"`
	Syntax      string           `json:"syntax,omitempty"`
	Access      string           `json:"access,omitempty"`
	Status      string           `json:"status,omitempty"`
	Units       string           `json:"units,omitempty"`
	Parent      string           `json:"parent,omitempty"`
	Enums       map[string]int64 `json:"enums,omitempty"`
	Description string           `json:"description,omitempty"` // sha256, 12 hex digits
	// Instance is what an OID under this node that no MIB defines is shown as.
	Instance string `json:"instance"`
}

type referenceTable struct {
	Section string     `json:"section"`
	Table   *TableInfo `json:"table"`
}

type referenceLoad struct {
	Section  string   `json:"section"`
	File     string   `json:"file"`
	Success  bool     `json:"success"`
	Warnings []string `json:"warnings,omitempty"`
}

func shortHash(s string) string {
	// Without the CRs: a checkout on Windows may carry the MIBs in CRLF, and the
	// reference must read the same on every platform CI runs.
	s = strings.ReplaceAll(s, "\r", "")
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// referenceLines builds the reference as it stands, keyed for comparison.
func referenceLines(t *testing.T) (keys []string, lines map[string]string, descriptions map[string]string) {
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

	lines = map[string]string{}
	descriptions = map[string]string{}
	add := func(key string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		lines[key] = string(raw)
	}

	var nodes []*Node
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			nodes = append(nodes, n)
			walk(n.Children)
		}
	}
	walk(resp.Tree)
	sort.Slice(nodes, func(i, j int) bool { return isOidLess(nodes[i].Oid, nodes[j].Oid) })
	for _, n := range nodes {
		descriptions[n.Oid] = n.Description
		add("node "+n.Oid, referenceNode{
			Section: "node", Oid: n.Oid, Name: n.Name, Kind: n.MibType, Syntax: n.Syntax,
			Access: n.Access, Status: n.Status, Units: n.Units, Parent: n.Parent, Enums: n.EnumValues,
			Description: shortHash(n.Description),
			// The largest sub-identifier: never one a MIB defines.
			Instance: s.Translate(n.Oid + ".4294967295").Name,
		})
	}
	for _, n := range nodes {
		if n.MibType != "Table" {
			continue
		}
		info, err := s.Table(n.Oid)
		if err != nil {
			t.Fatalf("%s %s: %v", n.Oid, n.Name, err)
		}
		add("table "+n.Oid, referenceTable{Section: "table", Table: info})
	}

	results := append([]MibLoadResult(nil), resp.Diagnostics...)
	sort.Slice(results, func(i, j int) bool { return results[i].FileName < results[j].FileName })
	for _, r := range results {
		var codes []string
		for _, w := range r.Warnings {
			codes = append(codes, w.Code)
		}
		sort.Strings(codes)
		add("load "+r.FileName, referenceLoad{Section: "load", File: r.FileName, Success: r.Success, Warnings: codes})
	}
	return keys, lines, descriptions
}

func TestTheMibEngineMatchesItsReference(t *testing.T) {
	keys, lines, descriptions := referenceLines(t)

	if *updateReference {
		var b bytes.Buffer
		for _, k := range keys {
			b.WriteString(lines[k])
			b.WriteByte('\n')
		}
		if err := os.MkdirAll(filepath.Dir(referenceFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(referenceFile, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d lines to %s", len(keys), referenceFile)
		return
	}

	raw, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatalf("%v; create it with -update-reference", err)
	}
	// A checkout on Windows may carry CRLF.
	want := map[string]string{}
	var wantKeys []string
	for line := range strings.SplitSeq(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		var head struct {
			Section, Oid, File string
			Table              *struct{ Oid string }
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		key := head.Section + " "
		switch head.Section {
		case "node":
			key += head.Oid
		case "table":
			key += head.Table.Oid
		case "load":
			key += head.File
		}
		want[key] = line
		wantKeys = append(wantKeys, key)
	}

	reported := 0
	report := func(format string, args ...any) {
		reported++
		if reported <= 25 {
			t.Errorf(format, args...)
		}
	}
	for _, k := range wantKeys {
		got, ok := lines[k]
		switch {
		case !ok:
			report("%s: gone\n  reference: %s", k, want[k])
		case got != want[k]:
			msg := "%s: changed\n  reference: %s\n  now:       %s"
			args := []any{k, want[k], got}
			if oid, isNode := strings.CutPrefix(k, "node "); isNode && strings.Contains(want[k], `"description"`) {
				msg += "\n  description now: %.300q"
				args = append(args, descriptions[oid])
			}
			report(msg, args...)
		}
	}
	for _, k := range keys {
		if _, ok := want[k]; !ok {
			report("%s: new\n  now: %s", k, lines[k])
		}
	}
	if reported > 25 {
		t.Errorf("... and %d more", reported-25)
	}
	if reported > 0 {
		t.Log("If the change is deliberate: go test ./pkg/mib -run TestTheMibEngineMatchesItsReference -update-reference")
	}
}
