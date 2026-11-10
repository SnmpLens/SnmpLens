package app

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/preset"
	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
)

// A preset that discovers its ports, of the shape the shipped ones now have:
// one walk of ifDescr feeding a grid of states and a chart of counters.
const discoveringPreset = `{
  "formatVersion": 1, "name": "Ports", "intervalSec": 60,
  "widgets": [
    {"kind": "value", "title": "Uptime", "oids": ["1.3.6.1.2.1.1.3.0"]},
    {"kind": "grid", "title": "Ports", "oids": ["1.3.6.1.2.1.2.2.1.8.{#}"],
     "labels": {"1": "up", "2": "down"},
     "discover": {"walk": "1.3.6.1.2.1.2.2.1.2"}},
    {"kind": "chart", "title": "Traffic", "oids": ["1.3.6.1.2.1.2.2.1.10.{#}"],
     "discover": {"walk": "1.3.6.1.2.1.2.2.1.2"}}
  ]
}`

// Binding a discovering preset to a device that cannot answer the walk is
// refused, and nothing is left behind.
//
// The alternative is what the first version did: an empty instance list expands
// every template to nothing, the widgets end up with no OIDs at all, and what
// the operator gets is a session that looks bound, polls one scalar, and draws
// two empty boxes. The walk failing is the most likely thing to go wrong when
// somebody binds a preset — wrong community, wrong version, an ACL on the
// column — so it is the case that must not be quiet.
func TestBindingADiscoveringPresetToASilentDeviceIsRefused(t *testing.T) {
	a, dir := newBindApp(t)
	putPreset(t, dir, "ports.json", discoveringPreset)

	// A port nothing listens on, on the loopback: the answer is an ICMP
	// unreachable rather than a timeout, so this costs milliseconds.
	_, err := a.PresetBind("ports.json", "127.0.0.1", "v2c", engine.MonitorConnection{
		Port: closedUDPPort(t), TimeoutSec: 1, Retries: 0, Community: "public",
	})
	if err == nil {
		t.Fatal("a preset was bound to a device that could not answer its walk")
	}
	// The message names the column, because "bind failed" sends the operator
	// looking at the preset rather than at the device.
	if !strings.Contains(err.Error(), "1.3.6.1.2.1.2.2.1.2") {
		t.Errorf("the refusal does not say what could not be walked: %v", err)
	}

	if sessions, _ := a.localEngine().Storage().ListSessions(); len(sessions) != 0 {
		t.Errorf("%d session(s) left behind by a refused bind", len(sessions))
	}
}

// A preset that lists its OIDs does not pay for the discovery path.
//
// The client is nil here on purpose: discoverFor must decide there is nothing
// to walk BEFORE it reaches for anything, or every hand-written preset acquires
// a round trip — and a nil dereference in a unit test that has no network.
func TestAPresetWithoutDiscoveryIsNeverWalked(t *testing.T) {
	a, _ := newBindApp(t)
	a.snmpClient = nil

	p := preset.Preset{
		FormatVersion: 1, Name: "Plain", IntervalSec: 60,
		Widgets: []preset.Widget{{
			Kind: "value", Title: "Uptime", OIDs: []string{"1.3.6.1.2.1.1.3.0"},
		}},
	}
	out, err := a.discoverFor(p, "10.0.0.1", "v2c", engine.MonitorConnection{Port: 161})
	if err != nil {
		t.Fatalf("discoverFor on a preset that discovers nothing: %v", err)
	}
	if len(out.Widgets) != 1 || len(out.Widgets[0].OIDs) != 1 ||
		out.Widgets[0].OIDs[0] != "1.3.6.1.2.1.1.3.0" {
		t.Errorf("the preset came back changed: %+v", out.Widgets)
	}
}

// The whole of discovery against a real agent — the simulator, run in-process:
// bind, and the session polls the interfaces the equipment actually has, named
// as it names them.
func TestPresetDiscoveryIntegration(t *testing.T) {
	d := simtest.Start(t, simulator.Device{})
	host, port := d.Address, d.Port
	a, dir := newBindApp(t)
	putPreset(t, dir, "ports.json", discoveringPreset)

	sess, err := a.PresetBind("ports.json", host, "v2c", engine.MonitorConnection{
		Port: port, TimeoutSec: 2, Retries: 1, Community: "public",
	})
	if err != nil {
		t.Fatalf("PresetBind: %v", err)
	}

	// The scalar plus one state and one counter per interface the agent serves.
	// The count is not hard-coded: the point of discovery is that the preset
	// does not know it either.
	oids := strings.Split(sess.OID, ",")
	states, counters := 0, 0
	for _, oid := range oids {
		switch {
		case strings.HasPrefix(oid, "1.3.6.1.2.1.2.2.1.8."):
			states++
		case strings.HasPrefix(oid, "1.3.6.1.2.1.2.2.1.10."):
			counters++
		}
	}
	if states < 2 || states != counters {
		t.Fatalf("discovered %d state(s) and %d counter(s) from %q", states, counters, sess.OID)
	}
	t.Logf("discovered %d interface(s)", states)

	// The templates are gone: what is stored is a plain preset, which is what
	// keeps the poll path GET-only.
	for _, w := range sess.Preset.Widgets {
		if w.Discover != nil {
			t.Errorf("widget %q still carries a discovery declaration", w.Title)
		}
		for _, oid := range w.OIDs {
			if strings.Contains(oid, preset.InstancePlaceholder) {
				t.Errorf("widget %q still has a template: %s", w.Title, oid)
			}
		}
	}

	// And the labels came off the walked column, which is the other half of why
	// the walk is worth its round trip: a grid reading "eth0" rather than "8".
	grid := sess.Preset.Widgets[1]
	if len(grid.OIDLabels) != states {
		t.Fatalf("%d label(s) for %d cell(s)", len(grid.OIDLabels), states)
	}
	for oid, label := range grid.OIDLabels {
		if strings.TrimSpace(label) == "" {
			t.Errorf("%s has an empty label", oid)
		}
		if _, err := strconv.Atoi(label); err == nil {
			t.Errorf("%s is labelled %q, which is an index rather than a name", oid, label)
		}
	}
}

// closedUDPPort returns a loopback UDP port nothing is listening on.
//
// Bound and released rather than picked: a hard-coded number is a port somebody
// else's agent is on, and this test's whole premise is that nothing answers.
func closedUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	if err := c.Close(); err != nil {
		t.Fatalf("releasing %d: %v", port, err)
	}
	return port
}
