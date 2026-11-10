package engine

import (
	"errors"
	"testing"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/storage"
)

// A batch whose WRITE fails must stay owed, so that a later flush cannot claim
// it.
//
// settle ran before EnqueueRouted, and the comment at the failure site argued
// that the stored watermark stays put — true, the write is atomic — and that a
// restart therefore replays. What it missed is the running process: settle had
// already removed those seqs from `inflight` and advanced `confirmed`, so the
// NEXT successful flush computed its mark from an inflight they were no longer
// in and wrote a watermark above a batch whose deliveries were never written.
// Neither delivered nor replayed.
//
// The two flushes below are the whole point. One failing flush proves little;
// what proves the defect is what the SECOND one writes.
func TestAFailedWriteIsNotClaimedByTheNextFlush(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	var wrote []int64
	fail := true
	r.enqueue = func(_ []storage.RoutedGroup, mark int64) error {
		if fail {
			return errors.New("disk full")
		}
		wrote = append(wrote, mark)
		return nil
	}

	// Batch one: seqs 10 and 11. Accepted, taken back out of the queue so the
	// flush is driven by hand.
	for _, seq := range []int64{10, 11} {
		e := trapEvent(int(seq))
		e.Seq = seq
		if !r.accept(e) {
			t.Fatal("the queue refused an event")
		}
		<-r.queue
	}
	first := []events.Event{{Seq: 10, ID: "e10"}, {Seq: 11, ID: "e11"}}
	r.flush(&first)

	// It failed, so both are still owed.
	r.mu.Lock()
	owed := len(r.inflight)
	confirmed := r.confirmed
	r.mu.Unlock()
	if owed == 0 {
		t.Error("a batch whose write failed was marked as done")
	}
	if confirmed >= 10 {
		t.Errorf("the in-memory watermark reached %d despite the write failing", confirmed)
	}

	// The flush requeued them; drop those copies, because this test drives the
	// second batch by hand and a requeued event would be flushed twice.
	for len(r.queue) > 0 {
		<-r.queue
	}

	// Batch two: a later, higher event that writes fine. This is the moment the
	// defect showed — its mark must NOT reach past the batch that failed.
	fail = false
	e := trapEvent(20)
	e.Seq = 20
	r.accept(e)
	<-r.queue
	second := []events.Event{{Seq: 20, ID: "e20"}}
	r.flush(&second)

	if len(wrote) != 1 {
		t.Fatalf("the second flush wrote %d watermarks, want 1", len(wrote))
	}
	if wrote[0] >= 10 {
		t.Errorf("the second flush recorded the watermark at %d, past events 10 and 11 "+
			"whose deliveries were never written: they are neither delivered nor replayed", wrote[0])
	}
}

// The control: with the write succeeding, the watermark still advances. A fix
// that simply never settles would pass the test above and break the feature.
func TestACleanBatchStillAdvancesTheWatermark(t *testing.T) {
	eng := newTestApp(t)
	r := newEventRouter(eng)

	var wrote []int64
	r.enqueue = func(_ []storage.RoutedGroup, mark int64) error {
		wrote = append(wrote, mark)
		return nil
	}

	for _, seq := range []int64{3, 4} {
		e := trapEvent(int(seq))
		e.Seq = seq
		r.accept(e)
		<-r.queue
	}
	batch := []events.Event{{Seq: 3, ID: "e3"}, {Seq: 4, ID: "e4"}}
	r.flush(&batch)

	r.mu.Lock()
	owed := len(r.inflight)
	confirmed := r.confirmed
	r.mu.Unlock()

	if owed != 0 {
		t.Errorf("%d events still owed after a clean flush", owed)
	}
	if confirmed != 4 {
		t.Errorf("in-memory watermark = %d, want 4", confirmed)
	}
	if len(wrote) != 1 || wrote[0] != 4 {
		t.Errorf("watermarks written = %v, want [4]", wrote)
	}
}

// markIfSettled must answer what settle would, without settling — otherwise
// the write records one number and the bookkeeping keeps another.
func TestMarkIfSettledAgreesWithSettle(t *testing.T) {
	eng := newTestApp(t)

	for _, seqs := range [][]int64{{5}, {5, 6}, {5, 6, 7}, {}} {
		r := newEventRouter(eng)
		for _, seq := range []int64{5, 6, 7} {
			e := trapEvent(int(seq))
			e.Seq = seq
			r.accept(e)
			<-r.queue
		}
		predicted := r.markIfSettled(seqs)
		actual := r.settle(seqs, 0)
		if predicted != actual {
			t.Errorf("settling %v: markIfSettled said %d, settle produced %d", seqs, predicted, actual)
		}
	}
}
