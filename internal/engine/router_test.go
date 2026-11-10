package engine

import (
	"fmt"
	"testing"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
)

// routerApp is a test app with a live router and one catch-all rule.
func routerApp(t *testing.T) (*Engine, string) {
	t.Helper()
	eng := newTestApp(t)

	sink, err := eng.storage.SaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.storage.SaveRoute(notify.Route{
		Name: "everything", Enabled: true, SinkIDs: []string{sink.ID},
	}); err != nil {
		t.Fatal(err)
	}

	eng.router = newEventRouter(eng)
	eng.router.start()
	t.Cleanup(eng.router.stop)
	return eng, sink.ID
}

func trapEvent(n int) events.Event {
	return events.Event{
		Ts: time.Now().UTC().Format(time.RFC3339), Category: events.CategoryTrap,
		Kind: events.KindTrapReceived, Severity: events.SevMajor.String(),
		State: events.StateOneshot, Source: "10.0.0.1",
		TitleKey: "events.kind.trap.received",
		Summary:  fmt.Sprintf("trap %d", n),
	}
}

// waitForDeliveries polls until n deliveries exist, or gives up.
func waitForDeliveries(t *testing.T, eng *Engine, n int, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		rows, err := eng.storage.ListDeliveries("", n*2+10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= n {
			return len(rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := eng.storage.ListDeliveries("", n*2+10)
	return len(rows)
}

// The point of the whole thing: an event journalled on a producer's goroutine
// still reaches the outbox, without that goroutine paying for it.
func TestAnEventRoutedAsynchronouslyStillReachesTheOutbox(t *testing.T) {
	eng, _ := routerApp(t)

	const n = 30
	for i := 0; i < n; i++ {
		if err := eng.recordEvent(trapEvent(i), ""); err != nil {
			t.Fatal(err)
		}
	}

	if got := waitForDeliveries(t, eng, n, 10*time.Second); got != n {
		t.Errorf("%d of %d events reached the outbox", got, n)
	}
}

// The watermark must never claim more than has been written, and seq order is
// NOT the order events reach the router: seq is allocated by the insert and the
// hand-over happens later. A watermark taken as "the highest seq in this batch"
// would strand a lower-seq event still in flight.
func TestTheWatermarkNeverPassesAnEventStillInFlight(t *testing.T) {
	eng, _ := routerApp(t)
	r := eng.router

	// Three events accepted, so all three are owed.
	for _, seq := range []int64{10, 11, 12} {
		r.accept(events.Event{Seq: seq, ID: fmt.Sprintf("e%d", seq)})
	}

	// The HIGHEST settles first — the out-of-order case.
	if mark := r.settle([]int64{12}, 0); mark >= 10 {
		t.Errorf("watermark = %d while seq 10 is still owed; it would never be replayed", mark)
	}
	if mark := r.settle([]int64{11}, 0); mark >= 10 {
		t.Errorf("watermark = %d while seq 10 is still owed", mark)
	}

	// Only once nothing is owed may it reach the highest accepted.
	if mark := r.settle([]int64{10}, 0); mark != 12 {
		t.Errorf("watermark = %d with nothing in flight, want 12", mark)
	}
}

// It must not go backwards either, whatever order batches finish in.
func TestTheRoutersWatermarkNeverRetreats(t *testing.T) {
	eng, _ := routerApp(t)
	r := eng.router

	r.accept(events.Event{Seq: 100})
	high := r.settle([]int64{100}, 0)

	r.accept(events.Event{Seq: 50})
	if mark := r.settle([]int64{50}, 0); mark < high {
		t.Errorf("watermark went from %d back to %d", high, mark)
	}
}

// A full queue must fall back to routing inline, never drop. That fallback is
// exactly what the application did before the router existed, so the worst case
// of the whole design is the behaviour we already had.
func TestAFullQueueFallsBackInsteadOfDropping(t *testing.T) {
	eng, _ := routerApp(t)

	// A router that is NOT running, so nothing drains the queue while it is
	// filled. With the loop live this is a race: the router empties the queue as
	// fast as the test fills it, and accept succeeds.
	eng.router.stop()
	eng.router = newEventRouter(eng)

	for i := 0; i < routeQueueDepth; i++ {
		eng.router.queue <- events.Event{Seq: int64(1_000_000 + i)}
	}
	if eng.router.accept(events.Event{Seq: 2_000_000}) {
		t.Fatal("accept succeeded on a full queue; the fallback is never exercised")
	}

	e := trapEvent(999)
	if err := eng.recordEvent(e, ""); err != nil {
		t.Fatal(err)
	}

	// Routed inline, so it is in the outbox already — no waiting.
	rows, err := eng.storage.ListDeliveries("", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Error("a full queue dropped the event instead of routing it inline")
	}
}

// Shutdown must write what is queued, not abandon it silently.
func TestStoppingTheRouterFlushesWhatIsQueued(t *testing.T) {
	eng, _ := routerApp(t)

	const n = 20
	for i := 0; i < n; i++ {
		if err := eng.recordEvent(trapEvent(i), ""); err != nil {
			t.Fatal(err)
		}
	}

	eng.router.stop()

	rows, err := eng.storage.ListDeliveries("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Errorf("%d of %d events were written by the time stop returned", len(rows), n)
	}
}

// And what a crash leaves behind is replayed.
func TestEventsJournalledButNotRoutedAreReplayed(t *testing.T) {
	eng, _ := routerApp(t)

	// Stop the router, then journal directly: the events exist, nothing routed
	// them, and the watermark has not moved. That is what a crash between the
	// insert and the flush leaves on disk.
	eng.router.stop()
	eng.router = nil

	const n = 12
	for i := 0; i < n; i++ {
		if _, err := eng.storage.InsertEvent(trapEvent(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	if rows, _ := eng.storage.ListDeliveries("", 50); len(rows) != 0 {
		t.Fatalf("%d deliveries exist before the replay", len(rows))
	}

	eng.replayUnrouted()

	rows, err := eng.storage.ListDeliveries("", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Errorf("%d of %d unrouted events were replayed", len(rows), n)
	}

	// And replaying again queues nothing more: the outbox's UNIQUE constraint
	// is what makes a crash mid-flush harmless.
	eng.replayUnrouted()
	again, _ := eng.storage.ListDeliveries("", 50)
	if len(again) != n {
		t.Errorf("a second replay took the outbox from %d to %d", n, len(again))
	}
}

// A clean run must leave nothing to replay, or every restart re-delivers.
func TestACleanRunLeavesNothingToReplay(t *testing.T) {
	eng, _ := routerApp(t)

	for i := 0; i < 15; i++ {
		if err := eng.recordEvent(trapEvent(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	waitForDeliveries(t, eng, 15, 10*time.Second)
	eng.router.stop()

	mark, err := eng.storage.RoutedThrough()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := eng.storage.EventsAfter(mark, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("%d events are above the watermark after a clean run; every "+
			"restart would re-route them", len(pending))
	}
}

// One transaction must not grow with the queue. SQLite serialises writers, so
// its size is how long the trap listener's insert can be blocked: 10 000 rows
// was measured at 392 ms, and a busy timeout of 5 s is what stands between that
// and a lost trap.
func TestAFlushIsBoundedRegardlessOfHowMuchIsQueued(t *testing.T) {
	if routeBatchSize > 200 {
		t.Fatalf("routeBatchSize is %d; a transaction that large holds the write "+
			"lock long enough to lose a trap", routeBatchSize)
	}

	eng, _ := routerApp(t)

	// Well past the batch size, so the flush loop has to run several times.
	n := routeBatchSize * 3
	for i := 0; i < n; i++ {
		if err := eng.recordEvent(trapEvent(i), ""); err != nil {
			t.Fatal(err)
		}
	}

	if got := waitForDeliveries(t, eng, n, 20*time.Second); got != n {
		t.Errorf("%d of %d events routed across several bounded flushes", got, n)
	}
}
