package simulator

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// A device's standard MIBs — IF-MIB, IP-MIB, IP-FORWARD-MIB, TCP-MIB, UDP-MIB,
// EtherLike-MIB, and ENTITY-MIB's account of its hardware, BRIDGE-MIB and
// LLDP-MIB when it is a switch — built from one description of the device so
// that they agree with each other: an address sits on an interface the device
// has, a route leaves through one, a TCP session runs to an address it can
// reach, a bridge port is an interface, a neighbour is heard where a cable is.
type stack struct {
	seed uint64
	ifs  []iface
	// addrs are the device's IPv4 addresses. A loopback interface carries
	// 127.0.0.1 as well, as a host's does.
	addrs []ifAddr
	// gateway is the next hop of the default route, or the zero address.
	gateway netip.Addr
	// router forwards (ipForwarding), and routes are what it has learnt.
	router bool
	routes []route
	// ttl is ipDefaultTTL, 64 when not given.
	ttl int
	// pps is how many packets a second the device's own IP stack sends and
	// receives — not what it forwards — which moves every protocol counter.
	pps float64
	// listen and udp are the ports the device listens on; sessions are the
	// TCP connections established to it.
	listen   []uint16
	udp      []uint16
	sessions []session
	entity   []physical
	bridge   *bridgeInfo
	lldp     *lldpInfo
	// modules are the MIB modules sysORTable lists besides the standard ones.
	modules []sysOR
	// physicalARP is an agent answering IP-MIB's ipNetToPhysicalTable beside
	// ipNetToMediaTable: the ARP cache in the table that replaced it, which
	// holds the IPv6 neighbours as well.
	physicalARP bool
	// addrs6 are the device's IPv6 addresses.
	addrs6 []ifAddr
	// wiring is where the device sits on the bench (bench.go); nil, it answers
	// as its model does.
	wiring *Wiring
}

// ifAddr is one of the device's addresses, on an interface.
type ifAddr struct {
	ifIndex int
	prefix  netip.Prefix
	// neighbours is how many other hosts of the subnet the ARP cache holds,
	// the gateway aside.
	neighbours int
}

// route is one the device knows beyond its own subnets and its default route.
type route struct {
	dest    netip.Prefix
	nextHop netip.Addr
	ifIndex int
	proto   int // ipCidrRouteProto
	metric  int
	as      int // the next hop's AS, for a route BGP learnt
}

// The ipCidrRouteProto values the models use.
const (
	protoLocal  = 2
	protoStatic = 3 // netmgmt
	protoOSPF   = 13
	protoBGP    = 14
)

// session is a TCP connection established to the device's first address, on
// its port local, from peer.
type session struct {
	local uint16
	peer  netip.AddrPort
}

// sysOR is a MIB module the agent says it implements: a row of sysORTable.
type sysOR struct{ oid, descr string }

// standardModules are the modules every agent here lists, described as
// net-snmp describes them.
var standardModules = []sysOR{
	{".1.3.6.1.6.3.11.3.1.1", "The MIB for Message Processing and Dispatching."},
	{".1.3.6.1.6.3.15.2.1.1", "The management information definitions for the SNMP User-based Security Model."},
	{".1.3.6.1.6.3.10.3.1.1", "The SNMP Management Architecture MIB."},
	{".1.3.6.1.6.3.1", "The MIB module for SNMPv2 entities"},
	{".1.3.6.1.2.1.31", "The MIB module to describe generic objects for network interface sub-layers"},
	{".1.3.6.1.2.1.49", "The MIB module for managing TCP implementations"},
	{".1.3.6.1.2.1.4", "The MIB module for managing IP and ICMP implementations"},
	{".1.3.6.1.2.1.50", "The MIB module for managing UDP implementations"},
}

// The other modules a model may list.
var (
	moduleHostResources = sysOR{".1.3.6.1.2.1.25.7.1", "The MIB module for managing host systems."}
	moduleEntity        = sysOR{".1.3.6.1.2.1.47", "The MIB module for representing multiple logical entities supported by a single SNMP agent."}
	moduleBridge        = sysOR{".1.3.6.1.2.1.17", "The Bridge MIB module for managing devices that support IEEE 802.1D."}
	moduleLLDP          = sysOR{".1.0.8802.1.1.2", "Management Information Base module for LLDP configuration, statistics, local system data and remote systems data components."}
)

