package network

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

// Real output, both families. Before this, the hop address was matched with an
// IPv4-shaped regex, so every hop of an IPv6 traceroute rendered blank — the
// command ran, the RTTs were right, and the column was simply empty.

const windowsV6 = `
Détermination de l'itinéraire vers 2001:db8::64

  1     1 ms     1 ms     1 ms  2001:db8::1
  2     8 ms     7 ms     9 ms  2001:db8:1::ffff
  3     *        *        *     Délai d'attente de la demande dépassé.
  4    12 ms    11 ms    11 ms  2001:db8::64
`

const windowsV4 = `
  1     1 ms     1 ms     1 ms  192.168.1.1
  2     *        *        *     Délai d'attente de la demande dépassé.
  3    12 ms    11 ms    11 ms  8.8.8.8
`

const unixV6 = `traceroute to 2001:db8::64 (2001:db8::64), 30 hops max, 80 byte packets
 1  2001:db8::1  1.234 ms  0.987 ms  1.123 ms
 2  2001:db8:1::ffff  8.100 ms  7.900 ms  9.000 ms
 3  * * *
 4  2001:db8::64  12.010 ms  11.500 ms  11.200 ms
`

const unixV4 = ` 1  192.168.1.1  1.234 ms  0.987 ms  1.123 ms
 2  * * *
 3  8.8.8.8  12.010 ms  11.500 ms  11.200 ms
`

func parse(t *testing.T, text string, windows bool) []TracerouteHop {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(text))
	noop := func(TracerouteHop) {}
	if windows {
		return parseWindowsTraceroute(sc, noop)
	}
	return parseUnixTraceroute(sc, noop)
}

func TestTracerouteParsesIPv6Hops(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		windows bool
		want    []string
	}{
		{"windows v6", windowsV6, true, []string{"2001:db8::1", "2001:db8:1::ffff", "", "2001:db8::64"}},
		{"windows v4", windowsV4, true, []string{"192.168.1.1", "", "8.8.8.8"}},
		{"unix v6", unixV6, false, []string{"2001:db8::1", "2001:db8:1::ffff", "", "2001:db8::64"}},
		{"unix v4", unixV4, false, []string{"192.168.1.1", "", "8.8.8.8"}},
	}
	for _, c := range cases {
		hops := parse(t, c.text, c.windows)
		if len(hops) != len(c.want) {
			t.Errorf("%s: %d hops, want %d (%+v)", c.name, len(hops), len(c.want), hops)
			continue
		}
		for i, want := range c.want {
			if hops[i].IP != want {
				t.Errorf("%s hop %d: IP = %q, want %q", c.name, i+1, hops[i].IP, want)
			}
		}
	}
}

// The RTTs must survive the change: colons in an address must not be read as
// timings, and a timed-out hop must still be marked as one.
func TestTracerouteKeepsTimings(t *testing.T) {
	hops := parse(t, unixV6, false)
	// The unit is part of the value the UI shows; what matters here is that
	// the colons in the address were not read as timings.
	if hops[0].RTT1 != "1.234 ms" {
		t.Errorf("RTT1 = %q, want \"1.234 ms\"", hops[0].RTT1)
	}
	if hops[1].RTT1 != "8.100 ms" {
		t.Errorf("the address colons leaked into the timing: RTT1 = %q", hops[1].RTT1)
	}
	if !hops[2].Timeout {
		t.Errorf("hop 3 should be a timeout: %+v", hops[2])
	}
	if hops[3].Timeout {
		t.Errorf("hop 4 answered and must not be a timeout: %+v", hops[3])
	}
}

// A hop number must not be mistaken for an address, nor a timing.
func TestTracerouteDoesNotInventAddresses(t *testing.T) {
	hops := parse(t, " 1  * * *\n 2  * * *\n", false)
	for _, h := range hops {
		if h.IP != "" {
			t.Errorf("hop %d invented the address %q", h.Hop, h.IP)
		}
	}
}

func TestIPv6TargetDetection(t *testing.T) {
	cases := map[string]bool{
		"2001:db8::1":  true,
		"::1":          true,
		"fe80::1%eth0": true,
		"192.168.1.1":  false,
		"8.8.8.8":      false,
	}
	for in, want := range cases {
		if got := isIPv6Target(in); got != want {
			t.Errorf("isIPv6Target(%q) = %v, want %v", in, got, want)
		}
	}
}

// The validation must be on the path that builds argv, not merely available.
func TestPingAndTracerouteRefuseAnOptionAsATarget(t *testing.T) {
	for _, target := range []string{"-h", "--help", "-w 2000", "", "host name"} {
		if _, err := Ping(target, 1); err == nil {
			t.Errorf("Ping(%q) was attempted", target)
		}
		if _, err := Traceroute(context.Background(), target, func(TracerouteHop) {}); err == nil {
			t.Errorf("Traceroute(%q) was attempted", target)
		}
	}
}

// A traceroute that starts and fails must say so.
//
// cmd.Wait's error was dropped and stderr was never captured, so an unknown
// name returned an empty hop list and a nil error — the panel showed an empty
// table with nothing to explain it. Measured on this machine: tracert writes
// "cannot resolve the target system name" and exits non-zero.
func TestTracerouteReportsAFailureInsteadOfNoHops(t *testing.T) {
	hops, err := Traceroute(context.Background(), "nonexistent-xyz-host.invalid", func(TracerouteHop) {})
	if err == nil {
		t.Errorf("a failed traceroute returned %d hops and no error", len(hops))
	}
	if len(hops) != 0 {
		t.Errorf("hops = %+v", hops)
	}
	if err != nil && !strings.Contains(err.Error(), "traceroute failed") {
		t.Logf("error text: %v", err)
	}
}

// The message stays one line: these tools print a paragraph.
func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"one line":             "one line",
		"first\r\nsecond":      "first",
		"first\nsecond\nthird": "first",
		"":                     "",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
