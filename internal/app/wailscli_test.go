package app

// The Wails CLI and the Wails library are two halves of one version, and only
// one of them is where Dependabot looks.
//
// The library is a requirement in go.mod. The CLI is a `go install
// .../cmd/wails3@vX` inside a `run:` step of ci.yml and release.yml, which
// Dependabot does not read — the blind spot staticcheck's pin sits in too. It
// costs more here: the CLI writes frontend/wailsjs/runtime, the JavaScript half
// of the bridge whose Go half is the library, so a release built across the gap
// ships two versions of one protocol. And nothing says so. On a mismatch
// `wails build` prints "go.mod is using Wails 'v2.17.0' but the CLI is
// 'v2.16.0'" as a WARNING and builds anyway, in a log nobody reads. Dependabot
// raised the 2.17.0 library — a security release — with every workflow still
// installing 2.16.0.
//
// So every install of the CLI is pinned to what go.mod requires, and a bump of
// either fails here until the other follows. The npm package is a third half, and
// the one Dependabot moves on its own day: frontend/package.json pins it to the
// same version, an exact one, since the JavaScript it holds speaks to the library.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEveryWorkflowInstallsTheWailsCLIGoModRequires(t *testing.T) {
	want := wailsVersionFromGoMod(t)

	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	install := regexp.MustCompile(`github\.com/wailsapp/wails/v3/cmd/wails3@(\S+)`)
	found := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, m := range install.FindAllStringSubmatch(line, -1) {
				found++
				if m[1] != want {
					t.Errorf("%s:%d installs the Wails CLI at %s, but go.mod builds against %s.\n"+
						"  The CLI generates the runtime the library answers: move both in one commit.",
						e.Name(), i+1, m[1], want)
				}
			}
		}
	}

	// A test that finds nothing to check passes on nothing.
	if found == 0 {
		t.Fatal("no workflow installs the Wails CLI — the pattern or the path is wrong")
	}
}

func TestTheWailsRuntimePackageIsTheVersionGoModRequires(t *testing.T) {
	want := strings.TrimPrefix(wailsVersionFromGoMod(t), "v")

	body, err := os.ReadFile(filepath.Join(repoRoot, "frontend", "package.json"))
	if err != nil {
		t.Fatalf("reading package.json: %v", err)
	}
	m := regexp.MustCompile(`"@wailsio/runtime":\s*"([^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatal("frontend/package.json does not depend on @wailsio/runtime")
	}
	if string(m[1]) != want {
		t.Errorf("frontend/package.json pins @wailsio/runtime at %q, but go.mod builds against %s: move both in one commit",
			m[1], want)
	}
}

func wailsVersionFromGoMod(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	m := regexp.MustCompile(`github\.com/wailsapp/wails/v3 (v[0-9]+\.[0-9]+\.[0-9]+)`).FindSubmatch(body)
	if m == nil {
		t.Fatal("go.mod does not require github.com/wailsapp/wails/v3")
	}
	return string(m[1])
}
