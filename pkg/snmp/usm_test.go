package snmp

import (
	"strings"
	"sync"
	"testing"
	"time"

	"SnmpLens/pkg/events"

	"github.com/gosnmp/gosnmp"
)

// The trap listener accepts SEVERAL SNMPv3 users at once.
//
// Driven end to end — a real gosnmp sender with an engine of its own, a real
// socket, the real listener — because what the listener relies on is gosnmp's
// table trying every entry and re-localising the keys to the SENDER's engine
// ID. A test of our own table-building would pass just as well if gosnmp tried
// only the first entry, or kept the keys it was given.

var (
	alice = V3Params{User: "alice", SecLevel: "AuthPriv",
		AuthProto: "SHA", AuthPass: "alice-auth-123", PrivProto: "AES", PrivPass: "alice-priv-123"}
	bob = V3Params{User: "bob", SecLevel: "AuthNoPriv",
		AuthProto: "SHA256", AuthPass: "bob-auth-1234"}
	carol = V3Params{User: "carol", SecLevel: "AuthPriv",
		AuthProto: "SHA512", AuthPass: "carol-auth-12", PrivProto: "AES256C", PrivPass: "carol-priv-12"}
)

// Two devices, two engines. RFC 3411 bounds an engine ID to 5..32 octets.
const (
	engineA = "\x80\x00\x4e\x20\x04device-a"
	engineB = "\x80\x00\x4e\x20\x04device-b"
)

// trapLog records the trap OID of every notification the listener journals.
type trapLog struct {
	mu   sync.Mutex
	oids []string
}

func (l *trapLog) record(ev events.Event, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.oids = append(l.oids, strings.TrimPrefix(ev.OID, "."))
	return nil
}

func (l *trapLog) has(oid string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range l.oids {
		if o == oid {
			return true
		}
	}
	return false
}

func (l *trapLog) waitFor(oid string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if l.has(oid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func listenerWithLog(t *testing.T) (*Client, *trapLog) {
	t.Helper()
	c := newHeadlessClient()
	l := &trapLog{}
	c.SetRecorder(events.RecorderFunc(l.record))
	return c, l
}

// sendV3Trap sends one notification as u, from engineID — what a device does:
// for a trap the SENDER is the authoritative engine.
func sendV3Trap(t *testing.T, port int, u V3Params, engineID, trapOID string) {
	t.Helper()
	sp, level, err := usmFor(u)
	if err != nil {
		t.Fatalf("usmFor(%s): %v", u.User, err)
	}
	sp.AuthoritativeEngineID = engineID
	sp.AuthoritativeEngineBoots = 1
	sp.AuthoritativeEngineTime = 1000

	g := &gosnmp.GoSNMP{
		Target:             "127.0.0.1",
		Port:               uint16(port),
		Version:            gosnmp.Version3,
		SecurityModel:      gosnmp.UserSecurityModel,
		MsgFlags:           level,
		SecurityParameters: sp,
		Timeout:            2 * time.Second,
	}
	if err := g.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer g.Conn.Close()

	trap := gosnmp.SnmpTrap{Variables: []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(42)},
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: trapOID},
	}}
	if _, err := g.SendTrap(trap); err != nil {
		t.Fatalf("SendTrap as %s: %v", u.User, err)
	}
}