// lanPrefix is a private /24 drawn from seed: the network a device sits on.
func lanPrefix(seed uint64) netip.Prefix {
	h := mix(seed + 70001)
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(16 + h%224), byte(h >> 8), 0}), 24)
}

// hostIn is the address numbered n in p.
func hostIn(p netip.Prefix, n int) netip.Addr {
	b := p.Masked().Addr().As4()
	binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])+uint32(n))
	return netip.AddrFrom4(b)
}

// addrIn is the address numbered n in p, with p's length.
func addrIn(p netip.Prefix, n int) netip.Prefix { return netip.PrefixFrom(hostIn(p, n), p.Bits()) }

// maskOf writes a prefix length as a dotted netmask.
func maskOf(bits int) string { return net.IP(net.CIDRMask(bits, 32)).String() }

// peerMAC is the MAC of another station, drawn apart from the device's own.
func peerMAC(seed, n uint64) net.HardwareAddr { return deviceMAC(seed^0x5eedc0ffee, n) }

// counter is one of the stack's protocol counters: a share of its packet rate,
// from a start drawn from the seed. counter64 with the same arguments is the
// same count in 64 bits, as tcpHCInSegs is tcpInSegs.
func (s stack) counter(n uint64, share float64) Reading {
	return Counter(s.pps*share, s.start(n), s.swing(n))
}

func (s stack) counter64(n uint64, share float64) Reading {
	return Counter64(s.pps*share, s.start(n), s.swing(n))
}

func (s stack) start(n uint64) uint64 { return uint64(1e5 + 9e6*unit(s.seed+3100+n)) }

func (s stack) swing(n uint64) Swing {
	return Swing{Depth: 0.4, Period: 7 * time.Minute, Seed: s.seed + 3000 + n}
}

// ownAddrs are the device's addresses, 127.0.0.1 on its loopback included.
func (s stack) ownAddrs() []ifAddr {
	out := slices.Clone(s.addrs)
	for _, f := range s.ifs {
		if f.ifType == ifTypeSoftwareLoopback {
			out = append(out, ifAddr{ifIndex: f.index, prefix: netip.MustParsePrefix("127.0.0.1/8")})
			break
		}
	}
	return out
}

// primary is the address the device is managed at, which sessions run to.
func (s stack) primary() netip.Addr {
	if len(s.addrs) > 0 {
		return s.addrs[0].prefix.Addr()
	}
	return netip.IPv4Unspecified()
}

// ifIndexFor is the interface an address is reached through: the one whose
// subnet holds it.
func (s stack) ifIndexFor(a netip.Addr) int {
	for _, own := range s.addrs {
		if own.prefix.Masked().Contains(a) {
			return own.ifIndex
		}
	}
	return 0
}

// addStack adds the standard MIBs s describes, for the device id is — as it is
// cabled on the bench, when it is.
func addStack(o *objects, id Identity, s stack) {
	if id.Wiring != nil {
		s = s.cabled(id, *o)
	}
	addInterfaces(o, s.seed, s.ifs)
	addIP(o, s)
	addICMP(o, s.seed)
	addTCP(o, s)
	addUDP(o, s)
	addEtherLike(o, s.seed, s.ifs)
	modules := slices.Concat(standardModules, s.modules)
	if len(s.entity) > 0 {
		addEntity(o, s.entity)
		modules = append(modules, moduleEntity)
	}
	if s.bridge != nil {
		addBridge(o, s, *s.bridge)
		modules = append(modules, moduleBridge)
	}
	if s.lldp != nil {
		addLLDP(o, s, *s.lldp)
		modules = append(modules, moduleLLDP)
	}
	addSysOR(o, modules)
}

