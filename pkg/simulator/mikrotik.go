package simulator

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/gosnmp/gosnmp"
)

// mikrotikRB4011 is a MikroTik RB4011 on RouterOS 7, set up as a small office's
// router: ether1 to the provider, the LAN ports in a bridge on RouterOS's own
// 192.168.88.0/24, an SFP+ cage with nothing in it. It routes, NATs and serves
// DHCP and DNS, which its sockets show; MIKROTIK-MIB gives the board's health
// both ways RouterOS does (the old scalars and the gauge table) and the
// neighbours MNDP and LLDP found; HOST-RESOURCES-MIB gives its memory, flash and
// four cores — enough for the "Server" preset as well as the interface ones.
var mikrotikRB4011 = model{
	ModelInfo:  ModelInfo{ID: "mikrotik-rb4011", Category: "network"},
	enterprise: 14988, // MikroTik
	build:      buildMikrotikRB4011,
	notifications: []Notification{
		linkNotification(false, 11), // sfp-sfpplus1, empty
		linkNotification(true, 1),   // ether1, the uplink
		// MIKROTIK-MIB gives it no objects.
		{Name: "mtxrTemperatureException", OID: ".1.3.6.1.4.1.14988.1.1.9.0.2"},
	},
}

func buildMikrotikRB4011(id Identity) []Object {
	var o objects
	const version = "7.14.3"
	descr := "RouterOS RB4011iGS+"
	addSystem(&o, id, descr, ".1.3.6.1.4.1.14988.1", "noc@example.com", "Branch office, network room", 78)
	ifs := make([]iface, 0, 12)
	for p := 1; p <= 10; p++ {
		f := iface{index: p, descr: fmt.Sprintf("ether%d", p), ifType: ifTypeEthernet, mtu: 1500,
			speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(p))}
		switch {
		case p == 1:
			f.alias, f.up, f.inRate, f.outRate, f.errorRate = "uplink", true, 5_600_000, 1_700_000, 0.001
		case p <= 6:
			f.up = true
			f.inRate = 20_000 + 900_000*unit(id.Seed+8100+uint64(p))
			f.outRate = 60_000 + 2_400_000*unit(id.Seed+8200+uint64(p))
		}
		ifs = append(ifs, f)
	}
	ifs = append(ifs,
		iface{index: 11, descr: "sfp-sfpplus1", ifType: ifTypeEthernet, mtu: 1500, speed: 10_000_000_000,
			mac: deviceMAC(id.Seed, 11)},
		// The bridge carries the router's LAN address, and its base MAC — the
		// one its engine ID carries too.
		iface{index: 12, descr: "bridge", alias: "LAN", ifType: ifTypeBridge, mtu: 1500,
			mac: deviceMAC(id.Seed, 0), up: true, inRate: 1_800_000, outRate: 5_000_000},
	)
	wan := netip.MustParsePrefix("198.51.100.0/29")
	lan := netip.MustParsePrefix("192.168.88.0/24")
	switchMAC, apMAC := peerMAC(id.Seed, 901), peerMAC(id.Seed, 902)
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs:  ifs,
		addrs: []ifAddr{
			{ifIndex: 12, prefix: addrIn(lan, 1), neighbours: 14},
			{ifIndex: 1, prefix: addrIn(wan, 2)},
		},
		gateway: hostIn(wan, 1),
		router:  true,
		routes: []route{
			{dest: netip.MustParsePrefix("10.10.0.0/16"), nextHop: hostIn(lan, 2), ifIndex: 12, proto: protoStatic, metric: 1},
		},
		pps:      600,
		listen:   []uint16{22, 53, 80, 443, 2000, 8291, 8728, 8729},
		udp:      []uint16{53, 67, 123, 161, 5678, 20561},
		sessions: []session{{8291, netip.AddrPortFrom(hostIn(lan, 254), 50917)}}, // someone in Winbox
		lldp: &lldpInfo{name: id.Name, descr: descr, caps: capRouter | capBridge, neighbours: []neighbour{
			{ifIndex: 2, name: "sw-office", descr: catalystDescr, port: "Gi0/24", portDescr: "GigabitEthernet0/24",
				chassis: switchMAC, caps: capBridge},
			{ifIndex: 3, name: "ap-office", descr: "U6-Pro 6.6.77.15402", port: "eth0", portDescr: "eth0",
				chassis: apMAC, caps: capWLAN | capBridge},
		}},
	})

	// mtxrHealth, in the units RouterOS reports: tenths of a volt, of a degree
	// and of a watt, and milliamps. The gauge table says the same in its own
	// units, from the same swings.
	const mtxr = "1.3.6.1.4.1.14988.1.1."
	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 40000 + n} }
	o.add(mtxr+"3.8.0", gosnmp.Integer, IntegerGauge(238, 242, swing(0, 30*time.Minute)))  // mtxrHlVoltage
	o.add(mtxr+"3.10.0", gosnmp.Integer, IntegerGauge(390, 440, swing(1, 50*time.Minute))) // mtxrHlTemperature
	o.add(mtxr+"3.11.0", gosnmp.Integer, IntegerGauge(470, 560, swing(2, 12*time.Minute))) // mtxrHlProcessorTemperature
	// The current follows the power: one swing for both.
	o.add(mtxr+"3.12.0", gosnmp.Integer, IntegerGauge(142, 188, swing(3, 9*time.Minute))) // mtxrHlPower
	o.add(mtxr+"3.13.0", gosnmp.Integer, IntegerGauge(590, 780, swing(3, 9*time.Minute))) // mtxrHlCurrent
	for i, g := range []struct {
		name   string
		lo, hi float64
		unit   int // celsius(1), dV(3), dA(4), dW(5)
		swing  Swing
	}{
		{"voltage", 238, 242, 3, swing(0, 30*time.Minute)},
		{"temperature", 39, 44, 1, swing(1, 50*time.Minute)},
		{"cpu-temperature", 47, 56, 1, swing(2, 12*time.Minute)},
		{"power-consumption", 142, 188, 5, swing(3, 9*time.Minute)},
		{"current", 6, 8, 4, swing(3, 9*time.Minute)},
	} {
		n := i + 1
		col := func(c int) string { return fmt.Sprintf(mtxr+"3.100.1.%d.%d", c, n) }
		o.add(col(2), gosnmp.OctetString, Const(g.name))
		o.add(col(3), gosnmp.Integer, IntegerGauge(g.lo, g.hi, g.swing))
		o.add(col(4), gosnmp.Integer, Const(g.unit))
	}
	o.add(mtxr+"4.4.0", gosnmp.OctetString, Const(version)) // mtxrLicVersion
	o.add(mtxr+"7.3.0", gosnmp.OctetString, Const(fmt.Sprintf("%012X", mix(id.Seed+3)&0xFFFFFFFFFFFF)))
	o.add(mtxr+"7.4.0", gosnmp.OctetString, Const(version)) // mtxrFirmwareVersion
	// mtxrNeighborTable: what MNDP and LLDP found on the LAN ports.
	for i, n := range []struct {
		ip                          netip.Addr
		mac                         []byte
		version, platform, identity string
		ifIndex                     int
	}{
		{hostIn(lan, 2), switchMAC, "15.0(2)SE11", "Cisco WS-C2960-24TT-L", "sw-office", 2},
		{hostIn(lan, 3), apMAC, "6.6.77.15402", "U6-Pro", "ap-openspace", 3},
	} {
		row := i + 1
		col := func(c int) string { return fmt.Sprintf(mtxr+"11.1.1.%d.%d", c, row) }
		o.add(col(2), gosnmp.IPAddress, Const(n.ip.String()))
		o.add(col(3), gosnmp.OctetString, Const(n.mac))
		o.add(col(4), gosnmp.OctetString, Const(n.version))
		o.add(col(5), gosnmp.OctetString, Const(n.platform))
		o.add(col(6), gosnmp.OctetString, Const(n.identity))
		o.add(col(7), gosnmp.OctetString, Const(""))
		o.add(col(8), gosnmp.Integer, Const(n.ifIndex))
	}

	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: 1048576, // 1 GiB
		users:     [2]float64{0, 1},
		processes: [2]float64{38, 44},
		storage: []storageArea{
			{65536, hrStorageRam, "main memory", 1024, 1048576, 0.17, 0.23, 20 * time.Minute},
			{131072, hrStorageFixedDisk, "system disk", 1024, 524288, 0.09, 0.10, 12 * time.Hour},
		},
		processors: []int{1, 2, 3, 4},
		cpuLo:      1,
		cpuHi:      24,
		cpu:        "AL21400 Cortex-A15",
	})
	return o
}
