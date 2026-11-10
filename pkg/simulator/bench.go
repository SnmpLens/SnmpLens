package simulator

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The bench: simulated devices cabled to each other, port to port, so that what
// each says of the others agrees. A switch's LLDP and CDP name the device at the
// far end of a cable, with the port it is plugged into and the address it
// answers on; the switch learns that device's MACs on the port, and everything
// behind it; a router's ARP cache holds the address of every station it routes
// for. Without cables a device answers as its model always has, its neighbours
// invented — and the moment two are cabled, a network map drawn from them is
// one network, and following the neighbours reaches the next device.
//
// The bench has an addressing plan of its own, because the models each draw a
// private /24 from their seed and nothing that routes would agree with them: VLAN
// v is 10.(20+v/256).(v%256).0/24 and fd20:0:0:v::/64, its gateway is .1, the
// devices take .2 to .99 and the stations .100 to .249. Its management network is
// this machine's loopback: LLDP and CDP give a device's address as the one it
// answers on, since that is where a manager following them must go.

// The world the models invent when nothing is cabled to them is one world: an
// access switch hangs from the core every simulated access switch hangs from,
// and a router's LAN is plugged into it, so that two devices naming it name
// one device.
const fictionalCoreName = "core-sw-01"

var fictionalCoreChassis = net.HardwareAddr{0x02, 0x00, 0x5e, 0x10, 0x00, 0x01}

// Link is a cable between two ports of two simulated devices.
type Link struct {
	ID string  `json:"id"`
	A  LinkEnd `json:"a"`
	B  LinkEnd `json:"b"`
}

// LinkEnd is one end of a cable: a device, by ID, and one of its ports by the
// name its model gives the interface (ifName).
type LinkEnd struct {
	Device string `json:"device"`
	Port   string `json:"port"`
}

// NewLinkID is a new cable's ID, random as a device's is.
func NewLinkID() string { return NewDeviceID() }

// Port is a port a cable can go into: an Ethernet interface of the device.
type Port struct {
	// Name is ifName, which a cable names the port by; Descr is ifDescr.
	Name  string `json:"name"`
	Descr string `json:"descr"`
	// SpeedMbps is ifHighSpeed.
	SpeedMbps uint64 `json:"speedMbps"`
}

// Wiring is where a device sits on the bench. The zero of every field is a
// device answering as its model does.
type Wiring struct {
	// Ports are what is cabled to the device's ports, by ifName.
	Ports map[string]Wire
	// Listen is where the device answers SNMP: the management address its own
	// LLDP gives.
	Listen netip.Addr
	// Mgmt is the address the bench gives a device with one address, Gateway
	// its next hop, and GatewayMAC the MAC that address answers with when a
	// device of the bench routes it.
	Mgmt       netip.Prefix
	Gateway    netip.Addr
	GatewayMAC net.HardwareAddr
	// Hosts are the stations a device that routes can reach, each with its
	// addresses: what its ARP cache holds for the subnets it is on.
	Hosts []Station
}

// Wire is one cabled port: the device at the far end, and every station learnt
// through the port — that device's own MACs, and what is behind it.
type Wire struct {
	Peer   Peer
	Behind []Station
}

// Peer is the device at the far end of a cable, as LLDP and CDP describe it.
type Peer struct {
	Name, Descr string
	// Chassis is its LLDP chassis ID, a MAC.
	Chassis net.HardwareAddr
	// Port and PortDescr are the port the cable goes into, ifName and ifDescr;
	// MAC is that port's.
	Port, PortDescr string
	MAC             net.HardwareAddr
	// Caps are its LLDP system capabilities, as one octet.
	Caps byte
	// Addr is where it answers SNMP, which LLDP and CDP give as its management
	// address.
	Addr netip.Addr
	// CDP is a device that runs CDP, and Platform what CDP calls it.
	CDP      bool
	Platform string
}

