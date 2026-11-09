package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
)

// An app with everything binding needs: storage, a secret store, a preset
// directory and a scheduler.
func newBindApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()

	st, err := storage.Init(filepath.Join(dir, "monitoring.db"))
	if err != nil {
		t.Fatalf("storage.Init: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sec, err := secrets.Open(dir)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}

	mibDir := filepath.Join(dir, "SnmpLens", "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}

	a := &App{storage: st, secrets: sec, persistentMibDir: mibDir}
	// A real client, because PresetBind STARTS the session: without one the
	// poll goroutine dereferenced nil inside newGoSNMP and killed the test
	// binary — on macOS only, because the other platforms finished before the
	// first tick landed. The targets below are addresses nothing answers on, so
	// no packet ever reaches anything.
	a.snmpClient = snmp.NewClient()
	a.scheduler = monitor.NewScheduler()
	// The clock must not actually reach a network in a unit test, and Persist
	// is required by the scheduler.
	a.scheduler.Persist = func([]monitor.Point) {}
	t.Cleanup(func() { a.scheduler.StopAll() })

	presetDir, err := a.presetDir()
	if err != nil {
		t.Fatal(err)
	}
	return a, presetDir
}

func putPreset(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Binding turns a file into a monitoring, and what to POLL has to land in the
// session's own columns.
//
// The mistake this catches is storing what to poll only in the snapshot: the
// dashboard would look perfect while specFor — which reads sess.OID and
// sess.IntervalMs and nothing else — polls nothing at all.
func TestPresetBindMaterialisesTheOidsAndTheInterval(t *testing.T) {
	a, dir := newBindApp(t)

	// Three widgets, two of which share an OID: the poll list is the
	// deduplicated set, in a stable order.
	putPreset(t, dir, "cisco.json", `{
	  "formatVersion": 1, "name": "Cisco Catalyst", "intervalSec": 45,
	  "widgets": [
	    {"kind": "value", "title": "Uptime", "oids": ["1.3.6.1.2.1.1.3.0"]},
	    {"kind": "chart", "title": "Uptime again", "oids": [".1.3.6.1.2.1.1.3.0", "1.3.6.1.2.1.2.2.1.10.1"]},
	    {"kind": "status", "title": "Link", "oids": ["1.3.6.1.2.1.2.2.1.8.1"], "labels": {"1": "up"}}
	  ]
	}`)

	sess, err := a.PresetBind("cisco.json", "10.0.0.1", "v2c", MonitorConnection{Port: 161, TimeoutSec: 2, Retries: 1})
	if err != nil {
		t.Fatalf("PresetBind: %v", err)
	}

	if want := "1.3.6.1.2.1.1.3.0,1.3.6.1.2.1.2.2.1.10.1,1.3.6.1.2.1.2.2.1.8.1"; sess.OID != want {
		t.Errorf("oid column = %q, want %q", sess.OID, want)
	}
	if sess.IntervalMs != 45000 {
		t.Errorf("interval_ms = %d, want 45000 (intervalSec x 1000)", sess.IntervalMs)
	}
	if len(sess.Targets) != 1 || sess.Targets[0] != "10.0.0.1" {
		t.Errorf("targets = %v; a preset is bound to ONE equipment", sess.Targets)
	}
	if sess.Name != "Cisco Catalyst" {
		t.Errorf("name = %q", sess.Name)
	}

	// The snapshot came with it, and it is the layout rather than the poll list.
	if sess.Preset == nil {
		t.Fatal("the session carries no snapshot")
	}
	if sess.Preset.File != "cisco.json" || len(sess.Preset.Widgets) != 3 {
		t.Errorf("snapshot: %+v", sess.Preset)
	}

	// And the specification the poll clock would build reads the columns.
	spec := a.specFor(sess)
	if len(spec.OIDs) != 3 || spec.Interval.Seconds() != 45 {
		t.Errorf("the poll clock would use %d OID(s) every %v", len(spec.OIDs), spec.Interval)
	}
	if !a.scheduler.IsRunning(sess.ID) {
		t.Error("binding did not start the session")
	}
}

// Binding is the step that puts traffic on somebody's network. Doing it from a
// file this application has already said it does not understand is not a thing
// to do quietly.
func TestAPresetWithProblemsCannotBeBound(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "broken.json", `{"formatVersion": 1, "name": "", "intervalSec": 0,
	  "widgets": [{"kind": "iframe", "title": "x", "oids": ["sysUpTime.0"]}]}`)

	_, err := a.PresetBind("broken.json", "10.0.0.1", "v2c", MonitorConnection{Port: 161})
	if err == nil {
		t.Fatal("a preset with problems was bound")
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	// And nothing was created on the way to refusing.
	if sessions, _ := a.storage.ListSessions(); len(sessions) != 0 {
		t.Errorf("%d session(s) left behind by a refused bind", len(sessions))
	}
}

// A preset is bound to an EQUIPMENT. Without one there is nothing to poll and
// nothing to name the session after.
func TestBindingRefusesWithoutATarget(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ok.json", goodPreset)

	if _, err := a.PresetBind("ok.json", "   ", "v2c", MonitorConnection{Port: 161}); err == nil {
		t.Error("a preset was bound to no equipment")
	}
	if _, err := a.PresetBind("../monitoring.db", "10.0.0.1", "v2c", MonitorConnection{Port: 161}); err == nil {
		t.Error("a path outside the library was bound")
	}
	if sessions, _ := a.storage.ListSessions(); len(sessions) != 0 {
		t.Errorf("%d session(s) created by refused binds", len(sessions))
	}
}

// The credentials go to pkg/secrets, never into monitoring.db — the same rule
// MonitorCreateSession follows, and the reason SessionConn exists.
func TestBindingStoresTheCredentialsOutsideTheDatabase(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ok.json", goodPreset)

	sess, err := a.PresetBind("ok.json", "10.0.0.1", "v2c", MonitorConnection{
		Port: 161, TimeoutSec: 2, Community: "s3cret-community",
	})
	if err != nil {
		t.Fatalf("PresetBind: %v", err)
	}

	raw, err := a.secrets.Get(secrets.SessionRef(sess.ID))
	if err != nil || !strings.Contains(raw, "s3cret-community") {
		t.Errorf("the community did not reach the secret store: %q %v", raw, err)
	}
	if sess.Conn == nil || sess.Conn.Port != 161 {
		t.Errorf("the connection profile did not survive: %+v", sess.Conn)
	}

	// Nothing secret in the row itself.
	sessions, _ := a.storage.ListSessions()
	for _, s := range sessions {
		if s.Conn != nil && strings.Contains(strings.ToLower(s.Name+s.OID), "s3cret") {
			t.Error("the community reached the session row")
		}
	}
}

// Two equipments, two sessions: the cost was stated per equipment, the
// credentials are per equipment, and the guardrail in pkg/monitor is per
// session — so one slow device must not back off the healthy one with it.
func TestBindingTheSamePresetTwiceGivesTwoSessions(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ok.json", goodPreset)

	first, err := a.PresetBind("ok.json", "10.0.0.1", "v2c", MonitorConnection{Port: 161})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.PresetBind("ok.json", "10.0.0.2", "v2c", MonitorConnection{Port: 161})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("the second bind reused the first session")
	}
	if !a.scheduler.IsRunning(first.ID) || !a.scheduler.IsRunning(second.ID) {
		t.Error("both sessions should be polling independently")
	}
	if sessions, _ := a.storage.ListSessions(); len(sessions) != 2 {
		t.Errorf("%d session(s) for two equipments", len(sessions))
	}
}

// The snapshot is a snapshot. Editing the file afterwards changes nothing
// already bound, and deleting it stops nothing — rebinding is how an edit is
// adopted, and the opposite behaviour reads as a bug either way round.
func TestEditingOrDeletingThePresetDoesNotChangeABoundSession(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ok.json", goodPreset)

	sess, err := a.PresetBind("ok.json", "10.0.0.1", "v2c", MonitorConnection{Port: 161})
	if err != nil {
		t.Fatal(err)
	}
	before := sess.OID
	widgets := len(sess.Preset.Widgets)

	// Rewrite it with a different OID and a different cadence, then delete it.
	putPreset(t, dir, "ok.json", `{"formatVersion": 1, "name": "Changed", "intervalSec": 600,
	  "widgets": [{"kind": "value", "title": "Something else", "oids": ["1.3.6.1.2.1.1.5.0"]}]}`)
	if err := a.DeletePreset("ok.json"); err != nil {
		t.Fatal(err)
	}

	sessions, _ := a.storage.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("%d session(s) after deleting the file", len(sessions))
	}
	if sessions[0].OID != before {
		t.Errorf("the session started polling something else: %q then %q", before, sessions[0].OID)
	}
	if sessions[0].Preset == nil || len(sessions[0].Preset.Widgets) != widgets {
		t.Error("the layout followed the file instead of staying put")
	}
	if !a.scheduler.IsRunning(sessions[0].ID) {
		t.Error("deleting the preset stopped the monitoring")
	}
}
