package mib

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sleepinggenius2/gosmi/parser"
)

// Semantic analysis of a MIB, which is the part nothing else does.
//
// Measured, not assumed: a MIB declaring SYNTAX Integerr32 and assigning the
// same OID twice loads with err=nil and IsLoaded=true. Its objects then resolve
// to a nil type and an EMPTY OID. gosmi looks the type up, gets nothing, breaks
// out of the loop and adds the object anyway. So the user sees a MIB that
// "loaded fine" and a browser that answers wrongly, and nothing anywhere says a
// word.
//
// parser.Parse gives syntax. gosmi.LoadModule gives a boolean that is not even
// true. This is the layer in between: everything that can be decided from the
// AST plus the loaded tree, reported where it happened.

// Additional diagnostic codes for the semantic pass.
const (
	CodeUnknownType    = "unknown-type"
	CodeDuplicateOid   = "duplicate-oid"
	CodeUnknownParent  = "unknown-parent"
	CodeUnusedImport   = "unused-import"
	CodeUnknownModule  = "unknown-module"
	CodeNoDescription  = "no-description"
	CodeIndexUndefined = "index-undefined"
	CodeRowAccess      = "row-access"
	CodeDuplicateName  = "duplicate-name"
	CodeImportLost     = "import-lost"
)

// Analyse reports every semantic problem it can locate.
//
// cat is the loaded tree; when it is empty the checks that need it are skipped
// rather than producing confident nonsense.
func Analyse(content string, cat Catalogue) []Diagnostic {
	p := prepare(content)
	return p.remap(analyseParsed(p.Text, p.module, p.err, cat))
}

// analyseParsed works from an already-parsed file.
func analyseParsed(content string, module *parser.Module, err error, cat Catalogue) []Diagnostic {
	if err != nil || module == nil {
		// A file that does not parse has a syntax error already being shown.
		// Guessing at its meaning would bury the thing that matters.
		return []Diagnostic{}
	}

	a := &analysis{
		module:    module,
		cat:       cat,
		known:     map[string]bool{},
		types:     map[string]bool{},
		imports:   map[string]*importUse{},
		byOid:     map[string][]*parser.Node{},
		diags:     []Diagnostic{},
		rawSource: content,
	}
	a.collect()
	a.checkImportedModules()
	a.checkLostImports()
	a.checkSyntaxTypes()
	a.checkOids()
	a.checkTables()
	a.checkDescriptions()
	a.checkUnusedImports()

	sort.Slice(a.diags, func(i, j int) bool {
		if a.diags[i].Line != a.diags[j].Line {
			return a.diags[i].Line < a.diags[j].Line
		}
		return a.diags[i].Column < a.diags[j].Column
	})
	return a.diags
}

type importUse struct {
	module string
	line   int
	column int
	used   bool
}

type analysis struct {
	module  *parser.Module
	cat     Catalogue
	known   map[string]bool // every name defined or imported
	types   map[string]bool // names usable as a SYNTAX type
	imports map[string]*importUse
	byOid   map[string][]*parser.Node
	diags   []Diagnostic
	// rawSource is kept because the AST does not retain the text, and the
	// unused-import check has to look at what the file actually mentions.
	rawSource string
	// catDefined is every name the loaded tree knows, built lazily and once.
	catDefined map[string]bool
}

func (a *analysis) add(pos lexerPos, severity, code, message, symbol string) {
	a.diags = append(a.diags, Diagnostic{
		Line: pos.Line, Column: pos.Column,
		Severity: severity, Code: code, Message: message, Symbol: symbol,
	})
}

// lexerPos is the shape every AST node carries.
type lexerPos struct{ Line, Column int }

func at(line, column int) lexerPos { return lexerPos{Line: line, Column: column} }

func (a *analysis) collect() {
	for _, imp := range a.module.Body.Imports {
		mod := string(imp.Module)
		for _, n := range imp.Names {
			name := string(n)
			a.known[name] = true
			a.types[name] = true
			a.imports[name] = &importUse{module: mod, line: imp.Pos.Line, column: imp.Pos.Column}
		}
	}
	if a.module.Body.Identity != nil {
		a.known[string(a.module.Body.Identity.Name)] = true
	}
	for i := range a.module.Body.Types {
		name := string(a.module.Body.Types[i].Name)
		a.known[name] = true
		a.types[name] = true
	}
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		a.known[string(node.Name)] = true
		// A SEQUENCE type shares the name of its row object, so a node name is
		// also usable as a SYNTAX type.
		a.types[string(node.Name)] = true
		if key := oidKey(node.Oid); key != "" {
			a.byOid[key] = append(a.byOid[key], node)
		}
	}
	for i := range a.module.Body.Macros {
		a.known[string(a.module.Body.Macros[i].Name)] = true
	}
}

