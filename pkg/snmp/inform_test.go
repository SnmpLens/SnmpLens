package snmp

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// An INFORM is a trap that is acknowledged, and the acknowledgement is the
// only reason to send one. So the test is a real exchange with a real
// receiver: asserting that no error came back would pass just as well if
// nothing were ever sent.

// freePort returns a port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return port
}

// receiver starts a listener that answers informs, and reports what it saw.
type receiver struct {
	mu     sync.Mutex
	got    []seen
	tl     *gosnmp.TrapListener
	closed bool
}

// seen is a copy taken inside the callback. gosnmp answers an inform by
// REUSING the packet it just handed us — it sets PDUType to GetResponse and
// sends it back — so a stored pointer describes the reply, not the request.
type seen struct {
	pduType gosnmp.PDUType
	names   []string
}

func startReceiver(t *testing.T, port int) *receiver {
	t.Helper()
	r := &receiver{tl: gosnmp.NewTrapListener()}
	r.tl.Params = gosnmp.Default
	r.tl.Params.Community = "public"
	r.tl.OnNewTrap = func(p *gosnmp.SnmpPacket, _ *net.UDPAddr) {
		item := seen{pduType: p.PDUType}
		for _, v := range p.Variables {
			item.names = append(item.names, v.Name)
		}
		r.mu.Lock()
		r.got = append(r.got, item)
		r.mu.Unlock()
	}

	go func() {
		if err := r.tl.Listen(fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			return
		}
	}()
	select {
	case <-r.tl.Listening():
	case <-time.After(3 * time.Second):
		t.Skip("the receiver never came up")
	}
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.closed {
			r.closed = true
			r.tl.Close()
		}
	})
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func TestSendInformIsAcknowledged(t *testing.T) {
	port := freePort(t)
	rec := startReceiver(t, port)
	c := NewClient()

	res := c.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3",
		[]TrapVariable{{Oid: "1.3.6.1.2.1.2.2.1.1.1", Type: "integer", Value: "1"}})

	if res.Error != "" {
		t.Fatalf("inform failed: %s", res.Error)
	}
	if !res.Acknowledged {
		t.Error("the inform was sent but reported as unacknowledged")
	}
	if rec.count() != 1 {
		t.Errorf("the receiver saw %d notifications", rec.count())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.got[0].pduType; got != gosnmp.InformRequest {
		t.Errorf("PDU type = %v, want InformRequest — a trap was sent instead", got)
	}
}

// The failure that matters: nothing is listening. A trap would report success
// because UDP cannot tell; an inform must not.
func TestSendInformReportsNoAnswer(t *testing.T) {
	port := freePort(t)
	c := NewClient()

	res := c.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", nil)
	if res.Acknowledged {
		t.Error("an inform to nobody was reported as acknowledged")
	}
	if res.Error == "" {
		t.Error("an inform to nobody reported no error")
	}
}

// v1 has no InformRequest PDU. Refusing beats silently sending a trap and
// telling the caller their notification was confirmed.
func TestSendInformRefusesV1(t *testing.T) {
	c := NewClient()
	res := c.SendInform("127.0.0.1", 162, "public", "v1", "1.3.6.1.6.3.1.1.5.3", nil)
	if res.Acknowledged || res.Error == "" {
		t.Fatalf("v1 inform was accepted: %+v", res)
	}
	if !strings.Contains(res.Error, "INFORM") {
		t.Errorf("the error should name the problem: %s", res.Error)
	}
}

// Traps must keep working, and must keep being traps.
func TestSendTrapStillSendsATrap(t *testing.T) {
	port := freePort(t)
	rec := startReceiver(t, port)
	c := NewClient()

	if err := c.SendTrap("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", nil); err != nil {
		t.Fatalf("trap failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for rec.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if rec.count() != 1 {
		t.Fatalf("the receiver saw %d notifications", rec.count())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.got[0].pduType; got != gosnmp.SNMPv2Trap {
		t.Errorf("PDU type = %v, want SNMPv2Trap", got)
	}
}

// An inform carries its varbinds like a trap does; losing them would make it
// an acknowledged notification about nothing.
func TestInformCarriesItsVarbinds(t *testing.T) {
	port := freePort(t)
	rec := startReceiver(t, port)
	c := NewClient()

	res := c.SendInform("127.0.0.1", port, "public", "v2c", "1.3.6.1.6.3.1.1.5.3", []TrapVariable{
		{Oid: "1.3.6.1.2.1.1.5.0", Type: "octetstring", Value: "switch-01"},
		{Oid: "1.3.6.1.2.1.2.2.1.1.1", Type: "integer", Value: "7"},
	})
	if res.Error != "" || !res.Acknowledged {
		t.Fatalf("inform: %+v", res)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	names := rec.got[0].names
	for _, want := range []string{"1.3.6.1.2.1.1.5.0", "1.3.6.1.2.1.2.2.1.1.1"} {
		var found bool
		for _, n := range names {
			if n == want || n == "."+want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s did not arrive; got %v", want, names)
		}
	}
}

// The two refusals must agree with each other.
//
// The v1 guard used to advise "use v2c or v3", and sendNotification then
// rejects v3 with "supports v1 and v2c only" — a caller who followed the first
// error landed on a contradicting second one. Any version the sender refuses
// must not be named as a way forward.
func TestRefusalsDoNotContradictEachOther(t *testing.T) {
	c := NewClient()

	advice := c.SendInform("127.0.0.1", 162, "public", "v1", "1.3.6.1.6.3.1.1.5.3", nil).Error
	if advice == "" {
		t.Fatal("v1 was accepted")
	}

	// Whole tokens, not substrings: "v2c" contains "v2", and a naive
	// Contains check flags the correct message.
	offered := regexp.MustCompile(`\bv[0-9]+[a-z]*`).FindAllString(advice, -1)
	if len(offered) == 0 {
		t.Fatalf("the v1 error names no alternative at all: %q", advice)
	}

	for _, version := range offered {
		res := c.SendInform("127.0.0.1", 162, "public", version, "1.3.6.1.6.3.1.1.5.3", nil)
		if strings.Contains(res.Error, "supports") || strings.Contains(res.Error, "no INFORM") {
			t.Errorf("the v1 error offers %q, which the sender then refuses with %q",
				version, res.Error)
		}
	}
}
