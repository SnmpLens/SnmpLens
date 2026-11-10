package app

import (
	"SnmpLens/internal/engine"
	"testing"
)

// A preset carrying a band ends up watching, through the ordinary path.
//
// The mistake this catches is materialising the bands into the SNAPSHOT alone.
// The dashboard would draw them, the detail screen would count them, and the
// evaluator — which reads sess.Thresholds and nothing else — would never
// compare a single reading. A monitoring that looks armed and is not is worse
// than one that plainly is not.
func TestBindingMaterialisesThePresetsThresholds(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ups.json", `{
	  "formatVersion": 1, "name": "UPS", "intervalSec": 60,
	  "widgets": [
	    {"kind": "status", "title": "Battery", "oids": ["1.3.6.1.2.1.33.1.2.1.0"],
	     "labels": {"1": "unknown", "2": "normal", "3": "low"},
	     "threshold": {"max": 2, "forSeconds": 120, "alertEnabled": true}},
	    {"kind": "value", "title": "Charge", "oids": [".1.3.6.1.2.1.33.1.2.4.0"],
	     "unit": "%", "threshold": {"min": 20}},
	    {"kind": "chart", "title": "Line", "oids": ["1.3.6.1.2.1.33.1.2.5.0"],
	     "threshold": {"min": 228, "alertEnabled": false}},
	    {"kind": "value", "title": "Uptime", "oids": ["1.3.6.1.2.1.1.3.0"]}
	  ]
	}`)

	sess, err := a.PresetBind("ups.json", "10.0.0.1", "v2c", engine.MonitorConnection{Port: 161, TimeoutSec: 2})
	if err != nil {
		t.Fatalf("PresetBind: %v", err)
	}

	if len(sess.Thresholds) != 3 {
		t.Fatalf("%d band(s) on the session, want 3: %v", len(sess.Thresholds), sess.Thresholds)
	}
	// Keyed by the FULL OID without its leading dot — the same key the poll
	// results carry, or the lookup silently finds nothing.
	battery := sess.Thresholds["1.3.6.1.2.1.33.1.2.1.0"]
	if battery == nil || battery.Max == nil || *battery.Max != 2 {
		t.Fatalf("the battery band did not survive: %+v", battery)
	}
	if battery.ForSeconds != 120 || !battery.AlertEnabled {
		t.Errorf("the hold or the alert flag was dropped: %+v", battery)
	}
	charge := sess.Thresholds["1.3.6.1.2.1.33.1.2.4.0"]
	if charge == nil || charge.Min == nil || *charge.Min != 20 {
		t.Errorf("a leading dot lost a band: %v", sess.Thresholds)
	}
	// The flag was OMITTED on that widget, and omitted means yes. A plain bool
	// would have decoded it to false, which does not mean "notify me less" —
	// classify() returns nothing without it, so the band would be compared to
	// nothing at all while the dashboard looked armed.
	if charge != nil && !charge.AlertEnabled {
		t.Error("an omitted alertEnabled disarmed the band")
	}
	// Explicitly off, on the other hand, is the reference line: stored, drawn,
	// never evaluated.
	if line := sess.Thresholds["1.3.6.1.2.1.33.1.2.5.0"]; line == nil || line.AlertEnabled {
		t.Errorf("an explicit alertEnabled:false was not honoured: %+v", line)
	}
	if _, watched := sess.Thresholds["1.3.6.1.2.1.1.3.0"]; watched {
		t.Error("a widget with no band came back watched")
	}

	// And the shape the evaluator is actually handed.
	spec := a.localEngine().SessionSpec(sess)
	if len(spec.Thresholds) != 3 {
		t.Fatalf("the poll clock would evaluate %d band(s)", len(spec.Thresholds))
	}
	if th := spec.Thresholds["1.3.6.1.2.1.33.1.2.1.0"]; th == nil || !th.AlertEnabled {
		t.Errorf("the evaluator's copy is not the preset's: %+v", th)
	}
}

// A preset that carries no band is bound exactly as it was before presets could
// carry any: nil, not an empty map that reads as "watched, with no rules".
func TestAPresetWithoutBandsWatchesNothing(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ok.json", goodPreset)

	sess, err := a.PresetBind("ok.json", "10.0.0.1", "v2c", engine.MonitorConnection{Port: 161})
	if err != nil {
		t.Fatalf("PresetBind: %v", err)
	}
	if len(sess.Thresholds) != 0 {
		t.Errorf("%d band(s) invented: %v", len(sess.Thresholds), sess.Thresholds)
	}
}

// A band the format refuses stops the bind, like any other problem: binding is
// the step that puts traffic on somebody's network AND arms notifications, and
// doing either from a file this application has said it does not understand is
// not a thing to do quietly.
func TestABrokenBandCannotBeBound(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "bad.json", `{
	  "formatVersion": 1, "name": "Bad band", "intervalSec": 60,
	  "widgets": [
	    {"kind": "rate", "title": "In", "oids": ["1.3.6.1.2.1.2.2.1.10.1"],
	     "threshold": {"max": 1000000, "alertEnabled": true}}
	  ]
	}`)

	if _, err := a.PresetBind("bad.json", "10.0.0.1", "v2c", engine.MonitorConnection{Port: 161}); err == nil {
		t.Fatal("a band on a counter was bound")
	}
	if sessions, _ := a.localEngine().Storage().ListSessions(); len(sessions) != 0 {
		t.Errorf("%d session(s) left behind", len(sessions))
	}
}
