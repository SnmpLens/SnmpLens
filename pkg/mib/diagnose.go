package mib

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sleepinggenius2/gosmi"
	"github.com/sleepinggenius2/gosmi/parser"
)

// Why a MIB did not load.
//
// gosmi says "Could not load module at X" and nothing else. That single string
// covers a file that is not there, a file that is a PDF, a syntax error on line
// 412, and an IMPORTS clause naming a module nobody has — four problems with
// four different answers, and the message distinguishes none of them.
//
// It also throws away the one thing that would: smi.LoadModule receives
// internal.GetModule's real error — "Parse module: 12:5: unexpected token" —
// prints it with fmt.Println, and returns an empty string. So the position
// exists, is computed, and is discarded one frame below the API.
//
// This file recovers it, and adds what gosmi never had: which imported module
// is missing and which symbols came from it, whether a file even is a MIB,
// whether its module name matches its file name, and the semantic problems
// that pass the loader silently.

// Load stages, in the order a file goes through them.
const (
	StageRead     = "read"     // the bytes could not be obtained
	StageContent  = "content"  // the bytes are not a MIB
	StageParse    = "parse"    // the grammar rejected it
	StageImports  = "imports"  // it needs a module that is not here
	StageBuild    = "build"    // gosmi refused it for another reason
	StageSemantic = "semantic" // it loaded, but resolves to nonsense
	StageLoaded   = "loaded"   // it is in the tree
	// StageDuplicate is a file not loaded because another file declaring the
	// same module was: gosmi holds one module per name.
	StageDuplicate = "duplicate"
)

// LoadDiagnosis explains how far one file got and what stopped it.
type LoadDiagnosis struct {
	FileName string `json:"fileName"`
	// ModuleName is what the file DECLARES, which is what every IMPORTS clause
	// elsewhere will look for — not necessarily what the file is called.
	ModuleName string `json:"moduleName,omitempty"`
	Loaded     bool   `json:"loaded"`
	Stage      string `json:"stage"`
	// Summary is one line saying what stopped it, in a person's words.
	Summary string `json:"summary"`
	// Detail is the underlying message, including the one gosmi discards.
	Detail string `json:"detail,omitempty"`
	// Diagnostics are located problems: syntax, unknown types, duplicate OIDs.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	// Missing lists the imported modules that are not available.
	Missing []MissingModule `json:"missing,omitempty"`
	// Hints are what to do about it.
	Hints []string `json:"hints,omitempty"`
	Bytes int      `json:"bytes"`
}

// Reasons an imported module is unavailable.
const (
	ImportAbsent    = "absent"    // no file for it anywhere in the MIB directory
	ImportFailed    = "failed"    // the file is there and has a problem of its own
	ImportNotLoaded = "notloaded" // the file is fine; it is simply not loaded
)

// MissingModule is one entry of an IMPORTS clause that cannot be satisfied.
type MissingModule struct {
	Module  string   `json:"module"`
	Symbols []string `json:"symbols,omitempty"`
	Reason  string   `json:"reason"`
	// Cause is the missing module's own failure, when it has one. This is how
	// a chain gets reported at its root rather than at its symptom.
	Cause string `json:"cause,omitempty"`
}

// Diagnose explains one file, whether or not it loads.
//
// It is deliberately willing to do work the fast path does not: this runs when
// something has already gone wrong, or when someone asks, and an answer that
// takes 40 ms and names the line beats one that is instant and says nothing.
func (s *Service) Diagnose(fileName string) LoadDiagnosis {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	return s.diagnose(fileName, newDiagContext(s), "")
}

// diagContext is the per-request state a diagnosis shares with the ones it
// triggers: the directory listing, and the modules already visited.
//
// The listing costs an Open and an 8 KB read of every file in the folder. It
// used to be rebuilt at every recursion level, so a directory of 200 vendor
// MIBs with 50 failures did that fifty times over — thousands of file opens,
// all of them inside the lock.
type diagContext struct {
	visiting  map[string]bool
	available map[string]string
	hasFiles  bool
}

