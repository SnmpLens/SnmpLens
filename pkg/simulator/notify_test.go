package simulator

import (
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/snmp"

	"github.com/gosnmp/gosnmp"
)

const sysNameOID = ".1.3.6.1.2.1.1.5.0"

var notifyUser = User{Name: "ops", SecLevel: "AuthPriv",
	AuthProto: "SHA256", AuthPass: "authpass-1", PrivProto: "AES", PrivPass: "privpass-1"}

// notifyingAgent starts an agent built as the Linux server, whose
// notifications carry objects, sending what traps says.
func notifyingAgent(t *testing.T, listen string, traps Traps) *Agent {
	t.Helper()
	return startAgent(t, Config{
		Listen:        listen,
		Versions:      []string{"v1", "v2c", "v3"},
		Community:     "public",
		Users:         []User{notifyUser},
		Objects:       linuxServer.build(Identity{Name: "srv-01", Seed: 1}),
		Notifications: linuxServer.catalogue(),
		Traps:         traps,
	})
}

// trapReceiver is a UDP socket a test reads notifications from, as a manager
// would.
type trapReceiver struct{ conn *net.UDPConn }

func listenForTraps(t *testing.T) *trapReceiver {
	t.Helper()
	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &trapReceiver{conn: conn}
}

func (r *trapReceiver) port() int { return r.conn.LocalAddr().(*net.UDPAddr).Port }

// next is the next datagram and where it came from; the test fails if none
// comes within two seconds.
func (r *trapReceiver) next(t *testing.T) ([]byte, netip.Addr) {
	t.Helper()
	buf := make([]byte, 65535)
	r.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := r.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("no notification arrived: %v", err)
	}
	return buf[:n], from.Addr().Unmap()
}

// count is how many datagrams arrive within d.
func (r *trapReceiver) count(d time.Duration) int {
	buf := make([]byte, 65535)
	r.conn.SetReadDeadline(time.Now().Add(d))
	n := 0
	for {
		if _, _, err := r.conn.ReadFromUDPAddrPort(buf); err != nil {
			return n
		}
		n++
	}
}

func decodeTrap(t *testing.T, raw []byte, ver gosnmp.SnmpVersion) *gosnmp.SnmpPacket {
	t.Helper()
	p, err := (&gosnmp.GoSNMP{Version: ver}).SnmpDecodePacket(raw)
	if err != nil {
		t.Fatalf("an undecodable notification: %v", err)
	}
	return p
}

// trapOIDOf is a v2c or v3 notification's snmpTrapOID.0, which RFC 3416 puts
// second.
func trapOIDOf(t *testing.T, p *gosnmp.SnmpPacket) string {
	t.Helper()
	if len(p.Variables) < 2 || p.Variables[1].Name != oidSnmpTrapOID {
		t.Fatalf("no snmpTrapOID.0 in second place: %v", names(p.Variables))
	}
	oid, _ := p.Variables[1].Value.(string)
	return oid
}

func usmOf(u User) *gosnmp.UsmSecurityParameters {
	return &gosnmp.UsmSecurityParameters{UserName: u.Name,
		AuthenticationProtocol: gosnmp.SHA256, AuthenticationPassphrase: u.AuthPass,
		PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: u.PrivPass}
}

// ownAddress is a loopback address other than 127.0.0.1 where this machine
// answers on one — Windows and Linux do, macOS without aliases does not — so a
// notification's source can be told from the receiver's own address.
func ownAddress() string {
	c, err := net.ListenPacket("udp", "127.0.0.2:0")
	if err != nil {
		return "127.0.0.1"
	}
	c.Close()
	return "127.0.0.2"
}

