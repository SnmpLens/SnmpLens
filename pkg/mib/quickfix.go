package mib

import (
	"regexp"
	"sort"
)

// Fixes the editor can write for a problem it reports.
//
// Two of the analysis's findings have exactly one right answer, and it is
// mechanical: an import nothing uses is taken out of IMPORTS, and a table or a
// conceptual row whose MAX-ACCESS is not "not-accessible" (RFC 2578 7.3) is
// given it. Both used to be reported line by line and left to be typed in by
// hand — on a vendor MIB that is forty edits of one word.
//
// The text is edited as TEXT, the way FixImports adds to IMPORTS: a MIB carries
// comments and alignment no printer of the AST would keep, and the person
// reviewing the change should see the word that changed and nothing else.

// QuickFixable says which codes QuickFix knows how to fix.
var QuickFixable = map[string]bool{CodeUnusedImport: true, CodeRowAccess: true}

// QuickFixResult is a buffer with fixes written in.
type QuickFixResult struct {
	// Content is the whole buffer, fixed. Empty when nothing was fixed.
	Content string `json:"content,omitempty"`
	// Fixed names the symbols the fixes were for.
	Fixed []string `json:"fixed"`
}

// QuickFix fixes every problem of kind code whose symbol is one of symbols —
// or every problem of that kind when symbols is empty.
func QuickFix(content, code string, symbols []string, cat Catalogue) QuickFixResult {
	out := QuickFixResult{Fixed: []string{}}
	if !QuickFixable[code] {
		return out
	}
	wanted := map[string]bool{}
	for _, s := range symbols {
		wanted[s] = true
	}
	var targets []Diagnostic
	for _, d := range Analyse(content, cat) {
		if d.Code == code && (len(wanted) == 0 || wanted[d.Symbol]) {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return out
	}

	var edits []edit
	switch code {
	case CodeRowAccess:
		for _, d := range targets {
			if e, ok := notAccessibleEdit(content, d); ok {
				edits = append(edits, e)
				out.Fixed = append(out.Fixed, d.Symbol)
			}
		}
	case CodeUnusedImport:
		unused := map[string]bool{}
		for _, d := range targets {
			unused[d.Symbol] = true
		}
		var removed []string
		edits, removed = removeImports(content, unused)
		out.Fixed = append(out.Fixed, removed...)
	}
	if len(edits) == 0 {
		return out
	}
	sort.Strings(out.Fixed)
	out.Content = applyEdits(content, edits)
	return out
}

var tokenRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]*|::=|[;,{}]`)

type token struct {
	text       string
	start, end int
}

// tokensFrom is every token of content from offset on, outside comments and
// strings — the way the SMI lexer would read them.
func tokensFrom(content string, offset int) []token {
	var out []token
	codeSpans(content, false, func(from, to int) {
		if to <= offset {
			return
		}
		if from < offset {
			from = offset
		}
		for _, m := range tokenRe.FindAllStringIndex(content[from:to], -1) {
			out = append(out, token{content[from+m[0] : from+m[1]], from + m[0], from + m[1]})
		}
	}, nil)
	return out
}

// notAccessibleEdit gives the object the row-access finding is about the
// access RFC 2578 requires. The finding sits on the object's SYNTAX keyword;
// its ACCESS or MAX-ACCESS clause follows before the object's `::=`.
func notAccessibleEdit(content string, d Diagnostic) (edit, bool) {
	at := offsetOf(content, d.Line, d.Column)
	if at < 0 {
		return edit{}, false
	}
	toks := tokensFrom(content, at)
	for i, t := range toks {
		if t.text == "::=" {
			break
		}
		if (t.text == "MAX-ACCESS" || t.text == "ACCESS") && i+1 < len(toks) {
			v := toks[i+1]
			if v.text == "not-accessible" {
				return edit{}, false
			}
			return edit{Offset: v.start, Length: v.end - v.start, Text: "not-accessible"}, true
		}
	}
	return edit{}, false
}

// importGroup is one `a, b FROM M` of an IMPORTS clause.
type importGroup struct {
	names  []token
	module token
}

// removeImports takes the names in unused out of the IMPORTS clause, and a
// whole `… FROM M` when nothing of it is left, and the clause itself when
// nothing at all is: an empty `FROM M` and an empty `IMPORTS ;` do not parse.
func removeImports(content string, unused map[string]bool) ([]edit, []string) {
	toks := tokensFrom(content, 0)
	start := -1
	for i, t := range toks {
		if t.text == "IMPORTS" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, nil
	}
	var groups []importGroup
	var cur importGroup
	end := -1
	for i := start + 1; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.text == ";":
			end = i
		case t.text == ",":
			continue
		case t.text == "FROM" && i+1 < len(toks):
			cur.module = toks[i+1]
			groups = append(groups, cur)
			cur = importGroup{}
			i++
			continue
		default:
			cur.names = append(cur.names, t)
			continue
		}
		break
	}
	if end < 0 || len(groups) == 0 {
		return nil, nil
	}

	var edits []edit
	var removed []string
	cut := func(from, to int) { edits = append(edits, edit{Offset: from, Length: to - from}) }
	kept := 0
	gone := make([]bool, len(groups))
	for g, grp := range groups {
		left := 0
		for _, n := range grp.names {
			if unused[n.text] {
				removed = append(removed, n.text)
			} else {
				left++
			}
		}
		kept += left
		gone[g] = left == 0
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if kept == 0 {
		// Nothing imported at all: the clause goes, with the line it stood on.
		from, to := toks[start].start, toks[end].end
		for to < len(content) && (content[to] == ' ' || content[to] == '\t') {
			to++
		}
		if to < len(content) && content[to] == '\n' {
			to++
		}
		cut(from, to)
		return edits, removed
	}

	for g, grp := range groups {
		if len(grp.names) == 0 {
			continue
		}
		if gone[g] {
			// A whole group: from the end of the group before it, so its line
			// goes with it — or, for the first, up to the group after it.
			if g > 0 {
				cut(groups[g-1].module.end, grp.module.end)
			} else {
				next := g + 1
				for next < len(groups) && gone[next] {
					next++
				}
				cut(grp.names[0].start, groups[next].names[0].start)
				// The groups skipped over are inside that cut.
				for k := g + 1; k < next; k++ {
					gone[k] = false
					groups[k].names = nil
				}
			}
			continue
		}
		// Runs of names inside a group that keeps some: a run followed by a
		// name goes with the comma after it, a run at the end with the comma
		// before it.
		names := grp.names
		for i := 0; i < len(names); i++ {
			if !unused[names[i].text] {
				continue
			}
			j := i
			for j+1 < len(names) && unused[names[j+1].text] {
				j++
			}
			if j+1 < len(names) {
				cut(names[i].start, names[j+1].start)
			} else {
				cut(names[i-1].end, names[j].end)
			}
			i = j
		}
	}
	return edits, removed
}