func newDiagContext(s *Service) *diagContext {
	return &diagContext{visiting: map[string]bool{}}
}

func (c *diagContext) files(s *Service) map[string]string {
	if !c.hasFiles {
		c.available = s.availableModules()
		c.hasFiles = true
	}
	return c.available
}

// diagnose does the work.
//
// It READS. It does not call gosmi.LoadModule, and neither does anything it
// calls: an explanation that loads modules as a side effect would re-enable
// MIBs the user switched off — they stay in the global node index while
// absent from the tree, so Translate, ResolveOid, Table and Symbols all start
// answering from a module nobody asked for. `printed` carries what gosmi said
// when the caller tried the load itself.
func (s *Service) diagnose(fileName string, ctx *diagContext, printed string) LoadDiagnosis {
	d := LoadDiagnosis{FileName: fileName, Stage: StageRead}

	path, err := s.resolve(fileName)
	if err != nil {
		d.Summary = err.Error()
		return d
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		d.Summary = readFailureSummary(path, err)
		d.Detail = err.Error()
		return d
	}
	d.Bytes = len(raw)

	// --- is this a MIB at all? ---
	d.Stage = StageContent
	if summary, hint, bad := describeNonMib(raw); bad {
		d.Summary = summary
		if hint != "" {
			d.Hints = append(d.Hints, hint)
		}
		return d
	}

	// What the loader reads: decoded, then read leniently (mibfs.go). A file
	// whose name differs from its module is no longer worth a hint — gosmi
	// finds it by the name it declares.
	content, _, _ := DecodeSource(raw)
	d.ModuleName = moduleNameOf(content)

	// --- syntax ---
	d.Stage = StageParse
	p := prepare(content)
	d.Diagnostics = append(d.Diagnostics, p.Fixes...)
	module, parseErr := p.module, p.err
	if parseErr != nil {
		diag := syntaxDiagnostic(parseErr)
		d.Diagnostics = append(d.Diagnostics, diag)
		if diag.Line > 0 {
			d.Summary = fmt.Sprintf("syntax error at line %d, column %d: %s", diag.Line, diag.Column, diag.Message)
			d.Hints = append(d.Hints, excerpt(content, diag.Line, diag.Column))
		} else {
			d.Summary = "the file does not parse: " + diag.Message
		}
		d.Detail = parseErr.Error()
		return d
	}

	// --- imports ---
	d.Stage = StageImports
	d.Missing = s.missingImports(module, ctx)
	if len(d.Missing) > 0 {
		d.Summary = summariseMissing(d.Missing)
		d.Hints = append(d.Hints, "put the missing module in the MIB directory; SnmpLens finds it by the name it declares, whatever the file is called")
		// Not a return: a module can be missing AND the file have other
		// problems, and listing only the first wastes the round trip.
	}

	// --- what the loader made of it ---
	d.Stage = StageBuild
	d.Loaded = d.ModuleName != "" && gosmi.IsLoaded(d.ModuleName)
	printed = shortenPaths(strings.TrimSpace(printed), s.path)

	if !d.Loaded {
		if printed != "" {
			d.Detail = printed
			if d.Summary == "" {
				d.Summary = humaniseGosmi(printed)
			}
		}
		if d.Summary == "" {
			d.Summary = "gosmi refused the module without saying why"
		}
		return d
	}

	// --- loaded, which is not the same as correct ---
	// analyseParsed, not Analyse: the module is already parsed above, and a
	// 185 KB MIB costs about 40 ms to parse a second time for nothing.
	cat := symbolsLocked()
	for _, diag := range p.remap(analyseParsed(p.Text, module, nil, cat)) {
		if diag.Severity == SevError {
			d.Diagnostics = append(d.Diagnostics, diag)
		}
	}

	// A module loads even when its IMPORTS cannot be satisfied: gosmi resolves
	// them lazily and returns nil rather than failing. The unresolved
	// references that follow are the SYMPTOM, so the missing module stays the
	// headline — replacing it with "3 unresolved references" would report the
	// consequence and hide the one thing worth acting on.
	if len(d.Missing) > 0 {
		d.Stage = StageImports
		if len(d.Diagnostics) > 0 {
			d.Summary += fmt.Sprintf("; %s could not be resolved as a result",
				plural(len(d.Diagnostics), "reference", "references"))
		}
		return d
	}

	if len(d.Diagnostics) > 0 {
		d.Stage = StageSemantic
		d.Summary = fmt.Sprintf(
			"the module loaded but has %s; objects that use them resolve to nothing",
			plural(len(d.Diagnostics), "unresolved reference", "unresolved references"))
		return d
	}

	d.Stage = StageLoaded
	if d.Summary == "" {
		d.Summary = "loaded"
		if len(p.Fixes) > 0 {
			d.Summary = fmt.Sprintf("loaded, with %s that other MIB tools would refuse",
				plural(len(p.Fixes), "mistake", "mistakes"))
		}
	}
	return d
}

