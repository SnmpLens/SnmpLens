package engine

import (
	"testing"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
)

// Switching a sink off must stop it being written to, not turn every event into
// an alarm about it.
//
// routeEvent queued a delivery for whatever sink a route named, and only the
// dispatcher looked at Enabled — where a disabled sink cannot be resolved and
// the delivery is dead-lettered. A dead letter is a MAJOR system event, so
// disabling the mail sink for the weekend answered every single event with a
// major alarm saying the mail sink could not be reached. That is the opposite
// of what the switch means.
func TestADisabledSinkIsNotQueued(t *testing.T) {
	app := newTestApp(t)

	off, err := app.storage.SaveSink(notify.SinkConfig{
		Name: "weekend", Kind: notify.SinkWebhook, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	on, err := app.storage.SaveSink(notify.SinkConfig{
		Name: "always", Kind: notify.SinkWebhook, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.storage.SaveRoute(notify.Route{
		Name: "everything", Enabled: true, SinkIDs: []string{off.ID, on.ID},
	}); err != nil {
		t.Fatal(err)
	}

	app.routeEvent(events.Event{
		Category: events.CategoryThreshold,
		Kind:     events.KindThresholdOpened,
		Severity: events.SevMajor.String(),
		Summary:  "over the line",
	})

	queued, err := app.storage.ListDeliveries("", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queued {
		if q.SinkID == off.ID {
			t.Errorf("a delivery was queued for the sink that is switched off")
		}
	}
	var reachedEnabled bool
	for _, q := range queued {
		if q.SinkID == on.ID {
			reachedEnabled = true
		}
	}
	if !reachedEnabled {
		t.Error("the enabled sink got nothing — the filter is too wide")
	}
}

// A route naming a sink that no longer exists must not queue either: nothing
// can ever deliver it, so the row would only ever become a dead letter.
func TestARouteNamingAMissingSinkQueuesNothing(t *testing.T) {
	app := newTestApp(t)

	if _, err := app.storage.SaveRoute(notify.Route{
		Name: "stale", Enabled: true, SinkIDs: []string{"a-sink-that-was-deleted"},
	}); err != nil {
		t.Fatal(err)
	}

	app.routeEvent(events.Event{
		Category: events.CategoryThreshold,
		Kind:     events.KindThresholdOpened,
		Severity: events.SevMajor.String(),
		Summary:  "over the line",
	})

	queued, err := app.storage.ListDeliveries("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("%d deliveries queued for a sink that does not exist", len(queued))
	}
}
