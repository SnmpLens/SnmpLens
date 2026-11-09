package monitor_test

// Integration checks for the Go poll clock against a real SNMP agent: the
// simulator the application ships, run in-process (pkg/simulator/simtest), so
// they run wherever the unit tests do instead of skipping for want of an agent.
//
// The unit tests in this package drive the scheduler with a stub fetcher, which
// proves the clock and the derived maths but says nothing about whether the
// thing actually talks to an agent. These close that gap, against counters that
// move and wrap the way a device's do.

import (
	"context"
	"sync"
	"testing"
	"time"

	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
	"SnmpLens/pkg/snmp"

	"github.com/gosnmp/gosnmp"
)

const (
	// sysUpTime is a TimeTicks counter that always moves, which makes it the
	// one OID guaranteed to produce a non-zero delta between two polls.
	sysUpTime = "1.3.6.1.2.1.1.3.0"
	// eth0HCIn is ifHCInOctets of the Linux model's eth0: a Counter64 moving
	// at about 1.25 MB/s, between 0.4 and 1.6 times that.
	eth0HCIn = "1.3.6.1.2.1.31.1.1.1.6.2"
)

// fetcher polls as the application does (app_monitor.go, buildFetch): GetMany,
// one connection per target and every OID in one PDU.
func fetcher(port int) monitor.FetchFunc {
	client := snmp.NewClient()
	return func(ctx context.Context, oids []string, targets []string) []monitor.Reading {
		out := []monitor.Reading{}
		for _, m := range client.GetMany(ctx, targets, oids, "public", "v2c", port, 1, 0, snmp.V3Params{}) {
			for _, oid := range oids {
				reading := monitor.Reading{
					Target: m.Target, OID: oid,
					Error: m.Errors[oid], ResponseTimeMs: int(m.ResponseTimeMs),
				}
				if r := m.Results[oid]; r != nil {
					reading.SnmpType = r.Type
					if f, ok := number(r.Value); ok {
						reading.Value = &f
					}
				}
				out = append(out, reading)
			}
		}
		return out
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// collector keeps what a scheduler persists.
type collector struct {
	mu     sync.Mutex
	points []monitor.Point
}

func (c *collector) persist(p []monitor.Point) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.points = append(c.points, p...)
}

// of is every point kept so far for oid.
func (c *collector) of(oid string) []monitor.Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []monitor.Point
	for _, p := range c.points {
		if p.OID == oid {
			out = append(out, p)
		}
	}
	return out
}