func TestTheTrapListenerAcceptsEveryUserItIsGiven(t *testing.T) {
	c, got := listenerWithLog(t)
	port := freePort(t)

	info, err := c.StartTrapListener(port, []V3Params{alice, bob})
	if err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	if info.Users != 2 || len(info.Refused) != 0 {
		t.Fatalf("expected both users accepted, got %+v", info)
	}

	sendV3Trap(t, port, alice, engineA, "1.3.6.1.4.1.99999.0.1")
	// Nobody configured carol. Sent BETWEEN the two good ones, so the second
	// arriving also proves the receive loop survived an unknown user.
	sendV3Trap(t, port, carol, engineB, "1.3.6.1.4.1.99999.0.3")
	sendV3Trap(t, port, bob, engineB, "1.3.6.1.4.1.99999.0.2")
	wrong := alice
	wrong.AuthPass = "not-alices-passphrase"
	sendV3Trap(t, port, wrong, engineA, "1.3.6.1.4.1.99999.0.4")
	// alice's name and no authentication at all. Before v1.47.0, gosnmp took
	// the security level from the message whenever a table was in use, and
	// delivered this without checking any digest (GHSA-w96x-5v55-24xx).
	unsigned := V3Params{User: alice.User, SecLevel: "NoAuthNoPriv"}
	sendV3Trap(t, port, unsigned, engineA, "1.3.6.1.4.1.99999.0.5")

	if !got.waitFor("1.3.6.1.4.1.99999.0.1", 3*time.Second) {
		t.Error("the first user's trap (AuthPriv, SHA/AES) was not received")
	}
	if !got.waitFor("1.3.6.1.4.1.99999.0.2", 3*time.Second) {
		t.Error("the SECOND user's trap was not received — the listener is still " +
			"accepting a single user")
	}
	time.Sleep(300 * time.Millisecond)
	if got.has("1.3.6.1.4.1.99999.0.3") {
		t.Error("a trap from a user nobody configured was accepted")
	}
	if got.has("1.3.6.1.4.1.99999.0.4") {
		t.Error("a trap signed with the wrong passphrase was accepted")
	}
	if got.has("1.3.6.1.4.1.99999.0.5") {
		t.Error("an unauthenticated trap naming a user who requires authentication was accepted")
	}
}

// One unusable user must not cost the others their traps, and must not be
// dropped without a word either.
func TestAnUnusableUserIsRefusedAndTheOthersStillWork(t *testing.T) {
	c, got := listenerWithLog(t)
	port := freePort(t)

	noPrivPass := V3Params{User: "dave", SecLevel: "AuthPriv",
		AuthProto: "SHA", AuthPass: "dave-auth-123", PrivProto: "AES"}
	noAuthProto := V3Params{User: "erin", SecLevel: "AuthNoPriv", AuthPass: "erin-auth-123"}
	badLevel := V3Params{User: "frank", SecLevel: "authPriv"}

	info, err := c.StartTrapListener(port, []V3Params{noPrivPass, alice, noAuthProto, badLevel})
	if err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	if info.Users != 1 {
		t.Errorf("expected one usable user, got %d", info.Users)
	}
	reasons := map[string]string{}
	for _, r := range info.Refused {
		reasons[r.User] = r.Reason
	}
	for user, want := range map[string]string{
		"dave":  "privacy passphrase",
		"erin":  "authentication protocol",
		"frank": "invalid security level",
	} {
		if !strings.Contains(reasons[user], want) {
			t.Errorf("%s: refusal should name %q, got %q", user, want, reasons[user])
		}
	}

	sendV3Trap(t, port, alice, engineA, "1.3.6.1.4.1.99999.1.1")
	if !got.waitFor("1.3.6.1.4.1.99999.1.1", 3*time.Second) {
		t.Error("the usable user lost its traps because another one was broken")
	}
}

// A table must never reach the listener without SecurityParameters beside it:
// they carry the engine ID gosnmp's receive loop compares every v3
// notification with. Before v1.47.0 a nil there was also a panic on a
// goroutine no recover of ours covers.
func TestATableNeverTravelsWithoutSecurityParameters(t *testing.T) {
	for _, users := range [][]V3Params{
		{alice},
		{V3Params{User: "broken", SecLevel: "AuthPriv"}, bob},
		{bob, alice, carol},
	} {
		sec := newTrapSecurity(users, gosnmp.Logger{})
		if sec.table != nil && sec.first == nil {
			t.Errorf("%d users: a table with no SecurityParameters", len(users))
		}
	}
	if sec := newTrapSecurity(nil, gosnmp.Logger{}); sec.table != nil || sec.first != nil {
		t.Error("no users must mean no table at all: the listener then hears v1 and v2c as before")
	}
}

