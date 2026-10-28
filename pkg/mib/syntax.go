package mib

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sleepinggenius2/gosmi/parser"
)

// Saying where a MIB is wrong when the parser's own answer points elsewhere.
//
// participle lexes the WHOLE input before the grammar sees a token, so a
// character the lexer cannot read anywhere in the file is reported instead of
// a grammar error that comes before it. The commonest way to get one is a
// stray quote: it ends a DESCRIPTION early, everything up to the next quote is
// read as SMI, and the first typographic dash in that prose — perfectly legal
// inside a string — is "no match found for –", lines below the quote that
// caused it. And because nothing was parsed, the editor's outline was empty.
//
// So a lexer error is looked behind: the character is blanked and the file
// parsed again, until what remains is a grammar error or nothing. Whichever
// problem comes FIRST in the file is the one reported, and the module the
// last parse produced is kept, so the outline shows everything above it.

// maxLexRetries bounds how many unreadable characters are looked behind. Each
// costs a parse; a file with more than this is not a MIB with a typo.
const maxLexRetries = 32

// syntaxError is a located parse error, formatted the way participle formats
// its own so that syntaxDiagnostic reads both.
type syntaxError struct {
	Line, Column int
	Message      string
}

func (e *syntaxError) Error() string { return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Message) }

// prepared is a buffer as every check reads it: repaired, then parsed.
type prepared struct {
	repaired
	module *parser.Module
	err    error
}

// prepare is the one place text becomes a parsed module, for the editor and
// for the diagnosis alike, so the two can never disagree about a file.
func prepare(content string) prepared {
	p := prepared{repaired: repairSource(content)}
	p.module, p.err = parseSMI(p.Text)
	if se, ok := p.err.(*syntaxError); ok && se.Line > 0 && se.Column > 0 {
		se.Column = p.original(se.Line, se.Column)
	}
	return p
}

// parseSMI parses text and, when it fails, reports the problem that comes
// first in the file, in words.
func parseSMI(text string) (*parser.Module, error) {
	module, err := parseOnce(text)
	if err == nil {
		return module, nil
	}
	first, ok := locate(err)
	if !ok {
		return module, err
	}
	if !isLexError(first) {
		return module, explain(text, first)
	}

	// Everything the lexer cannot read outside a string or a comment, blanked
	// at once: once a quote has shifted the strings, every full stop of the
	// prose that is now "code" is an error of its own, and looking behind them
	// one at a time ran out long before the grammar error.
	masked := neutralise(text)
	current := first
	for i := 0; i < maxLexRetries; i++ {
		m, err := parseOnce(masked)
		if err == nil {
			return m, explain(text, first)
		}
		next, ok := locate(err)
		if !ok {
			break
		}
		if isLexError(next) {
			if next.Line == current.Line && next.Column == current.Column && i > 0 {
				break
			}
			current = next
			module = m
			masked = blankRuneAt(masked, next.Line, next.Column)
			continue
		}
		if before(next, first) {
			return m, explain(text, next)
		}
		return m, explain(text, first)
	}
	return module, explain(text, first)
}

// neutralise blanks, outside strings and comments, every character the SMI
// lexer has no token for: anything that is not ASCII, a control character
// that is not whitespace, and a full stop that is not half of "..". One rune
// for one space, so positions are unchanged.
func neutralise(text string) string {
	b := []byte(text)
	var out strings.Builder
	out.Grow(len(text))
	at := 0
	codeSpans(text, false, func(from, to int) {
		out.Write(b[at:from])
		for i := from; i < to; {
			r, size := utf8.DecodeRuneInString(text[i:])
			lone := r == '.' && !(i+1 < to && text[i+1] == '.') && !(i > from && text[i-1] == '.')
			if r > 0x7E || r < 0x20 && r != 0x09 && r != 0x0A && r != 0x0D || lone {
				out.WriteByte(' ')
			} else {
				out.WriteString(text[i : i+size])
			}
			i += size
		}
		at = to
	}, nil)
	out.Write(b[at:])
	return out.String()
}

