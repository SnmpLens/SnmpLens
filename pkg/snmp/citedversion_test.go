package snmp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This repository STATES gosnmp's behaviour rather than assuming it: that the
// trap loop is strictly serial, that the USM table validates nothing, that
// SafeString prints the community despite its name. Each of those was read out
// of a particular version's source, and each is cited with that version --
// which is what makes the claim checkable by the next reader.
//
// It is also what makes it rot. Two bumps carried gosnmp from v1.43.2 to
// v1.45.0 and all five citations stayed where they were, naming a version
// nothing had built against for months, with nothing failing anywhere: a bump
// touches go.mod and go.sum and no prose at all. A cited observation that has
// silently become a guess is worse than no citation, because it reads as
// evidence.
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

	// The two shapes a citation takes: the version after the name in prose, or
	// after an @ in a module path. Written as a pattern rather than spelled out,
	// because an example here would be a citation this very test then reads.
	cite := regexp.MustCompile(`gosnmp[ @](v[0-9]+\.[0-9]+\.[0-9]+)`)

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
		for i, line := range strings.Split(string(body), "\n") {
			for _, m := range cite.FindAllStringSubmatch(line, -1) {
				found++
				if m[1] != want {
					t.Errorf("%s:%d cites gosnmp %s, but go.mod builds against %s.\n"+
						"Re-read the cited source in the module cache and confirm the claim still\n"+
						"holds, THEN move the number -- the citation is evidence, not decoration:\n  %s",
						filepath.ToSlash(path), i+1, m[1], want, strings.TrimSpace(line))
				}
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
