package engine

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/storage"
)

// A router that cannot write must not spin, and must still stop.
//
// A failed batch is put back so the watermark cannot pass it. Putting it back
// into the QUEUE meant the loop read it straight out again: measured at 1244
// failed database transactions in 300 ms — about 4 000 a second, each one a
// write attempt against a database that had just refused one, on a connection
// pool of four that the trap listener's insert also needs.
//
// Shutdown never converged either: drain kept finding the events it had just
// requeued, so stopping took the whole 5 s grace period. Measured at 5.49 s.
func TestAFailingRouterBacksOffAndStillStops(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	var attempts int64
	r.enqueue = func([]storage.RoutedGroup, int64) error {
		atomic.AddInt64(&attempts, 1)
		return errors.New("disk full")
	}

	// A full batch, which is what made it a tight loop: below routeBatchSize
	// the loop waits for the ticker anyway, so the defect only showed at the
	// size a real storm produces.
	for i := int64(1); i <= int64(routeBatchSize); i++ {
		e := trapEvent(int(i))
		e.Seq = i
		if !r.accept(e) {
			t.Fatal("the queue refused an event")
		}
	}

	r.start()
	time.Sleep(300 * time.Millisecond)

	stopStart := time.Now()
	r.stop()
	stopTook := time.Since(stopStart)

	got := atomic.LoadInt64(&attempts)
	// The first backoff is a second, so 300 ms can hold one attempt and the
	// retry of it at most. Anything in the hundreds is the spin.
	if got > 3 {
		t.Errorf("%d write attempts in 300 ms; the router is spinning on a database "+
			"that keeps refusing, on the same four-connection pool the trap insert needs", got)
	}
	if stopTook >= routeStopGrace {
		t.Errorf("stopping took %s, the whole grace period: drain never converges while "+
			"routing keeps failing", stopTook.Round(time.Millisecond))
	}

	// The events are not lost: none of them settled, so the watermark cannot
	// pass them and replay covers them at the next launch.
	r.mu.Lock()
	owed := len(r.inflight)
	confirmed := r.confirmed
	r.mu.Unlock()
	if owed == 0 {
		t.Error("the events were marked done although nothing was ever written")
	}
	if confirmed != 0 {
		t.Errorf("the watermark moved to %d with no successful write", confirmed)
	}
}

// The backoff must let go once the write works again, or one transient failure
// would slow routing for the rest of the session.
func TestTheBackoffClearsOnTheFirstSuccessfulWrite(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	fail := true
	r.enqueue = func([]storage.RoutedGroup, int64) error {
		if fail {
			return errors.New("temporarily unavailable")
		}
		return nil
	}

	e := trapEvent(1)
	e.Seq = 1
	r.accept(e)
	<-r.queue
	b := []events.Event{{Seq: 1, ID: "e1"}}
	r.flush(&b)
	if r.backoff == 0 {
		t.Fatal("a failed write did not arm a backoff")
	}

	fail = false
	// The deferred event is due after the backoff; take it by hand rather than
	// waiting a second for the ticker.
	r.retryAt = time.Time{}
	var next []events.Event
	r.takeDeferred(&next)
	if len(next) != 1 {
		t.Fatalf("the deferred event was not offered again: %d", len(next))
	}
	r.flush(&next)

	if r.backoff != 0 {
		t.Errorf("the backoff survived a successful write: %s", r.backoff)
	}
	r.mu.Lock()
	owed := len(r.inflight)
	r.mu.Unlock()
	if owed != 0 {
		t.Errorf("%d events still owed after the retry succeeded", owed)
	}
}
