package simulator

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// benchDevice is a device of model on address, ready to be planned and built.
func newBenchDevice(t *testing.T, id, name, model, address string) Device {
	t.Helper()
	engineID, err := NewEngineID(model, id)
	if err != nil {
		t.Fatal(err)
	}
	return Device{ID: id, Name: name, Model: model, Address: address, Port: 161, Versions: []string{"v2c"},
		Community: "public", EngineID: engineID}
}

// testBench is a campus on the bench: a core, a Cisco access switch with an
// access point on one of its ports, an Aruba with a phone on its voice VLAN,
// and a server on the core.
type testBench struct {
	core, access, aruba, phone, ap, srv Device
	links                               []Link
}

func newTestBench(t *testing.T) testBench {
	b := testBench{
		core:   newBenchDevice(t, "c0de000000000001", "core-01", "cisco-catalyst-9300", "127.0.0.2"),
		access: newBenchDevice(t, "acce550000000002", "sw-floor-2", "cisco-catalyst-24", "127.0.0.3"),
		aruba:  newBenchDevice(t, "a7ba000000000003", "sw-floor-3", "aruba-2930f-24", "127.0.0.4"),
		phone:  newBenchDevice(t, "f0e0000000000004", "phone-3-18", "ip-phone", "127.0.0.5"),
		ap:     newBenchDevice(t, "a9a9000000000005", "ap-hall", "unifi-u6-pro", "127.0.0.6"),
		srv:    newBenchDevice(t, "5e70000000000006", "srv-db", "linux-server", "127.0.0.7"),
	}
	cable := func(id string, a Device, aPort string, z Device, zPort string) Link {
		return Link{ID: id, A: LinkEnd{a.ID, aPort}, B: LinkEnd{z.ID, zPort}}
	}
	b.links = []Link{
		cable("l1", b.access, "Gi0/1", b.core, "Te1/1/1"),
		cable("l2", b.aruba, "25", b.core, "Te1/1/2"),
		cable("l3", b.phone, "eth0", b.aruba, "18"),
		cable("l4", b.ap, "eth0", b.access, "Fa0/5"),
		cable("l5", b.srv, "eth0", b.core, "Gi1/0/5"),
	}
	if err := CheckLinks(b.devices(), b.links); err != nil {
		t.Fatalf("the bench's cables: %v", err)
	}
	wiring := PlanBench(b.devices(), b.links)
	for _, d := range []*Device{&b.core, &b.access, &b.aruba, &b.phone, &b.ap, &b.srv} {
		if d.Wiring = wiring[d.ID]; d.Wiring == nil {
			t.Fatalf("%s is cabled and has no wiring", d.Name)
		}
	}
	return b
}

func (b testBench) devices() []Device {
	return []Device{b.core, b.access, b.aruba, b.phone, b.ap, b.srv}
}

// viewOf is d's tree: its default view, or the view of context.
func viewOf(t *testing.T, d Device, context string) *tree {
	t.Helper()
	objs, contexts, err := d.views()
	if err != nil {
		t.Fatal(err)
	}
	if context != "" {
		var ok bool
		if objs, ok = contexts[context]; !ok {
			t.Fatalf("%s has no context %q", d.Name, context)
		}
	}
	tr, err := newTree(objs)
	if err != nil {
		t.Fatalf("%s: %v", d.Name, err)
	}
	return tr
}

// under is every instance under prefix, by the arcs after it.
func under(tr *tree, prefix string) map[string]any {
	root, err := parseOID(prefix)
	if err != nil {
		panic(err)
	}
	out := map[string]any{}
	for e := tr.next(root, nil); e != nil && e.oid.hasPrefix(root); e = tr.next(e.oid, nil) {
		out[strings.TrimPrefix(e.oid[len(root):].String(), ".")] = e.val.read(clock{})
	}
	return out
}

type heard struct{ name, port, addr string }

