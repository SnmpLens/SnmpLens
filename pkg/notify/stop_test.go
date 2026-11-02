package notify

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"SnmpLens/pkg/events"
)

// blockingQueue holds a drain open until the test lets it go.
type blockingQueue struct {
	entered chan struct{}
	release chan struct{}
	served  atomic.Bool
	inDrain atomic.Bool
	done    atomic.Bool
}

func (q *blockingQueue) DueDeliveries(int) ([]Queued, error) {
	if q.served.Swap(true) {
		return nil, nil
	}
	q.inDrain.Store(true)
	close(q.entered)
	<-q.release
	q.inDrain.Store(false)
	q.done.Store(true)
	return nil, nil
}

func (q *blockingQueue) MarkSent(int64) error { return nil }
func (q *blockingQueue) MarkFailed(int64, string, time.Time, bool) error {
	return nil
}

// Stop must not return while a delivery is still being sent.
//
// It closed a channel and returned, so the drain loop finished the pass it was
// in — on its own goroutine, with nobody waiting. On shutdown that is a send in
// flight when the process exits: shutdown returns, Wails tears the app
// down, and an SMTP conversation or an HTTP POST is cut mid-way. The delivery is
// left `pending` with an attempt spent, and the operator's alert is neither
// sent nor reported as failed.
func TestStopWaitsForAnInFlightDrain(t *testing.T) {
	q := &blockingQueue{entered: make(chan struct{}), release: make(chan struct{})}
	d := NewDispatcher(q, func(string) (Sink, bool) { return nil, false }, time.Hour)
	d.Start()

	d.Wake()
	select {
	case <-q.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never started")
	}

	stopped := make(chan struct{})
	go func() { d.Stop(); close(stopped) }()

	// Stop is called while the drain is held open. It must block.
	select {
	case <-stopped:
		t.Fatal("Stop returned while a drain was still running")
	case <-time.After(150 * time.Millisecond):
	}

	close(q.release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after the drain finished")
	}
	if !q.done.Load() {
		t.Error("Stop returned before the drain had finished its work")
	}
	if q.inDrain.Load() {
		t.Error("a drain was still in flight when Stop returned")
	}
}

// Stop is called from shutdown and from tests; calling it twice must not
// panic on a closed channel.
func TestStopIsIdempotent(t *testing.T) {
	d := NewDispatcher(&blockingQueue{entered: make(chan struct{}), release: make(chan struct{})},
		func(string) (Sink, bool) { return nil, false }, time.Hour)
	d.Start()
	d.Stop()
	d.Stop()
}

// Stop on a dispatcher that was never started must not hang waiting for a
// goroutine that does not exist.
func TestStopWithoutStartReturns(t *testing.T) {
	d := NewDispatcher(nil, nil, time.Hour)
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a dispatcher that was never started")
	}
}

// And a wake-up after Stop must not restart anything.
func TestWakeAfterStopDoesNotDrain(t *testing.T) {
	var drains atomic.Int32
	q := &countingQueue{onDue: func() { drains.Add(1) }}
	d := NewDispatcher(q, func(string) (Sink, bool) { return nil, false }, time.Hour)
	d.Start()
	d.Stop()
	d.Wake()
	time.Sleep(100 * time.Millisecond)
	if n := drains.Load(); n != 0 {
		t.Errorf("%d drains ran after Stop", n)
	}
}

type countingQueue struct{ onDue func() }

func (q *countingQueue) DueDeliveries(int) ([]Queued, error) {
	q.onDue()
	return nil, nil
}
func (q *countingQueue) MarkSent(int64) error                            { return nil }
func (q *countingQueue) MarkFailed(int64, string, time.Time, bool) error { return nil }

// The wait is bounded, or an app that cannot close is the new bug. A relay that
// has stopped answering holds a send for the sink's whole timeout — 20 seconds
// by default for email — and Stop must not inherit that.
func TestStopGivesUpOnASendThatNeverReturns(t *testing.T) {
	q := &blockingQueue{entered: make(chan struct{}), release: make(chan struct{})}
	d := NewDispatcher(q, func(string) (Sink, bool) { return nil, false }, time.Hour)
	d.Start()

	d.Wake()
	<-q.entered

	start := time.Now()
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()

	select {
	case <-done:
	case <-time.After(StopGrace + 3*time.Second):
		t.Fatal("Stop never gave up — closing the window would hang")
	}
	if elapsed := time.Since(start); elapsed < StopGrace {
		t.Errorf("Stop gave up after %s, before the %s grace period", elapsed, StopGrace)
	}
	close(q.release)
}

// And a drain in progress must stop starting NEW sends once shutdown begins,
// so what Stop waits for is one delivery rather than the rest of the batch.
func TestDrainStopsStartingSendsAfterStop(t *testing.T) {
	const batchSize = 20

	var attempted atomic.Int32
	proceed := make(chan struct{})
	firstSend := make(chan struct{})
	var once sync.Once

	batch := make([]Queued, batchSize)
	for i := range batch {
		batch[i] = Queued{ID: int64(i + 1), SinkID: "s", Event: events.Event{ID: "e"}}
	}
	q := &scriptedQueue{batch: batch}

	sink := sinkFunc(func(events.Event, string, string) error {
		attempted.Add(1)
		once.Do(func() { close(firstSend) })
		<-proceed
		return nil
	})

	d := NewDispatcher(q, func(string) (Sink, bool) { return sink, true }, time.Hour)
	d.Start()
	d.Wake()

	<-firstSend
	stopped := make(chan struct{})
	go func() { d.Stop(); close(stopped) }()
	// Let Stop close `done` before the held send returns.
	time.Sleep(100 * time.Millisecond)
	close(proceed)
	<-stopped

	if n := attempted.Load(); n != 1 {
		t.Errorf("%d of %d deliveries were attempted after Stop; want just the one already in flight",
			n, batchSize)
	}
}

type scriptedQueue struct {
	batch  []Queued
	served atomic.Bool

	// The drain writes these from several goroutines at once.
	mu     sync.Mutex
	sent   []int64
	failed []recordedFailure
}

func (q *scriptedQueue) DueDeliveries(int) ([]Queued, error) {
	if q.served.Swap(true) {
		return nil, nil
	}
	return q.batch, nil
}

func (q *scriptedQueue) MarkSent(id int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sent = append(q.sent, id)
	return nil
}

func (q *scriptedQueue) MarkFailed(id int64, msg string, _ time.Time, dead bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed = append(q.failed, recordedFailure{id: id, msg: msg, dead: dead})
	return nil
}

type sinkFunc func(events.Event, string, string) error

func (f sinkFunc) Send(e events.Event, subject, body string) error { return f(e, subject, body) }
func (f sinkFunc) Describe() string                                { return "test sink" }
