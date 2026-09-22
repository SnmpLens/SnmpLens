package snmp

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"

	"github.com/gosnmp/gosnmp"
)

// The debug log used to carry the community string.
//
// gosnmp's SnmpPacket.SafeString formats "Community:%s" on every SENDING
// PACKET despite its name, and unmarshalling logs "Parsed community %s". Both
// landed in the ring buffer that SnmpGetDebugLog hands to the debug panel,
// where Anonymous Mode masked IP addresses and nothing else.

func TestScrubRemovesTheCommunityFromWhatGosnmpPrints(t *testing.T) {
	// The real shapes, copied from gosnmp v1.45.0 marshal.go.
	lines := []string{
		"SENDING PACKET: Version:2c, MsgFlags:NoAuthNoPriv, SecurityModel:UserSecurityModel, " +
			"SecurityParameters:, ContextEngineID:, ContextName:, Community:s3cr3t-community, " +
			"PDUType:GetRequest, MsgID:0, RequestID:1836429849, MsgMaxSize:0, Error:NoError, Variables:[]",
		"Parsed community s3cr3t-community",
	}
	for _, line := range lines {
		got := scrubSecrets(line, []string{"s3cr3t-community", "", ""})
		if strings.Contains(got, "s3cr3t-community") {
			t.Errorf("the community survived scrubbing:\n%s", got)
		}
		if !strings.Contains(got, redacted) {
			t.Errorf("nothing was marked as redacted:\n%s", got)
		}
	}
}

// A community this writer was never told about — a trap sender's, or one from
// a request built elsewhere — must still be removed.
func TestScrubRemovesAnUnknownCommunity(t *testing.T) {
	line := "SENDING PACKET: Version:1, Community:someone-elses, PDUType:GetRequest, MsgID:7"
	got := scrubSecrets(line, nil)
	if strings.Contains(got, "someone-elses") {
		t.Errorf("an unknown community survived: %s", got)
	}
	// The fields around it must survive, or the log stops being useful.
	for _, keep := range []string{"Version:1", "PDUType:GetRequest", "MsgID:7"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was destroyed: %s", keep, got)
		}
	}
}

// v3 passphrases are not logged today. Scrubbing them anyway is what makes a
// gosnmp upgrade that starts logging them a non-event.
func TestScrubRemovesV3Passphrases(t *testing.T) {
	line := "SEND STORE SECURITY PARAMS from result: user snmplens auth authpass123 priv privpass123"
	got := scrubSecrets(line, []string{"public", "authpass123", "privpass123"})
	for _, secret := range []string{"authpass123", "privpass123"} {
		if strings.Contains(got, secret) {
			t.Errorf("%s survived: %s", secret, got)
		}
	}
	if !strings.Contains(got, "snmplens") {
		t.Errorf("the username was destroyed; it is not a credential: %s", got)
	}
}

// A one- or two-character community must not turn every matching letter in the
// log into [redacted] — the patterns already cover the lines that carry it.
func TestScrubDoesNotShredTheLogForAShortCommunity(t *testing.T) {
	line := "SENDING PACKET: Version:2c, Community:ab, PDUType:GetRequest, Variables:[]"
	got := scrubSecrets(line, []string{"ab"})
	if strings.Contains(got, "Community:ab") {
		t.Errorf("the community field was not redacted: %s", got)
	}
	if !strings.Contains(got, "PDUType:GetRequest") {
		t.Errorf("the log was shredded: %s", got)
	}
}

