package notify

import (
	"fmt"
	"log"
	"sync"
	"time"

	"SnmpLens/pkg/events"
)

// Queued is one delivery waiting to be sent. It is deliberately self-contained:
// the dispatcher never reads the journal, so event retention can never strand a
// pending or dead delivery.
type Queued struct {
	ID       int64
	SinkID   string
	Subject  string
	Body     string
	Attempts int
	Event    events.Event
}

// Queue is the persistence the dispatcher needs. Implemented by pkg/storage.
type Queue interface {
	DueDeliveries(limit int) ([]Queued, error)
	MarkSent(id int64) error
	MarkFailed(id int64, errMsg string, nextTry time.Time, dead bool) error
}

// SinkResolver returns a live sink for an id, or false when the sink no longer
// exists or is disabled.
type SinkResolver func(sinkID string) (Sink, bool)

// StopGrace is how long Stop waits for a delivery already on the wire.
//
// Long enough for a syslog write or a webhook POST to a receiver that is
// answering, short enough that closing the window never feels stuck.
const StopGrace = 5 * time.Second

// Dispatcher drains the outbox on a ticker and on demand.
type Dispatcher struct {
	Queue    Queue
	Resolve  SinkResolver
	OnResult func(q Queued, err error, dead bool)

	interval time.Duration
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	running  sync.Mutex
	// loop is held by the drain goroutine for its whole life, so Stop can wait
	// for it rather than merely asking it to finish.
	loop sync.WaitGroup
}

// NewDispatcher returns a dispatcher that polls every interval.
func NewDispatcher(q Queue, resolve SinkResolver, interval time.Duration) *Dispatcher {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Dispatcher{
		Queue:    q,
		Resolve:  resolve,
		interval: interval,
		// Buffered: a wake-up must never block the event insert path.
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// Start runs the drain loop until Stop.
func (d *Dispatcher) Start() {
	d.loop.Add(1)
	go func() {
		defer d.loop.Done()
		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-ticker.C:
				d.Drain()
			case <-d.wake:
				d.Drain()
			}
		}
	}()
}

// Stop ends the drain loop and WAITS for a delivery already in flight.
//
// It used to close a channel and return, so the loop finished the pass it was
// in on its own goroutine with nobody waiting. On shutdown that means a send
// still running when the process exits: shutdown returns, Wails tears the
// app down, and an SMTP conversation or an HTTP POST is cut mid-way. The
// delivery is left `pending` with an attempt spent — neither sent nor reported
// as failed.
//
// The wait is BOUNDED. Drain stops starting new deliveries as soon as `done` is
// closed, so what is left to wait for is a single send — but that send is at the
// mercy of a relay that may be timing out, and an app that will not close is
// worse than an interrupted POST. After the grace period the delivery is
// abandoned in place: the row stays `pending` and the next launch picks it up.
//
// Safe to call more than once, and safe on a dispatcher that was never started.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() { close(d.done) })

	finished := make(chan struct{})
	go func() {
		d.loop.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(StopGrace):
		log.Printf("notify: a delivery was still in flight after %s; leaving it pending", StopGrace)
	}
}

// Wake asks for an immediate drain without blocking the caller.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default: // a drain is already pending; nothing to add
	}
}

// Drain sends every delivery that is due. Failures are rescheduled with
// exponential backoff; a permanent failure or too many attempts becomes a dead
// letter, which is kept for the operator to see rather than dropped.
func (d *Dispatcher) Drain() {
	// One drain at a time: a slow SMTP relay must not let the ticker stack up
	// overlapping passes that each retry the same rows.
	if !d.running.TryLock() {
		return
	}
	defer d.running.Unlock()

	if d.Queue == nil || d.Resolve == nil {
		return
	}

	batch, err := d.Queue.DueDeliveries(20)
	if err != nil {
		log.Printf("notify: reading the outbox failed: %v", err)
		return
	}

	// One goroutine per DESTINATION, deliveries within a destination serial.
	//
	// The batch used to be sent one at a time on a single goroutine, so the
	// worst case behind one unreachable SMTP relay was 20 x the 20-second
	// default timeout — around seven minutes of blocked outbox, during which a
	// critical trap routed to a perfectly healthy webhook waits. The Wake()
	// that trap triggered is dropped by TryLock, so it then waits for the
	// 15-second ticker on top. Measured: with one sink taking 1.5s, a healthy
	// sink's delivery went out 1.5s late.
	//
	// Serial WITHIN a destination on purpose: twenty parallel SMTP
	// conversations with one relay is how a sender gets rate-limited or
	// blocked, and the deliveries to one destination are the ones with a
	// reason to be ordered.
	bySink := map[string][]Queued{}
	order := make([]string, 0, len(batch))
	for _, q := range batch {
		if _, seen := bySink[q.SinkID]; !seen {
			order = append(order, q.SinkID)
		}
		bySink[q.SinkID] = append(bySink[q.SinkID], q)
	}

	var wg sync.WaitGroup
	for _, sinkID := range order {
		wg.Add(1)
		go func(queue []Queued) {
			defer wg.Done()
			for _, q := range queue {
				// Shutdown has begun: start nothing new. The rows stay
				// `pending` and the next launch picks them up, which is what a
				// durable outbox is for.
				select {
				case <-d.done:
					return
				default:
				}
				d.deliver(q)
			}
		}(bySink[sinkID])
	}
	wg.Wait()
}

// deliver sends one queued delivery and records what happened.
//
// The recover is not decoration. This runs on a background goroutine, where an
// unrecovered panic ends the PROCESS — taking the poll clock, the trap listener
// and the window with it, because one destination's formatting or TLS handling
// hit a nil. A bug in a sink must cost that delivery and nothing more.
func (d *Dispatcher) deliver(q Queued) {
	sink, ok := d.Resolve(q.SinkID)
	if !ok {
		// The sink was deleted or disabled after the event was queued.
		// Dead-letter it rather than retrying forever against nothing.
		d.fail(q, errSinkMissing{q.SinkID}, true)
		return
	}

	sendErr := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the destination panicked: %v", r)
			}
		}()
		return sink.Send(q.Event, q.Subject, q.Body)
	}()

	if sendErr == nil {
		if err := d.Queue.MarkSent(q.ID); err != nil {
			log.Printf("notify: could not mark delivery %d sent: %v", q.ID, err)
		}
		if d.OnResult != nil {
			d.OnResult(q, nil, false)
		}
		return
	}

	dead := Permanent(sendErr) || q.Attempts+1 >= MaxAttempts
	d.fail(q, sendErr, dead)
}

func (d *Dispatcher) fail(q Queued, sendErr error, dead bool) {
	next := time.Now().Add(Backoff(q.Attempts + 1))
	if err := d.Queue.MarkFailed(q.ID, sendErr.Error(), next, dead); err != nil {
		log.Printf("notify: could not record delivery failure for %d: %v", q.ID, err)
	}
	if d.OnResult != nil {
		d.OnResult(q, sendErr, dead)
	}
}

type errSinkMissing struct{ id string }

func (e errSinkMissing) Error() string {
	return "sink " + e.id + " no longer exists or is disabled"
}
