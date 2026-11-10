package simulator

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/gosnmp/gosnmp"
)

// unifiU6Pro is a UniFi U6 Pro access point: its uplink, two radios and three
// SSIDs — the office network on both bands and a guest one on 5 GHz.
// UBNT-UniFi-MIB gives what a wireless dashboard polls: each radio's channel use
// and traffic, each SSID's clients, traffic, errors and retries, the interfaces
// as the controller sees them. A U6 runs Linux and answers with net-snmp's
// sysObjectID, a sysDescr naming the model, host resources and UCD-SNMP-MIB, and
// LLDP says which switch port it hangs from.
var unifiU6Pro = model{
	ModelInfo:     ModelInfo{ID: "unifi-u6-pro", Category: "wireless"},
	enterprise:    41112, // Ubiquiti
	build:         buildUniFiU6Pro,
	notifications: []Notification{linkNotification(true, 2)}, // eth0, the uplink
}

// The access point's SSIDs, each on an interface of its own, as a UniFi names
// them.
var unifiVAPs = []struct {
	essid, iface, radio, usage string
	channel, txPower           int
	stations                   [2]float64
	rx, tx                     float64 // octets per second
}{
	{"Office", "ath0", "ng", "user", 6, 20, [2]float64{3, 9}, 90_000, 260_000},
	{"Office", "ath1", "na", "user", 44, 23, [2]float64{8, 22}, 700_000, 2_600_000},
	{"Guest", "ath2", "na", "guest", 44, 23, [2]float64{0, 6}, 60_000, 380_000},
}

