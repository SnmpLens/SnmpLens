package engine

import (
	"fmt"
	"log"
	"sync"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/storage"
)

// Routing off the producer's goroutine.
//
// A trap arrives on gosnmp's UDP receive loop, which is STRICTLY SERIAL: one
// goroutine, ReadFromUDP then handler then the next read, with no goroutine per
// datagram. Every millisecond the handler spends is a millisecond not reading
// the socket, and what does not fit the socket buffer is dropped by the kernel
// before Go ever sees it — silently, with no error and nothing to count.
//
// Measured, per event: the journal insert costs 0.97 ms and routing another
// 1.30 ms, essentially all of it a SECOND write transaction. Moving that off the
// loop roughly halves the service time. Batching is what makes it work rather
// than just relocating the queue: unbatched, the router costs the same 1.0 ms
// per event and cannot keep up with a producer it just made twice as fast. At 50
// events per transaction it costs 0.13 ms.
//
// The INSERT stays on the producer's goroutine. gosnmp sends an INFORM's
// acknowledgement after the handler returns, and acknowledging a confirmed
// notification before it is durably journalled would be a lie.

const (
	// routeQueueDepth is how many journalled events may be waiting to be routed.
	//
	// A full queue is not a failure: the producer routes inline instead, which
	// is what it did before any of this existed. So this trades memory for how
	// large a burst can be absorbed before falling back to the slower path.
	routeQueueDepth = 2048

	// routeBatchSize bounds ONE transaction, and therefore how long the write
	// lock is held against the trap listener's insert.
	//
	// Not the whole queue. SQLite serialises writers; 10 000 rows was measured
	// at 392 ms and a replay of the full retention caps would be about 14
	// seconds — long enough for the busy timeout to expire and a trap to be lost
	// while its INFORM is acknowledged. At 50 events a transaction measures
	// about 4 ms.
	routeBatchSize = 50

	// routeFlushInterval bounds how long a queued event waits when the batch
	// never fills. An alert is not urgent to the millisecond, but it should not
	// sit behind a quiet period either.
	routeFlushInterval = 250 * time.Millisecond

	// poolWatchInterval is how often the database connection pool is sampled.
	//
	// Waiting for a pooled connection is INVISIBLE: pkg/storage uses the
	// context-free db.Query/db.Exec/db.Begin, so a caller that cannot get one
	// simply blocks, and busy_timeout does not cover it — that governs SQLite's
	// lock, not Go's pool. Measured with all four connections held, InsertEvent
	// blocked 748 ms and returned no error at all.
	poolWatchInterval = 30 * time.Second

	// poolWaitThreshold is how much waiting in one window is worth telling the
	// operator about.
	//
	// A healthy run measures single-digit milliseconds over several seconds, so
	// three seconds in thirty is an order of magnitude past normal and means the
	// database is the bottleneck rather than the network.
	poolWaitThreshold = 3 * time.Second

	// routeStopGrace bounds the wait at shutdown, for the same reason
	// notify.StopGrace does: an application that will not close is worse than a
	// flush that finishes on the next launch, which the watermark makes safe.
	routeStopGrace = 5 * time.Second

	// routeRetryMin and routeRetryMax bound how fast a FAILING flush is retried.
	//
	// A batch that cannot be written is put back, and putting it back into the
	// queue meant the loop read it straight out again: measured at 1244 failed
	// database transactions in 300 ms, about 4 000 a second, each one a write
	// attempt against a database that had just refused one. Shutdown never
	// converged either — drain kept finding the events it had just requeued, so
	// stopping took the whole 5 s grace period every time.
	//
	// Failed events wait in `deferred` instead, held by the router goroutine
	// and retried on a doubling backoff. They stay in `inflight` throughout, so
	// the watermark cannot pass them whatever happens here.
	routeRetryMin = 1 * time.Second
	routeRetryMax = 30 * time.Second
)

