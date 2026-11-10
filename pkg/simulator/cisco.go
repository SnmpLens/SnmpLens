package simulator

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/gosnmp/gosnmp"
)

// The Catalyst 2960, Cisco's access switch of the last fifteen years: Fast
// Ethernet ports, two gigabit uplinks and VLAN 1 — what the "Switch ports"
// preset draws and, with 24 ports, "Switch drawing (24 ports)". Its
// sysObjectID is Cisco's, so the "Cisco (IF-MIB)" preset is offered first for
// it, as it is for a real one.
//
// What a walk of one finds is all here and all agrees: BRIDGE-MIB's ports, the
// stations learnt on each and the spanning tree towards the core; the VLANs,
// said the same in Q-BRIDGE-MIB and CISCO-VTP-MIB; the neighbours LLDP and CDP
// hear on the uplink and on the access point's port; ENTITY-MIB's chassis,
// supply, fan and ports, each port pointing at its interface; the processor,
// the memory pools and the environment as Cisco's own MIBs give them.
//
// The ports are numbered from 1, as the drawing preset polls them, where a
// real 2960 numbers them from 10001; the names are a 2960's. A device given
// fewer ports keeps its uplinks at the indexes its model has them — a gap in
// ifIndex is what a real switch's numbering has too — so that its
// notifications still name them. Two at least: the second is the access
// point LLDP hears, and the third, when there is one, a desk phone with a
// computer behind it.
//
// BRIDGE-MIB is answered as IOS answers it, one VLAN at a time: the community
// alone reads VLAN 1's stations, public@10 VLAN 10's, and in SNMPv3 the
// context vlan-10.
var (
	catalyst24 = model{
		ModelInfo: ModelInfo{ID: "cisco-catalyst-24", Category: "network",
			Params: []ModelParam{{Name: "ports", Min: 2, Max: 24, Default: 24}}},
		enterprise: 9, // Cisco
		build: func(id Identity) []Object {
			return buildCatalyst(id, 24, ".1.3.6.1.4.1.9.1.716", "WS-C2960-24TT-L") // catalyst296024TT
		},
		notifications: catalystNotifications(24),
		vlanContexts:  true,
	}
	catalyst48 = model{
		ModelInfo: ModelInfo{ID: "cisco-catalyst-48", Category: "network",
			Params: []ModelParam{{Name: "ports", Min: 2, Max: 48, Default: 48}}},
		enterprise: 9,
		build: func(id Identity) []Object {
			return buildCatalyst(id, 48, ".1.3.6.1.4.1.9.1.717", "WS-C2960-48TT-L") // catalyst296048TT
		},
		notifications: catalystNotifications(48),
		vlanContexts:  true,
	}
)

const catalystDescr = "Cisco IOS Software, C2960 Software (C2960-LANBASEK9-M), Version 15.0(2)SE11, RELEASE SOFTWARE (fc3)\r\n" +
	"Technical Support: http://www.cisco.com/techsupport\r\n" +
	"Copyright (c) 1986-2017 by Cisco Systems, Inc.\r\n" +
	"Compiled Sat 19-Aug-17 09:34 by prod_rel_team"

// coreDescr is what the core switch the Catalysts hang from says of itself.
const coreDescr = "Cisco IOS Software [Cupertino], Catalyst L3 Switch Software (CAT9K_IOSXE), Version 17.9.4a, RELEASE SOFTWARE (fc3)"