// oidKey renders an OID assignment as a comparable string.
func oidKey(oid *parser.Oid) string {
	if oid == nil || len(oid.SubIdentifiers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(oid.SubIdentifiers))
	for _, sub := range oid.SubIdentifiers {
		switch {
		case sub.Number != nil:
			parts = append(parts, fmt.Sprint(*sub.Number))
		case sub.Name != nil:
			parts = append(parts, string(*sub.Name))
		}
	}
	return strings.Join(parts, ".")
}

// checkImportedModules flags a FROM naming a module that is nowhere to be
// found. This is the commonest reason a pasted vendor MIB never loads, and the
// remedy is to fetch that module rather than to edit anything.
func (a *analysis) checkImportedModules() {
	if len(a.cat.Modules) == 0 {
		return
	}
	loaded := map[string]bool{}
	for _, m := range a.cat.Modules {
		loaded[m] = true
	}
	for _, imp := range a.module.Body.Imports {
		mod := string(imp.Module)
		if loaded[mod] || mod == string(a.module.Name) {
			continue
		}
		names := make([]string, 0, len(imp.Names))
		for _, n := range imp.Names {
			names = append(names, string(n))
		}
		// gosmi reads some of these from the modules that replaced this one
		// (implicit.go): saying it is missing would contradict a load that
		// resolves every one of them.
		needed, converted := notConverted(mod, names)
		if converted && len(needed) == 0 {
			continue
		}
		msg := fmt.Sprintf("module %q is not loaded; add its MIB file to SnmpLens or this module will never resolve", mod)
		if converted {
			msg = fmt.Sprintf("module %q is not loaded; SnmpLens reads %s from the modules that replaced it, but %s needs %s itself",
				mod, plural(len(names)-len(needed), "import", "imports"), strings.Join(needed, ", "), mod)
		}
		a.add(at(imp.Pos.Line, imp.Pos.Column), SevError, CodeUnknownModule, msg, mod)
	}
}

// checkLostImports flags an import gosmi rewrites to a module that does not
// define it (implicit.go): it resolves to nothing, and no file fixes that.
func (a *analysis) checkLostImports() {
	for _, imp := range a.module.Body.Imports {
		lost := gosmiLost[string(imp.Module)]
		for _, n := range imp.Names {
			if !lost[string(n)] {
				continue
			}
			a.add(at(imp.Pos.Line, imp.Pos.Column), SevWarning, CodeImportLost,
				fmt.Sprintf("SnmpLens reads %s from the module that replaced %s, which does not define it: "+
					"it resolves to nothing, whichever MIBs are loaded", n, imp.Module), string(n))
		}
	}
}

// checkSyntaxTypes is the one that matters most: gosmi accepts an unknown type
// silently and produces an object with no type and no OID.
func (a *analysis) checkSyntaxTypes() {
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		if node.ObjectType == nil {
			continue
		}
		syntax := node.ObjectType.Syntax

		if syntax.Sequence != nil {
			a.useType(string(*syntax.Sequence), syntax.Pos.Line, syntax.Pos.Column, node)
			continue
		}
		if syntax.Type != nil {
			a.useType(string(syntax.Type.Name), syntax.Type.Pos.Line, syntax.Type.Pos.Column, node)
		}
	}
	// Textual conventions refer to types too: a TEXTUAL-CONVENTION with a
	// mistyped SYNTAX is just as silently broken as an object with one.
	for i := range a.module.Body.Types {
		t := &a.module.Body.Types[i]
		if t.Syntax != nil {
			a.useType(string(t.Syntax.Name), t.Syntax.Pos.Line, t.Syntax.Pos.Column, nil)
		}
		if t.TextualConvention != nil {
			st := t.TextualConvention.Syntax
			a.useType(string(st.Name), st.Pos.Line, st.Pos.Column, nil)
		}
	}
}

func (a *analysis) useType(name string, line, column int, node *parser.Node) {
	if name == "" {
		return
	}
	if use, ok := a.imports[name]; ok {
		use.used = true
	}
	if a.types[name] || baseTypes[name] || isBuiltinSyntax(name) {
		return
	}
	// Known to the loaded tree but not imported: that is the IMPORTS check's
	// job, and saying it twice in different words helps nobody.
	if a.inCatalogue(name) {
		return
	}
	symbol := name
	msg := fmt.Sprintf("%q is not a known type; it is neither defined here, imported, nor a base type", name)
	if node != nil {
		msg = fmt.Sprintf("%q is not a known type, so %s will load with no type and no OID", name, node.Name)
	}
	a.add(at(line, column), SevError, CodeUnknownType, msg, symbol)
}