// eventRouter carries journalled events from their producer to the outbox.
type eventRouter struct {
	eng   *Engine
	queue chan events.Event
	done  chan struct{}
	loop  sync.WaitGroup
	once  sync.Once

	// mu guards the in-flight bookkeeping below.
	//
	// seq order is NOT routing order: seq is allocated by the insert, and the
	// event reaches the queue some time later — measured 2.4 ms, p95 7.5 ms.
	// Two events can therefore be handed over in the opposite order to their
	// seqs, so the watermark cannot be "the highest seq in the batch I just
	// flushed": that would strand a lower-seq event still waiting.
	//
	// What is safe is the LOWEST seq still in flight, minus one.
	// Retry bookkeeping for batches that could not be written, touched only by
	// the router goroutine.
	deferred []events.Event
	retryAt  time.Time
	backoff  time.Duration

	// Pool-wait bookkeeping, touched only by the router goroutine.
	lastPoolCount int64
	lastPoolWait  time.Duration
	lastPoolCheck time.Time
	poolReported  bool

	mu        sync.Mutex
	inflight  map[int64]bool
	highest   int64 // the highest seq ever accepted
	confirmed int64 // the highest seq known to be written

	// enqueue overrides the storage write, and exists so a test can fail THAT
	// specifically. Closing the database fails routedGroupsFor first, which is
	// a different path with a different answer, so the one case that matters
	// here — the configuration reads fine and the write does not — has no
	// other way to be reached.
	enqueue func([]storage.RoutedGroup, int64) error
}

func (r *eventRouter) enqueueRouted(groups []storage.RoutedGroup, mark int64) error {
	if r.enqueue != nil {
		return r.enqueue(groups, mark)
	}
	return r.eng.storage.EnqueueRouted(groups, mark)
}

func newEventRouter(eng *Engine) *eventRouter {
	return &eventRouter{
		eng:      eng,
		queue:    make(chan events.Event, routeQueueDepth),
		done:     make(chan struct{}),
		inflight: map[int64]bool{},
	}
}

// accept registers an event as owed and hands it to the router.
//
// Returns false when the queue is full, and the caller must then route inline —
// never drop. The fallback is exactly today's behaviour, so the worst case of
// this whole design is the performance we had before it.
func (r *eventRouter) accept(e events.Event) bool {
	r.mu.Lock()
	if e.Seq > r.highest {
		r.highest = e.Seq
	}
	r.inflight[e.Seq] = true
	r.mu.Unlock()

	select {
	case r.queue <- e:
		return true
	default:
		// No room. Take it back out of flight; the caller routes it now.
		r.settle(nil, e.Seq)
		return false
	}
}

// markIfSettled returns the watermark that WOULD be safe if seqs were settled,
// without settling them.
//
// The order matters and it was wrong. settle ran before EnqueueRouted, so a
// failed write left the batch out of `inflight` and `confirmed` already moved
// past it. The comment at the failure site was right that the STORED watermark
// stays put — EnqueueRouted is atomic — and that a restart therefore replays
// them. What it missed is the running process: the next successful flush
// computes its mark from an `inflight` those seqs are no longer in, and writes
// a watermark ABOVE a batch whose deliveries were never written. Neither
// delivered nor replayed, which is the single outcome this whole design exists
// to prevent.
//
// So nothing settles until the write has succeeded.
func (r *eventRouter) markIfSettled(seqs []int64) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	settling := make(map[int64]struct{}, len(seqs))
	for _, s := range seqs {
		settling[s] = struct{}{}
	}
	lowest := int64(0)
	for s := range r.inflight {
		if _, going := settling[s]; going {
			continue
		}
		if lowest == 0 || s < lowest {
			lowest = s
		}
	}
	mark := r.highest
	if lowest > 0 {
		mark = lowest - 1
	}
	if mark < r.confirmed {
		mark = r.confirmed
	}
	return mark
}

// settle marks seqs as no longer owed and returns the watermark that is now
// safe to record.
func (r *eventRouter) settle(seqs []int64, single int64) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range seqs {
		delete(r.inflight, s)
	}
	if single != 0 {
		delete(r.inflight, single)
	}

	// The lowest seq still owed bounds everything: nothing at or above it can
	// be claimed as routed.
	lowest := int64(0)
	for s := range r.inflight {
		if lowest == 0 || s < lowest {
			lowest = s
		}
	}
	mark := r.highest
	if lowest > 0 {
		mark = lowest - 1
	}
	if mark > r.confirmed {
		r.confirmed = mark
	}
	return r.confirmed
}