// lldpHeard is what a device's LLDP hears, by local port number.
func lldpHeard(tr *tree) map[int]heard {
	out := map[int]heard{}
	key := func(inst string) int {
		var mark, local, idx int
		fmt.Sscanf(inst, "%d.%d.%d", &mark, &local, &idx)
		return local
	}
	for inst, v := range under(tr, "1.0.8802.1.1.2.1.4.1.1.9") {
		h := out[key(inst)]
		h.name = textOf(v)
		out[key(inst)] = h
	}
	for inst, v := range under(tr, "1.0.8802.1.1.2.1.4.1.1.7") {
		h := out[key(inst)]
		h.port = textOf(v)
		out[key(inst)] = h
	}
	for inst := range under(tr, "1.0.8802.1.1.2.1.4.2.1.3") {
		parts := strings.Split(inst, ".")
		if len(parts) == 9 && parts[3] == "1" && parts[4] == "4" {
			h := out[key(inst)]
			h.addr = strings.Join(parts[5:], ".")
			out[key(inst)] = h
		}
	}
	return out
}

// Two devices cabled together say so of each other, each giving the other's
// name, the port the cable goes into and the address it answers on — what a
// manager following LLDP needs to reach the next device.
func TestCabledDevicesHearEachOther(t *testing.T) {
	b := newTestBench(t)
	core, access := viewOf(t, b.core, ""), viewOf(t, b.access, "")

	if got := lldpHeard(core)[c9300Uplink(1)]; got != (heard{"sw-floor-2", "Gi0/1", "127.0.0.3"}) {
		t.Errorf("the core hears on Te1/1/1: %+v", got)
	}
	if got := lldpHeard(access)[25]; got != (heard{"core-01", "Te1/1/1", "127.0.0.2"}) {
		t.Errorf("the access switch hears on Gi0/1: %+v", got)
	}
	// The invented core is gone from the uplink; the invented access point is
	// still on the port with no cable, and the real one on its own.
	for local, h := range lldpHeard(access) {
		if h.name == fictionalCoreName {
			t.Errorf("the invented core is still heard on %d", local)
		}
	}
	if got := lldpHeard(access)[2].name; got != "ap-openspace" {
		t.Errorf("port 2 hears %q, want the invented access point", got)
	}
	if got := lldpHeard(access)[5]; got.name != "ap-hall" || got.addr != "127.0.0.6" {
		t.Errorf("Fa0/5 hears %+v, want the access point cabled to it", got)
	}
	// A device with no LLDP of its own on the bench sends it: the server is heard
	// by the core, and hears it.
	if got := lldpHeard(viewOf(t, b.srv, ""))[2]; got.name != "core-01" || got.port != "Gi1/0/5" {
		t.Errorf("the server hears %+v", got)
	}

	// CDP between the two Ciscos, by the address each answers on; the Aruba
	// runs none.
	cdp := under(core, "1.3.6.1.4.1.9.9.23.1.2.1.1.6")
	var names []string
	for _, v := range cdp {
		names = append(names, textOf(v))
	}
	if len(names) != 1 || names[0] != "sw-floor-2" {
		t.Errorf("the core's CDP cache names %v", names)
	}
	addr := under(access, "1.3.6.1.4.1.9.9.23.1.2.1.1.4")[fmt.Sprintf("%d.1", 25)]
	if got := net.IP(bytesOf(addr)).String(); got != "127.0.0.2" {
		t.Errorf("the access switch's CDP gives the core at %s", got)
	}
}

