package app

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/snmp"
)

// What the trap path can actually take, measured as LOSS at a stated offered
// rate rather than as one over a mean service time.
//
// The two are not the same: offering at 1/mean is a utilisation of 1.0 by
// definition, where a queue is already unstable. Quoting "1035 traps/s" from a
// 0.97 ms mean was the wrong claim, and this test exists so the number that gets
// quoted is one that was observed.
//
// Measured here, end to end, with the read buffer raised and routing off the
// receive loop:
//
//	  200/s x   500 ....... 100%
//	  600/s x   500 ....... 100%
//	  900/s x   500 ....... 100%
//	 2000/s x  4000 ....... 100%
//	 5000/s x 10000 ....... 93%   <- first loss
//
// Note the shape: rate alone decides nothing any more. A burst that fits the
// socket buffer is absorbed whatever rate it arrives at, so what costs
// datagrams is sustained overload for longer than the buffer covers.

// offerTraps sends n datagrams at a target rate and reports how many the
// application journalled.
func offerTraps(t *testing.T, a *App, port, n int, perSecond float64) int {
	t.Helper()

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	packet := trapWireBytes(t)
	gap := time.Duration(float64(time.Second) / perSecond)

	before := countEvents(t, a)
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := conn.Write(packet); err != nil {
			break
		}
		// Busy-wait to the next slot: a sleep of tens of microseconds is not
		// honoured on Windows and would make the offered rate a fiction.
		for time.Since(start) < time.Duration(i+1)*gap {
		}
	}

	// Let the handler drain.
	settled := before
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
		now := countEvents(t, a)
		if now == settled {
			break
		}
		settled = now
	}
	return countEvents(t, a) - before
}

// countEvents counts the journal. NOT QueryEvents: its Limit is capped at 500,
// which reads as a 17% loss on a 600-datagram run that lost nothing.
func countEvents(t *testing.T, a *App) int {
	t.Helper()
	all, err := a.storage.EventsAfter(0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// trapWireBytes captures the exact bytes gosnmp puts on the wire for a trap.
func trapWireBytes(t *testing.T) []byte {
	t.Helper()
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	sender := &gosnmp.GoSNMP{
		Target: "127.0.0.1", Port: uint16(sink.LocalAddr().(*net.UDPAddr).Port),
		Version: gosnmp.Version2c, Community: "public", Timeout: 2 * time.Second,
	}
	if err := sender.Connect(); err != nil {
		t.Fatal(err)
	}
	defer sender.Conn.Close()

	go func() {
		_, _ = sender.SendTrap(gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
			{Name: "1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(1)},
			{Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier,
				Value: "1.3.6.1.6.3.1.1.5.3"},
		}})
	}()

	buf := make([]byte, 4096)
	_ = sink.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := sink.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

// The whole path: a datagram on the wire, through gosnmp's serial receive loop,
// journalled, and handed to the router.
//
// An INTEGRATION check, and it is worth being clear about what it does not do.
// At this volume the default socket buffer would cope too — verified by
// mutation, removing the read-buffer raise still gives 100% here — so this does
// not pin that. What pins the socket buffer is
// pkg/snmp.TestABurstSurvivesTheSocketBuffer, where a deliberately slow handler
// makes the buffer the only thing between a burst and silence: 64% without it,
// 100% with.
//
// What this catches is the chain being broken or grossly slower end to end.
func TestTrapsAreNotLostAtTheStatedRate(t *testing.T) {
	if testing.Short() {
		t.Skip("sends thousands of datagrams")
	}
	if raceEnabledMain {
		t.Skip("the race detector changes the service time by an order of magnitude")
	}

	a := newTestApp(t)
	sink, err := a.storage.SaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.storage.SaveRoute(notify.Route{
		Name: "all", Enabled: true, SinkIDs: []string{sink.ID},
	}); err != nil {
		t.Fatal(err)
	}

	a.router = newEventRouter(a)
	a.router.start()
	defer a.router.stop()

	// nil, deliberately: it is how this client says "no webview attached".
	// A context Wails did not issue is REFUSED by its runtime, which takes the
	// process down — which is exactly why handleTrap now checks for nil before
	// emitting. context.TODO() here would defeat the guard it is testing.
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient()
	a.snmpClient.SetRecorder(events.RecorderFunc(a.recordEvent))

	port := freeUDPPort(t)
	if _, err := a.snmpClient.StartTrapListener(port, nil); err != nil {
		t.Fatal(err)
	}
	defer a.snmpClient.StopTrapListener()
	time.Sleep(400 * time.Millisecond) // bind, and raise the read buffer

	// Asserted below what was measured — 100% at 2000/s — because the point is
	// to catch a REGRESSION in the path, not to pin this machine's speed.
	const offered = 1000.0
	const count = 1000

	got := offerTraps(t, a, port, count, offered)
	kept := float64(got) / float64(count) * 100
	t.Logf("%d of %d traps journalled at %.0f/s (%.1f%%)", got, count, offered, kept)

	if kept < 95 {
		t.Errorf("only %.0f%% of traps offered at %.0f/s were journalled; the trap "+
			"path has regressed and the loss is silent", kept, offered)
	}
}

// The measurement behind the claim: where loss actually starts. Reported, never
// asserted — it is a property of the machine, not of the code.
func TestMeasureWhereTrapLossBegins(t *testing.T) {
	if testing.Short() || !testing.Verbose() {
		t.Skip("measurement only; run with -v")
	}
	if raceEnabledMain {
		t.Skip("the race detector changes the service time by an order of magnitude")
	}

	a := newTestApp(t)
	a.router = newEventRouter(a)
	a.router.start()
	defer a.router.stop()

	// nil, deliberately: it is how this client says "no webview attached".
	// A context Wails did not issue is REFUSED by its runtime, which takes the
	// process down — which is exactly why handleTrap now checks for nil before
	// emitting. context.TODO() here would defeat the guard it is testing.
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient()
	a.snmpClient.SetRecorder(events.RecorderFunc(a.recordEvent))

	port := freeUDPPort(t)
	if _, err := a.snmpClient.StartTrapListener(port, nil); err != nil {
		t.Fatal(err)
	}
	defer a.snmpClient.StopTrapListener()
	time.Sleep(400 * time.Millisecond)

	// Rate alone no longer decides anything: with the read buffer raised, a
	// burst that fits the buffer is absorbed whatever rate it arrives at. What
	// costs datagrams is sustained overload for longer than the buffer can
	// cover, so the volume is varied as well.
	for _, c := range []struct {
		rate  float64
		count int
	}{
		{200, 500}, {600, 500}, {900, 500},
		{2000, 4000}, {5000, 10000},
	} {
		got := offerTraps(t, a, port, c.count, c.rate)
		t.Logf("offered %.0f/s for %d datagrams: %d journalled (%.0f%%)",
			c.rate, c.count, got, float64(got)/float64(c.count)*100)
	}
}