func (r *eventRouter) start() {
	r.loop.Add(1)
	go func() {
		defer r.loop.Done()
		ticker := time.NewTicker(routeFlushInterval)
		defer ticker.Stop()

		batch := make([]events.Event, 0, routeBatchSize)
		for {
			select {
			case <-r.done:
				// Drain what is already queued, then stop. The queue is closed
				// to new work by the caller stopping its producers first.
				r.drain(&batch)
				return

			case e := <-r.queue:
				batch = append(batch, e)
				if len(batch) >= routeBatchSize {
					r.flush(&batch)
				}

			case <-ticker.C:
				r.takeDeferred(&batch)
				r.flush(&batch)
				r.watchPool()
			}
		}
	}()
}

// drain empties the queue at shutdown, in bounded transactions.
func (r *eventRouter) drain(batch *[]events.Event) {
	for {
		select {
		case e := <-r.queue:
			*batch = append(*batch, e)
			if len(*batch) >= routeBatchSize {
				r.flush(batch)
			}
		default:
			r.flush(batch)
			return
		}
	}
}

// defer1 holds an event for a later attempt.
//
// Bounded: past routeQueueDepth the surplus is dropped FROM THE LIST ONLY. It
// stays in `inflight`, so the watermark cannot pass it and the next launch
// replays it from the journal — which is the same guarantee a crash would
// give, and strictly better than growing without limit inside a process that
// is already failing to write.
func (r *eventRouter) defer1(e events.Event) {
	if len(r.deferred) >= routeQueueDepth {
		return
	}
	r.deferred = append(r.deferred, e)
}

// failedFlush doubles the retry delay.
func (r *eventRouter) failedFlush() {
	if r.backoff == 0 {
		r.backoff = routeRetryMin
	} else if r.backoff < routeRetryMax {
		r.backoff *= 2
		if r.backoff > routeRetryMax {
			r.backoff = routeRetryMax
		}
	}
	r.retryAt = time.Now().Add(r.backoff)
}

// takeDeferred moves due events back into the batch, bounded by the batch size
// so one transaction stays one transaction.
func (r *eventRouter) takeDeferred(batch *[]events.Event) {
	if len(r.deferred) == 0 || time.Now().Before(r.retryAt) {
		return
	}
	room := routeBatchSize - len(*batch)
	if room <= 0 {
		return
	}
	if room > len(r.deferred) {
		room = len(r.deferred)
	}
	*batch = append(*batch, r.deferred[:room]...)
	r.deferred = append(r.deferred[:0], r.deferred[room:]...)
}

// flush routes a batch and writes it, with the watermark, in one transaction.
func (r *eventRouter) flush(batch *[]events.Event) {
	if len(*batch) == 0 {
		return
	}
	pending := *batch
	*batch = (*batch)[:0]

	groups := make([]storage.RoutedGroup, 0, len(pending))
	seqs := make([]int64, 0, len(pending))
	routed := make([]events.Event, 0, len(pending))
	var failed int
	for _, e := range pending {
		g, err := r.eng.routedGroupsFor(e)
		if err != nil {
			// Not routed, so it must NOT be settled: leaving it in flight pins
			// the watermark below it, and the next launch replays it. Claiming
			// it here would deliver it nowhere and replay it never.
			//
			// Best effort at retrying it in this process too; if the queue is
			// full, the watermark simply stays put until a restart.
			failed++
			r.defer1(e)
			continue
		}
		seqs = append(seqs, e.Seq)
		routed = append(routed, e)
		groups = append(groups, g...)
	}
	if failed > 0 {
		log.Printf("notify: %d of %d events could not be routed and stay above the "+
			"watermark", failed, len(pending))
	}

	mark := r.markIfSettled(seqs)
	if err := r.enqueueRouted(groups, mark); err != nil {
		// NOTHING has been settled at this point, so the batch is still owed:
		// the watermark cannot pass it in this process, and the stored one has
		// not moved either, so a restart replays it. Best effort at another
		// attempt here too — and if the queue is full, staying owed is the
		// safe outcome, since a replay is INSERT OR IGNORE against
		// UNIQUE(event_id, sink_id).
		for _, e := range routed {
			r.defer1(e)
		}
		r.failedFlush()
		log.Printf("notify: writing %d routed events failed; retrying in %s (they stay "+
			"owed, so the watermark cannot pass them): %v", len(routed), r.backoff, err)
		return
	}
	r.settle(seqs, 0)
	// A write got through, so whatever was wrong is not wrong any more.
	r.backoff = 0
	r.retryAt = time.Time{}
	if r.eng.dispatcher != nil {
		r.eng.dispatcher.Wake()
	}
}