func buildUniFiU6Pro(id Identity) []Object {
	var o objects
	const version = "6.6.77.15402"
	descr := "U6-Pro " + version
	addSystem(&o, id, descr, ".1.3.6.1.4.1.8072.3.2.10", "noc@example.com", "Open space, ceiling", 66)
	lan := lanPrefix(id.Seed)
	own := addrIn(lan, 60+int(id.Seed%20))
	ifs := []iface{
		{index: 1, descr: "lo", ifType: ifTypeSoftwareLoopback, mtu: 65536, speed: 10_000_000,
			up: true, inRate: 3_000, outRate: 3_000},
		{index: 2, descr: "eth0", alias: "uplink", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
			mac: deviceMAC(id.Seed, 1), up: true, inRate: 2_900_000, outRate: 900_000, errorRate: 0.0005},
		{index: 3, descr: "wifi0", ifType: ifTypeIEEE80211, mtu: 1500, speed: 573_500_000,
			mac: deviceMAC(id.Seed, 2), up: true, inRate: 90_000, outRate: 260_000},
		{index: 4, descr: "wifi1", ifType: ifTypeIEEE80211, mtu: 1500, speed: 4_800_000_000,
			mac: deviceMAC(id.Seed, 3), up: true, inRate: 760_000, outRate: 2_980_000},
	}
	for i, v := range unifiVAPs {
		ifs = append(ifs, iface{index: 5 + i, descr: v.iface, alias: v.essid, ifType: ifTypeIEEE80211, mtu: 1500,
			speed: ifs[2+min(i, 1)].speed, mac: deviceMAC(id.Seed, uint64(4+i)), up: true, inRate: v.rx, outRate: v.tx})
	}
	ifs = append(ifs, iface{index: 8, descr: "br0", ifType: ifTypeBridge, mtu: 1500,
		mac: deviceMAC(id.Seed, 0), up: true, inRate: 2_800_000, outRate: 850_000})
	addStack(&o, id, stack{
		seed:    id.Seed,
		ifs:     ifs,
		addrs:   []ifAddr{{ifIndex: 8, prefix: own, neighbours: 4}},
		gateway: hostIn(lan, 1),
		pps:     900,
		listen:  []uint16{22},
		udp:     []uint16{161, 10001},
		// The access point keeps its controller informed.
		sessions: []session{{45872, netip.AddrPortFrom(hostIn(lan, 20), 8080)}},
		lldp: &lldpInfo{name: id.Name, descr: descr, caps: capWLAN | capBridge, neighbours: []neighbour{
			{ifIndex: 2, name: "sw-floor-2", descr: catalystDescr, port: "Fa0/2", portDescr: "FastEthernet0/2",
				chassis: peerMAC(id.Seed, 900), caps: capBridge, addr: hostIn(lan, 2)},
		}},
		modules: []sysOR{moduleHostResources},
	})

	const unifi = "1.3.6.1.4.1.41112.1.6."
	swing := func(n uint64, depth float64, period time.Duration) Swing {
		return Swing{Depth: depth, Period: period, Seed: id.Seed + 45000 + n}
	}
	start := func(n uint64) uint64 { return uint64(1e5 + 1e7*unit(id.Seed+45500+n)) }
	// unifiRadioTable: how much of each radio's airtime is in use, how much of
	// that is this access point's own, and the other networks it hears.
	for i, r := range []struct {
		name, radio string
		lo, hi      float64
		pps         float64
		others      int
	}{{"wifi0", "ng", 28, 61, 420, 14}, {"wifi1", "na", 9, 34, 3100, 5}} {
		row := i + 1
		col := func(c int) string { return fmt.Sprintf(unifi+"1.1.1.%d.%d", c, row) }
		n := uint64(i) * 8
		o.add(col(1), gosnmp.Integer, Const(row))
		o.add(col(2), gosnmp.OctetString, Const(r.name))
		o.add(col(3), gosnmp.OctetString, Const(r.radio))
		o.add(col(4), gosnmp.Counter32, Counter(r.pps*0.4, start(n), swing(n+3, 0.6, 5*time.Minute)))
		o.add(col(5), gosnmp.Counter32, Counter(r.pps*0.6, start(n+1), swing(n+4, 0.6, 5*time.Minute)))
		o.add(col(6), gosnmp.Integer, IntegerGauge(r.lo, r.hi, swing(n, 0, 4*time.Minute)))
		o.add(col(7), gosnmp.Integer, IntegerGauge(r.lo*0.3, r.hi*0.3, swing(n+1, 0, 4*time.Minute)))
		o.add(col(8), gosnmp.Integer, IntegerGauge(r.lo*0.2, r.hi*0.25, swing(n+2, 0, 4*time.Minute)))
		o.add(col(9), gosnmp.Integer, Const(r.others))
	}
	// unifiVapTable: one row per SSID and band.
	for i, v := range unifiVAPs {
		row := i + 1
		col := func(c int) string { return fmt.Sprintf(unifi+"1.2.1.%d.%d", c, row) }
		n := 20 + uint64(i)*8
		rx := func(k uint64, share float64) Reading {
			return Counter(v.rx*share, start(n+k), swing(n+1, 0.6, 5*time.Minute))
		}
		tx := func(k uint64, share float64) Reading {
			return Counter(v.tx*share, start(n+k), swing(n+2, 0.6, 7*time.Minute))
		}
		o.add(col(1), gosnmp.Integer, Const(row))
		o.add(col(2), gosnmp.OctetString, Const([]byte(deviceMAC(id.Seed, uint64(4+i))))) // the BSSID
		o.add(col(3), gosnmp.Integer, IntegerGauge(78, 97, swing(n+3, 0, 9*time.Minute))) // CCQ, %
		o.add(col(4), gosnmp.Integer, Const(v.channel))
		o.add(col(5), gosnmp.Integer, Const(0))
		o.add(col(6), gosnmp.OctetString, Const(v.essid))
		o.add(col(7), gosnmp.OctetString, Const(v.iface))
		o.add(col(8), gosnmp.Integer, IntegerGauge(v.stations[0], v.stations[1], swing(n, 0, 30*time.Minute)))
		o.add(col(9), gosnmp.OctetString, Const(v.radio))
		o.add(col(10), gosnmp.Counter32, rx(0, 1))
		o.add(col(11), gosnmp.Counter32, rx(1, 0.0001))
		o.add(col(12), gosnmp.Counter32, rx(2, 0.00002))
		o.add(col(13), gosnmp.Counter32, rx(3, 0.00001))
		o.add(col(14), gosnmp.Counter32, Const(uint32(0)))
		o.add(col(15), gosnmp.Counter32, rx(4, 1.0/800))
		o.add(col(16), gosnmp.Counter32, tx(5, 1))
		o.add(col(17), gosnmp.Counter32, tx(6, 0.00003))
		o.add(col(18), gosnmp.Counter32, tx(7, 0.00001))
		o.add(col(19), gosnmp.Counter32, tx(5, 1.0/900))
		o.add(col(20), gosnmp.Counter32, tx(5, 1.0/9000)) // retries, a tenth of a percent of packets
		o.add(col(21), gosnmp.Integer, Const(v.txPower))  // dBm
		o.add(col(22), gosnmp.Integer, Const(1))          // up: true
		o.add(col(23), gosnmp.OctetString, Const(v.usage))
	}
	// unifiIfTable: the wired interfaces, as the controller counts them.
	for i, f := range []iface{ifs[1], ifs[len(ifs)-1]} {
		row := i + 1
		col := func(c int) string { return fmt.Sprintf(unifi+"2.1.1.%d.%d", c, row) }
		n := 60 + uint64(i)*8
		ip := "0.0.0.0"
		if f.ifType == ifTypeBridge {
			ip = own.Addr().String()
		}
		o.add(col(1), gosnmp.Integer, Const(row))
		o.add(col(2), gosnmp.Integer, Const(1)) // full duplex
		o.add(col(3), gosnmp.IPAddress, Const(ip))
		o.add(col(4), gosnmp.OctetString, Const([]byte(f.mac)))
		o.add(col(5), gosnmp.OctetString, Const(f.descr))
		o.add(col(6), gosnmp.Counter32, Counter(f.inRate, start(n), swing(n+1, 0.6, 5*time.Minute)))
		o.add(col(7), gosnmp.Counter32, Const(uint32(0)))
		o.add(col(8), gosnmp.Counter32, Const(uint32(0)))
		o.add(col(9), gosnmp.Counter32, Counter(f.inRate/900*0.02, start(n+2), swing(n+1, 0.6, 5*time.Minute)))
		o.add(col(10), gosnmp.Counter32, Counter(f.inRate/900, start(n+3), swing(n+1, 0.6, 5*time.Minute)))
		o.add(col(11), gosnmp.Integer, Const(int(f.speed/1_000_000)))
		o.add(col(12), gosnmp.Counter32, Counter(f.outRate, start(n+4), swing(n+2, 0.5, 7*time.Minute)))
		o.add(col(13), gosnmp.Counter32, Const(uint32(0)))
		o.add(col(14), gosnmp.Counter32, Const(uint32(0)))
		o.add(col(15), gosnmp.Counter32, Counter(f.outRate/700, start(n+5), swing(n+2, 0.5, 7*time.Minute)))
		o.add(col(16), gosnmp.Integer, Const(1)) // up: true
	}
	// unifiApSystem
	o.add(unifi+"3.1.0", gosnmp.IPAddress, Const(own.Addr().String()))
	o.add(unifi+"3.2.0", gosnmp.Integer, Const(2)) // isolated: false
	o.add(unifi+"3.3.0", gosnmp.OctetString, Const("U6-Pro"))
	o.add(unifi+"3.4.0", gosnmp.OctetString, Const("eth0"))
	o.add(unifi+"3.5.0", gosnmp.Counter32, Counter(1, 0, Swing{})) // seconds up
	o.add(unifi+"3.6.0", gosnmp.OctetString, Const(version))

	const mem = 1_015_808
	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: mem,
		users:     [2]float64{0, 1},
		storage: []storageArea{
			{1, hrStorageRam, "Physical memory", 1024, mem, 0.41, 0.52, 25 * time.Minute},
			{6, hrStorageOther, "Memory buffers", 1024, mem, 0.01, 0.02, 30 * time.Minute},
			{7, hrStorageOther, "Cached memory", 1024, mem, 0.12, 0.16, 25 * time.Minute},
			{31, hrStorageFixedDisk, "/", 4096, 7_680, 0.61, 0.62, 12 * time.Hour},
		},
		processors: []int{196608, 196609, 196610, 196611},
		cpuLo:      2,
		cpuHi:      31,
		cpu:        "Qualcomm IPQ8074 (ARMv8)",
		devices: []hrDevice{
			{index: 262145, kind: hrDeviceNetwork, descr: "network interface lo", ifIndex: 1},
			{index: 262146, kind: hrDeviceNetwork, descr: "network interface eth0", ifIndex: 2},
			{index: 262153, kind: hrDeviceNetwork, descr: "network interface br0", ifIndex: 8},
		},
		fs:    []hrFS{{"/", hrFSOther, 31, true}},
		procs: unifiProcesses(),
	})
	addUCD(&o, id.Seed, ucdInfo{
		memKB: mem, memUsed: [2]float64{0.35, 0.45}, cpus: 4,
		load: [2]float64{0.2, 0.9}, user: [2]float64{2, 20}, system: [2]float64{1, 9},
		disks: []ucdDisk{{"/", "overlay", 30720, [2]float64{0.61, 0.62}}},
	})
	return o
}