func locate(err error) (*syntaxError, bool) {
	m := positionRe.FindStringSubmatch(err.Error())
	if m == nil {
		return nil, false
	}
	line, _ := strconv.Atoi(m[1])
	col, _ := strconv.Atoi(m[2])
	return &syntaxError{Line: line, Column: col, Message: m[3]}, true
}

func isLexError(e *syntaxError) bool { return strings.HasPrefix(e.Message, "no match found for ") }

func before(a, b *syntaxError) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Column < b.Column
}

// offsetOf maps a 1-based line and rune column to a byte offset, or -1.
func offsetOf(text string, line, column int) int {
	at := 0
	for l := 1; l < line; l++ {
		nl := strings.IndexByte(text[at:], '\n')
		if nl < 0 {
			return -1
		}
		at += nl + 1
	}
	for c := 1; c < column; c++ {
		if at >= len(text) || text[at] == '\n' {
			return -1
		}
		_, size := utf8.DecodeRuneInString(text[at:])
		at += size
	}
	if at >= len(text) {
		return -1
	}
	return at
}

// blankRuneAt replaces one character with a space. One rune for one space, so
// every later column on the line still means the same character.
func blankRuneAt(text string, line, column int) string {
	at := offsetOf(text, line, column)
	if at < 0 {
		return text
	}
	_, size := utf8.DecodeRuneInString(text[at:])
	return text[:at] + " " + text[at+size:]
}

var (
	unexpectedTokenRe = regexp.MustCompile(`^unexpected "((?:[^"\\]|\\.)*)"`)
	dateExpectedRe    = regexp.MustCompile(`expected <extutctime>`)
)

// explain rewrites a parse error in terms of what is wrong with the FILE.
func explain(text string, e *syntaxError) *syntaxError {
	out := *e
	switch {
	case isLexError(e):
		r, _ := utf8.DecodeRuneInString(strings.TrimPrefix(e.Message, "no match found for "))
		out.Message = fmt.Sprintf("the character %q (U+%04X) can only appear inside a string or a comment", string(r), r)
		if strings.ContainsRune("“”„«»", r) {
			out.Message += `; a string is delimited by straight quotes (")`
		}

	case dateExpectedRe.MatchString(e.Message):
		if m := unexpectedTokenRe.FindStringSubmatch(e.Message); m != nil {
			out.Message = fmt.Sprintf("%q is not a valid date: SMI writes YYMMDDHHMMZ or YYYYMMDDHHMMZ", m[1])
		}

	default:
		m := unexpectedTokenRe.FindStringSubmatch(e.Message)
		if m == nil || !looksLikeProse(m[1]) {
			break
		}
		// A word the grammar did not expect, straight after a string: the
		// string ended before its author meant it to. Point at the quote that
		// ended it — that is the character to change.
		if line, col, ok := closingQuoteBefore(text, e.Line, e.Column); ok {
			out.Line, out.Column = line, col
			out.Message = fmt.Sprintf(
				"this quote ends the string, so %q on line %d is read as SMI rather than as text: a quote inside a string ends it, and SMI strings have no escape",
				m[1], e.Line)
		}
	}
	return &out
}

// looksLikeProse is a token that is a word and not a keyword: what a sentence
// spilling out of a DESCRIPTION looks like to the parser.
func looksLikeProse(token string) bool {
	if token == "" || baseTypes[token] {
		return false
	}
	r, _ := utf8.DecodeRuneInString(token)
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// closingQuoteBefore finds the quote immediately before a position, skipping
// only whitespace — the shape of a string that ended too early.
func closingQuoteBefore(text string, line, column int) (int, int, bool) {
	at := offsetOf(text, line, column)
	if at < 0 {
		return 0, 0, false
	}
	i := at - 1
	for i >= 0 && strings.IndexByte(" \t\r\n", text[i]) >= 0 {
		i--
	}
	if i < 0 || text[i] != '"' {
		return 0, 0, false
	}
	l, c := position(text, i)
	return l, c, true
}
