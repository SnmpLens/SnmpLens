package mib

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sleepinggenius2/gosmi/parser"
)

// Editing support for MIB files.
//
// The useful half of a MIB editor is not the colours, it is being able to say
// WHERE a file is wrong. gosmi cannot: its LoadModule swallows the parser's
// positioned error with a fmt.Println and returns "Could not load module at X"
// — no line, no column, no reason. So diagnostics come from gosmi's parser
// sub-package directly, which does carry a position, and the load is used only
// for the question the parser cannot answer: will this actually resolve.
//
// Two tiers, because they catch different things:
//
//   - parser.Parse gives syntax errors at line:column, cheaply, and can run on
//     every keystroke. It does NOT see semantic problems: a MIB declaring
//     SYNTAX Integerr32 parses perfectly.
//   - gosmi.LoadModule sees the semantic ones, has no position, and needs the
//     file on disk. That runs on save.

// Diagnostic is one problem with a MIB, located where a person can find it.
type Diagnostic struct {
	// Line and Column are 1-based. Zero means the problem has no position.
	Line   int `json:"line"`
	Column int `json:"column"`
	// Severity is error, warning or info.
	Severity string `json:"severity"`
	// Code is a stable machine key, so the UI can offer a fix without parsing
	// the message.
	Code    string `json:"code"`
	Message string `json:"message"`
	// Symbol names what the problem is about, when there is one.
	Symbol string `json:"symbol,omitempty"`
}

// Diagnostic severities and codes.
const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"

	CodeSyntax     = "syntax"
	CodeEncoding   = "encoding"
	CodeModuleName = "module-name"
	CodeStructure  = "structure"
	CodeLoad       = "load"
	CodeTabs       = "tabs"
	CodeLongLine   = "long-line"
)

// FileInfo describes one MIB in the persistent directory.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Module is what the file declares, read from its head: two files
	// declaring one module are two versions of it, and the file list offers
	// to choose between them.
	Module string `json:"module,omitempty"`
	// Bundled marks a MIB that ships inside the binary. Editing one is
	// allowed but flagged, because nearly every other MIB IMPORTS from them.
	Bundled bool `json:"bundled"`
	// Modified is true for a bundled MIB whose content no longer matches the
	// embedded copy.
	Modified bool `json:"modified"`
	// Problems is the diagnostic count, filled in only for files that have
	// been opened. Validating every vendor MIB on mount would be pointless
	// work on a directory that can hold hundreds.
	Problems int `json:"problems"`
}

// Source is a MIB opened in the editor.
type Source struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	// Eol records what the file used, so saving does not silently rewrite
	// every line of a file the user only touched in one place.
	Eol string `json:"eol"` // "lf" or "crlf"
	// Encoding is how the bytes were read: "utf-8", or "windows-1252" for a
	// file that is not UTF-8 (DecodeSource). Saving always writes UTF-8, and
	// the editor says so before it happens.
	Encoding string `json:"encoding"`
	Bundled  bool   `json:"bundled"`
	External bool   `json:"external"`
	// Sha256 of the file's bytes as read (ChecksumBytes), so a save can notice
	// the file changed underneath, whatever encoding it was read in.
	Sha256 string `json:"sha256"`
	// The complete analysis, as AnalyseAll gives it. It used to be the quick
	// syntax check alone, so a file opened marked "warning" and turned "error"
	// a moment later, when the first full analysis answered, and its outline
	// read "nothing defined" over a file defining forty things until then.
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Missing     []MissingImport `json:"missing"`
	Outline     []OutlineItem   `json:"outline"`
	Verdict     Verdict         `json:"verdict"`
}

// SetAnalysis fills in what an analysis of the source found.
func (s *Source) SetAnalysis(an Analysis) {
	s.Diagnostics, s.Missing, s.Outline, s.Verdict = an.Diagnostics, an.Missing, an.Outline, an.Verdict
}

// SaveResult reports what a save did.
type SaveResult struct {
	Saved       bool         `json:"saved"`
	BackupPath  string       `json:"backupPath,omitempty"`
	Sha256      string       `json:"sha256"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	// Conflict is set when the file changed on disk since it was read and the
	// save was refused.
	Conflict bool `json:"conflict"`
}

// maxLineLength is where a line stops being readable in an editor pane. SMI
// has no limit; this is advice, not a rule, hence a warning.
const maxLineLength = 200

// bomPrefix is the UTF-8 byte order mark.
const bomPrefix = "\ufeff"

// positionRe extracts "line:column:" from a participle lexer error, which
// formats itself as "2:1: unexpected \"this\" (expected \"END\")".
var positionRe = regexp.MustCompile(`^(\d+):(\d+):\s*(.*)$`)

// moduleNameRe finds the module name in the DEFINITIONS line.
var moduleNameRe = regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9-]*)\s+DEFINITIONS\b`)

// endRe finds the module terminator. Hoisted: it used to be compiled inside
// the function. A trailing comment is allowed — a file ending `END -- that is
// all` IS closed, and was reported as an error saying it never was.
var endRe = regexp.MustCompile(`(?m)^\s*END\s*(--.*)?$`)

// Validate reports every problem it can find in MIB source text.
//
// It is pure: no file is read, no global state is touched, and gosmi's loaded
// tree is untouched. That is what makes it safe to run while the user types.
func Validate(content string) []Diagnostic {
	return validateParsed(content, prepare(content))
}

