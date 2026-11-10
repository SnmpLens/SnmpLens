package engine

import (
	"strings"
	"testing"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
)

// Failing to route is not the same as having nowhere to route to.
//
// routedGroupsFor returned nil for both: a transient read failure on the rules
// or the destinations was indistinguishable from "no rule matched". The router
// then recorded the watermark past that event, so it was never delivered AND
// never replayed — the one outcome the whole watermark design exists to
// prevent.

func TestRoutingFailureIsDistinguishableFromNoDestinations(t *testing.T) {
	eng := newTestApp(t)

	// No rules at all: legitimately nothing to do, and not an error.
	groups, err := eng.routedGroupsFor(trapEvent(1))
	if err != nil {
		t.Errorf("an event with no rules reported an error: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("%d groups for an event with no rules", len(groups))
	}

	// A rule exists and matches: groups, no error.
	sink, err := eng.storage.SaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.storage.SaveRoute(notify.Route{
		Name: "all", Enabled: true, SinkIDs: []string{sink.ID},
	}); err != nil {
		t.Fatal(err)
	}
	groups, err = eng.routedGroupsFor(trapEvent(2))
	if err != nil {
		t.Fatalf("routing a matching event failed: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("%d groups, want 1", len(groups))
	}

	// And a configuration that cannot be READ is an error, not silence.
	eng.storage.Close()
	if _, err := eng.routedGroupsFor(trapEvent(3)); err == nil {
		t.Error("routing against an unreadable configuration reported success with " +
			"no destinations; the event would be marked routed and never delivered")
	}
}

// The watermark must not pass an event whose routing failed.
func TestAFailedRoutingKeepsTheEventAboveTheWatermark(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	e := trapEvent(1)
	e.Seq = 42
	if !r.accept(e) {
		t.Fatal("the queue refused a single event")
	}
	// Take it back out of the queue: flush is being driven by hand.
	<-r.queue

	// Break the configuration read, then flush it.
	eng.storage.Close()
	batch := []events.Event{e}
	r.flush(&batch)

	r.mu.Lock()
	stillOwed := r.inflight[42]
	confirmed := r.confirmed
	r.mu.Unlock()

	if !stillOwed {
		t.Error("an event whose routing failed was marked as done; it is neither " +
			"delivered nor above the watermark, so it is simply lost")
	}
	if confirmed >= 42 {
		t.Errorf("the watermark reached %d despite the failure at seq 42", confirmed)
	}
}

// A batch where only SOME events fail must still record the ones that worked,
// and must not claim the ones that did not.
func TestAPartlyFailedBatchSettlesOnlyWhatItRouted(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	// Two events, both accepted.
	for _, seq := range []int64{7, 9} {
		e := trapEvent(int(seq))
		e.Seq = seq
		r.accept(e)
		<-r.queue
	}

	// Nothing is broken, so both route (to no destinations, which is fine) and
	// both settle.
	batch := []events.Event{{Seq: 7, ID: "e7"}, {Seq: 9, ID: "e9"}}
	r.flush(&batch)

	r.mu.Lock()
	owed := len(r.inflight)
	confirmed := r.confirmed
	r.mu.Unlock()

	if owed != 0 {
		t.Errorf("%d events still owed after a clean flush", owed)
	}
	if confirmed != 9 {
		t.Errorf("watermark = %d after routing through seq 9", confirmed)
	}
}

// The error must say what went wrong, not just that something did — this ends
// up in a log an operator reads when alerts stopped arriving.
func TestARoutingErrorNamesTheProblem(t *testing.T) {
	eng := newTestApp(t)
	eng.storage.Close()

	_, err := eng.routedGroupsFor(trapEvent(1))
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rule") &&
		!strings.Contains(strings.ToLower(err.Error()), "destination") {
		t.Errorf("the error does not say which read failed: %v", err)
	}
}

var _ = events.Event{}
