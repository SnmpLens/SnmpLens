package mib

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The lenient reading: three mistakes vendor MIBs make often enough that
// refusing them refuses the vendor.
//
//   - An IMPORTS clause naming a module by a spelling that is not its name —
//     `FROM RFC-1213` for RFC1213-MIB. It parses, loads, and every object whose
//     SYNTAX came from it (a DisplayString, typically) has NO TYPE, while the
//     load reports success.
//   - A quote inside a DESCRIPTION written `\"`. SMI strings have no escape
//     character, so that quote ENDS the string, and the parser stops at the
//     first word after it with "unexpected" and a token nobody wrote as one.
//   - A LAST-UPDATED or REVISION date of the wrong length, typically
//     "20190415Z" — the day without the time. The grammar wants ten or twelve
//     digits and a Z, and the whole module is refused for it.
//
// The repair is applied to what is READ — what gosmi is given and what the
// editor analyses — and never to the file. Every change is reported as a
// warning at the place it was made, so a MIB that loads only because of it says
// so, and the editor offers to write the repair into the file (Repair).
//
// The last two are applied only to a file that does not parse without them. A
// valid file is never touched: `\"` is legal SMI at the end of a string that
// happens to finish with a backslash, and a repair that "fixed" one would break
// it.

// Codes for what the lenient reading changes.
const (
	CodeImportAlias  = "import-alias"
	CodeEscapedQuote = "escaped-quote"
	CodeBadDate      = "bad-date"
)

// moduleAliases are spellings of a module name found in IMPORTS clauses that
// are not the module's name. Only the SMIv1 base modules, whose RFC numbers
// invite the confusion; a vendor module's name is not ours to guess.
var moduleAliases = map[string]string{
	"RFC-1213": "RFC1213-MIB",
	"RFC1213":  "RFC1213-MIB",
	"RFC-1155": "RFC1155-SMI",
	"RFC1155":  "RFC1155-SMI",
	"RFC1212":  "RFC-1212",
	"RFC1215":  "RFC-1215",
}

// repaired is a source as SnmpLens reads it.
type repaired struct {
	// Text is what is parsed and what gosmi is given.
	Text string
	// Fixes are the changes, one warning each, located in the ORIGINAL text.
	// No change adds or removes a line, so a line number means the same thing
	// in both.
	Fixes []Diagnostic
	// shifts are the changes by line, to bring a column found in Text back to
	// the text on screen: `\"` becoming `'` moves everything after it on its
	// line one column left, and an underline drawn at the repaired column
	// lands beside the word it is about.
	shifts map[int][]lineShift
}

// lineShift is one change on a line: where it starts in the original text and
// how many characters it replaced with how many.
type lineShift struct {
	col, from, to int
}

// original maps a column of Text to the column of the same character in the
// text that was read. A column inside a replacement maps to its start.
func (r repaired) original(line, col int) int {
	shift := 0
	for _, e := range r.shifts[line] {
		start := e.col + shift
		if col < start {
			break
		}
		if col < start+e.to {
			return e.col
		}
		shift += e.to - e.from
	}
	return col - shift
}

// remap brings diagnostics located in Text back to the text that was read.
func (r repaired) remap(diags []Diagnostic) []Diagnostic {
	if len(r.shifts) == 0 {
		return diags
	}
	for i := range diags {
		if diags[i].Line > 0 && diags[i].Column > 0 {
			diags[i].Column = r.original(diags[i].Line, diags[i].Column)
		}
	}
	return diags
}

// edit is one replacement in the original text: Length bytes at Offset.
type edit struct {
	Offset, Length int
	Text           string
	Fix            Diagnostic
}

// repairSource reads content leniently.
//
// It parses only when there is something to decide: a file with neither an
// escaped quote nor a malformed date costs nothing beyond one scan.
func repairSource(content string) repaired {
	edits := importAliasEdits(content)

	gated := append(escapedQuoteEdits(content), badDateEdits(content)...)
	if len(gated) > 0 {
		if _, err := parseOnce(applyEdits(content, edits)); err != nil {
			edits = append(edits, gated...)
		}
	}
	if len(edits) == 0 {
		return repaired{Text: content}
	}

	out := repaired{Text: applyEdits(content, edits), shifts: map[int][]lineShift{}}
	for _, e := range sortedEdits(edits) {
		out.Fixes = append(out.Fixes, e.Fix)
		line, col := position(content, e.Offset)
		out.shifts[line] = append(out.shifts[line], lineShift{
			col:  col,
			from: utf8.RuneCountInString(content[e.Offset : e.Offset+e.Length]),
			to:   utf8.RuneCountInString(e.Text),
		})
	}
	return out
}

// RepairResult is what Repair did to a buffer.
type RepairResult struct {
	// Content is the buffer with every repair written in. Empty when there was
	// nothing to repair.
	Content string `json:"content,omitempty"`
	// Fixes says what changed, where.
	Fixes []Diagnostic `json:"fixes"`
}

// Repair writes the lenient reading into the text: what the editor's "fix"
// does, so the file loads in other MIB tools as it does in SnmpLens.
func Repair(content string) RepairResult {
	r := repairSource(content)
	if len(r.Fixes) == 0 {
		return RepairResult{Fixes: []Diagnostic{}}
	}
	return RepairResult{Content: r.Text, Fixes: r.Fixes}
}

func applyEdits(content string, edits []edit) string {
	if len(edits) == 0 {
		return content
	}
	var b strings.Builder
	b.Grow(len(content))
	at := 0
	for _, e := range sortedEdits(edits) {
		if e.Offset < at {
			continue // overlapping; the scanners never produce one, but never write twice
		}
		b.WriteString(content[at:e.Offset])
		b.WriteString(e.Text)
		at = e.Offset + e.Length
	}
	b.WriteString(content[at:])
	return b.String()
}

