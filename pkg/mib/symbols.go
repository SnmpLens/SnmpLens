package mib

import (
	"sort"
	"strings"

	"github.com/sleepinggenius2/gosmi"
	"github.com/sleepinggenius2/gosmi/parser"
)

// The symbol catalogue and the missing-import check.
//
// This is the assistance that answers the question people actually have in
// front of a vendor MIB: what is the exact name, and which module does it come
// from. The data already exists — it is the loaded tree — so the only work is
// exposing it and comparing it against what the buffer imports.

// Symbol is one name a MIB can refer to.
type Symbol struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	// Kind is "node", "type" or "module".
	Kind        string `json:"kind"`
	Oid         string `json:"oid,omitempty"`
	Description string `json:"description,omitempty"`
}

// Catalogue is everything the loaded tree knows, for the editor's picker.
type Catalogue struct {
	Modules []string `json:"modules"`
	Symbols []Symbol `json:"symbols"`
}

// catalogue caches what symbolsLocked builds, until something changes the
// loaded set.
//
// This is not an optimisation looking for a problem. MEASURED, by copying the
// bundled MIBs under new module names to build a corpus the size a vendor
// folder really is:
//
//	 15 modules,   767 symbols   1.36 ms
//	 35 modules,  4677 symbols   8.77 ms
//	 75 modules, 12497 symbols  32.73 ms
//	135 modules, 24227 symbols 113.32 ms
//
// Superlinear, and every millisecond of it is spent holding the EXCLUSIVE gosmi
// mutex — so every OID translation in every other tab waits. The editor calls
// AnalyseAll(content, Symbols()) 350 ms after every pause in typing: at 135
// modules that round cost 122.30 ms, of which 1.35 ms was the analysis and the
// rest was rebuilding a catalogue that had not changed since the last keystroke.
// With the catalogue cached, the same round is 1.35 ms.
//
// Guarded by gosmiMu, which every mutation of the loaded set already holds, so
// the cache cannot be read while it is being invalidated.
var catalogue *Catalogue

// invalidateCatalogue drops the cache. The caller MUST hold gosmiMu.
//
// Called from every place that changes what gosmi has loaded. A load that does
// not call it leaves the editor offering names from before the load and, worse,
// reporting an import as missing that has just been satisfied — which reads as
// a broken file rather than as a stale cache.
func invalidateCatalogue() {
	catalogue = nil
}

// Symbols lists every name in the loaded tree.
//
// Locked with the EXCLUSIVE gosmi mutex — this comment used to say "read
// locked", which pkg/mib has none of: gosmi memoises inside its getters, so
// there is no read-only operation to hold a shared lock for.
func Symbols() Catalogue {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	if catalogue == nil {
		built := symbolsLocked()
		catalogue = &built
	}
	// The slices are shared, deliberately: copying twenty-four thousand symbols
	// on every call would give back most of what the cache saves. What is
	// returned is capped with a full-slice expression, so a caller that appends
	// allocates its own array instead of writing into the cache.
	c := *catalogue
	c.Modules = c.Modules[:len(c.Modules):len(c.Modules)]
	c.Symbols = c.Symbols[:len(c.Symbols):len(c.Symbols)]
	return c
}

// symbolsLocked is Symbols for callers that already hold gosmiMu. Separate
// because the mutex is not reentrant: taking a read lock while holding the
// write lock deadlocks the whole application, silently.
func symbolsLocked() Catalogue {

	cat := Catalogue{Modules: []string{}, Symbols: []Symbol{}}
	seen := map[string]bool{}

	for _, m := range gosmi.GetLoadedModules() {
		name := m.Name
		if name == "" {
			continue
		}
		cat.Modules = append(cat.Modules, name)

		for _, n := range m.GetNodes() {
			key := name + "." + n.Name
			if n.Name == "" || seen[key] {
				continue
			}
			seen[key] = true
			cat.Symbols = append(cat.Symbols, Symbol{
				Name: n.Name, Module: name, Kind: "node",
				Oid:         n.Oid.String(),
				Description: firstLine(n.Description),
			})
		}
		for _, t := range m.GetTypes() {
			key := name + "." + t.Name
			if t.Name == "" || seen[key] {
				continue
			}
			seen[key] = true
			cat.Symbols = append(cat.Symbols, Symbol{
				Name: t.Name, Module: name, Kind: "type",
				Description: firstLine(t.Description),
			})
		}
	}

	sort.Strings(cat.Modules)
	sort.Slice(cat.Symbols, func(i, j int) bool { return cat.Symbols[i].Name < cat.Symbols[j].Name })
	return cat
}