// A device sends one notification in each version as that version has it:
// SNMPv1's generic-trap under the device's sysObjectID, SNMPv2's varbinds in
// RFC 3416's order, SNMPv3 signed and sealed as the device's own engine — and
// all three from the device's own address, as real equipment would send them.
func TestADeviceSendsANotificationInEachVersion(t *testing.T) {
	host := ownAddress()
	v1, v2c, v3 := listenForTraps(t), listenForTraps(t), listenForTraps(t)
	a := notifyingAgent(t, host+":0", Traps{Destinations: []Destination{
		{ID: "v1", Host: "127.0.0.1", Port: v1.port(), Version: "v1", Community: "v1-community"},
		{ID: "v2c", Host: "127.0.0.1", Port: v2c.port(), Version: "v2c", Community: "v2c-community"},
		{ID: "v3", Host: "localhost", Port: v3.port(), Version: "v3", User: "ops"},
	}})
	got, err := a.Notify("linkDown")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.Error != "" || d.Acknowledged {
			t.Errorf("%s: %+v", d.ID, d)
		}
	}

	const sysObjectID = ".1.3.6.1.4.1.8072.3.2.10"
	link := []string{".1.3.6.1.2.1.2.2.1.1.3", ".1.3.6.1.2.1.2.2.1.7.3", ".1.3.6.1.2.1.2.2.1.8.3"}
	v2Order := slices.Concat([]string{oidSysUpTime, oidSnmpTrapOID}, link, []string{oidSnmpTrapEnterprise})
	source := func(t *testing.T, from netip.Addr) {
		t.Helper()
		if from.String() != host {
			t.Errorf("sent from %s rather than from the device's own %s", from, host)
		}
	}

	t.Run("v1", func(t *testing.T) {
		raw, from := v1.next(t)
		p := decodeTrap(t, raw, gosnmp.Version1)
		if p.PDUType != gosnmp.Trap || p.Community != "v1-community" {
			t.Fatalf("%v with community %q", p.PDUType, p.Community)
		}
		if p.GenericTrap != 2 || p.SpecificTrap != 0 || p.Enterprise != sysObjectID || p.AgentAddress != host {
			t.Errorf("generic %d, specific %d, enterprise %s, agent %s", p.GenericTrap, p.SpecificTrap, p.Enterprise, p.AgentAddress)
		}
		if !slices.Equal(names(p.Variables), link) {
			t.Errorf("varbinds %v, want %v", names(p.Variables), link)
		}
		source(t, from)
	})
	t.Run("v2c", func(t *testing.T) {
		raw, from := v2c.next(t)
		p := decodeTrap(t, raw, gosnmp.Version2c)
		if p.PDUType != gosnmp.SNMPv2Trap || p.Community != "v2c-community" {
			t.Fatalf("%v with community %q", p.PDUType, p.Community)
		}
		if !slices.Equal(names(p.Variables), v2Order) {
			t.Fatalf("varbinds %v, want %v", names(p.Variables), v2Order)
		}
		if oid := trapOIDOf(t, p); oid != oidSnmpTraps+".3" {
			t.Errorf("snmpTrapOID.0 = %s", oid)
		}
		// What the notification says of eth1 is what a GET says: it is down.
		if oper := p.Variables[4].Value; oper != 2 {
			t.Errorf("ifOperStatus.3 = %v, want 2", oper)
		}
		source(t, from)
	})
	t.Run("v3", func(t *testing.T) {
		raw, from := v3.next(t)
		// Read with the user's keys for the DEVICE's engine: a v3 trap is sent
		// as the engine that is authoritative for it.
		sp := usmOf(notifyUser)
		sp.AuthoritativeEngineID = string(a.engineID)
		g := &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel,
			MsgFlags: gosnmp.AuthPriv, SecurityParameters: sp}
		p, err := g.UnmarshalTrap(raw, false)
		if err != nil {
			t.Fatalf("the v3 trap does not open with the device's keys: %v", err)
		}
		got := p.SecurityParameters.(*gosnmp.UsmSecurityParameters)
		if got.AuthoritativeEngineID != string(a.engineID) || got.AuthoritativeEngineBoots != a.boots || got.UserName != "ops" {
			t.Errorf("sent as engine %x, boots %d, user %q", got.AuthoritativeEngineID, got.AuthoritativeEngineBoots, got.UserName)
		}
		if p.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv || p.MsgFlags&gosnmp.Reportable != 0 {
			t.Errorf("flags %v: a trap is authPriv here, and never reportable (RFC 3412 6.4)", p.MsgFlags)
		}
		if !slices.Equal(names(p.Variables), v2Order) {
			t.Errorf("varbinds %v, want %v", names(p.Variables), v2Order)
		}
		source(t, from)
	})
}