// inCatalogue answers "does the loaded tree know this name", which is not the
// same as "can it be imported" — see Catalogue.defined.
func (a *analysis) inCatalogue(name string) bool {
	if a.catDefined == nil {
		a.catDefined = a.cat.defined()
	}
	return a.catDefined[name]
}

// isBuiltinSyntax covers the spellings the grammar accepts directly.
func isBuiltinSyntax(name string) bool {
	switch name {
	case "OCTET STRING", "OBJECT IDENTIFIER", "INTEGER", "BITS", "NULL":
		return true
	}
	return false
}

// checkOids finds two objects claiming the same identifier, which gosmi accepts
// and which makes one of them permanently unreachable.
func (a *analysis) checkOids() {
	keys := make([]string, 0, len(a.byOid))
	for k := range a.byOid {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// One name defined twice: whichever definition gosmi keeps, the other is
	// gone, and the module still "loads".
	first := map[string]*parser.Node{}
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		name := string(node.Name)
		prev, seen := first[name]
		if !seen {
			first[name] = node
			continue
		}
		a.add(at(node.Pos.Line, node.Pos.Column), SevError, CodeDuplicateName,
			fmt.Sprintf("%s is defined twice, on lines %d and %d; only one of the two definitions survives",
				name, prev.Pos.Line, node.Pos.Line),
			name)
	}

	for _, key := range keys {
		nodes := a.byOid[key]
		if len(nodes) < 2 {
			continue
		}
		// Distinct names only: the same name twice is the duplicate above, and
		// "assigned to x and x" said nothing a person could act on.
		var names []string
		seenName := map[string]bool{}
		for _, n := range nodes {
			if !seenName[string(n.Name)] {
				seenName[string(n.Name)] = true
				names = append(names, string(n.Name))
			}
		}
		if len(names) < 2 {
			continue
		}
		for _, n := range nodes[1:] {
			a.add(at(n.Oid.Pos.Line, n.Oid.Pos.Column), SevError, CodeDuplicateOid,
				fmt.Sprintf("OID { %s } is assigned to %s; only one of them will ever resolve",
					strings.ReplaceAll(key, ".", " "), strings.Join(names, " and ")),
				string(n.Name))
		}
	}

	// A parent named but never defined anywhere.
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		if node.Oid == nil || len(node.Oid.SubIdentifiers) == 0 {
			continue
		}
		first := node.Oid.SubIdentifiers[0]
		if first.Name == nil {
			continue
		}
		parent := string(*first.Name)
		if use, ok := a.imports[parent]; ok {
			use.used = true
		}
		if a.known[parent] || a.inCatalogue(parent) {
			continue
		}
		a.add(at(node.Oid.Pos.Line, node.Oid.Pos.Column), SevError, CodeUnknownParent,
			fmt.Sprintf("%s is placed under %q, which is not defined here, imported, or in any loaded MIB",
				node.Name, parent),
			parent)
	}
}

// checkTables applies the RFC2578 rules that are cheap to verify and painful to
// debug: a conceptual row must not be readable, and its INDEX must name objects
// that exist.
func (a *analysis) checkTables() {
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		ot := node.ObjectType
		if ot == nil {
			continue
		}

		isRow := len(ot.Index) > 0 || ot.Augments != nil
		isTable := ot.Syntax.Sequence != nil

		if (isRow || isTable) && ot.Access != parser.AccessNotAccessible {
			what := "a conceptual row"
			if isTable {
				what = "a table"
			}
			a.add(at(ot.Pos.Line, ot.Pos.Column), SevWarning, CodeRowAccess,
				fmt.Sprintf("%s is %s, so RFC2578 requires MAX-ACCESS not-accessible", node.Name, what),
				string(node.Name))
		}

		for _, idx := range ot.Index {
			name := string(idx.Name)
			if use, ok := a.imports[name]; ok {
				use.used = true
			}
			if a.known[name] || a.inCatalogue(name) {
				continue
			}
			a.add(at(idx.Pos.Line, idx.Pos.Column), SevError, CodeIndexUndefined,
				fmt.Sprintf("INDEX names %q, which is not defined anywhere", name), name)
		}
		if ot.Augments != nil {
			name := string(*ot.Augments)
			if use, ok := a.imports[name]; ok {
				use.used = true
			}
			if !a.known[name] && !a.inCatalogue(name) {
				a.add(at(ot.Pos.Line, ot.Pos.Column), SevError, CodeIndexUndefined,
					fmt.Sprintf("AUGMENTS names %q, which is not defined anywhere", name), name)
			}
		}
	}
}