// firstLine keeps a description short enough for a list row.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// baseTypes are built into SMI and never imported.
var baseTypes = map[string]bool{
	"INTEGER": true, "OCTET": true, "STRING": true, "OBJECT": true,
	"IDENTIFIER": true, "NULL": true, "SEQUENCE": true, "OF": true,
	"CHOICE": true, "BITS": true, "SIZE": true, "IMPLIED": true,
	"DEFINITIONS": true, "BEGIN": true, "END": true, "IMPORTS": true,
	"EXPORTS": true, "FROM": true, "MAX-ACCESS": true, "STATUS": true,
	"DESCRIPTION": true, "SYNTAX": true, "INDEX": true, "AUGMENTS": true,
	"UNITS": true, "REFERENCE": true, "DEFVAL": true, "ACCESS": true,
	"MIN-ACCESS": true, "OBJECTS": true, "NOTIFICATIONS": true, "MODULE": true,
	"MANDATORY-GROUPS": true, "GROUP": true, "WRITE-SYNTAX": true,
	"LAST-UPDATED": true, "ORGANIZATION": true, "CONTACT-INFO": true,
	"REVISION": true, "VARIABLES": true, "SUPPORTS": true, "PRODUCT-RELEASE": true,
	"MODULE-IDENTITY": true, "OBJECT-TYPE": true, "OBJECT-IDENTITY": true,
	"NOTIFICATION-TYPE": true, "TEXTUAL-CONVENTION": true, "OBJECT-GROUP": true,
	"NOTIFICATION-GROUP": true, "MODULE-COMPLIANCE": true, "AGENT-CAPABILITIES": true,
	"TRAP-TYPE": true, "MACRO": true,
}

// MissingImport is a symbol the buffer uses but never imports.
type MissingImport struct {
	Symbol string `json:"symbol"`
	// Module is where it is defined in the loaded tree.
	Module string `json:"module"`
	// Line and Column locate the first use.
	Line   int `json:"line"`
	Column int `json:"column"`
}

// ImportFix describes how to repair a set of missing imports.
type ImportFix struct {
	Missing []MissingImport `json:"missing"`
	// Content is the whole buffer with the IMPORTS clause repaired, ready to
	// replace what the editor holds. Empty when there is nothing to fix.
	Content string `json:"content,omitempty"`
}

// index maps each symbol name to the module that defines it. A name defined in
// several modules resolves to the first alphabetically, which is stable rather
// than arbitrary.
//
// Built once per analysis instead of scanned linearly per identifier: a few
// thousand symbols against a few thousand identifiers is millions of string
// comparisons for an answer a map gives in one.
// defined is every name the loaded tree knows, importable or not.
//
// Two different questions, and conflating them broke both in turn. "Does this
// name EXIST?" must include the ASN.1 roots — `org OBJECT IDENTIFIER ::=
// { iso 3 }` is how the bundled SNMPv2-SMI begins. "Where do I IMPORT it
// from?" must NOT, because gosmi files them under a pseudo-module and writing
// `FROM <well-known>` does not parse.
func (c Catalogue) defined() map[string]bool {
	out := make(map[string]bool, len(c.Symbols))
	for _, s := range c.Symbols {
		out[s.Name] = true
	}
	return out
}

func (c Catalogue) index() map[string]string {
	origin := make(map[string]string, len(c.Symbols))
	for _, s := range c.Symbols {
		// gosmi exposes the ASN.1 roots — iso, ccitt, joint-iso-ccitt — under a
		// pseudo-module called "<well-known>". They are not importable: every
		// MIB rooted at { iso 3 6 1 … } was told it was missing an import of
		// `iso`, and the fix wrote "FROM <well-known>", which does not parse.
		// The bundled SNMPv2-SMI is one of them.
		if s.Module == "" || strings.ContainsAny(s.Module, "<>") {
			continue
		}
		if _, exists := origin[s.Name]; !exists {
			origin[s.Name] = s.Module
		}
	}
	return origin
}