// cabled is s as the bench has it: every port with a cable in it up and named
// after what is at its far end; a device with one address given the bench's,
// the sessions to its old subnet moved with it; and an LLDP agent where the
// model has none, since every device on the bench is heard by its neighbours
// and says whom it hears.
func (s stack) cabled(id Identity, built []Object) stack {
	w := id.Wiring
	s.wiring = w
	s.ifs = slices.Clone(s.ifs)
	for i := range s.ifs {
		f := &s.ifs[i]
		wire, ok := w.Ports[f.ifName()]
		if !ok {
			continue
		}
		f.up, f.adminDown, f.alias = true, false, wire.Peer.Name
		if f.inRate == 0 {
			n := uint64(f.index)
			f.inRate = 20_000 + 200_000*unit(s.seed+7700+n)
			f.outRate = 20_000 + 200_000*unit(s.seed+7800+n)
		}
	}
	if w.Mgmt.IsValid() && len(s.addrs) == 1 && len(s.routes) == 0 && !s.router {
		old := s.addrs[0].prefix.Masked()
		s.addrs = slices.Clone(s.addrs)
		s.addrs[0].prefix = w.Mgmt
		s.gateway = w.Gateway
		s.sessions = slices.Clone(s.sessions)
		for i, se := range s.sessions {
			if a := se.peer.Addr(); a.Is4() && old.Contains(a) {
				off := binary.BigEndian.Uint32(a.AsSlice()) - binary.BigEndian.Uint32(old.Addr().AsSlice())
				s.sessions[i].peer = netip.AddrPortFrom(hostIn(w.Mgmt, int(off)), se.peer.Port())
			}
		}
	}
	if s.lldp == nil {
		descr := ""
		for _, o := range built {
			if o.OID == oidSysDescr {
				descr = textOf(valueAtStart(o))
			}
		}
		s.lldp = &lldpInfo{name: id.Name, descr: descr, caps: capsFor(s.router, s.bridge != nil)}
	}
	return s
}

// addSysOR adds sysORTable, every module there since the agent started.
func addSysOR(o *objects, modules []sysOR) {
	const or = "1.3.6.1.2.1.1."
	o.add(or+"8.0", gosnmp.TimeTicks, Const(uint32(0))) // sysORLastChange
	for i, m := range modules {
		n := i + 1
		o.add(fmt.Sprintf(or+"9.1.2.%d", n), gosnmp.ObjectIdentifier, Const(m.oid))
		o.add(fmt.Sprintf(or+"9.1.3.%d", n), gosnmp.OctetString, Const(m.descr))
		o.add(fmt.Sprintf(or+"9.1.4.%d", n), gosnmp.TimeTicks, Const(uint32(0)))
	}
}