// resolve locates a file inside the MIB directory.
//
// Through resolveMibPath like every other method here: monitoring.db,
// service.json and the secret store all sit one directory above mibs/.
func (s *Service) resolve(fileName string) (string, error) {
	path, err := SafeMibPath(s.path, fileName)
	if err != nil {
		return "", fmt.Errorf("%s: %v", fileName, err)
	}
	// A file of an added folder, when the MIB folder has none of that name.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if p, ok := s.library().path(fileName); ok {
			path = p
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s is not in the MIB directory", fileName)
		}
		return "", fmt.Errorf("%s cannot be opened: %v", fileName, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a MIB file", fileName)
	}
	return path, nil
}

func readFailureSummary(path string, err error) string {
	switch {
	case os.IsPermission(err):
		return fmt.Sprintf("%s cannot be read: permission denied", filepath.Base(path))
	case os.IsNotExist(err):
		return fmt.Sprintf("%s is not in the MIB directory", filepath.Base(path))
	}
	return fmt.Sprintf("%s cannot be read: %v", filepath.Base(path), err)
}

// describeNonMib recognises the files people actually drop in by mistake.
//
// A downloaded MIB is very often the HTML page around it, a PDF of the
// specification, or a zip nobody extracted. All three reach gosmi as "could
// not load module", which sends people looking for a syntax error in a file
// that is not text.
func describeNonMib(raw []byte) (summary, hint string, bad bool) {
	_, summary, hint, bad = classifyNonMib(raw)
	return summary, hint, bad
}

// Kinds of file that are not a MIB, for an interface that groups them rather
// than repeating one sentence per file: an archive dropped whole holds dozens.
const (
	NonMibEmpty         = "empty"
	NonMibPDF           = "pdf"
	NonMibArchive       = "archive"
	NonMibHTML          = "html"
	NonMibUTF16         = "utf16"
	NonMibBinary        = "binary"
	NonMibNoDefinitions = "nodefinitions"
)

func classifyNonMib(raw []byte) (kind, summary, hint string, bad bool) {
	if len(raw) == 0 {
		return NonMibEmpty, "the file is empty", "", true
	}
	switch {
	case bytes.HasPrefix(raw, []byte("%PDF-")):
		return NonMibPDF, "this is a PDF, not a MIB", "MIBs are plain text; extract the module from the document, or download the .mib file", true
	case bytes.HasPrefix(raw, []byte("PK\x03\x04")):
		// A .docx or an .xlsx is a zip too, and is not a MIB for the same reason.
		return NonMibArchive, "this is a zip archive, not a MIB", "extract it and import the MIB files inside", true
	case bytes.HasPrefix(raw, []byte{0x1f, 0x8b}):
		return NonMibArchive, "this is a gzip archive, not a MIB", "extract it and import the MIB files inside", true
	}

	head := raw
	if len(head) > 4096 {
		head = head[:4096]
	}
	lower := strings.ToLower(string(head))
	if strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html") {
		return NonMibHTML, "this is an HTML page, not a MIB",
			"the download returned the web page around the file; use the raw or download link", true
	}
	if bytes.IndexByte(head, 0) >= 0 {
		// UTF-16 is the one binary-looking encoding a MIB is plausibly in.
		if bytes.HasPrefix(raw, []byte{0xff, 0xfe}) || bytes.HasPrefix(raw, []byte{0xfe, 0xff}) {
			return NonMibUTF16, "the file is UTF-16 encoded", "save it as UTF-8 or ASCII; MIB parsers do not accept UTF-16", true
		}
		return NonMibBinary, "the file is binary, not text", "", true
	}
	// Not UTF-8 is not "not a MIB": a vendor MIB in Windows-1252 loads, and
	// DecodeSource reads it. What is refused is bytes that are not TEXT in any
	// encoding — control characters a text file does not hold.
	if controlRatio(head) > 0.01 {
		return NonMibBinary, "the file is binary, not text", "", true
	}
	if !strings.Contains(strings.ToUpper(string(head)), "DEFINITIONS") {
		return NonMibNoDefinitions, noDefinitionsSummary,
			"a MIB begins with `MODULE-NAME DEFINITIONS ::= BEGIN`; this file may be a fragment or a different format", true
	}
	return "", "", "", false
}

