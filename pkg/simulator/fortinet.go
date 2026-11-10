package simulator

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/gosnmp/gosnmp"
)

// fortiGate60F is a FortiGate 60F on FortiOS 7.2, a branch firewall: two WAN
// ports, the backup one down; the DMZ; the internal switch; the FortiLink port,
// with no FortiSwitch behind it; and the SSL VPN tunnel. It routes, which its
// IP-MIB and routing table say, and FORTINET-FORTIGATE-MIB gives what a
// FortiGate is watched by: processor, memory, disk and sessions, the VDOM, the
// HA mode, the firewall policies' counters and two IPsec tunnels, one up and one
// not. FORTINET-CORE-MIB gives the serial number its notifications carry. Its
// sysObjectID is fgt60F, under fgModel, which is how a monitoring tool
// recognises a FortiGate.
var fortiGate60F = model{
	ModelInfo:  ModelInfo{ID: "fortigate-60f", Category: "security"},
	enterprise: 12356, // Fortinet
	build:      buildFortiGate60F,
	notifications: []Notification{
		linkNotification(false, 2), // wan2, the backup, down
		linkNotification(true, 1),  // wan1
		// FORTINET-CORE-MIB: a processor or memory threshold crossed.
		{Name: "fnTrapCpuThreshold", OID: ".1.3.6.1.4.1.12356.100.1.3.0.101",
			Objects: []string{"." + fnSysSerial, ".1.3.6.1.2.1.1.5.0"}},
		{Name: "fnTrapMemThreshold", OID: ".1.3.6.1.4.1.12356.100.1.3.0.102",
			Objects: []string{"." + fnSysSerial, ".1.3.6.1.2.1.1.5.0", "." + fnGenTrapMsg}},
	},
}

const (
	fnSysSerial = "1.3.6.1.4.1.12356.100.1.1.1.0"
	// fnGenTrapMsg is accessible-for-notify: the text a notification carries.
	fnGenTrapMsg = "1.3.6.1.4.1.12356.100.1.3.1.1.0"
)