// checkDescriptions: RFC2578 makes DESCRIPTION mandatory, and a MIB without one
// is unusable to whoever inherits it.
func (a *analysis) checkDescriptions() {
	for i := range a.module.Body.Nodes {
		node := &a.module.Body.Nodes[i]
		if node.ObjectType == nil {
			continue
		}
		if strings.TrimSpace(node.ObjectType.Description) == "" {
			a.add(at(node.Pos.Line, node.Pos.Column), SevWarning, CodeNoDescription,
				fmt.Sprintf("%s has no DESCRIPTION, which RFC2578 requires", node.Name),
				string(node.Name))
		}
	}
}

// checkUnusedImports is advice: an import nobody uses is clutter that survives
// every copy-paste.
func (a *analysis) checkUnusedImports() {
	// Anything mentioned AFTER the IMPORTS clause counts as used: macros and
	// compliance statements reference names the AST does not expose, so
	// use.used is false for every one of them.
	//
	// After, not anywhere. scanIdentifiers records only the FIRST appearance
	// of a name, and for an imported symbol that is its own IMPORTS line — so
	// comparing against use.line could not tell "mentioned only in IMPORTS"
	// from "mentioned in IMPORTS and used below". Measured: 50 false
	// unused-import diagnostics across 12 of the 14 bundled MIBs, every one of
	// them a macro (OBJECT-TYPE, MODULE-IDENTITY, TEXTUAL-CONVENTION,
	// MODULE-COMPLIANCE, OBJECT-GROUP).
	mentioned := scanIdentifiers(bodyAfterImports(a.rawSource))

	names := make([]string, 0, len(a.imports))
	for n := range a.imports {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		use := a.imports[name]
		if use.used {
			continue
		}
		if _, ok := mentioned[name]; ok {
			continue
		}
		a.add(at(use.line, use.column), SevInfo, CodeUnusedImport,
			fmt.Sprintf("%q is imported from %s but never used", name, use.module), name)
	}
}

// Analysis is everything the editor needs from one parse.
//
// Grouped because each check parses the file, and running them as separate
// bridge calls meant parsing a 185 KB MIB three times on every pause in typing.
type Analysis struct {
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Missing     []MissingImport `json:"missing"`
	// Outline is what the buffer DEFINES, from the same parse. It rides along
	// because the parse is the expensive part and it has already happened —
	// asking for it separately would double the cost of every pause in typing
	// to answer a question the parse already knows.
	Outline []OutlineItem `json:"outline"`
}

// AnalyseAll runs every check from ONE parse.
//
// The editor called Validate, CheckImports and Analyse in turn, and each of
// them parsed the file: three parses of a 185 KB MIB on every pause in typing,
// about 130 ms of work for 45 ms of answers. This is the entry point the bridge
// uses; the three functions above remain for callers that want one of them.
func AnalyseAll(content string, cat Catalogue) Analysis {
	p := prepare(content)
	index := cat.index()

	out := Analysis{
		Diagnostics: validateParsed(content, p),
		Missing:     p.remapImports(checkImportsParsed(p.Text, p.module, p.err, index)),
	}
	out.Diagnostics = append(out.Diagnostics, p.remap(analyseParsed(p.Text, p.module, p.err, cat))...)
	out.Outline = outlineOf(p.module)
	if out.Missing == nil {
		out.Missing = []MissingImport{}
	}
	return out
}

// bodyAfterImports returns the source with the IMPORTS clause removed.
//
// Everything up to and including the clause's terminating semicolon is
// replaced by blank lines rather than deleted, so any position computed from
// the result still refers to the right line.
// bodyAfterImports returns the source with the IMPORTS clause blanked out.
//
// Blanked rather than deleted, so any position computed from the result still
// refers to the right line.
func bodyAfterImports(content string) string {
	lines := strings.Split(content, "\n")
	importsAt, semicolonAt := -1, -1
	for i, l := range lines {
		code := stripComment(l)
		if importsAt < 0 && containsWord(code, "IMPORTS") {
			importsAt = i
		}
		if importsAt >= 0 && i >= importsAt && strings.IndexByte(code, ';') >= 0 {
			semicolonAt = i
			break
		}
	}
	if importsAt < 0 || semicolonAt < 0 {
		return content
	}

	out := make([]string, len(lines))
	copy(out, lines)
	for i := importsAt; i <= semicolonAt; i++ {
		// Keep whatever follows the semicolon on its line: a single-line
		// clause can be followed by real code.
		if i == semicolonAt {
			code := stripComment(lines[i])
			if at := strings.IndexByte(code, ';'); at >= 0 && at+1 < len(lines[i]) {
				out[i] = strings.Repeat(" ", at+1) + lines[i][at+1:]
				continue
			}
		}
		out[i] = ""
	}
	return strings.Join(out, "\n")
}
