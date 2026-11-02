package snmp

import (
	"sync"
	"testing"
	"time"

	"SnmpLens/pkg/events"
)

// The trap listener's lifecycle.
//
// `Client.trapListener` is written by StartTrapListener on the caller's
// goroutine, written to nil by the listener goroutine when Listen returns, and
// read by StopTrapListener. Nothing guarded it, so the three race — reproducibly
// under -race, and visibly even without: a Start immediately following a Stop
// could be refused because the previous listener had not cleared itself yet, or
// could clobber a listener that was still running.
//
// This matters more now than it did: shutdown has to stop the listener, so the
// stop path is about to be exercised at exactly the moment everything else is
// being torn down.

func TestStartAndStopDoNotRace(t *testing.T) {
	c := NewClient()
	c.SetRecorder(events.Nop{})

	port := freePort(t)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				_, _ = c.StartTrapListener(port, nil)
				c.StopTrapListener()
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("start/stop deadlocked")
	}

	// Leave nothing bound behind.
	c.StopTrapListener()
}

// Stopping a listener that was never started is a normal thing for shutdown to
// do, and must not panic or block.
func TestStoppingAListenerThatIsNotRunning(t *testing.T) {
	c := NewClient()
	c.StopTrapListener()
	c.StopTrapListener()
}

// Starting twice must be refused rather than losing the first listener, which
// would leave a socket bound with nothing able to close it.
func TestStartingTwiceIsRefused(t *testing.T) {
	c := NewClient()
	c.SetRecorder(events.Nop{})
	port := freePort(t)

	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Fatalf("the first start failed: %v", err)
	}
	defer c.StopTrapListener()

	// Give it a moment to bind, so this is the interesting case rather than a
	// race against the goroutine that has not run yet.
	time.Sleep(150 * time.Millisecond)

	if _, err := c.StartTrapListener(port, nil); err == nil {
		t.Error("a second listener was started over the first; the first socket " +
			"is now bound with nothing holding a reference to close it")
	}
}

// Stopping a bound listener is immediate, and that is what says the two waits
// are not fighting over the same value.
//
// gosnmp's Listening() is `make(chan bool, 1)` carrying a VALUE, not a channel
// that closes. Two places here wanted to know the socket was up — the goroutine
// that enlarges the read buffer, and the stop — and the first to receive took
// it. In practice the buffer goroutine won at bind time, so EVERY stop then ran
// to its two-second timeout before closing anything: two seconds added to every
// window close, and a Close() with no happens-before edge to listenUDP's
// unlocked write of `conn`, which `go test -race` flagged on CI in
// TestStartingTwiceIsRefused and TestStartStopStart.
//
// Measured rather than asserted in the abstract: this fails at ~2s against the
// old implementation and passes in milliseconds against the current one.
func TestStoppingABoundListenerIsImmediate(t *testing.T) {
	c := NewClient()
	c.SetRecorder(events.Nop{})
	port := freePort(t)

	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Let it bind, so this measures the stop and not the bind.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	c.StopTrapListener()
	took := time.Since(start)

	// Generous: closing a UDP socket is a syscall. Anything approaching a second
	// means the stop is waiting on something it will never be handed.
	if took > 500*time.Millisecond {
		t.Errorf("stopping a bound listener took %v; it is waiting on a value "+
			"another goroutine has already taken", took)
	}
}

// And after a clean stop, starting again must work — otherwise changing the
// trap port would need a restart.
func TestStartStopStart(t *testing.T) {
	c := NewClient()
	c.SetRecorder(events.Nop{})
	port := freePort(t)

	if _, err := c.StartTrapListener(port, nil); err != nil {
		t.Fatalf("first start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	c.StopTrapListener()

	// Wait for the listener goroutine to clear the field.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.StartTrapListener(port, nil); err == nil {
			c.StopTrapListener()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error("the listener could not be restarted after a stop")
}