// What a switch learns on a cabled port is what is at the other end and behind
// it, each in its VLAN: the phone and the computer behind it reach the core
// through the Aruba, in the voice VLAN.
func TestAStationIsLearntThroughEverySwitchOnItsWay(t *testing.T) {
	b := newTestBench(t)
	phoneMAC := deviceMAC(deviceSeed(b.phone.ID), 0)
	pc := phonePC(deviceSeed(b.phone.ID))

	// The Aruba says the VLAN in Q-BRIDGE-MIB.
	qfdb := under(viewOf(t, b.aruba, ""), "1.3.6.1.2.1.17.7.1.2.2.1.2")
	for _, mac := range []net.HardwareAddr{phoneMAC, pc} {
		if got := qfdb["30."+macArcs(mac)]; got != 18 {
			t.Errorf("the Aruba learnt %s on %v in VLAN 30, want port 18", mac, got)
		}
	}
	// The core says it per VLAN: in VLAN 30's view, on the port the Aruba is
	// cabled to, and not in VLAN 1's.
	bridgePort := func(tr *tree, ifIndex int) int {
		for bp, v := range under(tr, "1.3.6.1.2.1.17.1.4.1.2") {
			if v == ifIndex {
				var n int
				fmt.Sscan(bp, &n)
				return n
			}
		}
		return 0
	}
	voice := viewOf(t, b.core, "vlan-30")
	want := bridgePort(voice, c9300Uplink(2))
	if want == 0 || want == c9300Uplink(2) {
		t.Fatalf("Te1/1/2 is bridge port %d: the core should number them apart", want)
	}
	if got := under(voice, "1.3.6.1.2.1.17.4.3.1.2")[macArcs(phoneMAC)]; got != want {
		t.Errorf("the core learnt the phone on %v in VLAN 30, want %d", got, want)
	}
	if _, ok := under(viewOf(t, b.core, ""), "1.3.6.1.2.1.17.4.3.1.2")[macArcs(phoneMAC)]; ok {
		t.Error("the phone is in the core's default view, which is VLAN 1's")
	}
}

// The core routes the bench's subnets: its ARP cache, in both IP-MIB tables,
// holds the address each station is given, and the IPv6 neighbours too.
func TestTheCoreKnowsEveryStationsAddress(t *testing.T) {
	b := newTestBench(t)
	core := viewOf(t, b.core, "")
	phoneMAC := deviceMAC(deviceSeed(b.phone.ID), 0)

	var v4, v6 bool
	for inst, v := range under(core, "1.3.6.1.2.1.4.35.1.4") {
		if net.HardwareAddr(bytesOf(v)).String() != phoneMAC.String() {
			continue
		}
		switch parts := strings.Split(inst, "."); {
		case parts[1] == "1" && strings.HasPrefix(strings.Join(parts[3:], "."), "10.20.30."):
			v4 = true
		case parts[1] == "2" && len(parts) == 19:
			var b16 [16]byte
			for i := range b16 {
				fmt.Sscan(parts[3+i], &b16[i])
			}
			v6 = benchPrefix6(30).Contains(netip.AddrFrom16(b16))
		}
	}
	if !v4 || !v6 {
		t.Errorf("the core's ipNetToPhysicalTable has the phone in IPv4: %v, in IPv6: %v", v4, v6)
	}

	// The phone is given an address in the voice VLAN, and its gateway is the
	// core, by the core's MAC.
	phone := viewOf(t, b.phone, "")
	var mine string
	for a := range under(phone, "1.3.6.1.2.1.4.20.1.1") {
		if strings.HasPrefix(a, "10.20.30.") {
			mine = a
		}
	}
	if mine == "" {
		t.Fatalf("the phone has no address in the voice VLAN: %v", under(phone, "1.3.6.1.2.1.4.20.1.1"))
	}
	gw := under(phone, "1.3.6.1.2.1.4.22.1.2")["1.10.20.30.1"]
	if got := net.HardwareAddr(bytesOf(gw)).String(); got != deviceMAC(deviceSeed(b.core.ID), 0).String() {
		t.Errorf("the phone's gateway answers with %s, want the core's MAC", got)
	}
}

