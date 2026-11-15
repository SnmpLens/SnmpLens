//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEachLinuxInstallIsSentToItsOwnFormat(t *testing.T) {
	cases := []struct {
		name      string
		exe       string
		sandboxed bool
		dpkgOwned bool
		asset     string
		mode      applyMode
	}{
		{"portable binary", "/home/u/Apps/SnmpLens", false, false, linuxBinaryAsset, applyReplace},
		{"the .deb", packageInstallPath, false, true, linuxDebAsset, applyBrowser},
		{"the .rpm", packageInstallPath, false, false, linuxRPMAsset, applyBrowser},
		// A portable copy on a Debian system is still a portable copy.
		{"portable binary beside the .deb", "/opt/SnmpLens", false, true, linuxBinaryAsset, applyReplace},
		// Inside the sandbox the path is /app/bin, read-only, and whatever the
		// host's dpkg says is about the host.
		{"flatpak", "/app/bin/snmplens", true, true, linuxFlatpakAsset, applyBrowser},
		{"executable unknown", "", false, false, linuxBinaryAsset, applyReplace},
	}
	for _, c := range cases {
		asset, mode := targetFor(c.exe, c.sandboxed, c.dpkgOwned)
		if asset != c.asset || mode != c.mode {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", c.name, asset, mode, c.asset, c.mode)
		}
	}
}

// An asset the updater asks for and the release does not carry is a failure
// that only shows on the first update after the release that dropped it: from a
// version already in users' hands, with nothing anyone can change on their side.
func TestEveryLinuxAssetTheUpdaterAsksForIsPublished(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, published, found := strings.Cut(string(body), "name: Create GitHub Release")
	if !found {
		t.Fatal("release.yml has no \"Create GitHub Release\" step -- the test cannot find what is published")
	}
	for _, asset := range []string{linuxBinaryAsset, linuxDebAsset, linuxRPMAsset, linuxFlatpakAsset} {
		if !strings.Contains(published, "artifacts/"+asset+"\n") {
			t.Errorf("the updater asks for %s, and release.yml does not publish it", asset)
		}
	}
}