// addIP adds IP-MIB — the ip group, the addresses in both of its tables, the
// ARP cache — and IP-FORWARD-MIB's routes.
func addIP(o *objects, s stack) {
	const ip = "1.3.6.1.2.1.4."
	zero := Const(uint32(0))
	forwarding, forwarded := 2, 0.0
	if s.router {
		// A router forwards far more than it receives itself.
		forwarding, forwarded = 1, 12
	}
	o.add(ip+"1.0", gosnmp.Integer, Const(forwarding))
	o.add(ip+"2.0", gosnmp.Integer, Const(cmp.Or(s.ttl, 64)))
	// Counters that share a number share a start, so that one never passes the
	// other: ipInDelivers stays under ipInReceives.
	o.add(ip+"3.0", gosnmp.Counter32, s.counter(1, 1))         // ipInReceives
	o.add(ip+"4.0", gosnmp.Counter32, s.counter(2, 1e-5))      // ipInHdrErrors
	o.add(ip+"5.0", gosnmp.Counter32, s.counter(3, 1e-4))      // ipInAddrErrors
	o.add(ip+"6.0", gosnmp.Counter32, s.counter(4, forwarded)) // ipForwDatagrams
	o.add(ip+"7.0", gosnmp.Counter32, zero)                    // ipInUnknownProtos
	o.add(ip+"8.0", gosnmp.Counter32, s.counter(6, 2e-4))      // ipInDiscards
	o.add(ip+"9.0", gosnmp.Counter32, s.counter(1, 0.99))      // ipInDelivers
	o.add(ip+"10.0", gosnmp.Counter32, s.counter(8, 0.97))     // ipOutRequests
	o.add(ip+"11.0", gosnmp.Counter32, s.counter(9, 1e-4))     // ipOutDiscards
	o.add(ip+"12.0", gosnmp.Counter32, s.counter(10, 1e-5))    // ipOutNoRoutes
	o.add(ip+"13.0", gosnmp.Integer, Const(30))                // ipReasmTimeout, seconds
	o.add(ip+"14.0", gosnmp.Counter32, s.counter(12, 1e-4))    // ipReasmReqds
	o.add(ip+"15.0", gosnmp.Counter32, s.counter(12, 5e-5))    // ipReasmOKs
	o.add(ip+"16.0", gosnmp.Counter32, zero)                   // ipReasmFails
	o.add(ip+"17.0", gosnmp.Counter32, s.counter(15, 1e-4))    // ipFragOKs
	o.add(ip+"18.0", gosnmp.Counter32, zero)                   // ipFragFails
	o.add(ip+"19.0", gosnmp.Counter32, s.counter(15, 2e-4))    // ipFragCreates
	o.add(ip+"23.0", gosnmp.Counter32, zero)                   // ipRoutingDiscards

	prefixes := map[string]bool{}
	for _, a := range s.ownAddrs() {
		addr := a.prefix.Addr().String()
		// ipAddrTable, indexed by the address.
		col := func(c int) string { return fmt.Sprintf(ip+"20.1.%d.%s", c, addr) }
		o.add(col(1), gosnmp.IPAddress, Const(addr))
		o.add(col(2), gosnmp.Integer, Const(a.ifIndex))
		o.add(col(3), gosnmp.IPAddress, Const(maskOf(a.prefix.Bits())))
		o.add(col(4), gosnmp.Integer, Const(1))
		o.add(col(5), gosnmp.Integer, Const(65535))

		// ipAddressPrefixTable, which ipAddressTable points at: indexed by the
		// interface, the address type, the prefix as an InetAddress — its
		// length first — and the prefix length.
		subnet := a.prefix.Masked()
		pfx := fmt.Sprintf("%d.1.4.%s.%d", a.ifIndex, subnet.Addr(), subnet.Bits())
		if !prefixes[pfx] {
			prefixes[pfx] = true
			pcol := func(c int) string { return fmt.Sprintf(ip+"32.1.%d.%s", c, pfx) }
			o.add(pcol(5), gosnmp.Integer, Const(2)) // origin: manual
			o.add(pcol(6), gosnmp.Integer, Const(1)) // on link: true
			o.add(pcol(7), gosnmp.Integer, Const(2)) // autonomous: false
			o.add(pcol(8), gosnmp.Gauge32, Const(uint32(math.MaxUint32)))
			o.add(pcol(9), gosnmp.Gauge32, Const(uint32(math.MaxUint32)))
		}
		// ipAddressTable, indexed by the type and the address, its length first.
		acol := func(c int) string { return fmt.Sprintf(ip+"34.1.%d.1.4.%s", c, addr) }
		o.add(acol(3), gosnmp.Integer, Const(a.ifIndex))
		o.add(acol(4), gosnmp.Integer, Const(1)) // unicast
		o.add(acol(5), gosnmp.ObjectIdentifier, Const(".1.3.6.1.2.1.4.32.1.5."+pfx))
		o.add(acol(6), gosnmp.Integer, Const(2)) // origin: manual
		o.add(acol(7), gosnmp.Integer, Const(1)) // status: preferred
		o.add(acol(8), gosnmp.TimeTicks, Const(uint32(0)))
		o.add(acol(9), gosnmp.TimeTicks, Const(uint32(0)))
		o.add(acol(10), gosnmp.Integer, Const(1)) // active
		o.add(acol(11), gosnmp.Integer, Const(2)) // volatile
	}

	// ipNetToMediaTable: the ARP cache, indexed by interface and address.
	arp := s.neighbours()
	for _, n := range arp {
		inst := fmt.Sprintf("%d.%s", n.ifIndex, n.addr)
		col := func(c int) string { return fmt.Sprintf(ip+"22.1.%d.%s", c, inst) }
		o.add(col(1), gosnmp.Integer, Const(n.ifIndex))
		o.add(col(2), gosnmp.OctetString, Const([]byte(n.mac)))
		o.add(col(3), gosnmp.IPAddress, Const(n.addr.String()))
		o.add(col(4), gosnmp.Integer, Const(3)) // dynamic
	}

	// The IPv6 addresses, in the two tables that carry them.
	for _, a := range s.addrs6 {
		subnet := a.prefix.Masked()
		pfx := fmt.Sprintf("%d.2.16.%s.%d", a.ifIndex, addrArcs(subnet.Addr()), subnet.Bits())
		pcol := func(c int) string { return fmt.Sprintf(ip+"32.1.%d.%s", c, pfx) }
		o.add(pcol(5), gosnmp.Integer, Const(2))
		o.add(pcol(6), gosnmp.Integer, Const(1))
		o.add(pcol(7), gosnmp.Integer, Const(1)) // autonomous: SLAAC may use it
		o.add(pcol(8), gosnmp.Gauge32, Const(uint32(math.MaxUint32)))
		o.add(pcol(9), gosnmp.Gauge32, Const(uint32(math.MaxUint32)))
		acol := func(c int) string { return fmt.Sprintf(ip+"34.1.%d.2.16.%s", c, addrArcs(a.prefix.Addr())) }
		o.add(acol(3), gosnmp.Integer, Const(a.ifIndex))
		o.add(acol(4), gosnmp.Integer, Const(1))
		o.add(acol(5), gosnmp.ObjectIdentifier, Const(".1.3.6.1.2.1.4.32.1.5."+pfx))
		o.add(acol(6), gosnmp.Integer, Const(2))
		o.add(acol(7), gosnmp.Integer, Const(1))
		o.add(acol(8), gosnmp.TimeTicks, Const(uint32(0)))
		o.add(acol(9), gosnmp.TimeTicks, Const(uint32(0)))
		o.add(acol(10), gosnmp.Integer, Const(1))
		o.add(acol(11), gosnmp.Integer, Const(2))
	}

	// ipNetToPhysicalTable: the same cache, and the IPv6 neighbours, indexed by
	// interface, address type and the address with its length first.
	if s.physicalARP {
		for _, n := range slices.Concat(arp, s.neighbours6()) {
			kind, size := 1, 4
			if n.addr.Is6() {
				kind, size = 2, 16
			}
			inst := fmt.Sprintf("%d.%d.%d.%s", n.ifIndex, kind, size, addrArcs(n.addr))
			col := func(c int) string { return fmt.Sprintf(ip+"35.1.%d.%s", c, inst) }
			o.add(col(4), gosnmp.OctetString, Const([]byte(n.mac)))
			o.add(col(5), gosnmp.TimeTicks, Const(uint32(0)))
			o.add(col(6), gosnmp.Integer, Const(3)) // dynamic
			o.add(col(7), gosnmp.Integer, Const(1)) // reachable
			o.add(col(8), gosnmp.Integer, Const(1)) // active
		}
	}

	// IP-FORWARD-MIB's ipCidrRouteTable, indexed by destination, mask, TOS and
	// next hop.
	routes := s.allRoutes()
	o.add(ip+"24.3.0", gosnmp.Gauge32, Const(uint32(len(routes))))
	for _, r := range routes {
		hop := r.nextHop
		if !hop.IsValid() {
			hop = netip.IPv4Unspecified()
		}
		inst := fmt.Sprintf("%s.%s.0.%s", r.dest.Addr(), maskOf(r.dest.Bits()), hop)
		col := func(c int) string { return fmt.Sprintf(ip+"24.4.1.%d.%s", c, inst) }
		kind := 4 // remote
		if hop.IsUnspecified() {
			kind = 3 // local: the subnet itself
		}
		o.add(col(1), gosnmp.IPAddress, Const(r.dest.Addr().String()))
		o.add(col(2), gosnmp.IPAddress, Const(maskOf(r.dest.Bits())))
		o.add(col(3), gosnmp.Integer, Const(0))
		o.add(col(4), gosnmp.IPAddress, Const(hop.String()))
		o.add(col(5), gosnmp.Integer, Const(r.ifIndex))
		o.add(col(6), gosnmp.Integer, Const(kind))
		o.add(col(7), gosnmp.Integer, Const(r.proto))
		o.add(col(8), gosnmp.Integer, engineSeconds{}) // learnt as the device came up
		o.add(col(9), gosnmp.ObjectIdentifier, Const(".0.0"))
		o.add(col(10), gosnmp.Integer, Const(r.as))
		o.add(col(11), gosnmp.Integer, Const(r.metric))
		for c := 12; c <= 15; c++ {
			o.add(col(c), gosnmp.Integer, Const(-1)) // metrics this protocol has not
		}
		o.add(col(16), gosnmp.Integer, Const(1)) // active
	}
}