func buildCatalyst(id Identity, ports int, sysObjectID, model string) []Object {
	var o objects
	addSystem(&o, id, catalystDescr, sysObjectID, "noc@example.com", "Wiring closet, floor 2", 2) // layer 2
	lan := lanPrefix(id.Seed)
	serial := fmt.Sprintf("FOC%04dX%03d", 1800+int(unit(id.Seed+61)*300), int(unit(id.Seed+62)*1000))
	uplink, spare, vlan1 := ports+1, ports+2, ports+3
	n := id.count("ports", ports)
	ifs := make([]iface, 0, n+3)
	var access, printers []int
	for p := 1; p <= n; p++ {
		f := iface{index: p, descr: fmt.Sprintf("FastEthernet0/%d", p), name: fmt.Sprintf("Fa0/%d", p),
			ifType: ifTypeEthernet, mtu: 1500, speed: 100_000_000, mac: deviceMAC(id.Seed, uint64(p))}
		// Most of an access switch's ports are in use and some are not, which is
		// also why no bundled preset watches ifOperStatus. The first two are
		// always up: a server, where the presets look for traffic, and the
		// access point LLDP hears.
		switch r := unit(id.Seed + 7000 + uint64(p)); {
		case p == 1:
			f.up, f.alias, f.inRate, f.outRate = true, "srv-01", 900_000, 2_400_000
		case p == 2:
			f.up, f.alias, f.inRate, f.outRate = true, "ap-openspace", 380_000, 1_900_000
		case p == 3:
			f.up, f.alias, f.inRate, f.outRate = true, "desk 2-03 phone", 12_000, 30_000
		case r < 0.06:
			f.adminDown = true
		case r < 0.64:
			f.up, f.alias = true, fmt.Sprintf("desk 2-%02d", p)
			f.inRate = 4_000 + 120_000*unit(id.Seed+7100+uint64(p))
			f.outRate = 8_000 + 300_000*unit(id.Seed+7200+uint64(p))
			f.errorRate = 0.0005
		}
		if p > n-2 {
			printers = append(printers, p)
		} else {
			access = append(access, p)
		}
		ifs = append(ifs, f)
	}
	ifs = append(ifs,
		iface{index: uplink, descr: "GigabitEthernet0/1", name: "Gi0/1", alias: "uplink core-sw-01",
			ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(uplink)),
			up: true, inRate: 6_500_000, outRate: 2_800_000, errorRate: 0.001},
		iface{index: spare, descr: "GigabitEthernet0/2", name: "Gi0/2", alias: "spare uplink",
			ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(spare))},
		// VLAN 1 carries the switch's own address, and its base MAC — the one
		// its engine ID carries too.
		iface{index: vlan1, descr: "Vlan1", name: "Vl1", ifType: ifTypePropVirtual, mtu: 1500,
			speed: 1_000_000_000, mac: deviceMAC(id.Seed, 0), up: true, inRate: 2_000, outRate: 1_500},
	)
	// Every physical interface is a bridge port: all of them but VLAN 1.
	physicalIfs := ifs[:len(ifs)-1]
	bridged := make([]int, 0, len(physicalIfs))
	for _, f := range physicalIfs {
		bridged = append(bridged, f.index)
	}
	// A bridge port is numbered by its place among them, from 1: once the
	// switch has fewer ports than its model, an uplink's ifIndex is not its
	// port's number.
	portOf := func(ifIndexes ...int) []int {
		out := make([]int, len(ifIndexes))
		for i, x := range ifIndexes {
			out[i] = slices.Index(bridged, x) + 1
		}
		return out
	}
	// What the switch's neighbours are when nothing is cabled to it: the core
	// every simulated access switch hangs from, the access point on port 2 and
	// the phone on port 3, each learnt on its port as well as named there.
	core := fictionalCoreChassis
	apMAC, phoneMAC, pcMAC := peerMAC(id.Seed, 902), peerMAC(id.Seed, 904), peerMAC(id.Seed, 905)
	fixed := map[int][]net.HardwareAddr{portOf(uplink)[0]: {core}, portOf(2)[0]: {apMAC}}
	invented := []neighbour{
		{ifIndex: uplink, name: fictionalCoreName, descr: coreDescr, port: "Gi1/0/12", portDescr: "GigabitEthernet1/0/12",
			chassis: core, caps: capBridge | capRouter, addr: hostIn(lan, 1)},
		{ifIndex: 2, name: "ap-openspace", descr: "U6-Pro 6.6.77.15402", port: "eth0", portDescr: "eth0",
			chassis: apMAC, caps: capWLAN | capBridge, addr: hostIn(lan, 40)},
	}
	if n >= 3 {
		fixed[portOf(3)[0]] = []net.HardwareAddr{phoneMAC, pcMAC}
		invented = append(invented, neighbour{ifIndex: 3, name: "phone-2-03", descr: phoneDescr, port: "eth0",
			portDescr: "eth0", chassis: phoneMAC, caps: capTelephone | capBridge})
	}

	// ENTITY-MIB: the chassis, its supply, fan and temperature sensor, and a
	// port for every physical interface.
	entity := []physical{
		{index: 1001, class: classChassis, relPos: -1, descr: model, name: "1", model: model, serial: serial,
			hwRev: "V05", fwRev: "12.2(44r)SE3", swRev: "15.0(2)SE11", mfg: "Cisco Systems, Inc.", fru: false},
		{index: 1002, container: 1001, class: classPowerSupply, relPos: 1, descr: "Power Supply 1", name: "PS1",
			mfg: "Cisco Systems, Inc."},
		{index: 1003, container: 1001, class: classFan, relPos: 1, descr: "Fan 1", name: "Fan 1"},
		{index: 1004, container: 1001, class: classSensor, relPos: 1, descr: "Temperature Sensor 1", name: "Temp 1"},
	}
	for _, f := range physicalIfs {
		entity = append(entity, physical{index: 1010 + f.index, container: 1001, class: classPort, relPos: f.index,
			descr: f.descr, name: f.ifName(), ifIndex: f.index})
	}

	addStack(&o, id, stack{
		seed:     id.Seed,
		ifs:      ifs,
		addrs:    []ifAddr{{ifIndex: vlan1, prefix: addrIn(lan, 2+int(id.Seed%5)), neighbours: 3}},
		gateway:  hostIn(lan, 1),
		ttl:      255,
		pps:      80,
		listen:   []uint16{22},
		udp:      []uint16{123, 161},
		sessions: []session{{22, netip.AddrPortFrom(hostIn(lan, 50), 58113)}},
		entity:   entity,
		bridge: &bridgeInfo{
			ports: bridged,
			vlans: []vlanInfo{
				{id: 1, name: "default", untagged: portOf(uplink, spare)},
				{id: 10, name: "USERS", untagged: portOf(access...), tagged: portOf(uplink)},
				{id: 20, name: "PRINTERS", untagged: portOf(printers...), tagged: portOf(uplink)},
			},
			stations: 1, uplinkStations: 40, uplink: portOf(uplink)[0], root: core,
			fixed: fixed, perVLAN: true,
		},
		lldp: &lldpInfo{name: id.Name, descr: catalystDescr, caps: capBridge, neighbours: invented},
	})

	addCiscoCPU(&o, id.Seed, 1001, 3, 24, 65536)
	addCiscoMemory(&o, id.Seed, []memPool{
		{1, "Processor", 91_000_000, [2]float64{0.26, 0.29}},
		{2, "I/O", 25_000_000, [2]float64{0.3, 0.33}},
	})
	addCiscoEnv(&o, id.Seed, []envTemp{{"SW#1, Sensor#1, GREEN ", 36, 41, 60}},
		[]string{"Switch#1, Fan#1, Normal"}, []string{"Sw1, PS1 Normal, RPS NotExist"})
	addCDP(&o, id.Name, cdpOf(id, ifs, []cdpNeighbour{
		{ifIndex: uplink, name: fictionalCoreName, addr: hostIn(lan, 1), version: coreDescr,
			port: "GigabitEthernet1/0/12", platform: "cisco C9300-24T", caps: 0x29, nativeVLAN: 1},
	}))
	addVTP(&o, "OFFICE", []vtpVLAN{{1, "default", vlan1}, {10, "USERS", 0}, {20, "PRINTERS", 0}})
	addCiscoConfigHistory(&o)
	return o
}