// RFC 3584 3.2, for the notifications that are not generic: enterpriseSpecific,
// the last arc as the specific-trap, and the enterprise without it — and without
// the zero before it, where there is one.
func TestANotificationTranslatesToSNMPv1(t *testing.T) {
	a := newAgent(t, Config{Versions: []string{"v1"}, Community: "public",
		Objects: linuxServer.build(Identity{Name: "srv-01", Seed: 1})})
	c := clock{started: time.Now(), now: time.Now()}
	for _, tc := range []struct {
		n                 Notification
		generic, specific int
		enterprise        string
	}{
		{coldStart, 0, 0, ".1.3.6.1.4.1.8072.3.2.10"},
		{authenticationFailure, 4, 0, ".1.3.6.1.4.1.8072.3.2.10"},
		{Notification{Name: "nsNotifyShutdown", OID: ".1.3.6.1.4.1.8072.4.0.2"}, 6, 2, ".1.3.6.1.4.1.8072.4"},
		{Notification{Name: "vendor", OID: ".1.3.6.1.4.1.9.9.41.2.1"}, 6, 1, ".1.3.6.1.4.1.9.9.41.2"},
	} {
		trap, err := a.v1Trap(tc.n, c)
		if err != nil {
			t.Fatalf("%s: %v", tc.n.Name, err)
		}
		if trap.GenericTrap != tc.generic || trap.SpecificTrap != tc.specific || trap.Enterprise != tc.enterprise {
			t.Errorf("%s: generic %d, specific %d, enterprise %s", tc.n.Name, trap.GenericTrap, trap.SpecificTrap, trap.Enterprise)
		}
	}
	// SNMPv1 has no Counter64, and a notification carrying one is not sent in it.
	wide := Notification{Name: "wide", OID: ".1.3.6.1.4.1.9.0.1", Objects: []string{".1.3.6.1.2.1.31.1.1.1.6.2"}}
	if _, err := a.v1Trap(wide, c); err == nil {
		t.Error("a notification carrying a Counter64 was translated to SNMPv1")
	}
	// Nor has it a specific-trap past Integer32, which an arc can be.
	huge := Notification{Name: "huge", OID: ".1.3.6.1.4.1.9.0.4294967295"}
	if _, err := a.v1Trap(huge, c); err == nil {
		t.Error("a specific-trap past Integer32 was translated to SNMPv1")
	}
}

// Every notification a model lists can be sent: its OID parses, the device has
// every object it carries, and it has an SNMPv1 form.
func TestEveryNotificationOfAModelIsWhole(t *testing.T) {
	for _, m := range models {
		a := newAgent(t, Config{Versions: []string{"v1"}, Community: "public", Objects: m.build(Identity{Name: "x", Seed: 1})})
		c := clock{started: time.Now(), now: time.Now()}
		seen := map[string]bool{}
		for _, n := range m.catalogue() {
			if seen[n.Name] {
				t.Errorf("%s: %s is listed twice", m.ID, n.Name)
			}
			seen[n.Name] = true
			if _, err := parseOID(n.OID); err != nil {
				t.Errorf("%s: %s: %v", m.ID, n.Name, err)
			}
			if got := len(a.objectsOf(n, c)); got != len(n.Objects) {
				t.Errorf("%s: %s carries %d of its %d objects", m.ID, n.Name, got, len(n.Objects))
			}
			if _, err := a.v1Trap(n, c); err != nil {
				t.Errorf("%s: %s has no SNMPv1 form: %v", m.ID, n.Name, err)
			}
		}
	}
}