// arpEntry is a neighbour in the ARP cache.
type arpEntry struct {
	ifIndex int
	addr    netip.Addr
	mac     net.HardwareAddr
}

// neighbours are the ARP cache: the gateway where it is on a subnet of the
// device, and the other hosts each subnet says it has. On the bench, a device
// that routes one of its subnets holds every station there, and any other
// device its gateway alone.
func (s stack) neighbours() []arpEntry {
	var out []arpEntry
	for i, a := range s.addrs {
		subnet := a.prefix.Masked()
		if w := s.wiring; w != nil && (s.router && isBench(subnet) || w.Mgmt.IsValid() && a.prefix == w.Mgmt) {
			out = append(out, s.benchNeighbours(a)...)
			continue
		}
		if s.gateway.IsValid() && subnet.Contains(s.gateway) {
			out = append(out, arpEntry{a.ifIndex, s.gateway, peerMAC(s.seed, 0)})
		}
		offset := int(mix(s.seed+uint64(i)) % 7)
		for k, n := 0, 0; k < a.neighbours; n++ {
			addr := hostIn(subnet, 20+offset+3*n)
			if !subnet.Contains(addr) || n > 1<<10 {
				break
			}
			if addr == a.prefix.Addr() || addr == s.gateway {
				continue
			}
			out = append(out, arpEntry{a.ifIndex, addr, peerMAC(s.seed, uint64(100*(i+1)+k))})
			k++
		}
	}
	return out
}

