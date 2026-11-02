package app

import (
	"SnmpLens/pkg/snmp"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
)

// A dead letter must never be routed back to the sink that produced it.
//
// The failure becomes an event, the event is routed, and a catch-all rule —
// the natural thing to write — selects the sink that just failed. That
// delivery fails, produces another dead letter, and so on: one unreachable
// collector grows the event journal and the outbox without bound, six attempts
// at a time against a machine that is not answering.
func TestDeadLetterIsNotRoutedBackToItsOwnSink(t *testing.T) {
	a := newTestApp(t)

	for _, s := range []notify.SinkConfig{
		{ID: "sink-uuid-mail", Name: "Ops mailbox", Kind: notify.SinkEmail, Enabled: true,
			Email: notify.EmailConfig{Host: "127.0.0.1", Port: 1, From: "a@b", To: []string{"c@d"}}},
		{ID: "sink-uuid-log", Name: "Central syslog", Kind: notify.SinkSyslog, Enabled: true,
			Syslog: notify.SyslogConfig{Address: "127.0.0.1:1", Protocol: "udp"}},
	} {
		if _, err := a.storage.SaveSink(s); err != nil {
			t.Fatal(err)
		}
	}
	// The catch-all somebody would actually write.
	if _, err := a.storage.SaveRoute(notify.Route{
		ID: "all", Name: "everything", Enabled: true, Priority: 1,
		SinkIDs: []string{"sink-uuid-mail", "sink-uuid-log"},
	}); err != nil {
		t.Fatal(err)
	}

	before, err := a.storage.ListDeliveries("", 200)
	if err != nil {
		t.Fatal(err)
	}

	a.routeEvent(events.Event{
		ID:       "evt-dead-1",
		Category: events.CategorySystem,
		Kind:     events.KindSystemSinkDeadLetter,
		Severity: events.SevMajor.String(),
		Summary:  "Delivery to mail given up: connection refused",
		Params:   map[string]any{"sink": "sink-uuid-mail"},
	})

	after, err := a.storage.ListDeliveries("", 200)
	if err != nil {
		t.Fatal(err)
	}

	var toMail, toLog int
	for _, d := range after[len(before):] {
		switch d.SinkID {
		case "sink-uuid-mail":
			toMail++
		case "sink-uuid-log":
			toLog++
		}
	}

	if toMail != 0 {
		t.Errorf("the dead letter for 'mail' queued %d deliveries back to 'mail' — that is the loop", toMail)
	}
	// And it must still reach the OTHER sinks: "the mail relay is down" is
	// exactly what you want to hear over syslog.
	if toLog != 1 {
		t.Errorf("%d deliveries to 'log'; the dead letter must still be reported elsewhere", toLog)
	}
}