// answerInforms acknowledges every v2c INFORM that reaches conn, as a trap
// receiver does, until conn closes.
func answerInforms(conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		p, err := (&gosnmp.GoSNMP{Version: gosnmp.Version2c}).SnmpDecodePacket(buf[:n])
		if err != nil || p.PDUType != gosnmp.InformRequest {
			continue
		}
		p.PDUType = gosnmp.GetResponse
		if out, err := p.MarshalMsg(); err == nil {
			_, _ = conn.WriteToUDPAddrPort(out, from)
		}
	}
}

// An INFORM is reported acknowledged only when the receiver answers it — and a
// device stopping while one waits for an answer does not wait out the timeout.
func TestAnInformWaitsForItsAcknowledgement(t *testing.T) {
	answering := listenForTraps(t)
	go answerInforms(answering.conn)
	a := notifyingAgent(t, "127.0.0.1:0", Traps{Destinations: []Destination{
		{ID: "nms", Host: "127.0.0.1", Port: answering.port(), Version: "v2c", Community: "public", Inform: true}}})
	got, err := a.Notify("linkUp")
	if err != nil || len(got) != 1 || !got[0].Acknowledged || got[0].Error != "" {
		t.Fatalf("an answered INFORM: %+v, %v", got, err)
	}
	if st := a.Stats().Notifications; len(st) != 1 || st[0].Sent != 1 || st[0].Failed != 0 {
		t.Errorf("counted %+v", st)
	}

	unanswering := listenForTraps(t)
	b := notifyingAgent(t, "127.0.0.1:0", Traps{Destinations: []Destination{
		{ID: "void", Host: "127.0.0.1", Port: unanswering.port(), Version: "v2c", Community: "public", Inform: true}}})
	result := make(chan error, 1)
	go func() {
		_, err := b.Notify("linkUp")
		result <- err
	}()
	unanswering.next(t) // the INFORM is out, and waits
	start := time.Now()
	b.Stop()
	if took := time.Since(start); took > time.Second {
		t.Errorf("stopping took %v with an INFORM waiting", took)
	}
	if err := <-result; !errors.Is(err, ErrNotRunning) {
		t.Errorf("the waiting Notify returned %v, want ErrNotRunning", err)
	}
}

// managerOf is a gosnmp manager aimed at a, waiting timeout for an answer.
func managerOf(t *testing.T, a *Agent, g *gosnmp.GoSNMP, timeout time.Duration) *gosnmp.GoSNMP {
	t.Helper()
	ap := a.Addr()
	g.Target, g.Port, g.Timeout, g.Retries = ap.Addr().String(), ap.Port(), timeout, 0
	if g.Version == gosnmp.Version3 {
		g.SecurityModel, g.MsgFlags = gosnmp.UserSecurityModel, gosnmp.AuthPriv
	}
	return connect(t, g)
}

// A request refused as not authentic is notified — a wrong community, an
// unknown user, a wrong digest — and discovery and an authentic request are
// not. A burst of refusals is capped: each is a datagram anyone on the machine
// can send, and the notification may go to the network.
func TestARefusedRequestSendsAuthenticationFailure(t *testing.T) {
	r := listenForTraps(t)
	traps := func(port int) Traps {
		return Traps{OnAuthFailure: true, Destinations: []Destination{
			{ID: "nms", Host: "127.0.0.1", Port: port, Version: "v2c", Community: "public"}}}
	}
	a := notifyingAgent(t, "127.0.0.1:0", traps(r.port()))

	good := managerOf(t, a, &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityParameters: usmOf(notifyUser)}, 2*time.Second)
	if _, err := good.Get([]string{sysNameOID}); err != nil {
		t.Fatal(err)
	}
	if n := r.count(silent); n != 0 {
		t.Fatalf("discovery and an authentic request sent %d notifications", n)
	}

	bad := managerOf(t, a, &gosnmp.GoSNMP{Version: gosnmp.Version2c, Community: "wrong"}, 20*time.Millisecond)
	for range 10 {
		_, _ = bad.Get([]string{sysNameOID})
	}
	raw, _ := r.next(t)
	if oid := trapOIDOf(t, decodeTrap(t, raw, gosnmp.Version2c)); oid != authenticationFailure.OID {
		t.Errorf("a wrong community sent %s", oid)
	}
	if n := 1 + r.count(silent); n > authFailureBurst+1 {
		t.Errorf("ten refused requests sent %d notifications", n)
	}

	stranger := notifyUser
	stranger.Name = "nobody"
	forger := notifyUser
	forger.AuthPass = "not-the-passphrase"
	for name, u := range map[string]User{"an unknown user": stranger, "a wrong digest": forger} {
		t.Run(name, func(t *testing.T) {
			r := listenForTraps(t)
			a := notifyingAgent(t, "127.0.0.1:0", traps(r.port()))
			g := managerOf(t, a, &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityParameters: usmOf(u)}, 500*time.Millisecond)
			if _, err := g.Get([]string{sysNameOID}); err == nil {
				t.Fatal("the request was answered")
			}
			raw, _ := r.next(t)
			if oid := trapOIDOf(t, decodeTrap(t, raw, gosnmp.Version2c)); oid != authenticationFailure.OID {
				t.Errorf("sent %s", oid)
			}
		})
	}
}