// catalystNotifications are a Catalyst's: linkDown about its spare uplink,
// which is down, linkUp about the one in use, and a configuration change.
func catalystNotifications(ports int) []Notification {
	return []Notification{linkNotification(false, ports+2), linkNotification(true, ports+1), ciscoConfigManEvent}
}

// isr4331 is a branch router: a WAN port to its transit provider, a LAN port, a
// backup WAN port that is down, the management port, and two eBGP sessions, one
// established and one not. What it routes agrees with BGP4-MIB: the prefixes the
// established peer sent are in its routing table, through that peer, learnt by
// BGP from its AS, and the peer's TCP session on port 179 is in tcpConnTable.
var isr4331 = model{
	ModelInfo:  ModelInfo{ID: "cisco-isr-4331", Category: "network"},
	enterprise: 9,
	build:      buildISR4331,
	notifications: []Notification{
		linkNotification(false, 3), // the backup WAN, down
		linkNotification(true, 1),  // the WAN
		bgpNotification("bgpEstablishedNotification", 1, bgpPeers[0].remote),
		bgpNotification("bgpBackwardTransNotification", 2, bgpPeers[1].remote),
		ciscoConfigManEvent,
	},
}

const isrDescr = "Cisco IOS XE Software, Version 17.09.04a\r\n" +
	"Cisco IOS Software [Cupertino], ISR Software (X86_64_LINUX_IOSD-UNIVERSALK9-M), Version 17.9.4a, RELEASE SOFTWARE (fc3)\r\n" +
	"Technical Support: http://www.cisco.com/techsupport\r\n" +
	"Copyright (c) 1986-2023 by Cisco Systems, Inc.\r\n" +
	"Compiled Fri 20-Oct-23 10:44 by mcpre"