// benchNeighbours are the ARP cache of a, a subnet of the bench.
func (s stack) benchNeighbours(a ifAddr) []arpEntry {
	w := s.wiring
	if !s.router {
		if !w.Gateway.IsValid() {
			return nil
		}
		mac := w.GatewayMAC
		if mac == nil {
			mac = peerMAC(s.seed, 0)
		}
		return []arpEntry{{a.ifIndex, w.Gateway, mac}}
	}
	var out []arpEntry
	subnet := a.prefix.Masked()
	for _, h := range w.Hosts {
		if h.IP.IsValid() && subnet.Contains(h.IP) && h.IP != a.prefix.Addr() {
			out = append(out, arpEntry{a.ifIndex, h.IP, h.MAC})
		}
	}
	return out
}

// neighbours6 are the IPv6 neighbours of a device that routes the bench's
// subnets: every station with an address in one of them.
func (s stack) neighbours6() []arpEntry {
	w := s.wiring
	if w == nil || !s.router {
		return nil
	}
	var out []arpEntry
	for _, a := range s.addrs6 {
		subnet := a.prefix.Masked()
		for _, h := range w.Hosts {
			if h.IP6.IsValid() && subnet.Contains(h.IP6) && h.IP6 != a.prefix.Addr() {
				out = append(out, arpEntry{a.ifIndex, h.IP6, h.MAC})
			}
		}
	}
	return out
}

// addrArcs is an address as the sub-identifiers of an index, one an octet.
func addrArcs(a netip.Addr) string {
	b := a.AsSlice()
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = strconv.Itoa(int(x))
	}
	return strings.Join(parts, ".")
}

// inetIndex is an address as an index of type InetAddressType then InetAddress
// with its length first: "1.4.10.0.0.1", "2.16.…".
func inetIndex(a netip.Addr) string {
	if a.Is4() {
		return "1.4." + addrArcs(a)
	}
	return "2.16." + addrArcs(a)
}

// allRoutes are the routes to the device's own subnets, the default one, and
// those it learnt.
func (s stack) allRoutes() []route {
	var out []route
	seen := map[netip.Prefix]bool{}
	for _, a := range s.addrs {
		if subnet := a.prefix.Masked(); !seen[subnet] {
			seen[subnet] = true
			out = append(out, route{dest: subnet, ifIndex: a.ifIndex, proto: protoLocal})
		}
	}
	if s.gateway.IsValid() {
		out = append(out, route{dest: netip.PrefixFrom(netip.IPv4Unspecified(), 0), nextHop: s.gateway,
			ifIndex: s.ifIndexFor(s.gateway), proto: protoStatic, metric: 1})
	}
	return append(out, s.routes...)
}

// addICMP adds the icmp group: what pings and unreachable ports make of a
// device, a few a minute.
func addICMP(o *objects, seed uint64) {
	const icmp = "1.3.6.1.2.1.5."
	rates := [26]float64{
		// in: messages, errors, destination unreachable, time exceeded, parameter
		// problems, source quenches, redirects, echoes, echo replies, timestamps,
		// timestamp replies, address masks, address mask replies
		0.09, 0.001, 0.02, 0.002, 0, 0, 0, 0.06, 0.01, 0, 0, 0, 0,
		// out, in the same order
		0.1, 0, 0.03, 0, 0, 0, 0, 0.01, 0.06, 0, 0, 0, 0,
	}
	for i, r := range rates {
		n := uint64(i)
		var v Reading = Const(uint32(0))
		if r > 0 {
			v = Counter(r, uint64(100+5000*unit(seed+3300+n)), Swing{Depth: 0.8, Period: 20 * time.Minute, Seed: seed + 3300 + n})
		}
		o.add(fmt.Sprintf(icmp+"%d.0", i+1), gosnmp.Counter32, v)
	}
}