// A cable is plugged into ports that exist, one cable a port, between two
// devices.
func TestCablesAreChecked(t *testing.T) {
	b := newTestBench(t)
	ds := b.devices()
	for what, links := range map[string][]Link{
		"a device to itself": {{ID: "x", A: LinkEnd{b.core.ID, "Gi1/0/1"}, B: LinkEnd{b.core.ID, "Gi1/0/2"}}},
		"a port nobody has":  {{ID: "x", A: LinkEnd{b.core.ID, "Gi9/9/9"}, B: LinkEnd{b.srv.ID, "eth0"}}},
		"a VLAN interface":   {{ID: "x", A: LinkEnd{b.core.ID, "Vl10"}, B: LinkEnd{b.srv.ID, "eth0"}}},
		"a device gone":      {{ID: "x", A: LinkEnd{"nobody", "eth0"}, B: LinkEnd{b.srv.ID, "eth0"}}},
		"a port twice": {
			{ID: "x", A: LinkEnd{b.core.ID, "Gi1/0/1"}, B: LinkEnd{b.srv.ID, "eth0"}},
			{ID: "y", A: LinkEnd{b.access.ID, "Fa0/1"}, B: LinkEnd{b.srv.ID, "eth0"}},
		},
		"no ID": {{A: LinkEnd{b.core.ID, "Gi1/0/1"}, B: LinkEnd{b.srv.ID, "eth0"}}},
	} {
		if err := CheckLinks(ds, links); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}

	ports, err := Ports(b.access)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 26 || ports[0].Name != "Fa0/1" || ports[25].Name != "Gi0/2" {
		t.Errorf("the 2960's ports: %d, %+v … %+v", len(ports), ports[0], ports[len(ports)-1])
	}
}

// A Cisco answers BRIDGE-MIB per VLAN over the wire: the community alone reads
// VLAN 1's stations, the community indexed by a VLAN that VLAN's, and so does
// the SNMPv3 context named after it; a VLAN the switch has not is refused.
func TestAVLANIsReadInItsOwnContext(t *testing.T) {
	d := newBenchDevice(t, "acce550000000009", "sw-floor-2", "cisco-catalyst-24", "127.0.0.1")
	d.Versions = []string{"v2c", "v3"}
	d.Users = []User{{Name: "ops", SecLevel: "AuthPriv", AuthProto: "SHA256", AuthPass: "authpass-1",
		PrivProto: "AES", PrivPass: "privpass-1"}}
	cfg, err := d.config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "127.0.0.1:0"
	a := startAgent(t, cfg)

	stations := func(g *gosnmp.GoSNMP) (int, error) {
		n := 0
		err := g.BulkWalk("1.3.6.1.2.1.17.4.3.1.2", func(gosnmp.SnmpPDU) error { n++; return nil })
		return n, err
	}
	inVLAN1, err := stations(connect(t, manager(a, gosnmp.Version2c, "public")))
	if err != nil {
		t.Fatal(err)
	}
	inVLAN10, err := stations(connect(t, manager(a, gosnmp.Version2c, "public@10")))
	if err != nil {
		t.Fatal(err)
	}
	if inVLAN1 == 0 || inVLAN10 == 0 {
		t.Fatalf("stations in VLAN 1: %d, in VLAN 10: %d", inVLAN1, inVLAN10)
	}

	g := v3Manager(a, "ops", gosnmp.AuthPriv, gosnmp.SHA256, "authpass-1", gosnmp.AES, "privpass-1")
	g.ContextName = "vlan-10"
	if n, err := stations(connect(t, g)); err != nil || n != inVLAN10 {
		t.Errorf("SNMPv3 in context vlan-10: %d stations (%v), want %d", n, err, inVLAN10)
	}

	none := manager(a, gosnmp.Version2c, "public@99")
	none.Timeout = silent
	if _, err := connect(t, none).Get([]string{"1.3.6.1.2.1.1.5.0"}); err == nil {
		t.Error("a VLAN the switch has not was answered")
	}
	wrong := manager(a, gosnmp.Version2c, "private@10")
	wrong.Timeout = silent
	if _, err := connect(t, wrong).Get([]string{"1.3.6.1.2.1.1.5.0"}); err == nil {
		t.Error("a wrong community indexed by a VLAN was answered")
	}
	// A VLAN's view is read-only.
	set := connect(t, manager(a, gosnmp.Version2c, "public@10"))
	res, err := set.Set([]gosnmp.SnmpPDU{{Name: "1.3.6.1.2.1.1.5.0", Type: gosnmp.OctetString, Value: "x"}})
	if err == nil && res.Error == gosnmp.NoError {
		t.Error("a SET in a VLAN's view was applied")
	}
}