func buildFortiGate60F(id Identity) []Object {
	var o objects
	addSystem(&o, id, "FortiGate-60F", ".1.3.6.1.4.1.12356.101.1.644", // fgt60F
		"secops@example.com", "Branch office, network room", 78)
	serial := fmt.Sprintf("FGT60FTK2%07d", int(unit(id.Seed+1)*1e7))
	port := func(index int, descr, alias string) iface {
		return iface{index: index, descr: descr, alias: alias, ifType: ifTypeEthernet, mtu: 1500,
			speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(index))}
	}
	wan1 := port(1, "wan1", "ISP fibre")
	wan1.up, wan1.inRate, wan1.outRate, wan1.errorRate = true, 4_800_000, 1_100_000, 0.001
	dmz := port(3, "dmz", "DMZ servers")
	dmz.up, dmz.inRate, dmz.outRate = true, 600_000, 2_100_000
	internal := port(4, "internal", "LAN")
	internal.up, internal.inRate, internal.outRate = true, 1_300_000, 5_200_000
	isp := netip.MustParsePrefix("198.51.100.32/29")
	lan := lanPrefix(id.Seed)
	dmzNet := netip.MustParsePrefix("172.16.10.0/24")
	sslvpn := netip.MustParsePrefix("10.212.134.0/24")
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			wan1,
			port(2, "wan2", "ISP backup"),
			dmz,
			internal,
			port(5, "fortilink", ""),
			// A tunnel has no speed of its own.
			{index: 6, descr: "ssl.root", alias: "SSL VPN", ifType: ifTypeTunnel, mtu: 1500, up: true,
				inRate: 45_000, outRate: 110_000},
		},
		addrs: []ifAddr{
			{ifIndex: 4, prefix: addrIn(lan, 1), neighbours: 16},
			{ifIndex: 1, prefix: addrIn(isp, 2)},
			{ifIndex: 3, prefix: addrIn(dmzNet, 1), neighbours: 3},
			{ifIndex: 6, prefix: addrIn(sslvpn, 1)},
		},
		gateway: hostIn(isp, 1),
		router:  true,
		ttl:     255,
		pps:     500,
		listen:  []uint16{22, 443, 541, 10443},
		udp:     []uint16{53, 161, 500, 4500, 8013},
		sessions: []session{
			{443, netip.AddrPortFrom(hostIn(lan, 50), 54121)},      // someone in the GUI
			{10443, netip.AddrPortFrom(hostIn(sslvpn, 11), 60312)}, // an SSL VPN user
		},
	})

	o.add(fnSysSerial, gosnmp.OctetString, Const(serial))
	o.addForNotify(fnGenTrapMsg, gosnmp.OctetString, Const("Memory usage has exceeded the configured threshold."))

	const fg = "1.3.6.1.4.1.12356.101."
	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 30000 + n} }
	cpu := Gauge(6, 38, swing(0, 3*time.Minute))
	mem := Gauge(47, 58, swing(1, 25*time.Minute))
	sessions := Gauge(900, 4_200, swing(3, 8*time.Minute))
	// fgSystemInfo
	o.add(fg+"4.1.1.0", gosnmp.OctetString, Const("v7.2.8,build1639,240313 (GA.M)")) // fgSysVersion
	o.add(fg+"4.1.2.0", gosnmp.Integer, Const(1))                                    // fgSysMgmtVdom: root
	o.add(fg+"4.1.3.0", gosnmp.Gauge32, cpu)                                         // fgSysCpuUsage, %
	o.add(fg+"4.1.4.0", gosnmp.Gauge32, mem)                                         // fgSysMemUsage, %
	o.add(fg+"4.1.5.0", gosnmp.Gauge32, Const(uint32(1_906_380)))                    // fgSysMemCapacity, KB
	o.add(fg+"4.1.6.0", gosnmp.Gauge32, Gauge(212, 236, swing(2, 6*time.Hour)))      // fgSysDiskUsage, MB
	o.add(fg+"4.1.7.0", gosnmp.Gauge32, Const(uint32(1_907)))                        // fgSysDiskCapacity, MB
	o.add(fg+"4.1.8.0", gosnmp.Gauge32, sessions)                                    // fgSysSesCount
	o.add(fg+"4.1.9.0", gosnmp.Gauge32, Gauge(20, 26, swing(4, 25*time.Minute)))     // fgSysLowMemUsage
	o.add(fg+"4.1.10.0", gosnmp.Gauge32, Const(uint32(1_906_380)))                   // fgSysLowMemCapacity
	// New sessions a second, averaged over one, ten, thirty and sixty minutes:
	// the longer the average, the slower it moves.
	for i, minutes := range []int{1, 10, 30, 60} {
		rate := Gauge(8, 60, swing(10+uint64(i), time.Duration(minutes+1)*time.Minute))
		o.add(fmt.Sprintf(fg+"4.1.%d.0", 11+i), gosnmp.Gauge32, rate)
	}
	o.add(fg+"4.1.15.0", gosnmp.Gauge32, Gauge(40, 160, swing(5, 8*time.Minute))) // fgSysSes6Count
	// fgSysUpTime counts hundredths of a second in 64 bits, where sysUpTime
	// wraps after 497 days.
	o.add(fg+"4.1.20.0", gosnmp.Counter64, Counter64(100, 0, Swing{}))
	o.add(fg+"4.1.24.0", gosnmp.Gauge32, Gauge(300, 1_900, swing(3, 8*time.Minute))) // fgSysNpuSesCount

	// fgVirtualDomain: one VDOM, root, in NAT mode, standing alone.
	o.add(fg+"3.1.1.0", gosnmp.Integer, Const(1))  // fgVdNumber
	o.add(fg+"3.1.2.0", gosnmp.Integer, Const(10)) // fgVdMaxVdoms
	vd := func(c int) string { return fmt.Sprintf(fg+"3.2.1.1.%d.1", c) }
	o.add(vd(1), gosnmp.Integer, Const(1))
	o.add(vd(2), gosnmp.OctetString, Const("root"))
	o.add(vd(3), gosnmp.Integer, Const(1)) // nat
	o.add(vd(4), gosnmp.Integer, Const(3)) // standalone
	o.add(vd(5), gosnmp.Gauge32, cpu)
	o.add(vd(6), gosnmp.Gauge32, mem)
	o.add(vd(7), gosnmp.Gauge32, sessions)
	o.add(vd(8), gosnmp.Gauge32, Gauge(8, 60, swing(10, 2*time.Minute)))
	o.add(vd(9), gosnmp.OctetString, Const(fmt.Sprintf("%08x", uint32(mix(id.Seed+81)))))

	// fgFwPolStatsTable, indexed by VDOM and policy: what each policy has let
	// through.
	for i, p := range []struct {
		id  int
		bps float64
	}{{1, 600_000}, {2, 2_400_000}, {3, 90_000}, {4, 12_000}} {
		col := func(c int) string { return fmt.Sprintf(fg+"5.1.2.1.1.%d.1.%d", c, p.id) }
		n := 40 + uint64(i)*4
		s := Swing{Depth: 0.6, Period: 9 * time.Minute, Seed: id.Seed + 31000 + n}
		start := uint64(1e6 + 1e9*unit(id.Seed+31100+n))
		o.add(col(2), gosnmp.Counter32, Counter(p.bps/800, start/800, s))
		o.add(col(3), gosnmp.Counter32, Counter(p.bps, start, s))
		o.add(col(4), gosnmp.OctetString, Const("2024-09-11 06:25:41"))
		o.add(col(5), gosnmp.Counter64, Counter64(p.bps/800, start/800, s))
		o.add(col(6), gosnmp.Counter64, Counter64(p.bps, start, s))
	}

	// fgHaInfo: standalone, so its statistics table has the one member.
	o.add(fg+"13.1.1.0", gosnmp.Integer, Const(1)) // fgHaSystemMode: standalone
	o.add(fg+"13.1.2.0", gosnmp.Integer, Const(0))
	o.add(fg+"13.1.3.0", gosnmp.Integer, Const(128))
	o.add(fg+"13.1.7.0", gosnmp.OctetString, Const(""))
	ha := func(c int) string { return fmt.Sprintf(fg+"13.2.1.1.%d.1", c) }
	o.add(ha(2), gosnmp.OctetString, Const(serial))
	o.add(ha(11), gosnmp.OctetString, Const(id.Name))
	o.add(ha(12), gosnmp.Integer, Const(1)) // synchronised

	// fgVpnTunTable: two site-to-site tunnels, indexed by tunnel and phase-2.
	for i, t := range []struct {
		phase1, phase2, remote string
		local, remoteNet       netip.Prefix
		up                     bool
		rate                   float64
	}{
		{"to-hq", "to-hq-p2", "203.0.113.77", lan, netip.MustParsePrefix("10.0.0.0/16"), true, 180_000},
		{"to-store-12", "to-store-12-p2", "192.0.2.144", lan, netip.MustParsePrefix("10.12.0.0/24"), false, 0},
	} {
		row := i + 1
		col := func(c int) string { return fmt.Sprintf(fg+"12.2.2.1.%d.%d.1", c, row) }
		n := 60 + uint64(i)*4
		s := Swing{Depth: 0.6, Period: 11 * time.Minute, Seed: id.Seed + 32000 + n}
		status := 1 // down
		var in, out Reading = Const(uint64(0)), Const(uint64(0))
		if t.up {
			status = 2
			in = Counter64(t.rate, uint64(1e8*unit(id.Seed+32100+n)), s)
			out = Counter64(t.rate*0.4, uint64(1e8*unit(id.Seed+32101+n)), s)
		}
		last := func(p netip.Prefix) string { return hostIn(p, (1<<(32-p.Bits()))-1).String() }
		o.add(col(2), gosnmp.OctetString, Const(t.phase1))
		o.add(col(3), gosnmp.OctetString, Const(t.phase2))
		o.add(col(4), gosnmp.IPAddress, Const(t.remote))
		o.add(col(5), gosnmp.Integer, Const(500))
		o.add(col(6), gosnmp.IPAddress, Const(hostIn(isp, 2).String()))
		o.add(col(7), gosnmp.Integer, Const(500))
		o.add(col(8), gosnmp.IPAddress, Const(t.local.Masked().Addr().String()))
		o.add(col(9), gosnmp.IPAddress, Const(last(t.local)))
		o.add(col(10), gosnmp.Integer, Const(0))
		o.add(col(11), gosnmp.IPAddress, Const(t.remoteNet.Masked().Addr().String()))
		o.add(col(12), gosnmp.IPAddress, Const(last(t.remoteNet)))
		o.add(col(13), gosnmp.Integer, Const(0))
		o.add(col(14), gosnmp.Integer, Const(0))
		o.add(col(15), gosnmp.Gauge32, Const(uint32(43200)))
		o.add(col(16), gosnmp.Gauge32, Const(uint32(0)))
		o.add(col(17), gosnmp.Gauge32, Const(uint32(43200)))
		o.add(col(18), gosnmp.Counter64, in)
		o.add(col(19), gosnmp.Counter64, out)
		o.add(col(20), gosnmp.Integer, Const(status))
		o.add(col(21), gosnmp.Integer, Const(1)) // VDOM root
	}
	return o
}