func buildISR4331(id Identity) []Object {
	var o objects
	addSystem(&o, id, isrDescr, ".1.3.6.1.4.1.9.1.2068", // ciscoISR4331
		"noc@example.com", "Branch office, network room", 78)
	gig := func(index int, descr, name, alias string) iface {
		return iface{index: index, descr: descr, name: name, alias: alias, ifType: ifTypeEthernet, mtu: 1500,
			speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(index))}
	}
	wan := gig(1, "GigabitEthernet0/0/0", "Gi0/0/0", "WAN transit 203.0.113.0/30")
	wan.up, wan.inRate, wan.outRate, wan.errorRate = true, 3_200_000, 900_000, 0.002
	lanPort := gig(2, "GigabitEthernet0/0/1", "Gi0/0/1", "LAN")
	lanPort.up, lanPort.inRate, lanPort.outRate = true, 850_000, 3_000_000
	mgmt := gig(4, "GigabitEthernet0", "Gi0", "management")
	mgmt.up, mgmt.inRate, mgmt.outRate = true, 3_000, 5_000
	ifs := []iface{
		wan,
		lanPort,
		gig(3, "GigabitEthernet0/0/2", "Gi0/0/2", "backup WAN"),
		mgmt,
		{index: 5, descr: "Null0", name: "Nu0", ifType: ifTypeOther, mtu: 1500, speed: 10_000_000_000, up: true},
		{index: 6, descr: "Loopback0", name: "Lo0", ifType: ifTypeSoftwareLoopback, mtu: 1514,
			speed: 8_000_000_000, up: true},
	}
	lan := lanPrefix(id.Seed)
	transit := netip.MustParsePrefix(bgpPeers[0].remote + "/30")
	provider := netip.MustParseAddr(bgpPeers[0].remote)
	serial := fmt.Sprintf("FDO%04dA%03d", 2100+int(unit(id.Seed+63)*200), int(unit(id.Seed+64)*1000))

	// What the established peer sent, learnt by BGP from its AS.
	var learnt []route
	for _, p := range []string{"192.0.2.0/24", "198.18.0.0/15", "203.0.113.64/26", "100.64.0.0/10"} {
		learnt = append(learnt, route{dest: netip.MustParsePrefix(p), nextHop: provider, ifIndex: 1,
			proto: protoBGP, as: bgpPeers[0].as})
	}
	learnt = append(learnt, route{dest: netip.MustParsePrefix("10.99.0.0/16"), nextHop: hostIn(lan, 254),
		ifIndex: 2, proto: protoStatic, metric: 1})

	entity := []physical{
		{index: 1, class: classChassis, relPos: -1, descr: "Cisco ISR4331 Chassis", name: "Chassis", model: "ISR4331/K9",
			serial: serial, hwRev: "V05", fwRev: "16.12(2r)", swRev: "17.09.04a", mfg: "Cisco Systems Inc"},
		{index: 2, container: 1, class: classContainer, relPos: 0, descr: "Cisco ISR4331 Module 0 Container", name: "module 0"},
		{index: 3, container: 2, class: classModule, relPos: 0, descr: "Front Panel 3 ports Gigabitethernet Module",
			name: "NIM subslot 0/0", model: "ISR4331-3x1GE", mfg: "Cisco Systems Inc", fru: false},
		{index: 4, container: 1, class: classPowerSupply, relPos: 1, descr: "250W AC Power Supply for Cisco ISR 4330",
			name: "Power Supply Module 0", model: "PWR-4330-AC", fru: true, mfg: "Cisco Systems Inc"},
		{index: 5, container: 1, class: classFan, relPos: 1, descr: "Cisco ISR4331 Fan Tray", name: "Fan Tray", fru: true},
		{index: 6, container: 1, class: classCPU, relPos: 1, descr: "CPU 0 of module R0", name: "cpu R0/0"},
	}
	for k, f := range ifs[:3] {
		entity = append(entity, physical{index: 10 + f.index, container: 3, class: classPort, relPos: k,
			descr: f.descr, name: f.ifName(), ifIndex: f.index})
	}

	addStack(&o, id, stack{
		seed: id.Seed,
		ifs:  ifs,
		addrs: []ifAddr{
			{ifIndex: 1, prefix: netip.PrefixFrom(netip.MustParseAddr(bgpPeers[0].local), transit.Bits())},
			{ifIndex: 2, prefix: addrIn(lan, 1), neighbours: 20},
			{ifIndex: 4, prefix: netip.MustParsePrefix("192.168.255.4/24")},
			{ifIndex: 6, prefix: netip.MustParsePrefix("10.255.0.1/32")},
		},
		gateway: provider,
		router:  true,
		routes:  learnt,
		ttl:     255,
		pps:     300,
		listen:  []uint16{22},
		udp:     []uint16{123, 161, 500, 4500},
		sessions: []session{
			{36011, netip.AddrPortFrom(provider, 179)}, // BGP, which the router opened
			{22, netip.AddrPortFrom(hostIn(lan, 50), 58213)},
		},
		entity: entity,
		lldp: &lldpInfo{name: id.Name, descr: isrDescr, caps: capRouter, neighbours: []neighbour{
			{ifIndex: 2, name: fictionalCoreName, descr: coreDescr, port: "Gi1/0/1", portDescr: "GigabitEthernet1/0/1",
				chassis: fictionalCoreChassis, caps: capBridge | capRouter, addr: hostIn(lan, 2)},
		}},
	})
	addCiscoCPU(&o, id.Seed, 6, 2, 14, 3_900_000)
	addCiscoMemory(&o, id.Seed, []memPool{
		{1, "Processor", 1_966_000_000, [2]float64{0.21, 0.24}},
		{2, "lsmpi_io", 3_149_000, [2]float64{0.99, 0.99}},
	})
	addCiscoEnv(&o, id.Seed, []envTemp{
		{"Temp: Inlet 1", 24, 29, 50},
		{"Temp: Outlet 1", 31, 38, 60},
		{"Temp: CPU 0", 41, 55, 90},
	}, []string{"Fan 1", "Fan 2", "Fan 3"}, []string{"PEM Iout: power-supply 0"})
	addCDP(&o, id.Name, cdpOf(id, ifs, []cdpNeighbour{
		{ifIndex: 2, name: fictionalCoreName, addr: hostIn(lan, 2), version: coreDescr, port: "GigabitEthernet1/0/1",
			platform: "cisco C9300-24T", caps: 0x29, nativeVLAN: 1},
	}))
	addBGP(&o, id.Seed)
	addCiscoConfigHistory(&o)
	return o
}

