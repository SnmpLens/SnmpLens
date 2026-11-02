package snmp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"SnmpLens/pkg/events"

	"github.com/gosnmp/gosnmp"
)

type captureRecorder struct {
	events   []events.Event
	payloads []string
	err      error
}

func (c *captureRecorder) Record(e events.Event, payload string) error {
	c.events = append(c.events, e)
	c.payloads = append(c.payloads, payload)
	return c.err
}

func testPacket(version gosnmp.SnmpVersion) *gosnmp.SnmpPacket {
	return &gosnmp.SnmpPacket{
		Version: version,
		Variables: []gosnmp.SnmpPDU{
			{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1.6.3.1.1.5.3"},
			{Name: "1.3.6.1.2.1.2.2.1.1.1", Type: gosnmp.Integer, Value: 1},
		},
	}
}

// A received trap must reach the journal, because EventsEmit only reaches a
// live webview: with no window listening the packet would otherwise vanish
// without a trace or an error.
func TestRecordTrapJournalsEventAndPayload(t *testing.T) {
	rec := &captureRecorder{}
	c := &Client{recorder: rec}

	pkt := testPacket(gosnmp.Version2c)
	vars := []Result{
		{Oid: "snmpTrapOID.0", Type: "ObjectIdentifier", Value: "1.3.6.1.6.3.1.1.5.3"},
		{Oid: "1.3.6.1.2.1.2.2.1.1.1", Type: "Integer", Value: 1},
	}
	ts := time.Now().UTC().Format(time.RFC3339)

	c.recordTrap("192.0.2.10", ts, "Trap", pkt, vars)

	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	e := rec.events[0]

	if e.Category != events.CategoryTrap {
		t.Errorf("category = %q, want %q", e.Category, events.CategoryTrap)
	}
	if e.Kind != events.KindTrapReceived {
		t.Errorf("kind = %q, want %q", e.Kind, events.KindTrapReceived)
	}
	if e.Source != "192.0.2.10" {
		t.Errorf("source = %q", e.Source)
	}
	if e.OID != "1.3.6.1.6.3.1.1.5.3" {
		t.Errorf("trap OID not extracted, got %q", e.OID)
	}
	if e.DedupKey != "trap|192.0.2.10|1.3.6.1.6.3.1.1.5.3" {
		t.Errorf("dedupKey = %q", e.DedupKey)
	}
	if e.TitleKey != "events.kind."+events.KindTrapReceived {
		t.Errorf("titleKey = %q", e.TitleKey)
	}
	if err := events.Validate(e); err != nil {
		t.Errorf("recorded event is not valid: %v", err)
	}

	// The varbinds belong to the payload side, so listing the journal never has
	// to read a large trap.
	var decoded []Result
	if err := json.Unmarshal([]byte(rec.payloads[0]), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if len(decoded) != 2 {
		t.Errorf("payload holds %d varbinds, want 2", len(decoded))
	}
}

func TestRecordTrapMarksInforms(t *testing.T) {
	rec := &captureRecorder{}
	c := &Client{recorder: rec}

	c.recordTrap("192.0.2.11", time.Now().UTC().Format(time.RFC3339), "Inform",
		testPacket(gosnmp.Version2c), []Result{})

	if len(rec.events) != 1 || rec.events[0].Kind != events.KindTrapInform {
		t.Fatalf("inform not distinguished: %+v", rec.events)
	}
}

// A journal failure must not take down the listener goroutine: the trap is
// already lost at that point, and panicking would lose every later one too.
func TestRecordTrapSurvivesRecorderFailure(t *testing.T) {
	rec := &captureRecorder{err: errors.New("disk on fire")}
	c := &Client{recorder: rec}

	c.recordTrap("192.0.2.12", time.Now().UTC().Format(time.RFC3339), "Trap",
		testPacket(gosnmp.Version1), []Result{})
}

// NewClient must leave a usable recorder in place so a trap arriving before
// storage is wired cannot nil-panic inside the listener goroutine.
func TestNewClientHasNonNilRecorder(t *testing.T) {
	c := NewClient()
	if c.recorder == nil {
		t.Fatal("recorder is nil; a trap would panic the listener")
	}
	c.recordTrap("192.0.2.13", time.Now().UTC().Format(time.RFC3339), "Trap",
		testPacket(gosnmp.Version2c), []Result{})

	c.SetRecorder(nil) // must fall back to Nop rather than storing nil
	if c.recorder == nil {
		t.Fatal("SetRecorder(nil) left a nil recorder")
	}
}