// watchPool reports a database that has become the bottleneck.
//
// Edge-triggered: it says so once when the waiting crosses the threshold and
// stays quiet until a window comes back clean, so a busy hour produces one event
// rather than one every thirty seconds.
//
// It does NOT try to fix the wait. Raising the pool was measured removing the
// waits entirely and making InsertEvent slightly SLOWER — four connections gave
// a 5.53 ms average with 5541 waits totalling 650 ms, sixteen gave 0 waits and
// 6.19 ms — because the real serialiser is SQLite's single writer. And a
// deadline on the trap insert would lose the event and leave an INFORM
// unacknowledged, which is worse than waiting. What was missing was not a fix
// but a way to know.
func (r *eventRouter) watchPool() {
	if r.eng.storage == nil {
		return
	}
	now := time.Now()
	if !r.lastPoolCheck.IsZero() && now.Sub(r.lastPoolCheck) < poolWatchInterval {
		return
	}

	count, waited := r.eng.storage.PoolWaits()
	if r.lastPoolCheck.IsZero() {
		r.lastPoolCheck, r.lastPoolCount, r.lastPoolWait = now, count, waited
		return
	}

	window := now.Sub(r.lastPoolCheck)
	deltaCount := count - r.lastPoolCount
	deltaWait := waited - r.lastPoolWait
	r.lastPoolCheck, r.lastPoolCount, r.lastPoolWait = now, count, waited

	if deltaWait < poolWaitThreshold {
		// Clean window: arm the report again.
		r.poolReported = false
		return
	}
	if r.poolReported {
		return
	}
	r.poolReported = true

	share := float64(deltaWait) / float64(window) * 100
	r.eng.recordEvent(events.Event{
		Category: events.CategorySystem,
		Kind:     events.KindSystemInfo,
		Severity: events.SevWarning.String(),
		TitleKey: "events.kind." + events.KindSystemInfo,
		Params: map[string]any{
			"detail": fmt.Sprintf("database contention: %d waits for a connection "+
				"totalling %s over %s (%.0f%% of the time)",
				deltaCount, deltaWait.Round(time.Millisecond), window.Round(time.Second), share),
		},
		Summary: fmt.Sprintf("The database is the bottleneck: %s of waiting for a "+
			"connection in the last %s", deltaWait.Round(time.Millisecond), window.Round(time.Second)),
	}, "")
}

// stop drains what is queued and waits, bounded.
func (r *eventRouter) stop() {
	r.once.Do(func() { close(r.done) })

	finished := make(chan struct{})
	go func() {
		r.loop.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(routeStopGrace):
		log.Printf("notify: routing was still running after %s; the events it had "+
			"not written stay above the watermark and are replayed next launch",
			routeStopGrace)
	}
}

// replayUnrouted routes everything journalled above the watermark.
//
// This is what makes async routing as durable as doing it inline: a crash
// between the insert and the flush loses nothing, because the journal is the
// record and the watermark says how far routing got.
func (eng *Engine) replayUnrouted() {
	if eng.storage == nil {
		return
	}
	mark, err := eng.storage.RoutedThrough()
	if err != nil {
		log.Printf("notify: could not read how far routing got (%v); not replaying", err)
		return
	}

	total := 0
	for {
		pending, err := eng.storage.EventsAfter(mark, routeBatchSize)
		if err != nil {
			log.Printf("notify: replay stopped after %d events: %v", total, err)
			return
		}
		if len(pending) == 0 {
			break
		}

		groups := make([]storage.RoutedGroup, 0, len(pending))
		for _, e := range pending {
			g, err := eng.routedGroupsFor(e)
			if err != nil {
				// Stop rather than skip: advancing past an event that could not
				// be routed would lose it for good, and replay is the last
				// chance it gets.
				log.Printf("notify: replay stopped at seq %d after %d events: %v",
					e.Seq, total, err)
				return
			}
			groups = append(groups, g...)
		}
		mark = pending[len(pending)-1].Seq
		if err := eng.storage.EnqueueRouted(groups, mark); err != nil {
			log.Printf("notify: replay stopped after %d events: %v", total, err)
			return
		}
		total += len(pending)
	}

	if total > 0 {
		log.Printf("notify: replayed %d events that were journalled but not routed", total)
		if eng.dispatcher != nil {
			eng.dispatcher.Wake()
		}
	}
}