// addCiscoCPU adds CISCO-PROCESS-MIB's cpmCPUTotalTable for one processor: the
// entity it is, its load over five seconds, a minute and five minutes — the
// longer averages steadier than the shorter, in the deprecated columns as well
// as the current ones — and its memory, in kilobytes.
func addCiscoCPU(o *objects, seed uint64, entity int, lo, hi float64, memKB float64) {
	col := func(n int) string { return fmt.Sprintf("1.3.6.1.4.1.9.9.109.1.1.1.1.%d.1", n) }
	span := hi - lo
	five := Gauge(lo, hi, Swing{Period: 2 * time.Minute, Seed: seed + 9000})
	minute := Gauge(lo+span*0.2, hi-span*0.3, Swing{Period: 10 * time.Minute, Seed: seed + 9001})
	fiveMin := Gauge(lo+span*0.3, hi-span*0.5, Swing{Period: 30 * time.Minute, Seed: seed + 9002})
	used := Gauge(memKB*0.2, memKB*0.24, Swing{Period: 40 * time.Minute, Seed: seed + 9003})
	o.add(col(2), gosnmp.Integer, Const(entity))
	for k, r := range []Reading{five, minute, fiveMin} {
		o.add(col(3+k), gosnmp.Gauge32, r)
		o.add(col(6+k), gosnmp.Gauge32, r)
	}
	o.add(col(12), gosnmp.Gauge32, used)
	o.add(col(13), gosnmp.Gauge32, remainder(gosnmp.Gauge32, memKB, used))
}

// memPool is one of CISCO-MEMORY-POOL-MIB's pools.
type memPool struct {
	index int
	name  string
	size  float64 // bytes
	used  [2]float64
}

// addCiscoMemory adds CISCO-MEMORY-POOL-MIB: each pool's use in bytes, what
// is free of it, and its largest free block.
func addCiscoMemory(o *objects, seed uint64, pools []memPool) {
	for _, p := range pools {
		col := func(c int) string { return fmt.Sprintf("1.3.6.1.4.1.9.9.48.1.1.1.%d.%d", c, p.index) }
		used := Gauge(p.used[0]*p.size, p.used[1]*p.size, Swing{Period: 45 * time.Minute, Seed: seed + 9100 + uint64(p.index)})
		free := remainder(gosnmp.Gauge32, p.size, used)
		o.add(col(2), gosnmp.OctetString, Const(p.name))
		o.add(col(3), gosnmp.Integer, Const(0))
		o.add(col(4), gosnmp.Integer, Const(1)) // valid
		o.add(col(5), gosnmp.Gauge32, used)
		o.add(col(6), gosnmp.Gauge32, free)
		o.add(col(7), gosnmp.Gauge32, derived{typ: gosnmp.Gauge32, f: func(c clock) any {
			return uint32(number(free, c) * 0.8)
		}})
	}
}

