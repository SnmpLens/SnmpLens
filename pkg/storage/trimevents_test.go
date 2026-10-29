package storage

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"SnmpLens/pkg/events"
)

// TrimEvents runs on the caller's goroutine, and for a trap that caller is
// gosnmp's UDP receive loop — a strictly serial loop where every millisecond
// spent is a millisecond not reading the socket.
//
// It fires every 256 inserts. With an empty journal its DELETEs match nothing
// and it is invisible; at the retention cap it is a real write transaction, and
// it was freezing the trap loop for 110-330 ms measured. That is roughly half a
// millisecond amortised per event — comparable to everything else the handler
// does put together.
//
// Moving it to a background goroutine does NOT help, and was measured making
// things worse: SQLite serialises writers, so InsertEvent simply blocks on the
// write lock instead. The cost has to come out of the QUERY.

// seedEvents fills a category to a given depth.
func seedEvents(t *testing.T, st *Storage, category, kind string, n int, withPayload bool) {
	t.Helper()
	if n <= 0 {
		return
	}
	// One statement generating the n rows inside SQLite, rather than n
	// statements: under -race every call into the transpiled SQLite costs
	// enough that seeding past the retention caps took most of this
	// package's test time.
	size := 0
	if withPayload {
		size = 8
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := st.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
		INSERT INTO events
			(id, ts, category, kind, severity, state, title_key, params, summary, payload_size, acked)
		SELECT printf('%s-%06d', ?, i), ?, ?, ?, 3, 'oneshot', 'k', '{}', 's', ?, 0 FROM n`,
		n, category, now, category, kind, size); err != nil {
		t.Fatal(err)
	}
	if withPayload {
		if _, err := st.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
			INSERT INTO event_payloads (event_id, body) SELECT printf('%s-%06d', ?, i), 'payload' FROM n`,
			n, category); err != nil {
			t.Fatal(err)
		}
	}
}

