package snmp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Some of what this repository states about gosnmp cannot be asserted against
// gosnmp: that the receive loop is a single goroutine, that listenUDP
// dereferences a failed type assertion anyway. Those are read out of a
// particular version's source and cited with it, which is what makes the claim
// checkable by the next reader. Everything that CAN be asserted is, in
// gosnmpcontract_test.go, and carries no version at all.
//
// A citation is also what rots. Two bumps carried gosnmp from v1.43.2 to
// v1.45.0 and every citation stayed where it was, naming a version nothing had
// built against for months, with nothing failing anywhere: a bump touches
// go.mod and go.sum and no prose at all. An observation that has silently
// become a guess is worse than no observation, because it reads as evidence.
//
// So the citations are pinned to the version we build against. A gosnmp
// upgrade fails HERE, which is the moment to re-read the cited source and
// confirm each claim still holds before moving the number -- the same guard
// trapbuf.go has for gosnmp's unexported socket field, applied to the prose
// that explains why that guard exists.

// The cited files sit all over the tree -- CLAUDE.md, pkg/snmp, the screenshot
// spec -- so the check walks it rather than keeping a list a new citation
// would escape.
const citedRepoRoot = "../.."

// Directories a citation cannot sit in and a walk should not pay for:
// dependencies, build output, and the committed demo bundle.
var citedSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"demo":         true,
	"bin":          true,
}

// Files that carry the version as DATA rather than as a citation. go.sum in
// particular may legitimately name versions the build does not use.
var citedSkipFiles = map[string]bool{
	"go.mod": true,
	"go.sum": true,
}

var citedExtensions = map[string]bool{
	".go":     true,
	".md":     true,
	".json":   true,
	".js":     true,
	".mjs":    true,
	".svelte": true,
	".html":   true,
	".yml":    true,
	".yaml":   true,
}

func TestTheCitedGosnmpVersionIsTheOneWeBuildAgainst(t *testing.T) {
	want := gosnmpVersionFromGoMod(t)

	found := 0
	err := filepath.WalkDir(citedRepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if citedSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if citedSkipFiles[d.Name()] || !citedExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, c := range citationsIn(string(body)) {
			found++
			if c.version != want {
				t.Errorf("%s:%d cites gosnmp %s, but go.mod builds against %s.\n"+
					"Re-read the cited source in the module cache and confirm the claim still\n"+
					"holds, THEN move the number -- the citation is evidence, not decoration:\n  %s",
					filepath.ToSlash(path), c.line, c.version, want, c.excerpt)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	// A walk that matched nothing would pass forever. The citations are the
	// point of the test, so their absence is a failure of the test itself.
	if found == 0 {
		t.Fatal("found no gosnmp version citation anywhere -- the pattern or the walk is broken")
	}
	t.Logf("%d citations, all naming %s", found, want)
}

type citation struct {
	version string
	line    int
	excerpt string
}

// A version number, and only where the text right before it names gosnmp.
//
// Matching line by line is what the obvious version does, and it misses the
// citation that matters most: prose wraps, so CLAUDE.md carries "verified in
// gosnmp" at the end of one line and "v1.45.0 `trap.go`" at the start of the
// next, and a wrapped Go comment puts a "//" in between. The gap is therefore
// allowed to hold whitespace, comment markers and an @ -- and nothing else, so
// a version merely standing near the word is not taken for a citation of it.
var (
	versionNumber = regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`)
	citationGap   = regexp.MustCompile(`^[\s@/*]*$`)
)

func citationsIn(body string) []citation {
	var out []citation
	for _, m := range versionNumber.FindAllStringIndex(body, -1) {
		from := m[0] - 24
		if from < 0 {
			from = 0
		}
		before := body[from:m[0]]
		at := strings.LastIndex(before, "gosnmp")
		if at < 0 || !citationGap.MatchString(before[at+len("gosnmp"):]) {
			continue
		}
		out = append(out, citation{
			version: body[m[0]:m[1]],
			line:    1 + strings.Count(body[:m[0]], "\n"),
			excerpt: strings.TrimSpace(lineAt(body, m[0])),
		})
	}
	return out
}

// lineAt is the whole line the offset falls on, so a failure quotes a sentence
// rather than the fragment a wrap happened to leave on that line.
func lineAt(body string, offset int) string {
	from := strings.LastIndex(body[:offset], "\n") + 1
	to := strings.Index(body[offset:], "\n")
	if to < 0 {
		return body[from:]
	}
	return body[from : offset+to]
}

func gosnmpVersionFromGoMod(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(citedRepoRoot, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	m := regexp.MustCompile(`github\.com/gosnmp/gosnmp (v[0-9]+\.[0-9]+\.[0-9]+)`).FindSubmatch(body)
	if m == nil {
		t.Fatal("go.mod does not require github.com/gosnmp/gosnmp")
	}
	return string(m[1])
}
