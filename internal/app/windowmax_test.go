package app

// windowmax_linux.go works around what one version of Wails does with a limit
// of 0, read out of that version's window.c, and the reading is only as good as
// the version it names. A bump of Wails touches go.mod and no prose, so the
// citation is pinned to what go.mod requires: an upgrade fails HERE, which is
// the moment to re-read SetMinMaxSize — and to drop the workaround if 0 no
// longer becomes the size of a monitor — before moving the number.

import (
	"os"
	"regexp"
	"testing"
)

func TestTheWindowMaxSizeWorkaroundCitesTheWailsGoModRequires(t *testing.T) {
	want := wailsVersionFromGoMod(t)

	body, err := os.ReadFile("windowmax_linux.go")
	if err != nil {
		t.Fatalf("reading windowmax_linux.go: %v", err)
	}
	cited := regexp.MustCompile(`Wails (v[0-9]+\.[0-9]+\.[0-9]+)`).FindAllSubmatch(body, -1)
	if len(cited) == 0 {
		t.Fatal("windowmax_linux.go cites no Wails version -- the pattern or the comment is broken")
	}
	for _, m := range cited {
		if string(m[1]) != want {
			t.Errorf("windowmax_linux.go was checked against Wails %s, but go.mod builds against %s.\n"+
				"  Re-read SetMinMaxSize in internal/frontend/desktop/linux/window.c: if a limit of 0\n"+
				"  no longer becomes the size of a monitor, the workaround can go.", m[1], want)
		}
	}
}