// noDefinitionsSummary is named because ImportRejection has to tell that answer
// apart from the others: it is the only one describeNonMib reaches by looking
// at a WINDOW rather than at the bytes themselves.
const noDefinitionsSummary = "no DEFINITIONS clause was found near the start of the file"

// ImportRejection says why a file must not be copied into the MIB directory,
// or reports that it may be.
//
// This is a security boundary as much as a usability one. ImportMibFiles reads
// any absolute path the renderer names and copies it in; MibEditorRead then
// hands the content back. Those two calls together are an arbitrary-file-read
// primitive over the bridge, and the only thing standing between them is what
// is allowed to land in the directory. A private key, a password file, a
// browser profile: none of them are a MIB, and none of them get in.
//
// It does NOT try to decide whether a MIB is VALID — a MIB with a syntax error
// is exactly what the diagnosis feature exists for, and refusing those would
// remove the reason someone imports a broken file in the first place.
//
// The DEFINITIONS check is re-run over the WHOLE file rather than
// describeNonMib's first 4 KiB. That window is right for a diagnosis and too
// strict for a gate: vendor MIBs routinely carry a licence header longer than
// that, and refusing a real MIB is a worse failure here than accepting a text
// file that happens to contain the word.
func ImportRejection(raw []byte) (summary, hint string, reject bool) {
	_, summary, hint, reject = ClassifyImport(raw)
	return summary, hint, reject
}

// ClassifyImport is ImportRejection with the kind of file refused (NonMib*).
func ClassifyImport(raw []byte) (kind, summary, hint string, reject bool) {
	k, s, h, bad := classifyNonMib(raw)
	if !bad {
		return "", "", "", false
	}
	if k == NonMibNoDefinitions && strings.Contains(strings.ToUpper(string(raw)), "DEFINITIONS") {
		return "", "", "", false
	}
	return k, s, h, true
}

// missingImports reports the modules an IMPORTS clause names that are not
// available, with the symbols taken from each.
//
// The symbols matter: "CISCO-SMI is missing" is a fact, and "CISCO-SMI is
// missing, which is where ciscoMgmt comes from" is the same fact with the
// reason this file needs it.
func (s *Service) missingImports(module *parser.Module, ctx *diagContext) []MissingModule {
	if module == nil || module.Body.Imports == nil {
		return nil
	}

	available := ctx.files(s)
	var out []MissingModule

	for _, imp := range module.Body.Imports {
		name := strings.TrimSpace(imp.Module.String())
		if name == "" || gosmi.IsLoaded(name) {
			continue
		}

		symbols := make([]string, 0, len(imp.Names))
		for _, n := range imp.Names {
			symbols = append(symbols, n.String())
		}
		// gosmi reads some imports from the modules that replaced the one
		// named; only what it does not is missing (implicit.go).
		if needed, converted := notConverted(name, symbols); converted {
			if len(needed) == 0 {
				continue
			}
			symbols = needed
		}
		sort.Strings(symbols)

		file, present := available[strings.ToUpper(name)]
		if !present {
			out = append(out, MissingModule{
				Module: name, Symbols: symbols, Reason: ImportAbsent,
			})
			continue
		}

		// The file is here and the module is not loaded. Two very different
		// reasons: it is broken, or it is simply switched off. Look, and say
		// which — "does not load either" was wrong for the second, and the
		// second is the common one.
		entry := MissingModule{Module: name, Symbols: symbols, Reason: ImportNotLoaded}
		if !ctx.visiting[strings.ToUpper(name)] {
			ctx.visiting[strings.ToUpper(name)] = true
			sub := s.diagnose(file, ctx, "")
			switch sub.Stage {
			case StageRead, StageContent, StageParse:
				// A problem of its own, at its root.
				entry.Reason = ImportFailed
				entry.Cause = sub.Summary
			}
		}
		out = append(out, entry)
	}
	return out
}