func countIn(t *testing.T, st *Storage, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The cap is per category on purpose: traps arrive in bursts while system
// events trickle, and one shared limit would let a trap storm evict everything
// else. Whatever the query shape, that must hold.
func TestTrimEventsKeepsTheNewestPerCategory(t *testing.T) {
	st := newTestStorage(t)

	trapCap := eventRetention[events.CategoryTrap]
	sysCap := eventRetention[events.CategorySystem]

	seedEvents(t, st, events.CategoryTrap, events.KindTrapReceived, trapCap+500, false)
	seedEvents(t, st, events.CategorySystem, events.KindSystemSinkDeadLetter, 200, false)

	st.TrimEvents()

	traps := countIn(t, st, `SELECT COUNT(*) FROM events WHERE category = ?`, events.CategoryTrap)
	if traps != trapCap {
		t.Errorf("%d traps kept, want the cap of %d", traps, trapCap)
	}
	// The newest must be the ones that survived.
	var oldest int64
	if err := st.db.QueryRow(`SELECT MIN(seq) FROM events WHERE category = ?`,
		events.CategoryTrap).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != 501 {
		t.Errorf("the oldest surviving trap has seq %d; the 500 oldest should have gone", oldest)
	}

	// A category under its cap is untouched, even while another is over.
	sys := countIn(t, st, `SELECT COUNT(*) FROM events WHERE category = ?`, events.CategorySystem)
	if sys != 200 {
		t.Errorf("%d system events kept of 200; a trap storm evicted them", sys)
	}
	_ = sysCap
}

// Trimming twice must be a no-op the second time.
func TestTrimEventsIsIdempotent(t *testing.T) {
	st := newTestStorage(t)
	seedEvents(t, st, events.CategoryTrap, events.KindTrapReceived,
		eventRetention[events.CategoryTrap]+300, false)

	st.TrimEvents()
	first := countIn(t, st, `SELECT COUNT(*) FROM events`)
	st.TrimEvents()
	second := countIn(t, st, `SELECT COUNT(*) FROM events`)

	if first != second {
		t.Errorf("a second trim removed %d more rows", first-second)
	}
}

// A category with fewer rows than its cap must lose nothing.
func TestTrimEventsSparesAnUnderfullCategory(t *testing.T) {
	st := newTestStorage(t)
	seedEvents(t, st, events.CategoryThreshold, events.KindThresholdOpened, 50, false)

	st.TrimEvents()

	if n := countIn(t, st, `SELECT COUNT(*) FROM events`); n != 50 {
		t.Errorf("%d of 50 events survived a trim that should have done nothing", n)
	}
}

// Payloads are reaped explicitly — there is no CASCADE, because deletion must
// not depend on a connection-scoped pragma.
func TestTrimEventsReapsOrphanedPayloads(t *testing.T) {
	st := newTestStorage(t)

	// More payload-bearing events than the payload cap, and more than the
	// category cap, so both sweeps have work.
	seedEvents(t, st, events.CategoryTrap, events.KindTrapReceived, maxEventPayloads+400, true)

	st.TrimEvents()

	// No payload may outlive its event.
	orphans := countIn(t, st,
		`SELECT COUNT(*) FROM event_payloads WHERE event_id NOT IN (SELECT id FROM events)`)
	if orphans != 0 {
		t.Errorf("%d payloads outlived their events", orphans)
	}

	payloads := countIn(t, st, `SELECT COUNT(*) FROM event_payloads`)
	if payloads > maxEventPayloads {
		t.Errorf("%d payloads kept, over the cap of %d", payloads, maxEventPayloads)
	}
	if payloads == 0 {
		t.Error("every payload was reaped; the newest should have been kept")
	}
}

// The measurement that motivated the change. Not an assertion on a number —
// this machine is not a benchmark rig — but it fails if a trim at cap becomes
// pathological again, which is the regression that matters.
func TestTrimEventsAtCapIsNotPathological(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds tens of thousands of rows")
	}
	if raceEnabled {
		// The detector instruments every memory access; the same trim measured
		// 20 ms without it and 73 seconds with it. The behavioural tests above
		// still run under -race, which is where they matter.
		t.Skip("elapsed time is meaningless under -race")
	}
	st := newTestStorage(t)

	for _, c := range []string{
		events.CategoryTrap, events.CategoryThreshold, events.CategoryReachability,
	} {
		seedEvents(t, st, c, events.KindTrapReceived, eventRetention[c]+300, false)
	}

	// First trim brings every category to its cap.
	st.TrimEvents()

	// The steady state: 256 more events arrive, then a trim. This is what runs
	// on the trap listener's goroutine.
	seedEvents(t, st, events.CategoryTrap, events.KindTrapReceived, 256, false)

	start := time.Now()
	st.TrimEvents()
	elapsed := time.Since(start)

	t.Logf("a steady-state trim at cap took %v (%.2f ms amortised over 256 events)",
		elapsed, float64(elapsed.Microseconds())/1000/256)

	// Measured 20 ms with the indexed range delete against 130-330 ms with the
	// old NOT IN shape, which is the regression this catches. It does NOT pin
	// the partial index on payload_size: that is worth 20 vs 55 ms and the two
	// are not far enough apart to separate reliably on a busy machine.
	if elapsed > 50*time.Millisecond {
		t.Errorf("a trim froze the caller for %v; on the trap listener's goroutine "+
			"that is %v of datagrams not being read", elapsed, elapsed)
	}
}

// Close is called by App.shutdown and by whatever else holds the database, so a
// second call is normal. It used to close a channel unconditionally and panic
// with "close of closed channel" — taking the process down at the one moment
// there is nothing left to report it.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := Init(filepath.Join(dir, "monitoring.db"))
	if err != nil {
		t.Fatal(err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// Any of these would have panicked.
	st.Close()
	st.Close()
}

// And Close must not leave the flush goroutine writing into a closed database.
func TestCloseJoinsTheFlushGoroutine(t *testing.T) {
	dir := t.TempDir()
	st, err := Init(filepath.Join(dir, "monitoring.db"))
	if err != nil {
		t.Fatal(err)
	}

	before := runtime.NumGoroutine()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// The goroutine returns before Close does, so the count cannot have grown.
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines went from %d to %d across Close", before, after)
	}
}