// Station is a MAC a switch learns, with the addresses it answers on and the
// VLAN it is in.
type Station struct {
	MAC  net.HardwareAddr
	IP   netip.Addr
	IP6  netip.Addr
	VLAN int
}

// capsFor is the LLDP capabilities a device without an LLDP agent of its own is
// given on the bench, read from what it is: whether it forwards (ipForwarding)
// and whether it bridges (BRIDGE-MIB).
func capsFor(router, bridge bool) byte {
	var c byte
	if bridge {
		c |= capBridge
	}
	if router {
		c |= capRouter
	}
	if c == 0 {
		c = capStation
	}
	return c
}

// benchPrefix is VLAN v's IPv4 subnet on the bench, and benchPrefix6 its IPv6
// one.
func benchPrefix(v int) netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(20 + v/256), byte(v % 256), 0}), 24)
}

func benchPrefix6(v int) netip.Prefix {
	var b [16]byte
	b[0], b[1] = 0xfd, 0x20
	binary.BigEndian.PutUint16(b[6:], uint16(v))
	return netip.PrefixFrom(netip.AddrFrom16(b), 64)
}

func host6In(p netip.Prefix, n int) netip.Addr {
	b := p.Masked().Addr().As16()
	binary.BigEndian.PutUint32(b[12:], uint32(n))
	return netip.AddrFrom16(b)
}

// isBench says whether p is one of the bench's subnets.
func isBench(p netip.Prefix) bool {
	a := p.Masked().Addr()
	if !a.Is4() || p.Bits() != 24 {
		return false
	}
	b := a.As4()
	v := (int(b[1])-20)*256 + int(b[2])
	return b[0] == 10 && b[1] >= 20 && b[1] <= 35 && v >= 1 && v <= 4094
}

// The OIDs a device is described from.
const (
	oidSysDescr         = "1.3.6.1.2.1.1.1.0"
	oidSysName          = "1.3.6.1.2.1.1.5.0"
	oidIfDescr          = "1.3.6.1.2.1.2.2.1.2."
	oidIfType           = "1.3.6.1.2.1.2.2.1.3."
	oidIfPhysAddress    = "1.3.6.1.2.1.2.2.1.6."
	oidIfName           = "1.3.6.1.2.1.31.1.1.1.1."
	oidIfHighSpeed      = "1.3.6.1.2.1.31.1.1.1.15."
	oidIPForwarding     = "1.3.6.1.2.1.4.1.0"
	oidIPAdEntIfIndex   = "1.3.6.1.2.1.4.20.1.2."
	oidLLDPChassis      = "1.0.8802.1.1.2.1.3.2.0"
	oidLLDPCapsEnabled  = "1.0.8802.1.1.2.1.3.6.0"
	oidBasePortIfIndex  = "1.3.6.1.2.1.17.1.4.1.2."
	oidStpRootPort      = "1.3.6.1.2.1.17.2.7.0"
	oidFdbPort          = "1.3.6.1.2.1.17.4.3.1.2."
	oidFdbStatus        = "1.3.6.1.2.1.17.4.3.1.3."
	oidPvid             = "1.3.6.1.2.1.17.7.1.4.5.1.1."
	oidCDPRun           = "1.3.6.1.4.1.9.9.23.1.3.1.0"
	oidEntPhysicalClass = "1.3.6.1.2.1.47.1.1.1.1.5."
	oidEntPhysicalModel = "1.3.6.1.2.1.47.1.1.1.1.13."
)

// benchDevice is a device as the bench sees it, read from its model built on
// its own: what it is, its ports, and what it learnt on them.
type benchDevice struct {
	id, name, descr string
	chassis         net.HardwareAddr
	caps            byte
	bridge, router  bool
	cdp             bool
	platform        string
	listen          netip.Addr
	ports           []benchPort
	byName          map[string]*benchPort
	// learnt are the stations its bridge learnt, by port name, the root port's
	// left out: those are the rest of a network the bench replaces. attached
	// are what it bridges for without a bridge MIB, by port name.
	learnt   map[string][]Station
	attached map[string][]net.HardwareAddr
	// addrs are its IPv4 addresses, 127.0.0.0/8 left out.
	addrs []netip.Addr
}

