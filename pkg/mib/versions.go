package mib

import (
	"fmt"
	"strings"
	"time"
)

// Several texts declaring one module, and which one to keep.
//
// gosmi holds ONE module per name, and two files declaring the same one used
// to load one after the other: the second silently replaced the first, so which
// definition the tree showed depended on how the files sorted. An archive can
// carry one folder per release of a module, with the same file name in each,
// and an import of it used to keep whichever was written last.
//
// The rule, in order: a text that parses beats one that does not, since a
// module that cannot load answers nothing; then the newest by LAST-UPDATED or
// latest REVISION; then the caller's own order, which is the tie-break it knows
// best (the shallower folder for an import, the file named after the module
// for a directory). LAST-UPDATED is left unchanged between releases often
// enough that the tie is common, which is why the callers' order matters:
// sorted with NaturalLess, folders "1.9" and "1.10" put the later one first.

// Candidate is one text declaring a module.
type Candidate struct {
	Name    string
	Content string
}

// candidateInfo is what the rule looks at.
type candidateInfo struct {
	parses bool
	date   time.Time
}

func inspectCandidate(content string) candidateInfo {
	p := prepare(content)
	info := candidateInfo{parses: p.err == nil}
	if p.module != nil {
		info.date = revisionOf(p)
	}
	return info
}

// revisionOf is the most recent date a module states about itself.
func revisionOf(p prepared) time.Time {
	var latest time.Time
	if p.module == nil || p.module.Body.Identity == nil {
		return latest
	}
	id := p.module.Body.Identity
	latest = id.LastUpdated.ToTime()
	for _, r := range id.Revisions {
		if t := r.Date.ToTime(); t.After(latest) {
			latest = t
		}
	}
	return latest
}

// Why a version was not the one kept.
const (
	LostIdentical  = "identical"  // the same text as the one kept
	LostUnparsable = "unparsable" // it does not parse, and the one kept does
	LostOlder      = "older"      // the one kept states a later date
	LostUndecided  = "undecided"  // nothing in the files decides; the caller's order did
)

// Lost says why one version was not kept: a code for an interface, and a
// sentence finishing "… was kept instead because" for a log or a load result.
type Lost struct {
	Code string
	Why  string
}

// Preferred picks which candidate to keep, and says for every other one why it
// was not. The entry for the one kept is empty.
func Preferred(cands []Candidate) (int, []Lost) {
	if len(cands) == 0 {
		return -1, nil
	}
	reasons := make([]Lost, len(cands))
	identical := true
	for _, c := range cands[1:] {
		identical = identical && c.Content == cands[0].Content
	}
	if identical {
		for i := 1; i < len(cands); i++ {
			reasons[i] = Lost{LostIdentical, "the files are identical"}
		}
		return 0, reasons
	}

	infos := make([]candidateInfo, len(cands))
	for i, c := range cands {
		infos[i] = inspectCandidate(c.Content)
	}

	best := 0
	for i := 1; i < len(cands); i++ {
		a, b := infos[i], infos[best]
		switch {
		case a.parses != b.parses:
			if a.parses {
				best = i
			}
		case a.date.After(b.date):
			best = i
		}
	}

	won := infos[best]
	date := won.date.Format("2006-01-02")
	for i, info := range infos {
		switch {
		case i == best:
		case cands[i].Content == cands[best].Content:
			reasons[i] = Lost{LostIdentical, "the files are identical"}
		case won.parses && !info.parses:
			reasons[i] = Lost{LostUnparsable, "this one does not parse"}
		case won.date.After(info.date):
			reasons[i] = Lost{LostOlder, fmt.Sprintf("it is newer (last updated %s)", date)}
		default:
			// Not decided by anything in the files.
			reasons[i] = Lost{LostUndecided, "nothing in the files says which is newer, and it comes first"}
		}
	}
	return best, reasons
}

// NaturalLess orders names with their numbers compared as numbers, so a
// folder "1.10" sorts after "1.9" rather than before it.
func NaturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na := strings.TrimLeft(a[:da], "0")
			nb := strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// RevisionDate is the most recent date a module's text states about itself:
// its LAST-UPDATED or latest REVISION. Zero for an SMIv1 module, which states
// none, or a text that does not parse far enough to say.
func RevisionDate(content string) time.Time {
	return revisionOf(prepare(content))
}