// A device configured to says it started, then keeps its schedule.
func TestADeviceNotifiesItsStartThenKeepsItsSchedule(t *testing.T) {
	r := listenForTraps(t)
	notifyingAgent(t, "127.0.0.1:0", Traps{OnStart: true, Schedules: []Schedule{{Notification: "linkUp", Every: 1}},
		Destinations: []Destination{{ID: "nms", Host: "127.0.0.1", Port: r.port(), Version: "v2c", Community: "public"}}})
	for _, want := range []Notification{coldStart, linkNotification(true, 2)} {
		raw, _ := r.next(t)
		if got := trapOIDOf(t, decodeTrap(t, raw, gosnmp.Version2c)); got != want.OID {
			t.Errorf("got %s, want %s (%s)", got, want.OID, want.Name)
		}
	}
}

// SnmpLens hears a simulated device: its trap listener journals the device's
// v2c trap, and acknowledges its SNMPv3 INFORM sent to the engine ID the
// listener stands for.
func TestSnmpLensHearsTheSimulator(t *testing.T) {
	engineID, _ := hex.DecodeString("8000000005a1b2c3d4e5f60718")
	traps, informs := make(chan events.Event, 1), make(chan events.Event, 1)
	// A headless client: it journals what it hears and tells no window.
	client := snmp.NewClient()
	client.SetRecorder(events.RecorderFunc(func(e events.Event, _ string) error {
		ch := traps
		if e.Kind == events.KindTrapInform {
			ch = informs
		}
		select {
		case ch <- e:
		default:
		}
		return nil
	}))
	client.SetTrapEngineID(engineID)
	c, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	info, err := client.StartTrapListener(port, []snmp.V3Params{{User: notifyUser.Name, SecLevel: notifyUser.SecLevel,
		AuthProto: notifyUser.AuthProto, AuthPass: notifyUser.AuthPass, PrivProto: notifyUser.PrivProto, PrivPass: notifyUser.PrivPass}})
	if err != nil {
		t.Skipf("cannot bind a trap listener: %v", err)
	}
	t.Cleanup(client.StopTrapListener)
	if info.Users != 1 {
		t.Fatalf("the listener took %d users: %+v", info.Users, info.Refused)
	}

	// The listener binds on a goroutine of its own, so the device sends until
	// it is heard.
	trapper := notifyingAgent(t, "127.0.0.1:0", Traps{Destinations: []Destination{
		{ID: "trap", Host: "127.0.0.1", Port: port, Version: "v2c", Community: "public"}}})
	deadline := time.After(5 * time.Second)
	for heard := false; !heard; {
		if _, err := trapper.Notify("linkUp"); err != nil {
			t.Fatal(err)
		}
		select {
		case e := <-traps:
			if e.OID != oidSnmpTraps+".4" {
				t.Errorf("journalled the trap as %q", e.OID)
			}
			heard = true
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("SnmpLens never heard the simulated device's trap")
		}
	}

	informer := notifyingAgent(t, "127.0.0.1:0", Traps{Destinations: []Destination{
		{ID: "inform", Host: "127.0.0.1", Port: port, Version: "v3", User: "ops", Inform: true,
			EngineID: hex.EncodeToString(engineID)}}})
	got, err := informer.Notify("linkDown")
	if err != nil || len(got) != 1 || !got[0].Acknowledged {
		t.Fatalf("the SNMPv3 INFORM: %+v, %v", got, err)
	}
	// Acknowledged means journalled first: the insert is what the
	// acknowledgement waits for.
	select {
	case e := <-informs:
		if e.OID != oidSnmpTraps+".3" {
			t.Errorf("journalled the INFORM as %q", e.OID)
		}
	default:
		t.Error("an acknowledged INFORM was not journalled")
	}
}

