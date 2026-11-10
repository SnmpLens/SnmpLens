package app

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"SnmpLens/internal/sysvc"
)

// The installer is a script nothing compiles until a release, and what it says
// to the executable is a command line nothing else checks: every mistake here
// is found by somebody installing.
func TestTheInstallerSpeaksToTheServiceItInstalls(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "build", "windows", "installer", "project.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	// Without the byte order mark makensis reads the script in the ANSI code
	// page, and every accented string of the localised pages comes out garbled.
	if !bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
		t.Error("project.nsi lost its UTF-8 byte order mark")
	}
	script := string(raw)

	if !strings.Contains(script, `!define SERVICE_NAME "`+sysvc.Name+`"`) {
		t.Errorf("the installer does not name the service %q", sysvc.Name)
	}

	// Every flag the installer passes to `service` is one the command line reads.
	cli, err := os.ReadFile(filepath.Join(repoRoot, "internal", "cli", "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	calls := regexp.MustCompile(`PRODUCT_EXECUTABLE\}" service ([a-z]+)([^']*)'`).FindAllStringSubmatch(script, -1)
	if len(calls) == 0 {
		t.Fatal("the installer never calls the executable's service commands")
	}
	for _, c := range calls {
		if !strings.Contains(string(cli), `case "`+c[1]+`":`) {
			t.Errorf("the installer runs `service %s`, which the command line does not know", c[1])
		}
		for _, flag := range regexp.MustCompile(`--([a-z-]+)`).FindAllStringSubmatch(c[2], -1) {
			if !strings.Contains(string(cli), `"`+flag[1]+`"`) {
				t.Errorf("the installer passes --%s, which the command line does not read", flag[1])
			}
		}
	}

	// The release defines the version for makensis rather than rewriting the
	// script, which is how the byte order mark was lost.
	release, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(release), "/DINFO_PRODUCTVERSION=") {
		t.Error("the release no longer defines the installer's version for makensis")
	}
	if strings.Contains(string(release), "Set-Content build\\windows\\installer\\project.nsi") {
		t.Error("the release rewrites the installer script, which drops its byte order mark")
	}
}