// addTCP adds TCP-MIB: its scalars, the 64-bit segment counts, and
// tcpConnTable — the sockets listening and the sessions established, indexed
// by four things, as the conceptual-table decoder meets them on a real agent.
func addTCP(o *objects, s stack) {
	const tcp = "1.3.6.1.2.1.6."
	o.add(tcp+"1.0", gosnmp.Integer, Const(1)) // tcpRtoAlgorithm: other, as Linux says
	o.add(tcp+"2.0", gosnmp.Integer, Const(200))
	o.add(tcp+"3.0", gosnmp.Integer, Const(120000))
	o.add(tcp+"4.0", gosnmp.Integer, Const(-1))              // no fixed limit
	o.add(tcp+"5.0", gosnmp.Counter32, s.counter(40, 0.002)) // tcpActiveOpens
	o.add(tcp+"6.0", gosnmp.Counter32, s.counter(41, 0.004)) // tcpPassiveOpens
	o.add(tcp+"7.0", gosnmp.Counter32, s.counter(42, 4e-4))  // tcpAttemptFails
	o.add(tcp+"8.0", gosnmp.Counter32, s.counter(43, 2e-4))  // tcpEstabResets
	o.add(tcp+"9.0", gosnmp.Gauge32, Const(uint32(len(s.sessions))))
	o.add(tcp+"10.0", gosnmp.Counter32, s.counter(45, 0.6))   // tcpInSegs
	o.add(tcp+"11.0", gosnmp.Counter32, s.counter(46, 0.62))  // tcpOutSegs
	o.add(tcp+"12.0", gosnmp.Counter32, s.counter(47, 0.002)) // tcpRetransSegs
	o.add(tcp+"14.0", gosnmp.Counter32, s.counter(48, 1e-5))  // tcpInErrs
	o.add(tcp+"15.0", gosnmp.Counter32, s.counter(49, 0.001)) // tcpOutRsts
	o.add(tcp+"17.0", gosnmp.Counter64, s.counter64(45, 0.6))
	o.add(tcp+"18.0", gosnmp.Counter64, s.counter64(46, 0.62))

	conn := func(state int, local netip.Addr, localPort uint16, remote netip.Addr, remotePort uint16) {
		inst := fmt.Sprintf("%s.%d.%s.%d", local, localPort, remote, remotePort)
		col := func(c int) string { return fmt.Sprintf(tcp+"13.1.%d.%s", c, inst) }
		o.add(col(1), gosnmp.Integer, Const(state))
		o.add(col(2), gosnmp.IPAddress, Const(local.String()))
		o.add(col(3), gosnmp.Integer, Const(int(localPort)))
		o.add(col(4), gosnmp.IPAddress, Const(remote.String()))
		o.add(col(5), gosnmp.Integer, Const(int(remotePort)))
	}
	any := netip.IPv4Unspecified()
	for _, port := range s.listen {
		conn(2, any, port, any, 0) // listen
	}
	for _, ses := range s.sessions {
		conn(5, s.primary(), ses.local, ses.peer.Addr(), ses.peer.Port()) // established
	}
}

// addUDP adds UDP-MIB: its counters and udpTable, the ports listening — SNMP's
// among them, since the device answers on it.
func addUDP(o *objects, s stack) {
	const udp = "1.3.6.1.2.1.7."
	o.add(udp+"1.0", gosnmp.Counter32, s.counter(60, 0.3)) // udpInDatagrams
	o.add(udp+"2.0", gosnmp.Counter32, s.counter(61, 0.01))
	o.add(udp+"3.0", gosnmp.Counter32, Const(uint32(0)))
	o.add(udp+"4.0", gosnmp.Counter32, s.counter(63, 0.3))
	o.add(udp+"8.0", gosnmp.Counter64, s.counter64(60, 0.3))
	o.add(udp+"9.0", gosnmp.Counter64, s.counter64(63, 0.3))
	ports := s.udp
	if !slices.Contains(ports, 161) {
		ports = append(slices.Clone(ports), 161)
	}
	for _, port := range ports {
		inst := fmt.Sprintf("0.0.0.0.%d", port)
		o.add(udp+"5.1.1."+inst, gosnmp.IPAddress, Const("0.0.0.0"))
		o.add(udp+"5.1.2."+inst, gosnmp.Integer, Const(int(port)))
	}
}