// Where a device may send, and what, is checked when it is saved.
func TestTrapsAreHeldToTheRules(t *testing.T) {
	valid := func() Device {
		d := validDevice(t)
		d.Traps = Traps{
			Destinations: []Destination{
				{ID: "a", Host: "192.0.2.10", Port: 162, Version: "v2c", Community: "public"},
				{ID: "b", Host: "nms.example.com", Port: 10162, Version: "v3", User: "ops", Inform: true, EngineID: "8000000005aabbccdd"},
			},
			Schedules: []Schedule{{Notification: "linkDown", Every: 60}, {Every: 5, Irregular: true}},
		}
		return d
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid traps: %v", err)
	}
	for missing, change := range map[string]func(*Traps){
		"a host":                         func(tr *Traps) { tr.Destinations[0].Host = "" },
		"a host name that is one":        func(tr *Traps) { tr.Destinations[0].Host = "-nms.example.com" },
		"an address to send to":          func(tr *Traps) { tr.Destinations[0].Host = "0.0.0.0" },
		"one receiver, not a group":      func(tr *Traps) { tr.Destinations[0].Host = "224.0.0.1" },
		"a port":                         func(tr *Traps) { tr.Destinations[0].Port = 0 },
		"a known version":                func(tr *Traps) { tr.Destinations[0].Version = "v2" },
		"an INFORM SNMPv1 has":           func(tr *Traps) { tr.Destinations[0].Version, tr.Destinations[0].Inform = "v1", true },
		"a community":                    func(tr *Traps) { tr.Destinations[0].Community = "" },
		"a user the device has":          func(tr *Traps) { tr.Destinations[1].User = "nobody" },
		"an engine ID in hex":            func(tr *Traps) { tr.Destinations[1].EngineID = "zz" },
		"an engine ID of 5 octets":       func(tr *Traps) { tr.Destinations[1].EngineID = "80000000" },
		"an ID":                          func(tr *Traps) { tr.Destinations[0].ID = "" },
		"an ID of its own":               func(tr *Traps) { tr.Destinations[1].ID = "a" },
		"a notification the device has":  func(tr *Traps) { tr.Schedules[0].Notification = "linkSideways" },
		"an interval":                    func(tr *Traps) { tr.Schedules[0].Every = 0 },
		"an interval of a day at most":   func(tr *Traps) { tr.Schedules[0].Every = MaxEvery + 1 },
		"a bounded list of destinations": func(tr *Traps) { tr.Destinations = make([]Destination, MaxDestinations+1) },
		"a bounded list of schedules":    func(tr *Traps) { tr.Schedules = make([]Schedule, MaxSchedules+1) },
	} {
		d := valid()
		change(&d.Traps)
		if err := d.Validate(); err == nil {
			t.Errorf("traps without %s were accepted", missing)
		}
	}
	// A device's users are its SNMPv3 users: one that does not answer v3 has
	// none to send a v3 notification as.
	d := valid()
	d.Versions = []string{"v2c"}
	if err := d.Validate(); err == nil {
		t.Error("a device that does not answer v3 was given a v3 destination")
	}
}
