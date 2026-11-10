package simulator

import (
	"cmp"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// A switch's BRIDGE-MIB (RFC 4188) and Q-BRIDGE-MIB (RFC 4363) — the ports it
// bridges, the stations learnt on each, its spanning tree, its VLANs — and the
// LLDP-MIB (IEEE 802.1AB) any device may answer: itself, its ports, and the
// neighbours it hears on them.

// bridgeInfo is what a switch bridges.
type bridgeInfo struct {
	// ports are the interfaces bridged, by ifIndex; a port's number is its
	// place in the list, from 1.
	ports []int
	vlans []vlanInfo
	// stations is how many MAC addresses an access port that is up has learnt,
	// and uplinkStations how many the uplink has: the rest of the network sits
	// behind it.
	stations, uplinkStations int
	// uplink is the bridge port towards the root of the spanning tree, whose
	// MAC is root. No uplink makes this switch the root.
	uplink int
	root   net.HardwareAddr
	// fixed are MACs learnt on a port besides its stations, by bridge port:
	// the devices the model's LLDP and CDP invent, which a switch hears from
	// as well as about.
	fixed map[int][]net.HardwareAddr
	// qFDB answers Q-BRIDGE-MIB's dot1qTpFdbTable, which says the VLAN each
	// station is in. perVLAN answers dot1dTpFdbTable one VLAN at a time, as
	// Cisco IOS does (Object.VLAN).
	qFDB, perVLAN bool
}

// vlanInfo is one VLAN and the bridge ports it is on.
type vlanInfo struct {
	id               int
	name             string
	untagged, tagged []int
}

// pathCost is a port's 802.1D path cost at a speed.
func pathCost(speed uint64) int {
	switch {
	case speed >= 10_000_000_000:
		return 2
	case speed >= 1_000_000_000:
		return 4
	case speed >= 100_000_000:
		return 19
	}
	return 100
}

// macArcs is a MAC address as the six sub-identifiers of an index.
func macArcs(mac net.HardwareAddr) string {
	parts := make([]string, len(mac))
	for i, b := range mac {
		parts[i] = strconv.Itoa(int(b))
	}
	return strings.Join(parts, ".")
}

func addBridge(o *objects, s stack, b bridgeInfo) {
	const br = "1.3.6.1.2.1.17."
	zero := Const(uint32(0))
	own := deviceMAC(s.seed, 0)
	byIndex := make(map[int]iface, len(s.ifs))
	for _, f := range s.ifs {
		byIndex[f.index] = f
	}
	bridgeID := func(mac net.HardwareAddr, priority int) []byte {
		return append([]byte{byte(priority >> 8), byte(priority)}, mac...)
	}
	self := bridgeID(own, 32768)
	root, rootCost := self, 0
	if b.uplink > 0 {
		root = bridgeID(b.root, 24576)
		rootCost = pathCost(byIndex[b.ports[b.uplink-1]].speed)
	}

	o.add(br+"1.1.0", gosnmp.OctetString, Const([]byte(own)))
	o.add(br+"1.2.0", gosnmp.Integer, Const(len(b.ports)))
	o.add(br+"1.3.0", gosnmp.Integer, Const(2)) // transparent-only
	// dot1dStp: IEEE 802.1D, with its default timers in hundredths.
	o.add(br+"2.1.0", gosnmp.Integer, Const(3))
	o.add(br+"2.2.0", gosnmp.Integer, Const(32768))
	o.add(br+"2.3.0", gosnmp.TimeTicks, Uptime()) // no change since it came up
	o.add(br+"2.4.0", gosnmp.Counter32, Const(uint32(1)))
	o.add(br+"2.5.0", gosnmp.OctetString, Const(root))
	o.add(br+"2.6.0", gosnmp.Integer, Const(rootCost))
	o.add(br+"2.7.0", gosnmp.Integer, Const(b.uplink))
	for i, v := range []int{2000, 200, 100, 1500, 2000, 200, 1500} {
		o.add(fmt.Sprintf(br+"2.%d.0", 8+i), gosnmp.Integer, Const(v))
	}
	o.add(br+"4.1.0", gosnmp.Counter32, zero)     // dot1dTpLearnedEntryDiscards
	o.add(br+"4.2.0", gosnmp.Integer, Const(300)) // dot1dTpAgingTime, seconds

	// Each port's own VLAN, and every VLAN the switch has.
	pvid := map[int]int{}
	var vlanIDs []int
	for _, v := range b.vlans {
		vlanIDs = append(vlanIDs, v.id)
		for _, p := range v.untagged {
			pvid[p] = v.id
		}
	}
	// What the switch learnt, on which port and in which VLAN; its own MAC is
	// in every VLAN.
	type learnt struct {
		mac                net.HardwareAddr
		port, status, vlan int
	}
	entries := []learnt{{own, 0, 4, 0}}
	var wires map[string]Wire
	if s.wiring != nil {
		wires = s.wiring.Ports
	}

	var station uint64
	for i, ifIndex := range b.ports {
		port := i + 1
		f := byIndex[ifIndex]
		up := f.up && !f.adminDown
		base := func(c int) string { return fmt.Sprintf(br+"1.4.1.%d.%d", c, port) }
		o.add(base(1), gosnmp.Integer, Const(port))
		o.add(base(2), gosnmp.Integer, Const(ifIndex))
		o.add(base(3), gosnmp.ObjectIdentifier, Const(".0.0"))
		o.add(base(4), gosnmp.Counter32, zero)
		o.add(base(5), gosnmp.Counter32, zero)

		// The port's place in the spanning tree: the uplink is the root port and
		// every other port that is up forwards as the designated one.
		state, enable := 5, 1 // forwarding, enabled
		switch {
		case f.adminDown:
			state, enable = 1, 2 // disabled
		case !up:
			state = 1 // disabled: no link
		}
		designatedBridge, designatedCost := self, rootCost
		if port == b.uplink {
			designatedBridge, designatedCost = root, 0
		}
		transitions := uint32(0)
		if up {
			transitions = 1
		}
		stp := func(c int) string { return fmt.Sprintf(br+"2.15.1.%d.%d", c, port) }
		o.add(stp(1), gosnmp.Integer, Const(port))
		o.add(stp(2), gosnmp.Integer, Const(128))
		o.add(stp(3), gosnmp.Integer, Const(state))
		o.add(stp(4), gosnmp.Integer, Const(enable))
		o.add(stp(5), gosnmp.Integer, Const(pathCost(f.speed)))
		o.add(stp(6), gosnmp.OctetString, Const(root))
		o.add(stp(7), gosnmp.Integer, Const(designatedCost))
		o.add(stp(8), gosnmp.OctetString, Const(designatedBridge))
		o.add(stp(9), gosnmp.OctetString, Const([]byte{0x80, byte(port)}))
		o.add(stp(10), gosnmp.Counter32, Const(transitions))

		// dot1dTpPortTable: the frames the port has passed, which follow the
		// interface's packets.
		in, out := zero, zero
		if up {
			n := uint64(port) * 8
			in = Counter(f.inRate/700, uint64(1e5+1e6*unit(s.seed+7400+n)), Swing{Depth: 0.6, Period: 5 * 60e9, Seed: s.seed + 7400 + n})
			out = Counter(f.outRate/700, uint64(1e5+1e6*unit(s.seed+7401+n)), Swing{Depth: 0.5, Period: 7 * 60e9, Seed: s.seed + 7401 + n})
		}
		tp := func(c int) string { return fmt.Sprintf(br+"4.4.1.%d.%d", c, port) }
		o.add(tp(1), gosnmp.Integer, Const(port))
		o.add(tp(2), gosnmp.Integer, Const(1500))
		o.add(tp(3), gosnmp.Counter32, in)
		o.add(tp(4), gosnmp.Counter32, out)
		o.add(tp(5), gosnmp.Counter32, zero)

		// The stations learnt on the port: on the bench, what the cable in it
		// leads to; otherwise invented, the gateway and the hosts in the
		// switch's ARP cache behind the uplink with the rest of the network.
		if !up {
			continue
		}
		if wire, cabled := wires[f.ifName()]; cabled {
			for _, st := range wire.Behind {
				entries = append(entries, learnt{st.MAC, port, 3, cmp.Or(st.VLAN, 1)})
			}
			continue
		}
		for _, mac := range b.fixed[port] {
			entries = append(entries, learnt{mac, port, 3, cmp.Or(pvid[port], 1)})
		}
		n := b.stations
		if port == b.uplink {
			n = b.uplinkStations
			entries = append(entries, learnt{peerMAC(s.seed, 0), port, 3, 1})
			for _, nb := range s.neighbours() {
				entries = append(entries, learnt{nb.mac, port, 3, 1})
			}
		}
		for k := 0; k < n; k++ {
			station++
			vlan := cmp.Or(pvid[port], 1)
			if port == b.uplink && len(vlanIDs) > 0 {
				vlan = vlanIDs[int(station)%len(vlanIDs)]
			}
			entries = append(entries, learnt{peerMAC(s.seed, 1000+station), port, 3, vlan})
		}
	}

	// dot1dTpFdbTable is indexed by the MAC alone: one entry a MAC, or on a
	// switch answering it per VLAN, one a MAC in each VLAN's view.
	seen := map[string]bool{}
	for _, e := range entries {
		key, view := macArcs(e.mac), 0
		if b.perVLAN {
			view = e.vlan
			key = strconv.Itoa(view) + "/" + key
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		inst := macArcs(e.mac)
		o.addInVLAN(view, br+"4.3.1.1."+inst, gosnmp.OctetString, Const([]byte(e.mac)))
		o.addInVLAN(view, br+"4.3.1.2."+inst, gosnmp.Integer, Const(e.port))
		o.addInVLAN(view, br+"4.3.1.3."+inst, gosnmp.Integer, Const(e.status))
	}

	// Q-BRIDGE-MIB: the VLANs, the ports each is on as a PortList — one bit a
	// port, the first port the high bit of the first octet — and each port's
	// own VLAN.
	const q = br + "7.1."
	o.add(q+"1.1.0", gosnmp.Integer, Const(1)) // version1
	o.add(q+"1.2.0", gosnmp.Integer, Const(4094))
	o.add(q+"1.3.0", gosnmp.Gauge32, Const(uint32(255)))
	o.add(q+"1.4.0", gosnmp.Gauge32, Const(uint32(len(b.vlans))))
	o.add(q+"1.5.0", gosnmp.Integer, Const(2)) // GVRP disabled
	portList := func(ports ...[]int) []byte {
		l := make([]byte, (len(b.ports)+7)/8)
		for _, set := range ports {
			for _, p := range set {
				l[(p-1)/8] |= 0x80 >> ((p - 1) % 8)
			}
		}
		return l
	}
	for _, v := range b.vlans {
		col := func(c int) string { return fmt.Sprintf(q+"4.3.1.%d.%d", c, v.id) }
		o.add(col(1), gosnmp.OctetString, Const(v.name))
		o.add(col(2), gosnmp.OctetString, Const(portList(v.untagged, v.tagged)))
		o.add(col(3), gosnmp.OctetString, Const(portList()))
		o.add(col(4), gosnmp.OctetString, Const(portList(v.untagged)))
		o.add(col(5), gosnmp.Integer, Const(1)) // active
	}
	for i := range b.ports {
		port := i + 1
		o.add(fmt.Sprintf(q+"4.5.1.1.%d", port), gosnmp.Gauge32, Const(uint32(cmp.Or(pvid[port], 1))))
	}

	// dot1qTpFdbTable: the same stations by VLAN — its filtering database is
	// numbered as the VLAN is — then MAC, and how many each VLAN learnt.
	if b.qFDB {
		qseen := map[string]bool{}
		count := map[int]uint32{}
		for _, e := range entries {
			vid := cmp.Or(e.vlan, 1)
			inst := strconv.Itoa(vid) + "." + macArcs(e.mac)
			if qseen[inst] {
				continue
			}
			qseen[inst] = true
			o.add(q+"2.2.1.2."+inst, gosnmp.Integer, Const(e.port))
			o.add(q+"2.2.1.3."+inst, gosnmp.Integer, Const(e.status))
			if e.status == 3 {
				count[vid]++
			}
		}
		for _, v := range b.vlans {
			o.add(fmt.Sprintf(q+"2.1.1.2.%d", v.id), gosnmp.Counter32, Const(count[v.id]))
		}
	}
}

// lldpInfo is what LLDP says of the device, and what it hears.
type lldpInfo struct {
	name, descr string
	// caps are the system's capabilities, all of them in use.
	caps       byte
	neighbours []neighbour
}

// neighbour is a device heard on a port, and the management address it gives.
type neighbour struct {
	ifIndex                      int
	name, descr, port, portDescr string
	chassis                      net.HardwareAddr
	caps                         byte
	addr                         netip.Addr
}

// The LLDP system capabilities, as the bits of one octet: bit 0, "other", is
// the high bit.
const (
	capBridge    = 0x20
	capWLAN      = 0x10
	capRouter    = 0x08
	capTelephone = 0x04
	capStation   = 0x01
)

func addLLDP(o *objects, s stack, l lldpInfo) {
	const lldp = "1.0.8802.1.1.2.1."
	// lldpConfiguration: a message every 30 s, held four times as long.
	for i, v := range []int{30, 4, 2, 2, 5} {
		o.add(fmt.Sprintf(lldp+"1.%d.0", i+1), gosnmp.Integer, Const(v))
	}
	o.add(lldp+"3.1.0", gosnmp.Integer, Const(4)) // the chassis is named by a MAC
	o.add(lldp+"3.2.0", gosnmp.OctetString, Const([]byte(deviceMAC(s.seed, 0))))
	o.add(lldp+"3.3.0", gosnmp.OctetString, Const(l.name))
	o.add(lldp+"3.4.0", gosnmp.OctetString, Const(l.descr))
	o.add(lldp+"3.5.0", gosnmp.OctetString, Const([]byte{l.caps}))
	o.add(lldp+"3.6.0", gosnmp.OctetString, Const([]byte{l.caps}))
	// lldpLocPortTable, numbered by ifIndex, a port named by its interface.
	for _, f := range s.ifs {
		if f.ifType != ifTypeEthernet {
			continue
		}
		col := func(c int) string { return fmt.Sprintf(lldp+"3.7.1.%d.%d", c, f.index) }
		o.add(col(2), gosnmp.Integer, Const(5)) // interfaceName
		o.add(col(3), gosnmp.OctetString, Const(f.ifName()))
		o.add(col(4), gosnmp.OctetString, Const(f.descr))
	}
	// lldpLocManAddrTable: the management address, indexed by its type and
	// the address with its length first. On the bench it is where the device
	// answers, which is where a manager following LLDP has to go.
	if len(s.addrs) > 0 || (s.wiring != nil && s.wiring.Listen.IsValid()) {
		mgmt, ifIndex := s.primary(), 0
		if len(s.addrs) > 0 {
			ifIndex = s.addrs[0].ifIndex
		}
		if s.wiring != nil && s.wiring.Listen.IsValid() {
			mgmt = s.wiring.Listen
		}
		inst := inetIndex(mgmt)
		col := func(c int) string { return fmt.Sprintf(lldp+"3.8.1.%d.%s", c, inst) }
		o.add(col(3), gosnmp.Integer, Const(len(mgmt.AsSlice())+1))
		o.add(col(4), gosnmp.Integer, Const(2)) // an ifIndex
		o.add(col(5), gosnmp.Integer, Const(ifIndex))
		o.add(col(6), gosnmp.ObjectIdentifier, Const(".0.0"))
	}
	// lldpRemTable, indexed by a time mark, the local port and an index; and
	// lldpRemManAddrTable, each neighbour's management address under the same
	// three and the address.
	for i, n := range neighboursOf(s, l) {
		inst := fmt.Sprintf("0.%d.%d", n.ifIndex, i+1)
		col := func(c int) string { return fmt.Sprintf(lldp+"4.1.1.%d.%s", c, inst) }
		o.add(col(4), gosnmp.Integer, Const(4))
		o.add(col(5), gosnmp.OctetString, Const([]byte(n.chassis)))
		o.add(col(6), gosnmp.Integer, Const(5))
		o.add(col(7), gosnmp.OctetString, Const(n.port))
		o.add(col(8), gosnmp.OctetString, Const(n.portDescr))
		o.add(col(9), gosnmp.OctetString, Const(n.name))
		o.add(col(10), gosnmp.OctetString, Const(n.descr))
		o.add(col(11), gosnmp.OctetString, Const([]byte{n.caps}))
		o.add(col(12), gosnmp.OctetString, Const([]byte{n.caps}))
		if n.addr.IsValid() {
			man := func(c int) string { return fmt.Sprintf(lldp+"4.2.1.%d.%s.%s", c, inst, inetIndex(n.addr)) }
			o.add(man(3), gosnmp.Integer, Const(2)) // an ifIndex, which LLDP did not say
			o.add(man(4), gosnmp.Integer, Const(0))
			o.add(man(5), gosnmp.ObjectIdentifier, Const(".0.0"))
		}
	}
}

// neighboursOf are what a device hears on its ports: on the bench, the device
// at the far end of each cable, and the ones its model invents on the ports
// with no cable in them.
func neighboursOf(s stack, l lldpInfo) []neighbour {
	w := s.wiring
	if w == nil {
		return l.neighbours
	}
	byName := map[string]int{}
	byIndex := map[int]string{}
	for _, f := range s.ifs {
		byName[f.ifName()] = f.index
		byIndex[f.index] = f.ifName()
	}
	var out []neighbour
	for _, n := range l.neighbours {
		if _, cabled := w.Ports[byIndex[n.ifIndex]]; !cabled {
			out = append(out, n)
		}
	}
	for _, name := range sortedKeys(w.Ports) {
		ifIndex, ok := byName[name]
		if !ok {
			continue
		}
		p := w.Ports[name].Peer
		out = append(out, neighbour{ifIndex: ifIndex, name: p.Name, descr: p.Descr, port: p.Port,
			portDescr: p.PortDescr, chassis: p.Chassis, caps: p.Caps, addr: p.Addr})
	}
	slices.SortStableFunc(out, func(a, b neighbour) int { return a.ifIndex - b.ifIndex })
	return out
}