func sortedEdits(edits []edit) []edit {
	out := append([]edit(nil), edits...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Offset < out[j-1].Offset; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// position turns a byte offset into a 1-based line and a 1-based column in
// runes, which is how the parser counts.
func position(content string, offset int) (line, column int) {
	line = 1 + strings.Count(content[:offset], "\n")
	start := strings.LastIndexByte(content[:offset], '\n') + 1
	return line, 1 + len([]rune(content[start:offset]))
}

// codeSpans walks content as the SMI lexer does — a comment runs from `--` to
// the end of the line, a string from one quote to the next — and calls code
// for every run of text that is neither. Strings are reported to str with the
// offsets of their opening and closing quotes (close is -1 for a string the
// file never closes).
//
// escapes makes a quote preceded by a backslash part of the string, which is
// how the vendor who wrote it read it and how the repair finds it.
func codeSpans(content string, escapes bool, code func(from, to int), str func(open, close int, escaped []int)) {
	i, from := 0, 0
	for i < len(content) {
		switch {
		case content[i] == '-' && i+1 < len(content) && content[i+1] == '-':
			if code != nil {
				code(from, i)
			}
			end := strings.IndexByte(content[i:], '\n')
			if end < 0 {
				return
			}
			i += end
			from = i
		case content[i] == '"':
			if code != nil {
				code(from, i)
			}
			open := i
			var escaped []int
			j := i + 1
			for ; j < len(content); j++ {
				if content[j] != '"' {
					continue
				}
				if escapes && content[j-1] == '\\' {
					escaped = append(escaped, j)
					continue
				}
				break
			}
			if j >= len(content) {
				if str != nil {
					str(open, -1, escaped)
				}
				return
			}
			if str != nil {
				str(open, j, escaped)
			}
			i = j + 1
			from = i
		default:
			i++
		}
	}
	if code != nil {
		code(from, len(content))
	}
}

var identRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]*|;`)

// importAliasEdits finds `FROM <alias>` in the IMPORTS clause.
func importAliasEdits(content string) []edit {
	var edits []edit
	inImports, done, afterFrom := false, false, false
	codeSpans(content, false, func(from, to int) {
		if done {
			return
		}
		for _, m := range identRe.FindAllStringIndex(content[from:to], -1) {
			word := content[from+m[0] : from+m[1]]
			switch {
			case !inImports:
				inImports = word == "IMPORTS"
			case word == ";":
				done = true
				return
			case afterFrom:
				afterFrom = false
				if canonical, ok := moduleAliases[word]; ok {
					line, col := position(content, from+m[0])
					edits = append(edits, edit{
						Offset: from + m[0], Length: len(word), Text: canonical,
						Fix: Diagnostic{
							Line: line, Column: col, Severity: SevWarning, Code: CodeImportAlias, Symbol: word,
							Message: fmt.Sprintf("%q is not a module name; SnmpLens reads it as %s, which is what it means, but other MIB tools will not find it",
								word, canonical),
						},
					})
				}
			default:
				afterFrom = word == "FROM"
			}
		}
	}, nil)
	return edits
}

// escapedQuoteEdits finds `\"` inside strings and rewrites it as an apostrophe.
func escapedQuoteEdits(content string) []edit {
	var edits []edit
	codeSpans(content, true, nil, func(open, close int, escaped []int) {
		for _, q := range escaped {
			line, col := position(content, q-1)
			edits = append(edits, edit{
				Offset: q - 1, Length: 2, Text: "'",
				Fix: Diagnostic{
					Line: line, Column: col, Severity: SevWarning, Code: CodeEscapedQuote,
					Message: `SMI strings have no escape character, so \" ends the string here; SnmpLens reads it as an apostrophe, but other MIB tools stop at it`,
				},
			})
		}
	})
	return edits
}

// dateClauseRe finds the clauses whose value is an ExtUTCTime. It runs over
// CODE only, so the words inside a DESCRIPTION never match.
var dateClauseRe = regexp.MustCompile(`\b(LAST-UPDATED|REVISION)\s*$`)

// datePartsRe splits a date string's content: the digits and the zone letter.
var datePartsRe = regexp.MustCompile(`^\s*([0-9]+)\s*([zZ]?)\s*$`)

// badDateEdits finds LAST-UPDATED and REVISION values the grammar refuses and
// that have one obvious reading.
func badDateEdits(content string) []edit {
	var edits []edit
	lastCode := ""
	codeSpans(content, false, func(from, to int) {
		lastCode = content[from:to]
	}, func(open, close int, _ []int) {
		clause := dateClauseRe.FindStringSubmatch(lastCode)
		lastCode = ""
		if clause == nil || close < 0 {
			return
		}
		value := content[open+1 : close]
		parts := datePartsRe.FindStringSubmatch(value)
		if parts == nil {
			return
		}
		digits, zone := parts[1], parts[2]
		var fixed string
		switch len(digits) {
		case 10, 12:
			if zone != "" && value == digits+zone {
				return // valid
			}
			fixed = digits + "Z"
		case 6, 8:
			// The day without the time: midnight is the only reading.
			fixed = digits + "0000Z"
		default:
			return
		}
		line, col := position(content, open)
		edits = append(edits, edit{
			Offset: open + 1, Length: close - open - 1, Text: fixed,
			Fix: Diagnostic{
				Line: line, Column: col, Severity: SevWarning, Code: CodeBadDate, Symbol: value,
				Message: fmt.Sprintf("%s %q is not a valid date (YYYYMMDDHHMMZ); SnmpLens reads it as %q, but other MIB tools reject the module",
					clause[1], value, fixed),
			},
		})
	})
	return edits
}