// await waits until done holds for the points of oid, and returns them.
func (c *collector) await(t *testing.T, oid string, within time.Duration, done func([]monitor.Point) bool) []monitor.Point {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := c.of(oid)
		if done(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sample(s) of %s in %v, and not the ones awaited", len(got), oid, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func atLeast(n int) func([]monitor.Point) bool {
	return func(p []monitor.Point) bool { return len(p) >= n }
}

// The scheduler polls a simulated server: every sample a value, and from the
// second on a delta and a rate — the reason the clock moved into Go — including
// for a 64-bit counter, whose rate is the one the device actually moves at.
func TestIntegrationSchedulerPollsASimulatedDevice(t *testing.T) {
	d := simtest.Start(t, simulator.Device{})
	var c collector
	s := monitor.NewScheduler()
	s.Persist = c.persist
	s.Start(monitor.SessionSpec{
		ID: "integration", OIDs: []string{sysUpTime, eth0HCIn}, Targets: []string{d.Address},
		Interval: 400 * time.Millisecond, Fetch: fetcher(d.Port),
	})
	defer s.StopAll()

	got := c.await(t, eth0HCIn, 8*time.Second, atLeast(3))
	for i, p := range got {
		if p.Error != "" || p.Value == nil {
			t.Fatalf("sample %d: error %q, value %v", i, p.Error, p.Value)
		}
	}
	if got[0].SnmpType != "Counter64" {
		t.Errorf("the SNMP type came through as %q; delta correction depends on it", got[0].SnmpType)
	}
	for _, p := range got[1:] {
		if p.Rate == nil || *p.Rate < 0.3e6 || *p.Rate > 2.5e6 {
			t.Errorf("eth0 moves at 0.5 to 2 MB/s, and the derived rate is %v", p.Rate)
		}
	}
	up := c.of(sysUpTime)
	if len(up) < 2 || up[1].Delta == nil || *up[1].Delta <= 0 {
		t.Errorf("sysUpTime did not advance between two polls: %+v", up)
	}
}

// A 32-bit counter wraps, and the scheduler's delta goes through the wrap: never
// negative, and the rate the one the device counts at. The counter here counts
// two gigabytes a second from a gigabyte short of the top, so it wraps within
// half a second of starting and every 2.1 seconds after.
func TestIntegrationACounterThatWrapsIsCorrected(t *testing.T) {
	const fast = "1.3.6.1.4.1.99999.1.0"
	a, err := simulator.NewAgent(simulator.Config{
		Listen: "127.0.0.1:0", Versions: []string{"v2c"}, Community: "public",
		Objects: []simulator.Object{
			{OID: sysUpTime, Type: gosnmp.TimeTicks, Value: simulator.Uptime()},
			{OID: fast, Type: gosnmp.Counter32, Value: simulator.Counter(2e9, 1<<32-1_000_000_000, simulator.Swing{})},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)

	var c collector
	s := monitor.NewScheduler()
	s.Persist = c.persist
	s.Start(monitor.SessionSpec{
		ID: "wrap", OIDs: []string{fast}, Targets: []string{"127.0.0.1"},
		Interval: 300 * time.Millisecond, Fetch: fetcher(int(a.Addr().Port())),
	})
	defer s.StopAll()

	// Until a sample is below the one before it: the wrap, seen on the wire.
	got := c.await(t, fast, 10*time.Second, func(p []monitor.Point) bool {
		for i := 1; i < len(p); i++ {
			if p[i].Value != nil && p[i-1].Value != nil && *p[i].Value < *p[i-1].Value {
				return i+1 < len(p) // and one more after it
			}
		}
		return false
	})
	for i, p := range got[1:] {
		if p.Error != "" {
			t.Fatalf("sample %d: %s", i+1, p.Error)
		}
		if p.Delta == nil || *p.Delta <= 0 {
			t.Errorf("sample %d: delta %v across a wrap; a Counter32 wraps, it does not go back", i+1, p.Delta)
		}
		if p.Rate == nil || *p.Rate < 1.5e9 || *p.Rate > 2.5e9 {
			t.Errorf("sample %d: rate %v, and the counter counts 2e9 a second", i+1, p.Rate)
		}
	}
}

// A device that stops answering produces recorded failures rather than
// silence — what reachability alerting is built on — and polling picks up
// again when it comes back, uptime started over.
func TestIntegrationADeviceThatStopsIsRecordedAndComesBack(t *testing.T) {
	d := simtest.Start(t, simulator.Device{})
	var c collector
	s := monitor.NewScheduler()
	s.Persist = c.persist
	s.Start(monitor.SessionSpec{
		ID: "outage", OIDs: []string{sysUpTime}, Targets: []string{d.Address},
		Interval: 300 * time.Millisecond, Fetch: fetcher(d.Port),
	})
	defer s.StopAll()

	// Up for a second, so a restarted uptime is told apart from a running one.
	before := c.await(t, sysUpTime, 8*time.Second, func(p []monitor.Point) bool {
		return len(p) > 0 && p[len(p)-1].Value != nil && *p[len(p)-1].Value >= 100
	})
	last := *before[len(before)-1].Value

	d.Stop()
	c.await(t, sysUpTime, 8*time.Second, func(p []monitor.Point) bool {
		n := len(p)
		return n > len(before) && p[n-1].Error != "" && p[n-1].Value == nil
	})

	d.Restart()
	after := c.await(t, sysUpTime, 8*time.Second, func(p []monitor.Point) bool {
		return len(p) > 0 && p[len(p)-1].Error == "" && p[len(p)-1].Value != nil
	})
	if again := *after[len(after)-1].Value; again >= last {
		t.Errorf("uptime %v after the restart, %v before it: the device did not start over", again, last)
	}
}

// An address that nothing answers on is a recorded error too, not a hang. Its
// port is irrelevant: 192.0.2.0/24 is TEST-NET-1, reserved for documentation.
func TestIntegrationUnreachableTargetIsRecorded(t *testing.T) {
	var c collector
	s := monitor.NewScheduler()
	s.Persist = c.persist
	s.Start(monitor.SessionSpec{
		ID: "unreachable", OIDs: []string{sysUpTime}, Targets: []string{"192.0.2.1"},
		Interval: 500 * time.Millisecond, Fetch: fetcher(161),
	})
	defer s.StopAll()

	got := c.await(t, sysUpTime, 10*time.Second, atLeast(1))
	if got[0].Error == "" {
		t.Errorf("expected a recorded error, got %+v", got[0])
	}
	if got[0].Value != nil {
		t.Error("a failed poll must not carry a value")
	}
}