// scanIdentifiers walks MIB source and returns every identifier used outside a
// comment or a string, with the position of its first appearance.
//
// Comments and strings have to be skipped for this to be usable at all: a
// DESCRIPTION is prose, and prose is full of words that happen to match symbol
// names.
func scanIdentifiers(content string) map[string][2]int {
	found := map[string][2]int{}
	inString := false

	for lineNo, line := range strings.Split(content, "\n") {
		i := 0
		for i < len(line) {
			if inString {
				if end := strings.IndexByte(line[i:], '"'); end >= 0 {
					i += end + 1
					inString = false
					continue
				}
				break
			}
			switch {
			case line[i] == '"':
				inString = true
				i++
			case line[i] == '-' && i+1 < len(line) && line[i+1] == '-':
				if close := strings.Index(line[i+2:], "--"); close >= 0 {
					i += 2 + close + 2
				} else {
					i = len(line)
				}
			case isIdentStart(line[i]):
				j := i
				for j < len(line) && isIdentPart(line[j]) {
					j++
				}
				word := line[i:j]
				// `ip(126)` in an INTEGER enumeration is a NAMED NUMBER, not a
				// reference to anything. Both `ip` and `udp` appear that way in
				// the bundled IANAifType-MIB, resolved in the catalogue to
				// IP-MIB and UDP-MIB, and pressing Fix rewrote a correct file's
				// IMPORTS to include them. The same shape covers BITS.
				if !namedNumberAt(line, j) {
					if _, seen := found[word]; !seen {
						found[word] = [2]int{lineNo + 1, i + 1}
					}
				}
				i = j
			default:
				i++
			}
		}
	}
	return found
}

// namedNumberAt reports whether what follows an identifier is "(<digits>)",
// which makes it an enumeration label rather than a reference.
func namedNumberAt(line string, at int) bool {
	skipSpace := func(i int) int {
		for i < len(line) && (line[i] == ' ' || line[i] == '	') {
			i++
		}
		return i
	}

	i := skipSpace(at)
	if i >= len(line) || line[i] != '(' {
		return false
	}
	// Inside the parentheses too: real MIBs write `tcp   ( 9 )`.
	i = skipSpace(i + 1)
	if i < len(line) && (line[i] == '-' || line[i] == '+') {
		i++
	}
	digits := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
		digits++
	}
	i = skipSpace(i)
	return digits > 0 && i < len(line) && line[i] == ')'
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// CheckImports finds symbols the buffer uses without importing them.
//
// Only names that the loaded tree actually knows are reported. That keeps the
// false-positive rate near zero: a stray English word in a DESCRIPTION will not
// match a loaded symbol, and an unknown vendor name is not something we could
// suggest an import for anyway.
func CheckImports(content string, cat Catalogue) []MissingImport {
	p := prepare(content)
	return p.remapImports(checkImportsParsed(p.Text, p.module, p.err, cat.index()))
}

// remapImports is remap for the positions of missing imports.
func (r repaired) remapImports(missing []MissingImport) []MissingImport {
	for i := range missing {
		if missing[i].Line > 0 && missing[i].Column > 0 {
			missing[i].Column = r.original(missing[i].Line, missing[i].Column)
		}
	}
	return missing
}

// checkImportsParsed works from an already-parsed file and a pre-built name
// index, so the pipeline pays for one parse and one index rather than three
// parses and a linear scan per identifier.
func checkImportsParsed(content string, module *parser.Module, err error, origin map[string]string) []MissingImport {
	if err != nil || module == nil {
		return nil // a file that does not parse has bigger problems
	}

	// What the buffer already has: imported names, plus everything it defines
	// itself.
	known := map[string]bool{}
	for _, imp := range module.Body.Imports {
		for _, n := range imp.Names {
			known[string(n)] = true
		}
	}
	if module.Body.Identity != nil {
		known[string(module.Body.Identity.Name)] = true
	}
	for _, n := range module.Body.Nodes {
		known[string(n.Name)] = true
	}
	for _, t := range module.Body.Types {
		known[string(t.Name)] = true
	}
	for _, m := range module.Body.Macros {
		known[string(m.Name)] = true
	}

	selfName := string(module.Name)
	var missing []MissingImport
	for word, pos := range scanIdentifiers(content) {
		if known[word] || baseTypes[word] || word == selfName {
			continue
		}
		mod, ok := origin[word]
		if !ok || mod == selfName {
			continue
		}
		missing = append(missing, MissingImport{
			Symbol: word, Module: mod, Line: pos[0], Column: pos[1],
		})
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Line != missing[j].Line {
			return missing[i].Line < missing[j].Line
		}
		return missing[i].Symbol < missing[j].Symbol
	})
	return missing
}