// The WIRING, with no agent and no network.
//
// The end-to-end test below needs a simulator and skips without one, so
// deleting the scrub from ringLogWriter.Write left the whole gate green — the
// four tests above call scrubSecrets directly and never touch the production
// path. This one drives the writer itself, so it runs everywhere.
func TestTheWriterScrubsWhatItBuffers(t *testing.T) {
	c := NewClient(context.Background())
	c.SetDebugMode(true)

	w := &ringLogWriter{client: c, secrets: []string{"s3cr3t-community", "authpass123", ""}}
	lines := []string{
		"SENDING PACKET: Version:2c, Community:s3cr3t-community, PDUType:GetRequest",
		"Parsed community s3cr3t-community",
		"GET RESPONSE OK: [48 101 2 1 1 4 6 115 51 99 114 51 116 45 99 111 109 109 117 110 105 116 121 162 88]",
		"auth is authpass123 here",
	}
	for _, line := range lines {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	entries := c.GetDebugLog()
	if len(entries) != len(lines) {
		t.Fatalf("%d entries for %d lines", len(entries), len(lines))
	}
	for i, e := range entries {
		if strings.Contains(e.Message, "s3cr3t-community") || strings.Contains(e.Message, "authpass123") {
			t.Errorf("line %d reached the buffer unscrubbed: %s", i, e.Message)
		}
	}
	// And the decimal rendering of the same string, which is what a raw packet
	// dump is: 115 51 99 114 51 116 spells "s3cr3t".
	if strings.Contains(entries[2].Message, "115 51 99 114") {
		t.Errorf("a raw packet dump was buffered verbatim: %s", entries[2].Message)
	}
	if !strings.Contains(entries[2].Message, "bytes") {
		t.Errorf("the dump lost its length, which is the part worth keeping: %s", entries[2].Message)
	}
}

// A raw v1/v2c packet contains the community by protocol design, so the dump
// is redacted whole. The length survives because "did it arrive, and how big
// was it" is what the line is read for.
func TestScrubRedactsRawPacketDumps(t *testing.T) {
	in := "GET RESPONSE OK: [48 101 2 1 1 4 6 112 117 98 108 105 99 162 88 2 4 77]"
	got := scrubSecrets(in, nil)

	if strings.Contains(got, "112 117 98") {
		t.Errorf("the packet bytes survived: %s", got)
	}
	if !strings.HasPrefix(got, "GET RESPONSE OK: [") || !strings.Contains(got, "18 bytes") {
		t.Errorf("the label or the length was lost: %s", got)
	}

	// Short bracketed lists are ordinary message content, not packets.
	for _, keep := range []string{
		"Variables:[{<nil> 1.3.6.1.2.1.1.1.0 Null}]",
		"errIndex:[1 2]",
		"empty:[]",
	} {
		if scrubSecrets(keep, nil) != keep {
			t.Errorf("%q was mangled into %q", keep, scrubSecrets(keep, nil))
		}
	}
}

// End to end against the simulator the application ships, run in-process.
//
// It used to need an external agent named by SNMPLENS_TEST_AGENT, and before
// that by SNMPLENS_AGENT_PORT, which nothing set: the only test of the
// production path never ran anywhere. Now it runs on every CI build.
func TestDebugLogNeverHoldsTheCommunity(t *testing.T) {
	const community = "s3cr3t-community"
	d := simtest.Start(t, simulator.Device{Community: community})

	c := NewClient(context.Background())
	c.SetDebugMode(true)
	defer c.SetDebugMode(false)

	c.Get([]string{d.Address}, "1.3.6.1.2.1.1.1.0", community, "v2c", d.Port, 2, 0, V3Params{})

	entries := c.GetDebugLog()
	if len(entries) == 0 {
		t.Skip("the client logged nothing")
	}

	// The literal AND its decimal rendering. A raw packet dump carries the
	// community as bytes, which a literal search walks straight past — that is
	// how this test passed while the community sat in the same buffer.
	decimal := make([]string, 0, len(community))
	for _, b := range []byte(community) {
		decimal = append(decimal, strconv.Itoa(int(b)))
	}
	needle := strings.Join(decimal, " ")

	for _, e := range entries {
		if strings.Contains(e.Message, community) {
			t.Fatalf("the community reached the debug buffer: %s", e.Message)
		}
		if strings.Contains(e.Message, needle) {
			t.Fatalf("the community reached the buffer as bytes: %s", e.Message)
		}
	}
	t.Logf("%d entries, none carrying the community in either form", len(entries))
}

var _ = gosnmp.Version2c

// A trap arrives from the network with a community we never configured, and
// the listener used to print it to stdout unconditionally — not through the
// ring writer, so nothing scrubbed it, and not gated on debug, so it happened
// whether or not anyone had asked for a log.
func TestTrapListenerLoggingIsScrubbedAndGated(t *testing.T) {
	src, err := os.ReadFile("trap.go")
	if err != nil {
		t.Fatal(err)
	}
	// Code only: the comment above the fix names the old call, and a plain
	// substring search matched its own explanation.
	var code []string
	for _, line := range strings.Split(string(src), "\n") {
		if t := strings.TrimSpace(line); !strings.HasPrefix(t, "//") {
			code = append(code, line)
		}
	}
	body := strings.Join(code, "\n")

	if strings.Contains(body, "log.New(os.Stdout") {
		t.Error("the trap listener still logs to stdout, where nothing scrubs it")
	}
	if !strings.Contains(body, "ringLogWriter{client: c}") {
		t.Error("the trap listener does not log through the scrubbed ring writer")
	}

	// And the writer it now uses removes a community it was never told about.
	c := NewClient(context.Background())
	c.SetDebugMode(true)
	w := &ringLogWriter{client: c}
	if _, err := w.Write([]byte("Parsed community someone-elses-trap-community")); err != nil {
		t.Fatal(err)
	}
	entries := c.GetDebugLog()
	if len(entries) != 1 {
		t.Fatalf("%d entries", len(entries))
	}
	if strings.Contains(entries[0].Message, "someone-elses-trap-community") {
		t.Errorf("a trap sender's community was buffered: %s", entries[0].Message)
	}
}

// A community is an arbitrary octet string, and the redaction used to assume
// it contained neither a comma nor a space.
//
// `Community:[^,]*` is right until a community has a comma in it, and gosnmp's
// SafeString prints "…, Community:%s, PDUType:%s, …" on every sending packet —
// so the tail survived into the ring buffer the debug panel shows. "Parsed
// community \S*" had the same shape of bug with a space, on every received
// packet, where the community belongs to whoever sent the trap.
func TestTheCommunityIsRedactedWhateverIsInIt(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		mustNotBe []string
	}{
		{
			"a comma in a sending packet",
			"SENDING PACKET: Version:2c, MsgFlags:NoAuthNoPriv, Community:pub,lic,secret, PDUType:GetRequest, MsgID:1",
			[]string{"lic", "secret"},
		},
		{
			"a space in a parsed community",
			"Parsed community hunter two three",
			[]string{"two", "three"},
		},
		{
			"the ordinary case still works",
			"SENDING PACKET: Version:2c, Community:public, PDUType:GetRequest, MsgID:1",
			[]string{"public"},
		},
	}

	for _, c := range cases {
		got := scrubSecrets(c.line, nil)
		for _, leak := range c.mustNotBe {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q survived redaction:\n%s", c.name, leak, got)
			}
		}
		// The line has to stay readable, or the debug panel loses its point.
		if !strings.Contains(got, redacted) {
			t.Errorf("%s: nothing was redacted at all: %s", c.name, got)
		}
	}

	// The fields AFTER the community must survive: redacting to the end of the
	// line would be safe and would also throw away what the log is read for.
	got := scrubSecrets("Version:2c, Community:pub,lic, PDUType:GetRequest, MsgID:77", nil)
	if !strings.Contains(got, "PDUType:GetRequest") || !strings.Contains(got, "MsgID:77") {
		t.Errorf("redaction ate the rest of the packet: %s", got)
	}
}