type benchPort struct {
	index       int
	name, descr string
	mac         net.HardwareAddr
	speedMbps   uint64
	bridgePort  int
	pvid        int
}

// describe reads d — built on its own, cabled to nothing — as the bench sees it.
func describe(d Device) (*benchDevice, error) {
	m, ok := findModel(d.Model)
	if !ok {
		return nil, fmt.Errorf("%q is not a device model", d.Model)
	}
	if err := m.checkParams(d.Params); err != nil {
		return nil, err
	}
	own, err := compileOverrides(d.Overrides)
	if err != nil {
		return nil, err
	}
	d.Wiring = nil
	id := d.identity()
	// Every object, every VLAN's: what a Cisco learnt in VLAN 20 is in no
	// single view of it.
	objs := withOverrides(m.build(id), own)

	b := &benchDevice{id: d.ID, byName: map[string]*benchPort{}, learnt: map[string][]Station{},
		listen: listenAddr(d.Address)}
	if m.attached != nil {
		b.attached = m.attached(id)
	}
	byIndex := map[int]*benchPort{}
	port := func(i int) *benchPort {
		if byIndex[i] == nil {
			byIndex[i] = &benchPort{index: i}
		}
		return byIndex[i]
	}
	types := map[int]int{}
	baseIf := map[int]int{}
	type learnt struct {
		mac        net.HardwareAddr
		port, vlan int
	}
	var fdb []learnt
	status := map[string]int{}
	classes := map[int]int{}
	modelNames := map[int]string{}
	rootPort := 0
	for _, o := range objs {
		v := valueAtStart(o)
		switch {
		case o.OID == oidSysDescr:
			b.descr = textOf(v)
		case o.OID == oidSysName:
			b.name = textOf(v)
		case o.OID == oidIPForwarding:
			b.router = intOf(v) == 1
		case o.OID == oidLLDPChassis:
			if mac := bytesOf(v); len(mac) == 6 {
				b.chassis = net.HardwareAddr(mac)
			}
		case o.OID == oidLLDPCapsEnabled:
			if c := bytesOf(v); len(c) > 0 {
				b.caps = c[0]
			}
		case o.OID == oidCDPRun:
			b.cdp = intOf(v) == 1
		case o.OID == oidStpRootPort:
			rootPort = intOf(v)
		}
		if i, ok := indexAfter(o.OID, oidIfDescr); ok {
			port(i).descr = textOf(v)
		} else if i, ok := indexAfter(o.OID, oidIfType); ok {
			types[i] = intOf(v)
		} else if i, ok := indexAfter(o.OID, oidIfPhysAddress); ok {
			port(i).mac = net.HardwareAddr(bytesOf(v))
		} else if i, ok := indexAfter(o.OID, oidIfName); ok {
			port(i).name = textOf(v)
		} else if i, ok := indexAfter(o.OID, oidIfHighSpeed); ok {
			port(i).speedMbps = uint64(intOf(v))
		} else if i, ok := indexAfter(o.OID, oidBasePortIfIndex); ok {
			baseIf[i] = intOf(v)
			b.bridge = true
		} else if i, ok := indexAfter(o.OID, oidEntPhysicalClass); ok {
			classes[i] = intOf(v)
		} else if i, ok := indexAfter(o.OID, oidEntPhysicalModel); ok {
			modelNames[i] = textOf(v)
		} else if rest, ok := strings.CutPrefix(o.OID, oidFdbPort); ok {
			if mac, ok := macFromArcs(rest); ok {
				fdb = append(fdb, learnt{mac, intOf(v), o.VLAN})
			}
		} else if rest, ok := strings.CutPrefix(o.OID, oidFdbStatus); ok {
			status[strconv.Itoa(o.VLAN)+"/"+rest] = intOf(v)
		} else if rest, ok := strings.CutPrefix(o.OID, oidIPAdEntIfIndex); ok {
			if a, err := netip.ParseAddr(rest); err == nil && a.Is4() && !a.IsLoopback() {
				b.addrs = append(b.addrs, a)
			}
		}
	}
	for bp, ifIndex := range baseIf {
		port(ifIndex).bridgePort = bp
	}
	// The pvids, now that every port knows its bridge port.
	pvids := map[int]int{}
	for _, o := range objs {
		if rest, ok := strings.CutPrefix(o.OID, oidPvid); ok {
			if bp, err := strconv.Atoi(rest); err == nil {
				pvids[bp] = intOf(valueAtStart(o))
			}
		}
	}
	for i, p := range byIndex {
		if types[i] != ifTypeEthernet || p.name == "" {
			continue
		}
		p.pvid = pvids[p.bridgePort]
		if p.pvid == 0 {
			p.pvid = 1
		}
		b.ports = append(b.ports, *p)
	}
	slices.SortFunc(b.ports, func(x, y benchPort) int { return x.index - y.index })
	for i := range b.ports {
		b.byName[b.ports[i].name] = &b.ports[i]
	}
	if len(b.chassis) != 6 {
		b.chassis = deviceMAC(deviceSeed(d.ID), 0)
	}
	if b.caps == 0 {
		b.caps = capsFor(b.router, b.bridge)
	}
	// What CDP calls it, which only a device running CDP is called.
	for i, class := range classes {
		if b.cdp && class == 3 && modelNames[i] != "" { // its chassis
			b.platform = "cisco " + modelNames[i]
			break
		}
	}
	// What the bridge learnt, on the ports it learnt it: never the root port's,
	// which stands for a network the bench is about to replace, and never
	// twice.
	seen := map[string]bool{}
	for _, e := range fdb {
		st, known := status[strconv.Itoa(e.vlan)+"/"+macArcs(e.mac)]
		if (known && st != 3) || e.port == 0 || e.port == rootPort {
			continue
		}
		ifIndex, ok := baseIf[e.port]
		p := byIndex[ifIndex]
		if !ok || p == nil || seen[e.mac.String()] {
			continue
		}
		seen[e.mac.String()] = true
		vlan := e.vlan
		if vlan == 0 {
			vlan = cmpOr(pvids[e.port], 1)
		}
		b.learnt[p.name] = append(b.learnt[p.name], Station{MAC: e.mac, VLAN: vlan})
	}
	return b, nil
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// Ports lists d's Ethernet ports, which is what a cable can go into.
func Ports(d Device) ([]Port, error) {
	b, err := describe(d)
	if err != nil {
		return nil, err
	}
	out := make([]Port, len(b.ports))
	for i, p := range b.ports {
		out[i] = Port{Name: p.name, Descr: p.descr, SpeedMbps: p.speedMbps}
	}
	return out, nil
}

// CheckLinks reports the first thing wrong with links among devices: a cable
// naming a device or a port that does not exist, joining a device to itself,
// or plugged into a port another cable is in.
func CheckLinks(devices []Device, links []Link) error {
	c := NewLinkChecker(devices)
	for _, l := range links {
		if err := c.Add(l); err != nil {
			return err
		}
	}
	return nil
}

// A LinkChecker holds cables to the devices they join, one cable at a time,
// each device described once however many cables are checked: keeping the
// cables of a bench that still fit is one pass, not one CheckLinks per cable.
type LinkChecker struct {
	byID      map[string]Device
	described map[string]*benchDevice
	ids       map[string]bool
	used      map[string]bool
}

// NewLinkChecker is a checker of cables among devices, holding none yet.
func NewLinkChecker(devices []Device) *LinkChecker {
	c := &LinkChecker{byID: map[string]Device{}, described: map[string]*benchDevice{}, ids: map[string]bool{},
		used: map[string]bool{}}
	for _, d := range devices {
		c.byID[d.ID] = d
	}
	return c
}

// Add checks l against the devices and the cables added before it, and adds it
// when nothing is wrong with it; a cable refused leaves the checker as it was.
func (c *LinkChecker) Add(l Link) error {
	if l.ID == "" || c.ids[l.ID] {
		return errors.New("every cable has an ID of its own")
	}
	if l.A.Device == l.B.Device {
		return errors.New("a cable joins two devices, not two ports of one")
	}
	ends := []LinkEnd{l.A, l.B}
	for _, end := range ends {
		d, ok := c.byID[end.Device]
		if !ok {
			return fmt.Errorf("a cable is plugged into a device that does not exist (%s)", end.Device)
		}
		b, ok := c.described[d.ID]
		if !ok {
			var err error
			if b, err = describe(d); err != nil {
				return fmt.Errorf("%s: %w", d.Name, err)
			}
			c.described[d.ID] = b
		}
		if b.byName[end.Port] == nil {
			return fmt.Errorf("%s has no port %q", d.Name, end.Port)
		}
		if c.used[d.ID+"\x00"+end.Port] {
			return fmt.Errorf("port %s of %s already has a cable in it", end.Port, d.Name)
		}
	}
	c.ids[l.ID] = true
	for _, end := range ends {
		c.used[end.Device+"\x00"+end.Port] = true
	}
	return nil
}

// PlanBench works out the wiring of every cabled device: what is at the far end
// of each of its cables, what is learnt through each, and the addresses the
// bench gives it. A device with no cable is not in the plan and answers as its
// model does. A cable naming what does not exist is ignored here; CheckLinks is
// what refuses it.
func PlanBench(devices []Device, links []Link) map[string]*Wiring {
	p := &planner{desc: map[string]*benchDevice{}, adj: map[string]map[string]LinkEnd{},
		home: map[string]int{}, ip: map[string]netip.Addr{}, ip6: map[string]netip.Addr{}}
	byID := map[string]Device{}
	for _, d := range devices {
		byID[d.ID] = d
	}
	describeOnce := func(id string) *benchDevice {
		if b, ok := p.desc[id]; ok {
			return b
		}
		d, ok := byID[id]
		if !ok {
			p.desc[id] = nil
			return nil
		}
		b, err := describe(d)
		if err != nil {
			b = nil
		}
		p.desc[id] = b
		return b
	}
	for _, l := range links {
		a, b := describeOnce(l.A.Device), describeOnce(l.B.Device)
		if a == nil || b == nil || l.A.Device == l.B.Device || a.byName[l.A.Port] == nil || b.byName[l.B.Port] == nil {
			continue
		}
		if p.adj[l.A.Device][l.A.Port] != (LinkEnd{}) || p.adj[l.B.Device][l.B.Port] != (LinkEnd{}) {
			continue
		}
		for _, e := range [][2]LinkEnd{{l.A, l.B}, {l.B, l.A}} {
			if p.adj[e[0].Device] == nil {
				p.adj[e[0].Device] = map[string]LinkEnd{}
			}
			p.adj[e[0].Device][e[0].Port] = e[1]
		}
	}
	// The members in the order the devices are listed, so that addresses are
	// handed out the same way every time.
	for _, d := range devices {
		if len(p.adj[d.ID]) > 0 {
			p.members = append(p.members, d.ID)
		}
	}
	p.address()

	out := make(map[string]*Wiring, len(p.members))
	for _, id := range p.members {
		b := p.desc[id]
		w := &Wiring{Ports: map[string]Wire{}, Listen: b.listen}
		for _, port := range sortedKeys(p.adj[id]) {
			far := p.adj[id][port]
			w.Ports[port] = Wire{Peer: p.peer(far), Behind: dedupe(p.via(far, map[string]bool{id: true}))}
		}
		if ip := p.ip[id]; ip.IsValid() && !b.router && len(b.addrs) <= 1 {
			v := p.home[id]
			w.Mgmt = netip.PrefixFrom(ip, 24)
			w.Gateway = hostIn(benchPrefix(v), 1)
			w.GatewayMAC = p.gatewayMAC(v)
		}
		if b.router {
			var hosts []Station
			for _, port := range sortedKeys(w.Ports) {
				hosts = append(hosts, w.Ports[port].Behind...)
			}
			hosts = append(hosts, p.local(id)...)
			w.Hosts = dedupe(hosts)
		}
		out[id] = w
	}
	return out
}

type planner struct {
	desc    map[string]*benchDevice
	adj     map[string]map[string]LinkEnd
	members []string
	// home is the VLAN a member sits in: 1 for a bridge, otherwise the VLAN
	// of the bridge port it is plugged into. ip and ip6 are the addresses the
	// bench gives it, or its own when it routes the bench's subnets itself.
	home    map[string]int
	ip, ip6 map[string]netip.Addr
	// stations are the addresses given to what the members' bridges learnt
	// and to what they bridge for, by MAC.
	stations map[string]Station
}

// address gives every member its VLAN and its addresses, and every station
// learnt on the bench its own.
func (p *planner) address() {
	for _, id := range p.members {
		b := p.desc[id]
		p.home[id] = 1
		if !b.bridge {
			for _, port := range sortedKeys(p.adj[id]) {
				far := p.adj[id][port]
				if fb := p.desc[far.Device]; fb != nil && fb.bridge {
					p.home[id] = fb.byName[far.Port].pvid
					break
				}
			}
		}
	}
	nextHost, nextStation := map[int]int{}, map[int]int{}
	for _, id := range p.members {
		b, v := p.desc[id], p.home[id]
		// A device routing the bench's subnets keeps its own address in them.
		for _, a := range b.addrs {
			if benchPrefix(v).Contains(a) {
				p.ip[id] = a
				p.ip6[id] = host6In(benchPrefix6(v), int(a.As4()[3]))
			}
		}
		if p.ip[id].IsValid() || b.router || len(b.addrs) > 1 {
			continue
		}
		n := 2 + nextHost[v]
		if n > 99 {
			continue
		}
		nextHost[v]++
		p.ip[id], p.ip6[id] = hostIn(benchPrefix(v), n), host6In(benchPrefix6(v), n)
	}
	p.stations = map[string]Station{}
	give := func(s Station) {
		key := s.MAC.String()
		if _, done := p.stations[key]; done {
			return
		}
		if n := 100 + nextStation[s.VLAN]; n <= 249 {
			nextStation[s.VLAN]++
			s.IP, s.IP6 = hostIn(benchPrefix(s.VLAN), n), host6In(benchPrefix6(s.VLAN), n)
		}
		p.stations[key] = s
	}
	for _, id := range p.members {
		b := p.desc[id]
		for _, port := range sortedKeys(b.learnt) {
			if _, cabled := p.adj[id][port]; cabled {
				continue
			}
			for _, s := range b.learnt[port] {
				give(s)
			}
		}
		for _, port := range sortedKeys(b.attached) {
			if _, cabled := p.adj[id][port]; cabled {
				continue
			}
			for _, mac := range b.attached[port] {
				give(Station{MAC: mac, VLAN: p.home[id]})
			}
		}
	}
}

// local are a member's own stations: what its bridge learnt on the ports with
// no cable in them, and what it bridges for.
func (p *planner) local(id string) []Station {
	b := p.desc[id]
	var out []Station
	for _, port := range sortedKeys(b.learnt) {
		if _, cabled := p.adj[id][port]; !cabled {
			for _, s := range b.learnt[port] {
				out = append(out, p.stations[s.MAC.String()])
			}
		}
	}
	for _, port := range sortedKeys(b.attached) {
		if _, cabled := p.adj[id][port]; !cabled {
			for _, mac := range b.attached[port] {
				out = append(out, p.stations[mac.String()])
			}
		}
	}
	return out
}

// self is what a device sends from the port at end: a bridge's own address
// from its base MAC and its frames from the port's, a host everything from
// the port's.
func (p *planner) self(end LinkEnd) []Station {
	b := p.desc[end.Device]
	port := b.byName[end.Port]
	if b.bridge {
		return []Station{
			{MAC: b.chassis, IP: p.ip[end.Device], IP6: p.ip6[end.Device], VLAN: 1},
			{MAC: port.mac, VLAN: 1},
		}
	}
	return []Station{{MAC: port.mac, IP: p.ip[end.Device], IP6: p.ip6[end.Device], VLAN: p.home[end.Device]}}
}

// via is what is learnt through a cable whose far end is end: that device, its
// own stations and, when it forwards, everything cabled beyond it.
func (p *planner) via(end LinkEnd, visited map[string]bool) []Station {
	visited[end.Device] = true
	out := p.self(end)
	out = append(out, p.local(end.Device)...)
	b := p.desc[end.Device]
	if !b.bridge && len(b.attached) == 0 {
		return out
	}
	for _, port := range sortedKeys(p.adj[end.Device]) {
		far := p.adj[end.Device][port]
		if port == end.Port || visited[far.Device] {
			continue
		}
		out = append(out, p.via(far, visited)...)
	}
	return out
}

// peer is the device at end as LLDP and CDP describe it.
func (p *planner) peer(end LinkEnd) Peer {
	b := p.desc[end.Device]
	port := b.byName[end.Port]
	return Peer{Name: b.name, Descr: b.descr, Chassis: b.chassis, Port: port.name, PortDescr: port.descr,
		MAC: port.mac, Caps: b.caps, Addr: b.listen, CDP: b.cdp, Platform: b.platform}
}

// gatewayMAC is the MAC of the member routing VLAN v at its .1, when one does.
func (p *planner) gatewayMAC(v int) net.HardwareAddr {
	gw := hostIn(benchPrefix(v), 1)
	for _, id := range p.members {
		if b := p.desc[id]; b.router && slices.Contains(b.addrs, gw) {
			return b.chassis
		}
	}
	return nil
}

func dedupe(in []Station) []Station {
	out := make([]Station, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if len(s.MAC) == 0 || seen[s.MAC.String()] {
			continue
		}
		seen[s.MAC.String()] = true
		out = append(out, s)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, naturalCompare)
	return keys
}

// naturalCompare orders port names as a person does: Fa0/2 before Fa0/10.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitsPrefix(a), digitsPrefix(b)
		if da != "" && db != "" {
			x, _ := strconv.Atoi(da)
			y, _ := strconv.Atoi(db)
			if x != y {
				return x - y
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitsPrefix(s string) string {
	i := 0
	for i < len(s) && i < 9 && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// listenAddr is the address a device answers on, as an address.
func listenAddr(address string) netip.Addr {
	address = strings.Trim(strings.TrimSpace(address), "[]")
	if strings.EqualFold(address, "localhost") {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	a, err := netip.ParseAddr(address)
	if err != nil {
		return netip.Addr{}
	}
	return a.WithZone("").Unmap()
}

// valueAtStart is what an object answers at the moment its agent starts: what a
// model is described from never moves.
func valueAtStart(o Object) any {
	if o.Value == nil {
		return nil
	}
	return o.Value.read(clock{})
}

func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

func bytesOf(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return bytes.Clone(x)
	case string:
		return []byte(x)
	}
	return nil
}

func intOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case uint:
		return int(x)
	case uint32:
		return int(x)
	case uint64:
		return int(x)
	}
	return 0
}

// indexAfter reads the single arc after prefix, as an integer.
func indexAfter(oid, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(oid, prefix)
	if !ok || strings.Contains(rest, ".") {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil
}

// macFromArcs reads a MAC an index carries as six arcs.
func macFromArcs(s string) (net.HardwareAddr, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 6 {
		return nil, false
	}
	mac := make(net.HardwareAddr, 6)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return nil, false
		}
		mac[i] = byte(n)
	}
	return mac, true
}
