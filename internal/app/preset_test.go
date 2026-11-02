package app

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"SnmpLens/pkg/preset"
	"SnmpLens/pkg/snmp"
)

func newPresetApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	mibDir := filepath.Join(root, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{persistentMibDir: mibDir}
	dir, err := a.presetDir()
	if err != nil {
		t.Fatal(err)
	}
	return a, dir
}

const goodPreset = `{
  "formatVersion": 1,
  "name": "Catalyst uplinks",
  "author": "someone",
  "intervalSec": 30,
  "match": {"sysObjectIdPrefix": ["1.3.6.1.4.1.9"], "vendor": "Cisco"},
  "widgets": [
    {"kind": "value", "title": "Uptime", "oids": ["1.3.6.1.2.1.1.3.0"]},
    {"kind": "chart", "title": "Traffic", "oids": ["1.3.6.1.2.1.2.2.1.10.1", "1.3.6.1.2.1.2.2.1.16.1"]}
  ]
}`

func writeSrc(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// monitoring.db, service.json and the secret store all sit one directory above
// the preset library, and every method here takes a file name from outside.
func TestAPresetPathCannotEscapeTheDirectory(t *testing.T) {
	a, dir := newPresetApp(t)

	for _, name := range []string{
		"../monitoring.db", "..\\service.json", "../../etc/passwd",
		"", ".", "..", "sub/other.json", "/etc/passwd",
	} {
		full, err := a.resolvePresetPath(name)
		if err != nil {
			continue // refused outright, which is the other acceptable answer
		}
		if filepath.Dir(full) != filepath.Clean(dir) {
			t.Errorf("%q resolved to %q, outside the preset directory", name, full)
		}
	}

	// And the ordinary case still works.
	full, err := a.resolvePresetPath("cisco.json")
	if err != nil || filepath.Base(full) != "cisco.json" {
		t.Errorf("a plain name did not resolve: %q %v", full, err)
	}
}

// ImportPresetFiles reads any absolute path the renderer names and ReadPreset
// hands back the content of anything in the destination. What bounds that pair
// is what is allowed to land — and the gate is positive: JSON, with a
// formatVersion.
func TestImportRefusesWhatIsNotAPreset(t *testing.T) {
	a, dir := newPresetApp(t)
	src := t.TempDir()

	cases := []struct {
		name    string
		content string
		says    string
	}{
		{"id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n-----END OPENSSH PRIVATE KEY-----\n", ""},
		{"passwords.txt", "admin:hunter2\nroot:correcthorse\n", ""},
		{"spec.pdf", "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n", "PDF"},
		{"presets.zip", "PK\x03\x04\x14\x00\x00\x00", "zip"},
		{"page.json", "<!DOCTYPE html>\n<html><body>404</body></html>", "HTML"},
		{"empty.json", "", ""},
		// JSON, and still not a preset: nothing says what format it is.
		{"settings.json", "{\"theme\": \"dark\", \"targets\": [\"10.0.0.1\"]}", "formatVersion"},
		{"array.json", "[1, 2, 3]", ""},
	}

	for _, c := range cases {
		res := a.importSinglePreset(writeSrc(t, src, c.name, c.content))
		if res.Success {
			t.Errorf("%s was imported; it is now readable through ReadPreset", c.name)
			continue
		}
		if c.says != "" && !strings.Contains(res.Error, c.says) {
			t.Errorf("%s: the refusal does not say what the file is: %q", c.name, res.Error)
		}
		if res.Error == "" {
			t.Errorf("%s: refused with no reason at all", c.name)
		}
		if _, err := os.Stat(filepath.Join(dir, c.name)); err == nil {
			t.Errorf("%s was copied in despite the refusal", c.name)
		}
	}

	// Nothing at all was left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the library holds %d file(s) after eight refusals", len(entries))
	}
}