// envTemp is a temperature CISCO-ENVMON-MIB reports, its range and threshold.
type envTemp struct {
	descr             string
	lo, hi, threshold float64
}

// addCiscoEnv adds CISCO-ENVMON-MIB: the temperatures, fans and supplies, all
// normal.
func addCiscoEnv(o *objects, seed uint64, temps []envTemp, fans, supplies []string) {
	const env = "1.3.6.1.4.1.9.9.13.1."
	for i, t := range temps {
		n := i + 1
		col := func(c int) string { return fmt.Sprintf(env+"3.1.%d.%d", c, n) }
		o.add(col(2), gosnmp.OctetString, Const(t.descr))
		o.add(col(3), gosnmp.Gauge32, Gauge(t.lo, t.hi, Swing{Period: 50 * time.Minute, Seed: seed + 9200 + uint64(n)}))
		o.add(col(4), gosnmp.Integer, Const(int(t.threshold)))
		o.add(col(5), gosnmp.Integer, Const(int(t.threshold+10)))
		o.add(col(6), gosnmp.Integer, Const(1)) // normal
	}
	for i, f := range fans {
		n := i + 1
		o.add(fmt.Sprintf(env+"4.1.2.%d", n), gosnmp.OctetString, Const(f))
		o.add(fmt.Sprintf(env+"4.1.3.%d", n), gosnmp.Integer, Const(1))
	}
	for i, s := range supplies {
		n := i + 1
		o.add(fmt.Sprintf(env+"5.1.2.%d", n), gosnmp.OctetString, Const(s))
		o.add(fmt.Sprintf(env+"5.1.3.%d", n), gosnmp.Integer, Const(1))
		o.add(fmt.Sprintf(env+"5.1.4.%d", n), gosnmp.Integer, Const(2)) // AC
	}
}

// cdpNeighbour is a device CDP has heard on a port.
type cdpNeighbour struct {
	ifIndex                       int
	name, version, port, platform string
	addr                          netip.Addr
	caps                          uint32 // router 0x01, switch 0x08, IGMP 0x20…
	nativeVLAN                    int
}

// cdpOf is a Cisco's CDP cache: on the bench, the Cisco at the far end of each
// cable, by the address it answers on, and the neighbours its model invents on
// the ports with no cable in them; elsewhere what its model invents.
func cdpOf(id Identity, ifs []iface, invented []cdpNeighbour) []cdpNeighbour {
	w := id.Wiring
	if w == nil {
		return invented
	}
	byName, byIndex := map[string]int{}, map[int]string{}
	for _, f := range ifs {
		byName[f.ifName()], byIndex[f.index] = f.index, f.ifName()
	}
	var out []cdpNeighbour
	for _, n := range invented {
		if _, cabled := w.Ports[byIndex[n.ifIndex]]; !cabled {
			out = append(out, n)
		}
	}
	for _, name := range sortedKeys(w.Ports) {
		p := w.Ports[name].Peer
		ifIndex, ok := byName[name]
		// CDP's address here is IPv4: a neighbour answering on ::1 alone is
		// heard by LLDP.
		if !ok || !p.CDP || !p.Addr.Is4() {
			continue
		}
		out = append(out, cdpNeighbour{ifIndex: ifIndex, name: p.Name, addr: p.Addr, version: p.Descr,
			port: p.PortDescr, platform: p.Platform, caps: cdpCaps(p.Caps), nativeVLAN: 1})
	}
	return out
}

// cdpCaps is cdpCacheCapabilities for LLDP capabilities: router 0x01, switch
// 0x08, host 0x10, phone 0x80.
func cdpCaps(lldp byte) uint32 {
	var c uint32
	if lldp&capRouter != 0 {
		c |= 0x01
	}
	if lldp&capBridge != 0 {
		c |= 0x08 | 0x20 // a switch, which snoops IGMP
	}
	if lldp&capTelephone != 0 {
		c = 0x80 | 0x10
	}
	if c == 0 {
		c = 0x10
	}
	return c
}