// addEtherLike adds EtherLike-MIB's dot3StatsTable for each Ethernet
// interface: full duplex and no collisions where a link is up, and FCS errors
// that follow the interface's input errors.
func addEtherLike(o *objects, seed uint64, ifs []iface) {
	const dot3 = "1.3.6.1.2.1.10.7.2.1."
	zero := Const(uint32(0))
	for _, f := range ifs {
		if f.ifType != ifTypeEthernet {
			continue
		}
		col := func(c int) string { return fmt.Sprintf(dot3+"%d.%d", c, f.index) }
		up := f.up && !f.adminDown
		o.add(col(1), gosnmp.Integer, Const(f.index))
		o.add(col(2), gosnmp.Counter32, zero) // alignment errors
		fcs := zero
		if up && f.errorRate > 0 {
			fcs = Counter(f.errorRate*0.6, 0, Swing{Depth: 0.9, Period: 11 * time.Minute, Seed: seed + uint64(f.index)*16 + 3})
		}
		o.add(col(3), gosnmp.Counter32, fcs)
		for _, c := range []int{4, 5, 6, 7, 8, 9, 10, 11, 13, 16, 18} {
			o.add(col(c), gosnmp.Counter32, zero)
		}
		duplex := 1 // unknown: no link
		if up {
			duplex = 3 // full
		}
		o.add(col(19), gosnmp.Integer, Const(duplex))
	}
}

// physical is one entry of ENTITY-MIB's entPhysicalTable.
type physical struct {
	index, container, relPos, class int
	descr, name, model, serial      string
	hwRev, fwRev, swRev, mfg        string
	// vendorType is an AutonomousType; zeroDotZero, which the MIB allows, when
	// it is not known.
	vendorType string
	fru        bool
	// ifIndex is a port's interface (entAliasMappingTable).
	ifIndex int
}

// The entPhysicalClass values the models use.
const (
	classOther       = 1
	classChassis     = 3
	classContainer   = 5
	classPowerSupply = 6
	classFan         = 7
	classSensor      = 8
	classModule      = 9
	classPort        = 10
	classCPU         = 12
)

// addEntity adds ENTITY-MIB: the physical table, what contains what, and which
// interface each port is.
func addEntity(o *objects, ents []physical) {
	const e = "1.3.6.1.2.1.47.1."
	for _, p := range ents {
		col := func(c int) string { return fmt.Sprintf(e+"1.1.1.%d.%d", c, p.index) }
		fru := 2
		if p.fru {
			fru = 1
		}
		o.add(col(2), gosnmp.OctetString, Const(p.descr))
		o.add(col(3), gosnmp.ObjectIdentifier, Const(cmp.Or(p.vendorType, ".0.0")))
		o.add(col(4), gosnmp.Integer, Const(p.container))
		o.add(col(5), gosnmp.Integer, Const(p.class))
		o.add(col(6), gosnmp.Integer, Const(p.relPos))
		o.add(col(7), gosnmp.OctetString, Const(p.name))
		o.add(col(8), gosnmp.OctetString, Const(p.hwRev))
		o.add(col(9), gosnmp.OctetString, Const(p.fwRev))
		o.add(col(10), gosnmp.OctetString, Const(p.swRev))
		o.add(col(11), gosnmp.OctetString, Const(p.serial))
		o.add(col(12), gosnmp.OctetString, Const(p.mfg))
		o.add(col(13), gosnmp.OctetString, Const(p.model))
		o.add(col(14), gosnmp.OctetString, Const(""))
		o.add(col(15), gosnmp.OctetString, Const(""))
		o.add(col(16), gosnmp.Integer, Const(fru))
		if p.ifIndex > 0 {
			o.add(fmt.Sprintf(e+"3.2.1.2.%d.0", p.index), gosnmp.ObjectIdentifier,
				Const(fmt.Sprintf(".1.3.6.1.2.1.2.2.1.1.%d", p.ifIndex)))
		}
		if p.container > 0 {
			o.add(fmt.Sprintf(e+"3.3.1.1.%d.%d", p.container, p.index), gosnmp.Integer, Const(p.index))
		}
	}
	o.add(e+"4.1.0", gosnmp.TimeTicks, Const(uint32(0))) // entLastChangeTime
}