// What is remembered and compared is what RECEIVING uses. Two profiles that
// differ only in a context name are one user, and nothing above the security
// level is kept — a remembered set holds no passphrase the listener ignores.
func TestTrapUsersKeepOnlyWhatReceivingUses(t *testing.T) {
	u := V3Params{User: "u", SecLevel: "AuthNoPriv", AuthProto: "SHA", AuthPass: "auth-pass-1",
		PrivProto: "DES", PrivPass: "leftover-priv", ContextName: "vlan-100"}
	got := trapUser(u)
	if got.PrivProto != "" || got.PrivPass != "" {
		t.Errorf("privacy kept at AuthNoPriv: %+v", got)
	}
	if got.ContextName != "" {
		t.Errorf("the context name was kept: %+v", got)
	}

	other := u
	other.ContextName = "vrf-MGMT"
	if n := newTrapSecurity([]V3Params{u, other}, gosnmp.Logger{}).info.Users; n != 1 {
		t.Errorf("two profiles differing only in context gave %d users", n)
	}

	plain := trapUser(V3Params{User: "p", SecLevel: "NoAuthNoPriv", AuthProto: "MD5", AuthPass: "x"})
	if plain.AuthProto != "" || plain.AuthPass != "" {
		t.Errorf("authentication kept at NoAuthNoPriv: %+v", plain)
	}
}

// Changing the users restarts the listener — only when the accepted set really
// changed — and a user taken away stops authenticating at once.
func TestUpdateTrapUsersRestartsOnlyWhenTheSetChanged(t *testing.T) {
	c, got := listenerWithLog(t)

	// Nothing listening: it only reports what the users would be.
	info, err := c.UpdateTrapUsers([]V3Params{alice, bob})
	if err != nil || info.Restarted || info.Users != 2 {
		t.Fatalf("with no listener: info %+v, err %v", info, err)
	}

	port := freePort(t)
	if _, err := c.StartTrapListener(port, []V3Params{alice}); err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	defer c.StopTrapListener()
	waitBound(t, c)

	// The same set, and the same set written differently, cost nothing.
	withContext := alice
	withContext.ContextName = "ctx"
	if info, err := c.UpdateTrapUsers([]V3Params{withContext}); err != nil || info.Restarted {
		t.Fatalf("an unchanged set restarted the listener: %+v, %v", info, err)
	}

	// Adding bob restarts, and bob is heard.
	info, err = c.UpdateTrapUsers([]V3Params{alice, bob})
	if err != nil || !info.Restarted {
		t.Fatalf("adding a user did not restart: %+v, %v", info, err)
	}
	waitBound(t, c)
	sendV3Trap(t, port, bob, engineB, "1.3.6.1.4.1.99999.2.1")
	if !got.waitFor("1.3.6.1.4.1.99999.2.1", 3*time.Second) {
		t.Error("a user added while listening is not heard")
	}

	// Listing them in another order is the same set.
	if info, err := c.UpdateTrapUsers([]V3Params{bob, alice}); err != nil || info.Restarted {
		t.Errorf("reordering the users restarted the listener: %+v, %v", info, err)
	}

	// Taking alice away: her traps stop at once, bob's do not.
	if info, err := c.UpdateTrapUsers([]V3Params{bob}); err != nil || !info.Restarted {
		t.Fatalf("removing a user did not restart: %+v, %v", info, err)
	}
	waitBound(t, c)
	sendV3Trap(t, port, alice, engineA, "1.3.6.1.4.1.99999.2.2")
	sendV3Trap(t, port, bob, engineB, "1.3.6.1.4.1.99999.2.3")
	if !got.waitFor("1.3.6.1.4.1.99999.2.3", 3*time.Second) {
		t.Error("the remaining user's trap was not received after the restart")
	}
	time.Sleep(300 * time.Millisecond)
	if got.has("1.3.6.1.4.1.99999.2.2") {
		t.Error("a user that was taken away still authenticates")
	}
}
