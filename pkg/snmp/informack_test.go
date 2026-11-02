package snmp

import (
	"errors"
	"testing"
	"time"

	"SnmpLens/pkg/events"
)

// An INFORM must not be acknowledged unless it was durably journalled.
//
// The synchronous insert on the receive loop exists for exactly this reason:
// gosnmp sends the acknowledgement after OnNewTrap returns, so acknowledging
// before the write would be a lie. When the write FAILED, the acknowledgement
// went out anyway — the same lie told at the one moment it matters, and the
// sender then has no reason to retry.
//
// Driven end to end, sender to listener over a real socket, because the
// property only exists at that layer: the suppression works by changing the
// PDU type gosnmp reads after the handler returns, and only a real sender can
// say whether an acknowledgement arrived.
func TestAnInformIsNotAcknowledgedWhenTheJournalWriteFails(t *testing.T) {
	c := newHeadlessClient()
	c.SetRecorder(events.RecorderFunc(func(events.Event, string) error {
		return errors.New("database is locked")
	}))

	port := freePort(t)
	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	sender := newHeadlessClient()
	res := sender.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", nil)

	if res.Acknowledged {
		t.Fatal("the INFORM was acknowledged although it was never journalled; " +
			"the sender has been told a confirmed notification was delivered and " +
			"will not retry")
	}
}

// The control, and the thing that makes the test above mean something: with
// the journal working, the INFORM IS acknowledged. A change that broke
// acknowledgement altogether would pass the test above.
func TestAnInformIsAcknowledgedWhenTheJournalWriteSucceeds(t *testing.T) {
	c := newHeadlessClient()
	c.SetRecorder(events.Nop{})

	port := freePort(t)
	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	sender := newHeadlessClient()
	res := sender.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", nil)

	if !res.Acknowledged {
		t.Fatalf("a journalled INFORM was not acknowledged: %+v", res)
	}
}

// This is the gosnmp behaviour the suppression depends on: the packet is
// passed to the handler rather than copied, and the PDU type is read back
// after the handler returns. gosnmp documents the assumption that a handler
// will NOT alter it, so an upgrade may legitimately start passing a copy —
// which would make the suppression a silent no-op and quietly restore the lie.
//
// Pinned here for the same reason trapbuf.go's reach into the socket is: the
// upgrade should fail CI rather than change behaviour nobody is watching.
func TestGosnmpStillLetsTheHandlerDeclineAnAcknowledgement(t *testing.T) {
	c := newHeadlessClient()
	// A recorder that fails is what triggers the decline; if gosnmp started
	// passing a copy, this listener would acknowledge anyway.
	c.SetRecorder(events.RecorderFunc(func(events.Event, string) error {
		return errors.New("journal unavailable")
	}))

	port := freePort(t)
	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	sender := newHeadlessClient()
	start := time.Now()
	res := sender.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", nil)
	elapsed := time.Since(start)

	if res.Acknowledged {
		t.Fatalf("gosnmp no longer honours a handler changing PDUType (answered in %v). "+
			"An INFORM that could not be journalled is being acknowledged again; the "+
			"suppression in declineToAcknowledge needs another mechanism.", elapsed)
	}
}

// waitBound waits for the listener's socket, rather than sleeping. A send into
// a port nothing is listening on would time out and look exactly like the
// suppression working.
func waitBound(t *testing.T, c *Client) {
	t.Helper()
	// trapBound, not TrapListenerRunning(): the latter reports trapListener !=
	// nil, which StartTrapListener sets on the CALLER's goroutine before the
	// socket exists. Polling it returned immediately, the INFORM went into a
	// port nothing was listening on yet, and the send timed out — which is the
	// exact result the first test asserts. It passed without the fix, twice.
	//
	// trapBound is closed by the listener goroutine once gosnmp reports the
	// socket up, and it is closed rather than sent to, so more than one waiter
	// is safe.
	c.trapMu.Lock()
	bound := c.trapBound
	c.trapMu.Unlock()
	if bound == nil {
		t.Fatal("the listener has no bound channel; StartTrapListener changed shape")
	}
	select {
	case <-bound:
	case <-time.After(3 * time.Second):
		t.Fatal("the trap listener never bound")
	}
}