// FixImports returns the buffer with the missing symbols added to IMPORTS.
//
// It edits the text rather than regenerating it from the AST: a MIB carries
// comments, alignment and copyright headers that no printer would preserve,
// and an assistant that reformats your file while fixing one line is not an
// assistant.
func FixImports(content string, cat Catalogue) ImportFix {
	missing := CheckImports(content, cat)
	if len(missing) == 0 {
		return ImportFix{Missing: []MissingImport{}}
	}

	byModule := map[string][]string{}
	for _, m := range missing {
		byModule[m.Module] = append(byModule[m.Module], m.Symbol)
	}
	modules := make([]string, 0, len(byModule))
	for m := range byModule {
		sort.Strings(byModule[m])
		modules = append(modules, m)
	}
	sort.Strings(modules)

	lines := strings.Split(content, "\n")
	fixed := insertImports(lines, modules, byModule)
	return ImportFix{Missing: missing, Content: strings.Join(fixed, "\n")}
}

// insertImports adds clauses to an existing IMPORTS block, or creates one just
// after BEGIN when there is none.
func insertImports(lines, modules []string, byModule map[string][]string) []string {
	clauses := make([]string, 0, len(modules)*2)
	for _, m := range modules {
		clauses = append(clauses,
			"    "+strings.Join(byModule[m], ", "),
			"        FROM "+m)
	}

	// By CONTENT, not by line shape. Matching `trimmed == "IMPORTS"` missed a
	// single-line clause, and `HasSuffix(trimmed, ";")` missed one ending in a
	// trailing comment — the two commonest shapes there are. Both then fell to
	// the BEGIN branch and inserted a SECOND IMPORTS clause, and the file
	// stopped parsing with `unexpected "IMPORTS"`. Comments are stripped for
	// the same reason: a header comment containing the word BEGIN put the new
	// clause outside the module.
	importsAt, semicolonAt, semicolonCol, beginAt := -1, -1, -1, -1
	for i, l := range lines {
		code := stripComment(l)
		if beginAt < 0 && containsWord(code, "BEGIN") {
			beginAt = i
		}
		if importsAt < 0 && containsWord(code, "IMPORTS") {
			importsAt = i
		}
		if importsAt >= 0 && i >= importsAt {
			if at := strings.IndexByte(code, ';'); at >= 0 {
				semicolonAt, semicolonCol = i, at
				break
			}
		}
	}

	if importsAt >= 0 && semicolonAt >= 0 {
		// Split at the semicolon rather than assuming it ends the line: on a
		// single-line clause everything before it is the existing imports and
		// everything after it is the rest of the file.
		line := lines[semicolonAt]
		head := strings.TrimRight(line[:semicolonCol], " 	")
		tail := line[semicolonCol+1:]

		out := append([]string{}, lines[:semicolonAt]...)
		if head != "" {
			out = append(out, head)
		}
		out = append(out, clauses...)
		out[len(out)-1] += ";"
		if strings.TrimSpace(tail) != "" {
			out = append(out, tail)
		}
		return append(out, lines[semicolonAt+1:]...)
	}

	if beginAt >= 0 {
		block := append([]string{"", "IMPORTS"}, clauses...)
		block[len(block)-1] += ";"
		out := append([]string{}, lines[:beginAt+1]...)
		out = append(out, block...)
		return append(out, lines[beginAt+1:]...)
	}

	return lines
}

// containsWord reports whether s contains word as a whole identifier.
func containsWord(s, word string) bool {
	for i := 0; ; {
		at := strings.Index(s[i:], word)
		if at < 0 {
			return false
		}
		at += i
		before := at == 0 || !isIdentPart(s[at-1])
		afterAt := at + len(word)
		after := afterAt >= len(s) || !isIdentPart(s[afterAt])
		if before && after {
			return true
		}
		i = at + 1
	}
}
