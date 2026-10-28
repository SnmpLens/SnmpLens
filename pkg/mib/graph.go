package mib

import (
	"os"
	"sort"
	"strings"

	"github.com/sleepinggenius2/gosmi"
)

// What imports what: the dependency graph of a directory of MIB files.
//
// The per-file load list answers "did this load", and the roll in the settings
// panel answers "what is missing and who wants it". Neither answers the third
// question, which is the one somebody has in front of a folder from a vendor:
// what does THIS file need, and what does that need in turn.
//
// It is cheap, and that is why it can be offered at all. readImports reads the
// head of each file as TEXT and stops at the semicolon ending the IMPORTS
// clause — no parse, no gosmi, and therefore no exclusive lock. The cycle
// detector has been doing exactly this on every load with diagnostics; this
// exposes the same scan as an answer rather than as a side effect.

// GraphNode is one module and what it imports.
type GraphNode struct {
	Module string `json:"module"`
	// File is where it was found, which is not always the module's name: gosmi
	// looks a module up BY FILE NAME, and a file whose name does not match what
	// it declares loads fine and is invisible to everything that imports it.
	File    string   `json:"file"`
	Imports []string `json:"imports"`
	// Present is whether a file for this module exists in the directory at all.
	// A module that is only ever imported and never found is the interesting
	// row: it is what somebody has to go and download.
	Present bool `json:"present"`
	// Loaded is whether gosmi has it right now. Present and not loaded usually
	// means the operator switched it off, which needs a checkbox rather than a
	// download — the same distinction the missing-import roll makes.
	Loaded bool `json:"loaded"`
	// ImportedBy is the edge read the other way, so the interface can answer
	// both questions from one structure.
	ImportedBy []string `json:"importedBy"`
}

// ModuleGraph scans every MIB file in the service's directory and reports what
// each one imports.
//
// A module named in an IMPORTS clause with no file behind it is included as a
// node with Present false: leaving it out would draw a tree whose branches stop
// without saying why, which is the opposite of what this is for.
//
// The lock is taken only for the gosmi.IsLoaded calls at the end. Reading the
// files does not need it, and holding it across a four-hundred-file directory
// scan would stop every OID translation in every other tab for the duration.
func (s *Service) ModuleGraph() ([]GraphNode, error) {
	if _, err := os.Stat(s.path); err != nil {
		return nil, err
	}
	lib := s.library()

	nodes := map[string]*GraphNode{}
	get := func(module string) *GraphNode {
		if n, ok := nodes[module]; ok {
			return n
		}
		n := &GraphNode{Module: module, Imports: []string{}, ImportedBy: []string{}}
		nodes[module] = n
		return n
	}

	for _, file := range lib.order {
		path := lib.files[file]
		module, imports := readImports(path)
		if module == "" {
			// The file name is what gosmi searches by, so it is the better
			// fallback than dropping the file entirely.
			module = moduleBase(file)
		}
		if module == "" {
			continue
		}

		n := get(module)
		// A directory can hold two files declaring the same module. The first
		// one wins here, as it does in gosmi, rather than the two of them
		// merging into a node that describes neither.
		if n.Present {
			continue
		}
		n.File = file
		n.Present = true
		n.Imports = dedupeSorted(imports)

		for _, dep := range n.Imports {
			d := get(dep)
			d.ImportedBy = append(d.ImportedBy, module)
		}
	}

	gosmiMu.Lock()
	for module, n := range nodes {
		n.Loaded = gosmi.IsLoaded(module)
	}
	gosmiMu.Unlock()

	out := make([]GraphNode, 0, len(nodes))
	for _, n := range nodes {
		n.ImportedBy = dedupeSorted(n.ImportedBy)
		out = append(out, *n)
	}
	// Stable, so the tree does not reshuffle between two identical scans.
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out, nil
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