// addCDP adds CISCO-CDP-MIB: CDP running, and its cache, indexed by the port
// and a number.
func addCDP(o *objects, name string, neighbours []cdpNeighbour) {
	const cdp = "1.3.6.1.4.1.9.9.23.1."
	o.add(cdp+"3.1.0", gosnmp.Integer, Const(1)) // running
	o.add(cdp+"3.2.0", gosnmp.Integer, Const(60))
	o.add(cdp+"3.3.0", gosnmp.Integer, Const(180))
	o.add(cdp+"3.4.0", gosnmp.OctetString, Const(name))
	for i, n := range neighbours {
		col := func(c int) string { return fmt.Sprintf(cdp+"2.1.1.%d.%d.%d", c, n.ifIndex, i+1) }
		addr := n.addr.As4()
		caps := binary.BigEndian.AppendUint32(nil, n.caps)
		o.add(col(3), gosnmp.Integer, Const(1)) // ip
		o.add(col(4), gosnmp.OctetString, Const(addr[:]))
		o.add(col(5), gosnmp.OctetString, Const(n.version))
		o.add(col(6), gosnmp.OctetString, Const(n.name))
		o.add(col(7), gosnmp.OctetString, Const(n.port))
		o.add(col(8), gosnmp.OctetString, Const(n.platform))
		o.add(col(9), gosnmp.OctetString, Const(caps))
		o.add(col(10), gosnmp.OctetString, Const(""))
		o.add(col(11), gosnmp.Integer, Const(n.nativeVLAN))
		o.add(col(12), gosnmp.Integer, Const(3)) // full duplex
	}
}

// vtpVLAN is one of CISCO-VTP-MIB's VLANs, and the interface routing it.
type vtpVLAN struct {
	id      int
	name    string
	ifIndex int
}

// addVTP adds CISCO-VTP-MIB's management domain and its VLANs: the ones the
// switch carries, and the four every IOS switch keeps for FDDI and Token Ring.
func addVTP(o *objects, domain string, vlans []vtpVLAN) {
	const vtp = "1.3.6.1.4.1.9.9.46.1."
	o.add(vtp+"2.1.1.2.1", gosnmp.OctetString, Const(domain))
	type row struct {
		vtpVLAN
		kind int // ethernet(1), fddi(2), tokenRing(3), fddiNet(4), trNet(5)
	}
	rows := make([]row, 0, len(vlans)+4)
	for _, v := range vlans {
		rows = append(rows, row{v, 1})
	}
	rows = append(rows, row{vtpVLAN{1002, "fddi-default", 0}, 2}, row{vtpVLAN{1003, "token-ring-default", 0}, 3},
		row{vtpVLAN{1004, "fddinet-default", 0}, 4}, row{vtpVLAN{1005, "trnet-default", 0}, 5})
	for _, v := range rows {
		col := func(c int) string { return fmt.Sprintf(vtp+"3.1.1.%d.1.%d", c, v.id) }
		o.add(col(2), gosnmp.Integer, Const(1)) // operational
		o.add(col(3), gosnmp.Integer, Const(v.kind))
		o.add(col(4), gosnmp.OctetString, Const(v.name))
		o.add(col(5), gosnmp.Integer, Const(1500))
		o.add(col(6), gosnmp.OctetString, Const(binary.BigEndian.AppendUint32(nil, uint32(100000+v.id))))
		o.add(col(18), gosnmp.Integer, Const(v.ifIndex))
	}
}

// ciscoConfigManEvent is CISCO-CONFIG-MAN-MIB's notice of a configuration
// change, carrying the history row that records it.
var ciscoConfigManEvent = Notification{
	Name: "ciscoConfigManEvent",
	OID:  ".1.3.6.1.4.1.9.9.43.2.0.1",
	// ccmHistoryEventCommandSource, …ConfigSource, …ConfigDestination
	Objects: []string{
		".1.3.6.1.4.1.9.9.43.1.1.6.1.3.1",
		".1.3.6.1.4.1.9.9.43.1.1.6.1.4.1",
		".1.3.6.1.4.1.9.9.43.1.1.6.1.5.1",
	},
}

// addCiscoConfigHistory adds the one history row ciscoConfigManEvent carries: a
// change typed at the command line into the running configuration.
func addCiscoConfigHistory(o *objects) {
	col := func(n int) string { return fmt.Sprintf("1.3.6.1.4.1.9.9.43.1.1.6.1.%d.1", n) }
	o.add(col(3), gosnmp.Integer, Const(1)) // command source: commandLine
	o.add(col(4), gosnmp.Integer, Const(2)) // configuration source: commandSource
	o.add(col(5), gosnmp.Integer, Const(3)) // configuration destination: running
}

// The router's two eBGP sessions: to its transit provider, established since the
// router started, and to a second provider that is not — which is what
// bgpBackwardTransNotification is about.
var bgpPeers = []struct {
	remote, local, identifier string
	as                        int
	established               bool
}{
	{remote: "203.0.113.1", local: "203.0.113.2", identifier: "203.0.113.1", as: 64500, established: true},
	{remote: "198.51.100.9", local: "0.0.0.0", identifier: "0.0.0.0", as: 64501},
}

