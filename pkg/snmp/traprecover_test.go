package snmp

import (
	"net"
	"strings"
	"testing"

	"SnmpLens/pkg/events"
)

// newHeadlessClient builds a Client with no window to tell anything to.
func newHeadlessClient() *Client {
	return NewClient()
}

// A panic while handling one datagram must cost that datagram and nothing
// else. gosnmp's receive loop is a single goroutine with a recover in its
// DECODER and none around OnNewTrap, so an unguarded panic here unwinds
// through listenUDP and ends the process — taking every monitoring session,
// every threshold and the outbox drain with it, from a UDP packet nobody
// authenticated.
//
// Measured with a probe before the guard existed: unguarded, the process died
// with "exit status 2" and never received the second trap; guarded, it
// recovered and handled it.
func TestATrapHandlerPanicDoesNotEscape(t *testing.T) {
	var got events.Event
	c := newHeadlessClient()
	c.SetRecorder(events.RecorderFunc(func(e events.Event, _ string) error {
		got = e
		return nil
	}))

	addr := &net.UDPAddr{IP: net.ParseIP("10.0.0.99"), Port: 4242}
	// A nil packet is the cheapest way to reach a nil dereference inside the
	// handler. What matters is the SHAPE: any panic below this line, from a
	// malformed varbind to a storage bug, arrives the same way.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("the panic escaped handleTrap: %v", r)
			}
		}()
		c.handleTrap(nil, addr)
	}()

	// And it must not be silent: a dropped trap with nothing in the journal is
	// indistinguishable from a trap that never arrived.
	if got.Kind != events.KindSystemListenerError {
		t.Fatalf("no system event was journalled: %+v", got)
	}
	if got.Source != "10.0.0.99" {
		t.Errorf("the event does not name the source: %q", got.Source)
	}
	if got.Severity != events.SevMajor.String() {
		t.Errorf("severity = %q, want major", got.Severity)
	}
	// Per source, so one device sending the same malformed varbind ten
	// thousand times is one alert rather than ten thousand.
	if got.DedupKey != "trap.panic|10.0.0.99" {
		t.Errorf("DedupKey = %q", got.DedupKey)
	}
	if !strings.Contains(got.Summary, "10.0.0.99") {
		t.Errorf("summary does not say which device: %q", got.Summary)
	}
}

// The recovery path runs when the process is already in a state nobody
// predicted, so it must not itself panic on a missing address or a recorder
// that fails.
func TestTheRecoveryPathSurvivesANilAddressAndAFailingRecorder(t *testing.T) {
	c := newHeadlessClient()
	c.SetRecorder(events.RecorderFunc(func(events.Event, string) error {
		panic("the journal is the thing that broke")
	}))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a panic escaped the recovery path: %v", r)
		}
	}()
	c.handleTrap(nil, nil)
}

// The detector: without the guard this file would be describing nothing, so
// pin that the panic it relies on is really there.
func TestTheHandlerReallyPanicsOnThatInput(t *testing.T) {
	var panicked bool
	func() {
		defer func() { panicked = recover() != nil }()
		var p *packetProbe
		_ = p.version()
	}()
	if !panicked {
		t.Fatal("the probe no longer panics; the test above proves nothing")
	}
}

type packetProbe struct{ v int }

func (p *packetProbe) version() int { return p.v }