// availableModules maps an upper-cased module name to the file that holds it.
//
// By CONTENT, not by file name: a vendor ships FOO.my declaring FOO-MIB, and a
// name-only check would report FOO-MIB as absent while it sits right there.
func (s *Service) availableModules() map[string]string {
	out := map[string]string{}
	lib := s.library()
	for _, f := range lib.order {
		// The file name is the cheap answer and usually the right one.
		if base := moduleBase(f); base != "" {
			if _, seen := out[strings.ToUpper(base)]; !seen {
				out[strings.ToUpper(base)] = f
			}
		}
		name := declaredModuleName(lib.files[f])
		if name == "" {
			continue
		}
		if _, seen := out[strings.ToUpper(name)]; !seen {
			out[strings.ToUpper(name)] = f
		}
	}
	return out
}

// controlRatio is the share of bytes that are control characters no text file
// holds: everything below a space except tab, line feed, form feed and
// carriage return.
func controlRatio(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	n := 0
	for _, c := range b {
		if c < 0x20 && c != 0x09 && c != 0x0A && c != 0x0C && c != 0x0D {
			n++
		}
	}
	return float64(n) / float64(len(b))
}

// declaredModuleName reads only the head of a file: the module name is on the
// first non-comment line, and reading 185 KB of every MIB to find it would
// turn a diagnosis into a directory scan.
func declaredModuleName(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	if n <= 0 {
		return ""
	}
	content, _, _ := DecodeSource(head[:n])
	return moduleNameOf(content)
}

// moduleNameOf finds the module name without a full parse.
func moduleNameOf(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		i := strings.Index(upper, "DEFINITIONS")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if fields := strings.Fields(name); len(fields) == 1 {
			return fields[0]
		}
		return ""
	}
	return ""
}

func stripComment(line string) string {
	if i := strings.Index(line, "--"); i >= 0 {
		return line[:i]
	}
	return line
}

// moduleBase is the file name without its extension, which is the name gosmi
// searches by.
func moduleBase(fileName string) string {
	base := filepath.Base(fileName)
	if i := strings.Index(base, "."); i > 0 {
		return base[:i]
	}
	return base
}

func summariseMissing(missing []MissingModule) string {
	names := make([]string, 0, len(missing))
	for _, m := range missing {
		names = append(names, m.Module)
	}
	if len(missing) == 1 {
		m := missing[0]
		switch m.Reason {
		case ImportAbsent:
			return fmt.Sprintf("it imports %s, which is not in the MIB directory", m.Module)
		case ImportFailed:
			return fmt.Sprintf("it imports %s, which is present and does not load", m.Module)
		}
		return fmt.Sprintf("it imports %s, which is present but not loaded — it may be disabled", m.Module)
	}
	return fmt.Sprintf("it imports %d modules that are unavailable: %s",
		len(missing), strings.Join(names, ", "))
}

// humaniseGosmi turns the message gosmi prints into one worth showing.
func humaniseGosmi(printed string) string {
	switch {
	case strings.Contains(printed, "Parse module:"):
		return "the grammar rejected the file: " + after(printed, "Parse module:")
	case strings.Contains(printed, "Get module file"):
		return "the file could not be opened by the MIB loader: " + after(printed, "Get module file")
	case strings.Contains(printed, "Build module:"):
		return "the module parsed but could not be built: " + after(printed, "Build module:")
	}
	return printed
}