// An ordinary event is unaffected: the exclusion applies to dead letters only.
func TestAnOrdinaryEventStillReachesEverySink(t *testing.T) {
	a := newTestApp(t)

	for _, id := range []string{"sink-uuid-mail", "sink-uuid-log"} {
		if _, err := a.storage.SaveSink(notify.SinkConfig{
			ID: id, Name: id, Kind: notify.SinkSyslog, Enabled: true,
			Syslog: notify.SyslogConfig{Address: "127.0.0.1:1", Protocol: "udp"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.storage.SaveRoute(notify.Route{
		ID: "all", Name: "everything", Enabled: true, Priority: 1,
		SinkIDs: []string{"sink-uuid-mail", "sink-uuid-log"},
	}); err != nil {
		t.Fatal(err)
	}

	before, _ := a.storage.ListDeliveries("", 200)
	a.routeEvent(events.Event{
		ID:       "evt-ordinary-1",
		Category: events.CategoryThreshold,
		Kind:     events.KindThresholdOpened,
		Severity: events.SevMajor.String(),
		Summary:  "ifInOctets above 900",
		// A params map naming a sink must NOT trigger the exclusion: only the
		// dead-letter kind does.
		Params: map[string]any{"sink": "Ops mailbox", "sinkId": "sink-uuid-mail"},
	})
	after, _ := a.storage.ListDeliveries("", 200)

	if got := len(after) - len(before); got != 2 {
		t.Errorf("%d deliveries for an ordinary event, want 2", got)
	}
}

// The reader that decides, on its own.
func TestDeadLetterSink(t *testing.T) {
	cases := []struct {
		name string
		e    events.Event
		want string
	}{
		{"a dead letter names its sink",
			events.Event{Kind: events.KindSystemSinkDeadLetter, Params: map[string]any{"sink": "sink-uuid-mail"}}, "sink-uuid-mail"},
		{"another kind never does",
			events.Event{Kind: events.KindThresholdOpened, Params: map[string]any{"sink": "sink-uuid-mail"}}, ""},
		{"a dead letter with no params",
			events.Event{Kind: events.KindSystemSinkDeadLetter}, ""},
		{"a dead letter whose sink is not a string",
			events.Event{Kind: events.KindSystemSinkDeadLetter, Params: map[string]any{"sink": 7}}, ""},
	}
	for _, c := range cases {
		if got := deadLetterSink(c.e); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The guard must survive the event being made READABLE.
//
// `Params["sink"]` carries the sink's NAME, because "Delivery to {sink} given
// up" is what an operator reads and a raw UUID names nothing they have seen.
// The guard compared that name against the ids a route holds, so it silently
// stopped firing the moment the message was improved — and the unbounded
// event/outbox storm it exists to prevent came back.
//
// This test drives the SAME construction production uses. The version that
// built the event by hand passed throughout the window in which the guard was
// broken, and its fixture used ID: "mail", Name: "mail" — two fields a test
// cannot tell apart once it makes them equal.
func TestTheDeadLetterGuardReadsTheIdNotTheName(t *testing.T) {
	a := newTestApp(t)

	saved, err := a.storage.SaveSink(notify.SinkConfig{
		ID: "sink-uuid-noc", Name: "NOC webhook", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev := a.deadLetterEvent(
		notify.Queued{ID: 1, SinkID: saved.ID, Attempts: 5},
		errors.New("webhook returned 503 Service Unavailable"),
	)

	// The operator reads the name...
	if !strings.Contains(ev.Summary, "NOC webhook") {
		t.Errorf("the summary does not name the destination: %q", ev.Summary)
	}
	if ev.Params["sink"] != "NOC webhook" {
		t.Errorf(`Params["sink"] = %v, want the name`, ev.Params["sink"])
	}

	// ...and the router gets the id.
	if got := deadLetterSink(ev); got != saved.ID {
		t.Errorf("deadLetterSink = %q, want %q — the loop guard compares this "+
			"against the ids a route holds, so a name here disables it", got, saved.ID)
	}
}

// An event journalled before the id was recorded carries only the name, which
// back then was also the id. Those must still be recognised.
func TestTheDeadLetterGuardStillReadsOlderEvents(t *testing.T) {
	old := events.Event{
		Kind:   events.KindSystemSinkDeadLetter,
		Params: map[string]any{"sink": "sink-uuid-legacy"},
	}
	if got := deadLetterSink(old); got != "sink-uuid-legacy" {
		t.Errorf("deadLetterSink on an older event = %q", got)
	}
}

// Shutdown must stop the trap listener, and stop it FIRST.
//
// It was not stopping it at all: a datagram arriving during teardown ran
// handleTrap on gosnmp's goroutine, which journals and routes through storage
// that shutdown is in the middle of closing. Stopping every producer before any
// consumer is the only ordering in which the rest of shutdown's reasoning holds.
func TestShutdownStopsTheTrapListener(t *testing.T) {
	a := newTestApp(t)
	a.snmpClient = snmp.NewClient()
	a.snmpClient.SetRecorder(events.RecorderFunc(a.recordEvent))

	port := freeUDPPort(t)
	if _, err := a.snmpClient.StartTrapListener(port, nil); err != nil {
		t.Fatalf("could not start a listener: %v", err)
	}
	// Let it bind, so this tests the running case.
	deadline := time.Now().Add(3 * time.Second)
	for !a.snmpClient.TrapListenerRunning() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !a.snmpClient.TrapListenerRunning() {
		t.Fatal("the listener never started")
	}

	a.shutdown()

	// Close is asynchronous; the field clears when the listen goroutine unwinds.
	deadline = time.Now().Add(5 * time.Second)
	for a.snmpClient.TrapListenerRunning() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.snmpClient.TrapListenerRunning() {
		t.Error("the trap listener is still running after shutdown; it keeps " +
			"journalling into storage that shutdown has closed")
	}
}

// And shutdown with no listener, no scheduler and no dispatcher must not panic:
// that is what a failed startup looks like on the way out.
func TestShutdownWithNothingRunning(t *testing.T) {
	a := newTestApp(t)
	a.shutdown()
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