// bgpNotification is one of BGP4-MIB's two (RFC 4273) about a peer: its
// address, its last error and its state.
func bgpNotification(name string, n int, peer string) Notification {
	col := func(c int) string { return fmt.Sprintf(".1.3.6.1.2.1.15.3.1.%d.%s", c, peer) }
	return Notification{Name: name, OID: fmt.Sprintf(".1.3.6.1.2.1.15.0.%d", n), Objects: []string{col(7), col(14), col(2)}}
}

// addBGP adds BGP4-MIB's scalars and its peer table, indexed by the peer's
// address.
func addBGP(o *objects, seed uint64) {
	o.add("1.3.6.1.2.1.15.1.0", gosnmp.OctetString, Const([]byte{0x10})) // bgpVersion: 4
	o.add("1.3.6.1.2.1.15.2.0", gosnmp.Integer, Const(65010))            // bgpLocalAs
	o.add("1.3.6.1.2.1.15.4.0", gosnmp.IPAddress, Const("10.255.0.1"))   // bgpIdentifier: Loopback0
	for i, p := range bgpPeers {
		col := func(c int) string { return fmt.Sprintf("1.3.6.1.2.1.15.3.1.%d.%s", c, p.remote) }
		n := uint64(i) * 8
		swing := Swing{Depth: 0.3, Period: 20 * time.Minute, Seed: seed + 60000 + n}
		start := func(k uint64) uint64 { return uint64(1e4 + 5e4*unit(seed+60100+n+k)) }
		o.add(col(1), gosnmp.IPAddress, Const(p.identifier))
		o.add(col(3), gosnmp.Integer, Const(2)) // admin status: start
		o.add(col(5), gosnmp.IPAddress, Const(p.local))
		o.add(col(7), gosnmp.IPAddress, Const(p.remote))
		o.add(col(9), gosnmp.Integer, Const(p.as))
		// In the established state, or out of it, since the router started.
		o.add(col(16), gosnmp.Gauge32, SecondsUp())
		o.add(col(17), gosnmp.Integer, Const(120)) // connect retry interval
		o.add(col(20), gosnmp.Integer, Const(180)) // hold time configured
		o.add(col(21), gosnmp.Integer, Const(60))  // keepalive configured
		o.add(col(22), gosnmp.Integer, Const(15))  // minimum AS origination interval
		o.add(col(23), gosnmp.Integer, Const(30))  // minimum route advertisement interval
		if p.established {
			o.add(col(2), gosnmp.Integer, Const(6)) // established
			o.add(col(4), gosnmp.Integer, Const(4))
			o.add(col(6), gosnmp.Integer, Const(36011))
			o.add(col(8), gosnmp.Integer, Const(179))
			o.add(col(10), gosnmp.Counter32, Counter(0.02, start(0), swing))
			o.add(col(11), gosnmp.Counter32, Counter(0.005, start(1), swing))
			// A keepalive a minute, and the updates.
			o.add(col(12), gosnmp.Counter32, Counter(1.0/60+0.02, start(2), swing))
			o.add(col(13), gosnmp.Counter32, Counter(1.0/60+0.005, start(3), swing))
			o.add(col(14), gosnmp.OctetString, Const([]byte{0, 0}))
			o.add(col(15), gosnmp.Counter32, Const(uint32(1)))
			o.add(col(18), gosnmp.Integer, Const(180))
			o.add(col(19), gosnmp.Integer, Const(60))
			o.add(col(24), gosnmp.Gauge32, Gauge(5, 240, swing))
		} else {
			o.add(col(2), gosnmp.Integer, Const(3)) // active: trying, and not getting through
			o.add(col(4), gosnmp.Integer, Const(0))
			o.add(col(6), gosnmp.Integer, Const(0))
			o.add(col(8), gosnmp.Integer, Const(0))
			o.add(col(10), gosnmp.Counter32, Const(uint32(1482)))
			o.add(col(11), gosnmp.Counter32, Const(uint32(37)))
			o.add(col(12), gosnmp.Counter32, Const(uint32(9163)))
			o.add(col(13), gosnmp.Counter32, Const(uint32(7702)))
			o.add(col(14), gosnmp.OctetString, Const([]byte{4, 0})) // hold timer expired
			o.add(col(15), gosnmp.Counter32, Const(uint32(3)))
			o.add(col(18), gosnmp.Integer, Const(0))
			o.add(col(19), gosnmp.Integer, Const(0))
			o.add(col(24), gosnmp.Gauge32, Const(uint32(0)))
		}
	}
}