// The gate must not become a validity check. A preset with a bad OID is exactly
// what the error list exists for, and you cannot fix a file you were not
// allowed to keep.
func TestABrokenPresetStillImportsWithItsProblemCount(t *testing.T) {
	a, dir := newPresetApp(t)
	src := t.TempDir()

	broken := `{"formatVersion": 1, "name": "", "intervalSec": 0,
	            "widgets": [{"kind": "iframe", "title": "x", "oids": ["sysUpTime.0"]}]}`
	res := a.importSinglePreset(writeSrc(t, src, "broken.json", broken))
	if !res.Success {
		t.Fatalf("a preset with problems was refused: %q", res.Error)
	}
	if res.Problems == 0 {
		t.Error("it imported reporting no problems, so nothing tells the author to look")
	}
	if _, err := os.Stat(filepath.Join(dir, "broken.json")); err != nil {
		t.Error("it reported success and wrote nothing")
	}

	// And it is listed, with its count, rather than hidden.
	list := a.ListPresets()
	if len(list) != 1 || list[0].Problems == 0 {
		t.Errorf("the library lists %+v", list)
	}
}

// An identical file is a no-op, and says so instead of reporting a fresh import
// on every drop of the same folder.
func TestImportingTheSameFileTwiceIsSkipped(t *testing.T) {
	a, _ := newPresetApp(t)
	src := writeSrc(t, t.TempDir(), "cisco.json", goodPreset)

	if res := a.importSinglePreset(src); !res.Success || res.Skipped {
		t.Fatalf("first import: %+v", res)
	}
	res := a.importSinglePreset(src)
	if !res.Success || !res.Skipped {
		t.Errorf("second import of an identical file: %+v", res)
	}
	if list := a.ListPresets(); len(list) != 1 {
		t.Errorf("%d entries after importing the same file twice", len(list))
	}
}