// parseOnce is the single parse the whole editor pipeline shares.
//
// It is by far the most expensive thing here: about 40 ms on a 185 KB MIB.
// Running it once per check meant THREE parses on every pause in typing, which
// is where the editor felt slow.
func parseOnce(content string) (*parser.Module, error) {
	return parser.Parse(strings.NewReader(strings.TrimPrefix(content, bomPrefix)))
}

// validateParsed produces the syntax, structure and style diagnostics from a
// file that has already been parsed, and says what the lenient reading
// changed: a MIB that loads only because of it is still wrong.
func validateParsed(content string, p prepared) []Diagnostic {
	diags := []Diagnostic{}
	parseErr := p.err

	if strings.HasPrefix(content, "\ufeff") {
		diags = append(diags, Diagnostic{
			Line: 1, Column: 1, Severity: SevWarning, Code: CodeEncoding,
			Message: "the file starts with a UTF-8 byte order mark, which some MIB compilers reject",
		})
		content = strings.TrimPrefix(content, "\ufeff")
	}

	// Syntax first: without a parse there is nothing else worth saying.
	if parseErr != nil {
		diags = append(diags, syntaxDiagnostic(parseErr))
	}
	diags = append(diags, p.Fixes...)

	diags = append(diags, structureDiagnostics(content)...)
	diags = append(diags, styleDiagnostics(content)...)
	return diags
}

// syntaxDiagnostic turns a parser error into a located diagnostic.
func syntaxDiagnostic(err error) Diagnostic {
	msg := err.Error()
	if m := positionRe.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		return Diagnostic{
			Line: line, Column: col, Severity: SevError, Code: CodeSyntax,
			Message: m[3],
		}
	}
	return Diagnostic{Severity: SevError, Code: CodeSyntax, Message: msg}
}

// structureDiagnostics catches the mistakes that are common, cheap to detect,
// and that the parser either misses or reports unhelpfully far from their
// cause.
func structureDiagnostics(content string) []Diagnostic {
	var diags []Diagnostic

	if !moduleNameRe.MatchString(content) {
		diags = append(diags, Diagnostic{
			Line: 1, Column: 1, Severity: SevError, Code: CodeStructure,
			Message: "no MODULE DEFINITIONS ::= BEGIN header found",
		})
	}

	// A missing END is reported by the parser as "unexpected <EOF>", which
	// points at the last line rather than at what is missing. Say it plainly.
	if !endRe.MatchString(content) {
		diags = append(diags, Diagnostic{
			Line: countLines(content), Column: 1, Severity: SevError, Code: CodeStructure,
			Message: "the module is never closed with END",
		})
	}
	return diags
}

// styleDiagnostics are advice, never errors: they describe files that load
// perfectly but are painful to work with.
func styleDiagnostics(content string) []Diagnostic {
	var diags []Diagnostic
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	line := 0
	tabsAt := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if tabsAt == 0 && strings.Contains(text, "\t") {
			tabsAt = line
		}
		// Runes, not bytes. len() counts bytes, so a line of 100 accented
		// characters was reported as 203 characters long — and the column the
		// caret is sent to was equally wrong.
		if width := utf8.RuneCountInString(text); width > maxLineLength {
			diags = append(diags, Diagnostic{
				Line: line, Column: maxLineLength, Severity: SevInfo, Code: CodeLongLine,
				Message: fmt.Sprintf("line is %d characters long", width),
			})
		}
	}
	if tabsAt > 0 {
		diags = append(diags, Diagnostic{
			Line: tabsAt, Column: 1, Severity: SevInfo, Code: CodeTabs,
			Message: "the file mixes tabs into its indentation, which renders differently in every tool",
		})
	}
	return diags
}

func countLines(s string) int {
	if s == "" {
		return 1
	}
	return strings.Count(s, "\n") + 1
}

// ModuleName reads the module name out of the DEFINITIONS line.
//
// This matters more than it looks: gosmi resolves a module by NAME, and the
// file name is what the search path matches on. A file called FOO-MIB that
// declares BAR-MIB inside will not load under either name reliably.
func ModuleName(content string) string {
	if m := moduleNameRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

// NormaliseSource strips a byte order mark and converts line endings to LF,
// reporting what the file used so a save can put it back.
func NormaliseSource(raw []byte) (content, eol string) {
	s := strings.TrimPrefix(string(raw), "\ufeff")
	if strings.Contains(s, "\r\n") {
		return strings.ReplaceAll(s, "\r\n", "\n"), "crlf"
	}
	// A lone CR is old Mac line endings; normalise those too rather than
	// leaving a file that looks like one enormous line.
	if strings.Contains(s, "\r") {
		return strings.ReplaceAll(s, "\r", "\n"), "lf"
	}
	return s, "lf"
}

// RestoreEol puts the file's original line endings back.
func RestoreEol(content, eol string) string {
	if eol == "crlf" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// Checksum identifies content for change detection.
func Checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// CountBySeverity summarises diagnostics for a file list badge.
func CountBySeverity(diags []Diagnostic) (errors, warnings, infos int) {
	for _, d := range diags {
		switch d.Severity {
		case SevError:
			errors++
		case SevWarning:
			warnings++
		default:
			infos++
		}
	}
	return errors, warnings, infos
}
