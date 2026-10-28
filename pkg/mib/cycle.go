package mib

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Import cycles, refused before gosmi is asked to load them.
//
// gosmi has no cycle guard. internal.GetModule looks the module up in the
// registry, does not find it because BuildModule has not finished registering
// it yet, and calls LoadModule again — so X importing a symbol from Y while Y
// imports one from X recurses until the stack ends. Measured: the load never
// returns, and it never returns while HOLDING the package write lock, so every
// other MIB operation in the application blocks behind it. A dropped vendor
// MIB set freezes the window with no error and nothing to click.
//
// SMIv2 modules form a directed acyclic graph — RFC 2578 has no notion of
// mutually importing modules — so a cycle is always a broken set, and refusing
// it with the names in it is both correct and the only answer that leaves the
// app running.
//
// Only the IMPORTS clause is read, from the head of each file: a full parse of
// every candidate would cost about 40 ms each, which is more than the load it
// is protecting.

// importScanLimit is how far into a file the IMPORTS clause is looked for.
//
// It is the first thing after DEFINITIONS ::= BEGIN by grammar, and the
// largest bundled clause is under 2 KB; 64 KB is far past any real one and
// still one read.
const importScanLimit = 64 * 1024

// ImportCycle is one cycle, as the modules that form it in order.
type ImportCycle struct {
	// Modules is the loop, e.g. [X-MIB Y-MIB X-MIB].
	Modules []string `json:"modules"`
}

// String renders the loop the way a person would read it.
func (c ImportCycle) String() string { return strings.Join(c.Modules, " -> ") }

// findImportCycles reports the cycles among the modules reachable from the
// given files, keyed by the file that is in one.
//
// A file whose imports leave the directory cannot close a loop, so an unknown
// module is simply an edge to nowhere.
func (s *Service) findImportCycles(fileNames []string) map[string]ImportCycle {
	// module -> file, so a cycle can be reported against something the caller
	// asked for.
	fileOf := map[string]string{}
	importsOf := map[string][]string{}

	lib := s.library()
	var scan func(file string)
	scan = func(file string) {
		path, ok := lib.path(file)
		if !ok {
			return
		}
		module, imports := readImports(path)
		if module == "" {
			// Fall back to the file name, which is what gosmi searches by.
			module = moduleBase(file)
		}
		if module == "" {
			return
		}
		if _, seen := fileOf[module]; seen {
			return
		}
		fileOf[module] = file
		importsOf[module] = imports

		// Follow, so a cycle two hops outside the requested set is still found.
		for _, dep := range imports {
			if _, seen := fileOf[dep]; seen {
				continue
			}
			if depFile, ok := s.fileForModule(dep); ok {
				scan(depFile)
			}
		}
	}

	for _, f := range fileNames {
		scan(f)
	}

	out := map[string]ImportCycle{}
	for _, cycle := range cyclesIn(importsOf) {
		for _, module := range cycle.Modules {
			if file, ok := fileOf[module]; ok {
				if _, already := out[file]; !already {
					out[file] = cycle
				}
			}
		}
	}
	return out
}

// fileForModule finds the file declaring a module, by name then by content.
func (s *Service) fileForModule(module string) (string, bool) {
	lib := s.library()
	upper := strings.ToUpper(module)
	for _, f := range lib.order {
		if strings.EqualFold(moduleBase(f), module) {
			return f, true
		}
	}
	for _, f := range lib.order {
		if strings.ToUpper(declaredModuleName(lib.files[f])) == upper {
			return f, true
		}
	}
	return "", false
}

// cyclesIn finds every cycle in the import graph.
//
// An ordinary depth-first search with a colour marking: a back edge to a node
// still on the stack closes a loop, and the loop is the stack from that node.
func cyclesIn(graph map[string][]string) []ImportCycle {
	const (
		white = 0 // not visited
		grey  = 1 // on the current path
		black = 2 // finished
	)
	colour := map[string]int{}
	var stack []string
	var found []ImportCycle
	seen := map[string]bool{}

	var walk func(node string)
	walk = func(node string) {
		colour[node] = grey
		stack = append(stack, node)

		for _, next := range graph[node] {
			switch colour[next] {
			case white:
				if _, known := graph[next]; known {
					walk(next)
				}
			case grey:
				// Back edge: the loop is the stack from `next` onwards.
				at := 0
				for i, n := range stack {
					if n == next {
						at = i
						break
					}
				}
				loop := append(append([]string{}, stack[at:]...), next)
				if key := strings.Join(sorted(loop), "|"); !seen[key] {
					seen[key] = true
					found = append(found, ImportCycle{Modules: loop})
				}
			}
		}

		stack = stack[:len(stack)-1]
		colour[node] = black
	}

	// Sorted, so the same set of files always reports the same cycle.
	for _, node := range sorted(keysOf(graph)) {
		if colour[node] == white {
			walk(node)
		}
	}
	return found
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// readImports returns a file's module name and the modules it imports from,
// reading only the head of the file.
func readImports(path string) (module string, imports []string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	head := make([]byte, importScanLimit)
	n, _ := io.ReadFull(f, head)
	if n <= 0 {
		return "", nil
	}
	content, _ := NormaliseSource(head[:n])
	module = moduleNameOf(content)

	// The clause ends at the first semicolon after IMPORTS.
	upper := strings.ToUpper(content)
	start := strings.Index(upper, "IMPORTS")
	if start < 0 {
		return module, nil
	}
	clause := content[start+len("IMPORTS"):]
	if end := strings.IndexByte(clause, ';'); end >= 0 {
		clause = clause[:end]
	}

	// Every "FROM <module>" in it. Comments are stripped first: a module named
	// in a comment is not an import.
	var lines []string
	for _, line := range strings.Split(clause, "\n") {
		lines = append(lines, stripComment(line))
	}
	fields := strings.Fields(strings.Join(lines, " "))
	seen := map[string]bool{}
	for i := 0; i < len(fields)-1; i++ {
		if !strings.EqualFold(fields[i], "FROM") {
			continue
		}
		name := strings.Trim(fields[i+1], ",")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		imports = append(imports, name)
	}
	return module, imports
}

// cycleDiagnostic describes a refused file.
func cycleDiagnostic(fileName string, cycle ImportCycle) MibLoadResult {
	return MibLoadResult{
		FileName: fileName,
		Success:  false,
		Error: fmt.Sprintf(
			"refused: its IMPORTS form a cycle (%s). SMI modules cannot import each other, and loading one would not return.",
			cycle),
		Diagnosis: &LoadDiagnosis{
			FileName: fileName,
			Stage:    StageImports,
			Summary:  "the IMPORTS form a cycle: " + cycle.String(),
			Hints: []string{
				"break the loop: one of these modules must not import from the other",
			},
		},
	}
}