// A running device recabled answers as it is cabled now, without restarting:
// its engine's boots and its uptime carry on.
func TestARunningDeviceIsRecabledWithoutRestarting(t *testing.T) {
	b := newTestBench(t)
	f := NewFleet()
	t.Cleanup(f.StopAll)
	core := b.core
	core.Wiring = nil
	core.EngineBoots = 4
	core = startOnFreePort(t, f, core)
	// Retries, unlike the tests that count what the agent receives: a loaded
	// runner lost the first walk after the start, and this test is about what
	// the answers say, not how many requests it took.
	g := connect(t, &gosnmp.GoSNMP{Target: core.Address, Port: uint16(core.Port), Version: gosnmp.Version2c,
		Community: "public", Timeout: 2 * time.Second, Retries: 3})
	// What lldpRemSysName says is on Te1/1/1, whatever its place in the table.
	heardOn := func() string {
		prefix := fmt.Sprintf(".1.0.8802.1.1.2.1.4.1.1.9.0.%d.", c9300Uplink(1))
		name := ""
		err := g.BulkWalk("1.0.8802.1.1.2.1.4.1.1.9", func(p gosnmp.SnmpPDU) error {
			if strings.HasPrefix(p.Name, prefix) {
				name = textOf(p.Value)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	if got := heardOn(); got != "" {
		t.Fatalf("the core cabled to nothing hears %q", got)
	}
	time.Sleep(20 * time.Millisecond)

	core.Wiring = b.core.Wiring
	if err := f.Rewire(core); err != nil {
		t.Fatal(err)
	}
	if got := heardOn(); got != "sw-floor-2" {
		t.Errorf("recabled, the core hears %q on Te1/1/1", got)
	}
	res, err := g.Get([]string{"1.3.6.1.2.1.1.3.0", "1.3.6.1.6.3.10.2.1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if up := res.Variables[0].Value.(uint32); up == 0 {
		t.Error("the uptime started again")
	}
	if !errors.Is(f.Rewire(Device{ID: "nobody"}), ErrNotRunning) {
		t.Error("a device that is not running was rewired")
	}
}

// An invented neighbour is the same device whichever model invents it: the
// core every access switch hangs from has one chassis, so two switches cabled
// to nothing draw one core between them.
func TestTheInventedCoreIsOneDevice(t *testing.T) {
	var chassis []string
	for _, model := range []string{"cisco-catalyst-24", "aruba-2930f-24", "cisco-isr-4331"} {
		d := newBenchDevice(t, "00000000000000"+fmt.Sprint(len(chassis)+10), "dev", model, "127.0.0.9")
		for inst, v := range under(viewOf(t, d, ""), "1.0.8802.1.1.2.1.4.1.1.9") {
			if textOf(v) == fictionalCoreName {
				c := under(viewOf(t, d, ""), "1.0.8802.1.1.2.1.4.1.1.5")[inst]
				chassis = append(chassis, net.HardwareAddr(bytesOf(c)).String())
			}
		}
	}
	if len(chassis) != 3 || chassis[0] != chassis[1] || chassis[1] != chassis[2] {
		t.Errorf("the invented core's chassis: %v", chassis)
	}
}