// Validate returns nil on success — it builds with append — and nil crosses the
// bridge as null, which throws on the first .map. On the VALID path, which is
// the one nobody exercises by hand.
func TestAValidPresetCrossesWithAnEmptyErrorListNotNull(t *testing.T) {
	a, _ := newPresetApp(t)
	a.importSinglePreset(writeSrc(t, t.TempDir(), "cisco.json", goodPreset))

	detail, err := a.ReadPreset("cisco.json")
	if err != nil {
		t.Fatalf("ReadPreset: %v", err)
	}
	if len(detail.Errors) != 0 {
		t.Fatalf("a valid preset reported %+v", detail.Errors)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"errors":null`) {
		t.Errorf("errors crossed as null: %s", raw)
	}
}

// The request count is the one number pkg/preset deliberately does not compute,
// because it depends on the transport's chunk size. It rounds UP: thirty-one
// OIDs is two requests, not one and a bit.
func TestTheRequestCountRoundsUpAtTheChunkSize(t *testing.T) {
	build := func(n int) preset.Preset {
		p := preset.Preset{FormatVersion: preset.FormatVersion, Name: "x", IntervalSec: 60}
		for i := 1; i <= n; i++ {
			p.Widgets = append(p.Widgets, preset.Widget{
				Kind: preset.KindValue, Title: "t",
				OIDs: []string{"1.3.6.1.2.1.2.2.1.10." + strconv.Itoa(i)},
			})
		}
		return p
	}
	chunk := snmp.MaxVarbindsPerGet
	perDay := 86400 / 60

	for _, tc := range []struct{ oids, rounds int }{
		{1, 1}, {chunk, 1}, {chunk + 1, 2}, {2 * chunk, 2}, {2*chunk + 1, 3},
	} {
		got := presetCost(build(tc.oids))
		if want := perDay * tc.rounds; got.RequestsPerDay != want {
			t.Errorf("%d OIDs: RequestsPerDay = %d, want %d (%d request(s) a round)",
				tc.oids, got.RequestsPerDay, want, tc.rounds)
		}
		if got.VarbindsPerDay != perDay*tc.oids {
			t.Errorf("%d OIDs: VarbindsPerDay = %d", tc.oids, got.VarbindsPerDay)
		}
	}

	// A preset that polls nothing costs nothing, and does not divide by zero.
	if c := presetCost(preset.Preset{}); c.RequestsPerDay != 0 || c.VarbindsPerDay != 0 {
		t.Errorf("an empty preset costs %+v", c)
	}
}

// The vocabulary the interface is served is the vocabulary Go enforces, and it
// arrives as i18n keys rather than English.
func TestTheServedVocabularyCrossesAsKeys(t *testing.T) {
	a, _ := newPresetApp(t)
	kinds := a.ListPresetWidgetKinds()
	if len(kinds) == 0 {
		t.Fatal("no widget kinds are served")
	}
	for _, k := range kinds {
		if !strings.HasPrefix(k.Description, "preset.widget.") {
			t.Errorf("%s: description %q is not an i18n key", k.Kind, k.Description)
		}
	}
}

// Deleting a preset is deleting a file you started from, never a monitoring.
func TestDeletingAPresetRemovesOnlyTheFile(t *testing.T) {
	a, dir := newPresetApp(t)
	a.importSinglePreset(writeSrc(t, t.TempDir(), "cisco.json", goodPreset))

	if err := a.DeletePreset("cisco.json"); err != nil {
		t.Fatalf("DeletePreset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cisco.json")); err == nil {
		t.Error("the file is still there")
	}
	if err := a.DeletePreset("../monitoring.db"); err == nil {
		t.Error("DeletePreset accepted a path outside the library")
	}
}

// An empty library is empty, not broken — and it is the state the feature ships
// in. It must never cross as null.
func TestAnEmptyLibraryListsAsAnArray(t *testing.T) {
	a, _ := newPresetApp(t)
	list := a.ListPresets()
	if list == nil {
		t.Fatal("ListPresets returned nil, which crosses as null")
	}
	if len(list) != 0 {
		t.Errorf("%d entries in a fresh library", len(list))
	}
	raw, _ := json.Marshal(list)
	if string(raw) != "[]" {
		t.Errorf("an empty library crossed as %s", raw)
	}
}

// Nothing in this application read sysObjectID before, so preset.Match had no
// data source at all: the rule was written and never wired. This is the wiring,
// and the part worth testing is the counting rather than the round trip.
func TestRankingTheLibraryForOneDevice(t *testing.T) {
	list := []preset.Info{
		{File: "zzz-generic.json", Name: "Generic interfaces"},
		{File: "cisco.json", Name: "Cisco", SysObjectIDPrefix: []string{"1.3.6.1.4.1.9"}},
		{File: "cat9k.json", Name: "Catalyst 9300", SysObjectIDPrefix: []string{"1.3.6.1.4.1.9.1.2494"}},
		{File: "juniper.json", Name: "Juniper", SysObjectIDPrefix: []string{"1.3.6.1.4.1.2636"}},
	}

	ranked, matched := rankForDevice(list, ".1.3.6.1.4.1.9.1.2494")
	if matched != 2 {
		t.Errorf("matched = %d, want 2 (the model and the vendor)", matched)
	}
	if len(ranked) != len(list) {
		t.Fatalf("ranking dropped %d entries; Match is advice, not a filter", len(list)-len(ranked))
	}
	if ranked[0].File != "cat9k.json" {
		t.Errorf("first is %q; the most specific match should lead", ranked[0].File)
	}
	// formatSnmpValue renders an ObjectIdentifier WITH a leading dot, and a
	// preset file is written by hand either way. If this stopped working the
	// symptom would be "0 presets match" on every device.
	if _, withoutDot := rankForDevice(list, "1.3.6.1.4.1.9.1.2494"); withoutDot != 2 {
		t.Error("the leading dot changed the answer")
	}

	// A device that did not answer still gets the library, in its own order.
	same, none := rankForDevice(list, "")
	if none != 0 || len(same) != len(list) || same[0].File != "zzz-generic.json" {
		t.Errorf("an unidentified device got %d entries, %d matched", len(same), none)
	}
}

// A device that does not answer must not take the picker away with it.
func TestIdentifyingAnUnreachableDeviceStillReturnsTheLibrary(t *testing.T) {
	a, _ := newPresetApp(t)
	a.snmpClient = snmp.NewClient()
	a.importSinglePreset(writeSrc(t, t.TempDir(), "cisco.json", goodPreset))

	// A bound-then-closed UDP port: the address is real and nothing answers.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()

	got := a.IdentifyDevice(snmp.TestRequest{
		Target: "127.0.0.1", Community: "public", Version: "v2c", Port: port, Timeout: 1,
	})

	if len(got.Presets) != 1 {
		t.Errorf("the library came back with %d entries", len(got.Presets))
	}
	if got.Presets == nil {
		t.Error("nil crosses the bridge as null and throws on .map")
	}
	if got.SysObjectID != "" {
		t.Errorf("an unreachable device reported %q", got.SysObjectID)
	}
	if got.Error == "" {
		t.Error("it failed silently")
	}
}
