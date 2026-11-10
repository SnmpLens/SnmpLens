package engine

import (
	"strings"
	"testing"
	"time"

	"SnmpLens/pkg/events"
)

// Waiting for a pooled database connection is invisible.
//
// pkg/storage uses the context-free db.Query/db.Exec/db.Begin throughout, so a
// caller that cannot get one of the four connections simply blocks — and
// busy_timeout does not cover it, since that governs SQLite's lock rather than
// Go's pool. Measured with all four held: InsertEvent blocked 748 ms and
// returned err=nil, with nothing in any log.
//
// The fix is NOT a bigger pool. Measured under three writers and two readers:
// four connections gave InsertEvent a 5.53 ms average with 5541 waits totalling
// 650 ms; sixteen gave 0 waits and a 6.19 ms average — the waits disappear and
// the path gets slightly slower, because the real serialiser is SQLite's single
// writer. What was missing was not a fix but a way to know.

// holdPool occupies every connection for a while, using READ transactions so
// SQLite's write lock is not what is being measured.
func holdPool(t *testing.T, eng *Engine, conns int, for_ time.Duration) {
	t.Helper()
	for i := 0; i < conns; i++ {
		tx, err := eng.storage.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var n int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
		go func() {
			time.Sleep(for_)
			_ = tx.Rollback()
		}()
	}
	time.Sleep(30 * time.Millisecond)
}

func TestPoolWaitsAreVisible(t *testing.T) {
	eng := newTestApp(t)

	before, beforeWait := eng.storage.PoolWaits()
	if before != 0 || beforeWait != 0 {
		t.Fatalf("a fresh pool already reports %d waits totalling %v", before, beforeWait)
	}

	holdPool(t, eng, 4, 400*time.Millisecond)

	start := time.Now()
	if _, err := eng.storage.InsertEvent(trapEvent(1), ""); err != nil {
		t.Fatal(err)
	}
	blocked := time.Since(start)

	count, waited := eng.storage.PoolWaits()
	t.Logf("InsertEvent blocked %v with no error; the pool reports %d waits "+
		"totalling %v", blocked, count, waited)

	if count == 0 {
		t.Error("the pool reports no waits despite a caller having blocked on it; " +
			"the stall would be invisible")
	}
	if waited < 50*time.Millisecond {
		t.Errorf("the pool reports only %v of waiting for a %v stall", waited, blocked)
	}
}

// The report is edge-triggered: once when it crosses, then quiet until a window
// comes back clean. A busy hour must produce one event, not one every thirty
// seconds.
func TestContentionIsReportedOnceUntilItClears(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	countSystem := func() int {
		all, err := eng.storage.EventsAfter(0, 10000)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range all {
			if e.Kind == events.KindSystemInfo {
				n++
			}
		}
		return n
	}

	// A first sample only establishes the baseline.
	r.watchPool()
	if countSystem() != 0 {
		t.Error("the first sample reported something; it has nothing to compare to")
	}

	// Pretend a window has passed with a lot of waiting in it.
	r.lastPoolCheck = time.Now().Add(-poolWatchInterval - time.Second)
	r.lastPoolWait = 0
	r.lastPoolCount = 0
	holdPool(t, eng, 4, 600*time.Millisecond)
	for i := 0; i < 6; i++ {
		go func() { _, _ = eng.storage.InsertEvent(trapEvent(2), "") }()
	}
	time.Sleep(900 * time.Millisecond)

	// Force the threshold to be met by the accumulated waiting.
	if _, waited := eng.storage.PoolWaits(); waited < poolWaitThreshold {
		// The machine was fast enough that the stall did not accumulate. Drive
		// the decision directly instead of making the test depend on timing.
		r.lastPoolWait = -poolWaitThreshold
	}
	r.lastPoolCheck = time.Now().Add(-poolWatchInterval - time.Second)
	r.watchPool()

	if got := countSystem(); got != 1 {
		t.Fatalf("%d contention events after crossing the threshold, want 1", got)
	}

	// Still contended, another window: no second event.
	r.lastPoolCheck = time.Now().Add(-poolWatchInterval - time.Second)
	r.lastPoolWait = -poolWaitThreshold
	r.watchPool()
	if got := countSystem(); got != 1 {
		t.Errorf("%d contention events; a busy hour would fill the journal", got)
	}

	// A clean window re-arms it.
	r.lastPoolCheck = time.Now().Add(-poolWatchInterval - time.Second)
	_, waited := eng.storage.PoolWaits()
	r.lastPoolWait = waited
	r.watchPool()
	if r.poolReported {
		t.Error("a clean window did not re-arm the report")
	}
}

// The event has to say what an operator can act on.
func TestTheContentionEventSaysWhatIsWrong(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	r.watchPool()
	r.lastPoolCheck = time.Now().Add(-poolWatchInterval - time.Second)
	r.lastPoolWait = -poolWaitThreshold
	r.watchPool()

	all, err := eng.storage.EventsAfter(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *events.Event
	for i := range all {
		if all[i].Kind == events.KindSystemInfo {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatal("no contention event was recorded")
	}
	if !strings.Contains(strings.ToLower(found.Summary), "database") {
		t.Errorf("the summary does not name the database: %q", found.Summary)
	}
	if found.Severity != events.SevWarning.String() {
		t.Errorf("severity = %q; contention is a warning, not a failure", found.Severity)
	}
	detail, _ := found.Params["detail"].(string)
	if !strings.Contains(detail, "waits") {
		t.Errorf("the detail does not carry the numbers: %q", detail)
	}
}

// Sampling must be cheap enough to sit on a 250 ms ticker, and must not sample
// on every tick.
func TestThePoolIsNotSampledOnEveryTick(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	r.watchPool()
	first := r.lastPoolCheck
	for i := 0; i < 20; i++ {
		r.watchPool()
	}
	if !r.lastPoolCheck.Equal(first) {
		t.Error("the pool was re-sampled inside the interval; on a 250 ms ticker " +
			"that is db.Stats() four times a second forever")
	}
}