// unifiProcesses is what a UniFi access point runs: busybox and Ubiquiti's
// daemons.
func unifiProcesses() []process {
	procs := []process{{pid: 1, name: "init", path: "/sbin/init", kind: 4, memKB: 1400}}
	for i, name := range []string{"kthreadd", "ksoftirqd/0", "kworker/0:0H", "migration/0", "migration/1",
		"ksoftirqd/1", "migration/2", "ksoftirqd/2", "migration/3", "ksoftirqd/3", "kswapd0", "cfg80211"} {
		procs = append(procs, process{pid: 2 + i, name: name, kind: 2})
	}
	pid := 812
	for _, s := range []struct {
		name, args string
		cpu        float64
		memKB      int
	}{
		{"ubnt-daemon", "", 0.001, 3200},
		{"syswrapper.sh", "", 0, 1100},
		{"mcad", "", 0.004, 8600},
		{"stamgr", "", 0.003, 7200},
		{"hostapd", "-B -P /run/hostapd.pid -g /run/hostapd/global", 0.012, 9800},
		{"wevent", "", 0.001, 2300},
		{"dropbear", "-F -r /etc/dropbear/dropbear_rsa_host_key -p 22", 0, 900},
		{"snmpd", "-Lsd -Lf /dev/null -p /var/run/snmpd.pid -c /etc/snmp/snmpd.conf", 0.003, 3900},
		{"lldpd", "-M 4", 0, 1600},
		{"utermd", "", 0, 2100},
		{"ubnt-protocol-support", "", 0.001, 2700},
		{"logread", "-f", 0, 800},
	} {
		procs = append(procs, process{pid: pid, name: s.name, path: "/usr/bin/" + s.name, args: s.args, kind: 4,
			cpu: s.cpu, memKB: s.memKB})
		pid += 9 + pid%13
	}
	return procs
}
