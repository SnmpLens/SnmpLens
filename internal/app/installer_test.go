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

// The installer speaks the user's language, all of it, and remembers it: the
// language is asked for, handed to the elevated copy it relaunches, and every
// text its own pages show is defined in the five languages the application
// speaks. Its pages used to be written in English beside localised MUI pages.
func TestTheInstallerSpeaksTheUsersLanguage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "build", "windows", "installer", "project.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, want := range []string{"MUI_LANGDLL_DISPLAY", `"/LANG="`, "/LANG=$LANGUAGE", "MUI_UNGETLANGUAGE"} {
		if !strings.Contains(script, want) {
			t.Errorf("the installer lacks %s", want)
		}
	}
	langs := []string{"LANG_ENGLISH", "LANG_FRENCH", "LANG_GERMAN", "LANG_SPANISH", "LANG_SIMPCHINESE"}
	defined := map[string]map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^LangString (\w+) \$\{(\w+)\}`).FindAllStringSubmatch(script, -1) {
		if defined[m[1]] == nil {
			defined[m[1]] = map[string]bool{}
		}
		defined[m[1]][m[2]] = true
	}
	for name, in := range defined {
		for _, l := range langs {
			if !in[l] {
				t.Errorf("%s is not defined in %s", name, l)
			}
		}
	}
	for _, m := range regexp.MustCompile(`\$\((\w+)\)`).FindAllStringSubmatch(script, -1) {
		if defined[m[1]] == nil {
			t.Errorf("$(%s) is used and defined in no language", m[1])
		}
	}
	// Every text a page of its own shows is a LangString.
	shown := regexp.MustCompile(`NSD_Create(?:Label|CheckBox|RadioButton|GroupBox)\} [^"]*"([^"]*)"`).FindAllStringSubmatch(script, -1)
	if len(shown) < 10 {
		t.Fatalf("found %d texts on the installer's own pages; the pattern no longer reads them", len(shown))
	}
	for _, m := range shown {
		if !strings.HasPrefix(m[1], "$(") {
			t.Errorf("a page shows %q, which no language translates", m[1])
		}
	}
}

// What the installer writes in the registry is where the application reads it:
// the all-users entry in the 64-bit view, which the 64-bit application sees,
// and start at sign-in as the current user's quoted entry, which Settings >
// Background shows and removes — never a machine-wide one it cannot see.
func TestTheInstallerWritesWhereTheApplicationReads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "build", "windows", "installer", "project.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, "SetRegView 64") {
		t.Error("the installer writes its all-users entry in the 32-bit view")
	}
	if strings.Contains(script, `WriteRegStr HKLM "${APP_RUN_KEY}"`) {
		t.Error("the installer writes a machine-wide start at sign-in")
	}
	if !strings.Contains(script, `WriteRegStr HKCU "${APP_RUN_KEY}" "${INFO_PRODUCTNAME}" '"$INSTDIR\${PRODUCT_EXECUTABLE}"'`) {
		t.Error("start at sign-in is not the quoted path the application writes")
	}
}

// The finish page starts the application. An all-users install runs elevated,
// and the window must not: Explorer starts it as the signed-in user.
func TestTheInstallerLaunchesTheApplicationUnelevated(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "build", "windows", "installer", "project.nsi"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(script, "!define MUI_FINISHPAGE_RUN_FUNCTION LaunchApp") {
		t.Fatal("the finish page starts the application itself, elevated after an all-users install")
	}
	i := strings.Index(script, "Function LaunchApp")
	if i < 0 {
		t.Fatal("no LaunchApp function")
	}
	body := script[i:]
	body = body[:strings.Index(body, "FunctionEnd")]
	if !strings.Contains(body, `$InstallMode == "admin"`) ||
		!strings.Contains(body, `Exec '"$WINDIR\explorer.exe" "$INSTDIR\${PRODUCT_EXECUTABLE}"'`) {
		t.Errorf("an all-users install does not start the application through Explorer:\n%s", body)
	}
}