func after(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return s
	}
	return strings.TrimSpace(s[i+len(marker):])
}

// excerpt shows the offending line with a caret under the column, which is the
// difference between "line 412" and seeing what is wrong on line 412.
// excerptWidth is how much of the offending line to show.
const excerptWidth = 160

func excerpt(content string, line, column int) string {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	text := strings.ReplaceAll(lines[line-1], "\t", " ")

	if column < 1 || column > len(text)+1 {
		if len(text) > excerptWidth {
			text = text[:excerptWidth] + "…"
		}
		return text
	}

	// Around the column, not from the start of the line. Truncating at a fixed
	// width from column 1 drops the reported position entirely on a long vendor
	// line, and the caret was then omitted — leaving an excerpt that does not
	// contain the thing it is an excerpt of.
	caret := column - 1
	if len(text) > excerptWidth {
		start := 0
		if caret > excerptWidth/2 {
			start = caret - excerptWidth/2
		}
		end := start + excerptWidth
		if end > len(text) {
			end = len(text)
			start = end - excerptWidth
		}
		prefix, suffix := "", ""
		if start > 0 {
			prefix = "…"
		}
		if end < len(text) {
			suffix = "…"
		}
		caret = caret - start + len([]rune(prefix))
		text = prefix + text[start:end] + suffix
	}
	return text + "\n" + strings.Repeat(" ", caret) + "^"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// captureStdout runs fn with os.Stdout redirected, and returns what was written.
//
// The only way to recover the real load error: smi.LoadModule receives it,
// fmt.Println's it, and returns an empty string. Everything else in this app
// logs through the log package, which writes to stderr, so what lands here is
// gosmi's — and callers hold gosmiMu, so two of these cannot overlap.
//
// The reader runs on its own goroutine because a pipe holds about 64 KB and a
// module with many errors can print more than that; reading after fn returns
// would deadlock at exactly the point where the output matters most.
func captureStdout(fn func()) string {
	r, w, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}

	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	func() {
		// fn is third-party code; a panic must still restore stdout, or every
		// later print in the process disappears into a pipe nobody reads.
		defer func() {
			os.Stdout = saved
			_ = w.Close()
		}()
		fn()
	}()

	out := <-done
	_ = r.Close()
	return out
}

// SafeMibPath maps a file name to a path inside dir, refusing anything that
// would land outside it.
//
// One implementation, shared with the editor's bridge methods: these read and
// write, and monitoring.db, service.json and the secret store all sit one
// directory above mibs/. Two copies of a containment check are two chances to
// fix only one of them.
func SafeMibPath(dir, name string) (string, error) {
	clean := filepath.Base(strings.TrimSpace(name))
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("invalid MIB file name %q", name)
	}
	full := filepath.Join(dir, clean)
	// Belt and braces: Base should make this impossible, but the check is
	// cheap and the consequence of being wrong is writing outside the folder.
	if filepath.Dir(full) != filepath.Clean(dir) {
		return "", fmt.Errorf("refusing a MIB path outside the MIB directory: %q", name)
	}
	return full, nil
}

// shortenPaths replaces the absolute path gosmi prints with the file name.
//
// Its message embeds the full path of the file it was reading — a temp
// directory, or the user's config folder — which buries the "12:5" that is the
// only part anyone needs. Removing the directory keeps the position and the
// file name, and drops eighty characters of noise before it.
func shortenPaths(msg, dir string) string {
	if dir == "" || msg == "" {
		return msg
	}
	// gosmi names the directory "[dir]" since it reads it through mibFS.
	for _, form := range []string{
		"[" + filepath.Clean(dir) + "]" + string(filepath.Separator),
		"[" + filepath.Clean(dir) + "]/",
		filepath.Clean(dir) + string(filepath.Separator),
		filepath.ToSlash(filepath.Clean(dir)) + "/",
	} {
		msg = strings.ReplaceAll(msg, form, "")
	}
	return msg
}