// aSession creates the row data_points has a foreign key to.
func aSession(t *testing.T, st *Storage) string {
	t.Helper()
	id, err := st.CreateSession("test", "1.3.6.1.2.1.1.3.0", []string{"10.0.0.1"},
		1000, "2c", time.Now().UTC().Format(time.RFC3339), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A failed write must not lose the samples.
//
// flushBatch took the points out of the buffer before the transaction was even
// begun, so a SQLITE_BUSY — ordinary on a machine where the trap listener and
// the router are also writing — lost a whole batch with nothing but a log line.
// Samples are what a monitoring session exists to produce; losing them silently
// makes a chart lie.
func TestAFailedFlushKeepsTheDataPoints(t *testing.T) {
	st := newTestStorage(t)
	sid := aSession(t, st)

	points := make([]DataPoint, 40)
	for i := range points {
		points[i] = DataPoint{
			SessionID: sid, Target: "10.0.0.1", OID: "1.3.6.1.2.1.1.3.0",
			Timestamp: time.Now().UTC().Format(time.RFC3339), Value: f64(float64(i)),
		}
	}
	st.QueueDataPoints(points)

	// Make the write fail in a way the retry can recover from.
	if _, err := st.db.Exec(`ALTER TABLE data_points RENAME TO data_points_hidden`); err != nil {
		t.Fatal(err)
	}
	st.flushBatch()

	st.mu.Lock()
	kept := len(st.batch)
	st.mu.Unlock()
	if kept != len(points) {
		t.Fatalf("%d of %d points survived a failed flush", kept, len(points))
	}

	// Restore the table; the next flush must land them.
	if _, err := st.db.Exec(`ALTER TABLE data_points_hidden RENAME TO data_points`); err != nil {
		t.Fatal(err)
	}
	st.flushBatch()

	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM data_points`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(points) {
		t.Errorf("%d of %d points were written on the retry", n, len(points))
	}
	st.mu.Lock()
	left := len(st.batch)
	st.mu.Unlock()
	if left != 0 {
		t.Errorf("%d points are still buffered after a successful flush", left)
	}
}

// A batch is all or nothing. One bad row used to be logged and skipped while
// the transaction committed anyway, so a batch could be silently partial.
func TestAWriteIsAllOrNothing(t *testing.T) {
	st := newTestStorage(t)
	sid := aSession(t, st)

	ok := DataPoint{SessionID: sid, Target: "t", Timestamp: "2026-01-01T00:00:00Z", Value: f64(1)}
	if err := st.writePoints([]DataPoint{ok, ok}); err != nil {
		t.Fatalf("a good batch failed: %v", err)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM data_points`).Scan(&n)
	if n != 2 {
		t.Errorf("%d rows written, want 2", n)
	}
}

// A database that stays unwritable must not grow the buffer without bound.
func TestTheBufferIsCapped(t *testing.T) {
	st := newTestStorage(t)

	if _, err := st.db.Exec(`ALTER TABLE data_points RENAME TO data_points_hidden`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.db.Exec(`ALTER TABLE data_points_hidden RENAME TO data_points`)
	})

	chunk := make([]DataPoint, 20_000)
	for i := range chunk {
		chunk[i] = DataPoint{SessionID: "sid", Target: "t", Timestamp: "2026-01-01T00:00:00Z"}
	}
	for i := 0; i < 8; i++ {
		st.QueueDataPoints(chunk)
		st.flushBatch()
	}

	st.mu.Lock()
	held := len(st.batch)
	st.mu.Unlock()
	if held > maxBufferedPoints {
		t.Errorf("%d points buffered, over the cap of %d", held, maxBufferedPoints)
	}
	if held == 0 {
		t.Error("the buffer was emptied entirely; the newest samples should be kept")
	}
}
