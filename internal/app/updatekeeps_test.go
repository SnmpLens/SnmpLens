package app

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// What an update must not change, or it opens on an empty application with the
// person's settings still on disk: see "Wails 3 and what an update must keep" in
// CLAUDE.md. Each of these is a value nothing else depends on, so moving one breaks
// no build and no other test, and shows only on the machines of people who updated.

func TestThePageKeepsTheOriginItsStorageBelongsTo(t *testing.T) {
	t.Setenv("FRONTEND_DEVSERVER_URL", "")
	want := "wails://wails/"
	if goruntime.GOOS == "windows" {
		want = "" // Wails's default there is http://wails.localhost/, as it always was
	}
	if got := startURL(); got != want {
		t.Errorf("startURL() = %q, want %q", got, want)
	}

	t.Setenv("FRONTEND_DEVSERVER_URL", "http://localhost:9245")
	if got := startURL(); got != "" {
		t.Errorf("under a development server startURL() = %q: the server's own address must be used", got)
	}
}

func TestTheMacBundleKeepsTheIdentifierItsStorageIsUnder(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot, "build", "darwin", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<string>com.wails.snmplens</string>") {
		t.Error("Info.plist no longer names com.wails.snmplens, where WebKit keeps the settings of every installed copy")
	}
}

func TestLinuxBuildsOnTheLibrariesInstalledCopiesHave(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot, "tools", "build.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "platform === 'linux' ? ['gtk3']") {
		t.Error("tools/build.mjs no longer builds Linux with -tags gtk3: the updater would replace a binary with one the machine cannot start")
	}

	release, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(release), "Depends: libgtk-3-0, libwebkit2gtk-4.1-0") {
		t.Error("the .deb no longer depends on GTK 3 and WebKitGTK 4.1, which is what the binary links")
	}
}
