package simulator

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// dellIDRAC9 is the iDRAC9 of a Dell PowerEdge R750: the controller a server is
// watched through out of band, whatever its operating system is doing.
// IDRAC-MIB gives the server's global and storage status, its power state and
// how long it has been on, its BIOS, its two processors and eight memory
// modules, its two power supplies, its temperatures, fans and power
// consumption, and its storage — the RAID controller, four disks and the two
// virtual disks made of them. Its alert is one of the iDRAC's, carrying the
// eleven objects every iDRAC alert carries — accessible-for-notify, so no
// request reads them.
var dellIDRAC9 = model{
	ModelInfo:  ModelInfo{ID: "dell-idrac9", Category: "server"},
	enterprise: 674, // Dell
	build:      buildDellIDRAC9,
	notifications: []Notification{{
		Name:    "alertTemperatureProbeWarning",
		OID:     "." + idrac + "3.2.1.0.2162",
		Objects: idracAlertObjects(),
	}},
}

// idrac is IDRAC-MIB's outOfBandGroup.
const idrac = "1.3.6.1.4.1.674.10892.5."

// idracAlertObjects are alertVariablesGroup, in the MIB's order: message ID,
// message, status, service tag, FQDN, FQDD, display name, arguments, chassis
// service tag, chassis name, and the controller's own FQDN.
func idracAlertObjects() []string {
	out := make([]string, 11)
	for i := range out {
		out[i] = fmt.Sprintf(".%s3.1.%d.0", idrac, i+1)
	}
	return out
}

// serviceTag is a Dell service tag drawn from a seed: seven characters, with
// no vowel to spell anything.
func serviceTag(seed uint64) string {
	const alphabet = "0123456789BCDFGHJKLMNPQRSTVWXYZ"
	b := make([]byte, 7)
	h := mix(seed)
	for i := range b {
		b[i] = alphabet[h%uint64(len(alphabet))]
		h /= uint64(len(alphabet))
	}
	return string(b)
}

func buildDellIDRAC9(id Identity) []Object {
	var o objects
	tag := serviceTag(id.Seed + 5)
	// The server's own name, which is not the controller's.
	fqdn := "r750-" + strings.ToLower(tag) + ".example.com"
	addSystem(&o, id, "Dell Out-of-band SNMP Agent for Remote Access Controller", ".1.3.6.1.4.1.674.10892.5",
		"it@example.com", "Server room, rack B3", 72)
	lan := lanPrefix(id.Seed)
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			{index: 1, descr: "eth0", alias: "iDRAC dedicated port", ifType: ifTypeEthernet, mtu: 1500,
				speed: 1_000_000_000, mac: deviceMAC(id.Seed, 1), up: true, inRate: 6_000, outRate: 9_000},
		},
		addrs:    []ifAddr{{ifIndex: 1, prefix: addrIn(lan, 30+int(id.Seed%20)), neighbours: 2}},
		gateway:  hostIn(lan, 1),
		pps:      40,
		listen:   []uint16{22, 80, 443, 5900},
		udp:      []uint16{161, 623},
		sessions: []session{{443, netip.AddrPortFrom(hostIn(lan, 50), 55301)}}, // someone in the web interface
	})

	// racInfoGroup and systemInfoGroup
	o.add(idrac+"1.1.1.0", gosnmp.OctetString, Const("Integrated Dell Remote Access Controller"))
	o.add(idrac+"1.1.2.0", gosnmp.OctetString, Const("iDRAC"))
	o.add(idrac+"1.1.7.0", gosnmp.Integer, Const(48)) // racType: idrac9Monolithic
	o.add(idrac+"1.1.8.0", gosnmp.OctetString, Const("7.00.60.00"))
	o.add(idrac+"1.3.1.0", gosnmp.OctetString, Const(fqdn))
	o.add(idrac+"1.3.2.0", gosnmp.OctetString, Const(tag))
	o.add(idrac+"1.3.12.0", gosnmp.OctetString, Const("PowerEdge R750"))
	// statusGroup: everything ok, the server on — since the device started.
	o.add(idrac+"2.1.0", gosnmp.Integer, Const(3))    // globalSystemStatus: ok
	o.add(idrac+"2.2.0", gosnmp.Integer, Const(3))    // systemLCDStatus: ok
	o.add(idrac+"2.3.0", gosnmp.Integer, Const(3))    // globalStorageStatus: ok
	o.add(idrac+"2.4.0", gosnmp.Integer, Const(4))    // systemPowerState: on
	o.add(idrac+"2.5.0", gosnmp.Gauge32, SecondsUp()) // systemPowerUpTime: an Unsigned32, seconds

	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 80000 + n} }
	// A table of systemDetailsGroup is indexed by the chassis, then the item.
	row := func(table string, n int) func(c int) string {
		return func(c int) string { return fmt.Sprintf(idrac+"4.%s.1.%d.1.%d", table, c, n) }
	}
	chassis := func(col func(int) string, n, status int) {
		o.add(col(1), gosnmp.Integer, Const(1))
		o.add(col(2), gosnmp.Integer, Const(n))
		o.add(col(5), gosnmp.Integer, Const(status))
	}

	// systemBIOSTable
	bios := row("300.50", 1)
	chassis(bios, 1, 3)
	o.add(bios(7), gosnmp.OctetString, Const("20240513000000.000000+000"))
	o.add(bios(8), gosnmp.OctetString, Const("1.13.2"))
	o.add(bios(11), gosnmp.OctetString, Const("Dell Inc."))

	// A probe says where it is and reads a value; the status is ok(3) throughout.
	probe := func(table string, n int, name string, reading Reading, kind int) {
		col := row(table, n)
		chassis(col, n, 3)
		o.add(col(6), gosnmp.Integer, reading)
		if kind != 0 {
			o.add(col(7), gosnmp.Integer, Const(kind))
		}
		o.add(col(8), gosnmp.OctetString, Const(name))
	}
	// temperatureProbeTable, in tenths of a degree
	for i, p := range []struct {
		name   string
		lo, hi float64
	}{
		{"System Board Inlet Temp", 210, 245},
		{"System Board Exhaust Temp", 330, 385},
		{"CPU1 Temp", 480, 620},
		{"CPU2 Temp", 470, 610},
	} {
		probe("700.20", i+1, p.name, IntegerGauge(p.lo, p.hi, swing(uint64(i), 20*time.Minute)), 0)
	}
	// coolingDeviceTable: six fans, in RPM
	for n := 1; n <= 6; n++ {
		probe("700.12", n, fmt.Sprintf("System Board Fan%d", n), IntegerGauge(5280, 6960, swing(10+uint64(n), 15*time.Minute)), 0)
	}
	// amperageProbeTable: each supply's current in tenths of an amp
	// (amperageProbeTypeIsPowerSupplyAmps), and what the server draws in watts
	// (amperageProbeTypeIsSystemWatts) — one swing, since they are one load.
	load := swing(20, 10*time.Minute)
	probe("600.30", 1, "PS1 Current 1", IntegerGauge(8, 14, load), 23)
	probe("600.30", 2, "PS2 Current 2", IntegerGauge(8, 13, load), 23)
	probe("600.30", 3, "System Board Pwr Consumption", IntegerGauge(294, 462, load), 26)
	// powerSupplyTable: two 1400 W supplies, sharing the load.
	for n := 1; n <= 2; n++ {
		col := row("600.12", n)
		chassis(col, n, 3)
		o.add(col(6), gosnmp.Integer, Const(14000)) // tenths of a watt
		o.add(col(8), gosnmp.OctetString, Const(fmt.Sprintf("PS%d Status", n)))
		o.add(col(9), gosnmp.Integer, Const(240))
		o.add(col(15), gosnmp.OctetString, Const(fmt.Sprintf("PSU.Slot.%d", n)))
		o.add(col(16), gosnmp.Integer, IntegerGauge(228, 233, swing(30+uint64(n), 40*time.Minute)))
	}
	// processorDeviceTable: two sockets.
	for n := 1; n <= 2; n++ {
		col := row("1100.30", n)
		chassis(col, n, 3)
		o.add(col(7), gosnmp.Integer, Const(3)) // a central processor
		o.add(col(8), gosnmp.OctetString, Const("Intel"))
		o.add(col(11), gosnmp.Gauge32, Const(uint32(4000))) // MHz
		o.add(col(12), gosnmp.Gauge32, Const(uint32(2000)))
		o.add(col(17), gosnmp.Gauge32, Const(uint32(28)))
		o.add(col(19), gosnmp.Gauge32, Const(uint32(56)))
		o.add(col(23), gosnmp.OctetString, Const("Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz"))
		o.add(col(26), gosnmp.OctetString, Const(fmt.Sprintf("CPU.Socket.%d", n)))
	}
	// memoryDeviceTable: eight 32 GiB DDR4 modules.
	for n := 1; n <= 8; n++ {
		slot := fmt.Sprintf("%c%d", 'A'+(n-1)/4, (n-1)%4+1)
		col := row("1100.50", n)
		chassis(col, n, 3)
		o.add(col(7), gosnmp.Integer, Const(26)) // DDR4
		o.add(col(8), gosnmp.OctetString, Const("DIMM.Socket."+slot))
		o.add(col(14), gosnmp.Gauge32, Const(uint32(33554432))) // KB
		o.add(col(15), gosnmp.Gauge32, Const(uint32(3200)))     // MT/s
		o.add(col(21), gosnmp.OctetString, Const("Samsung"))
		o.add(col(22), gosnmp.OctetString, Const("M393A4K40EB3-CWE"))
		o.add(col(23), gosnmp.OctetString, Const(fmt.Sprintf("%08X", uint32(mix(id.Seed+500+uint64(n))))))
		o.add(col(26), gosnmp.OctetString, Const("DIMM.Socket."+slot))
	}

	// storageDetailsGroup: the PERC controller, four disks, and the two
	// virtual disks made of them — a mirror for the system, RAID 5 for data.
	const storage = idrac + "5.1.20."
	ctl := func(c int) string { return fmt.Sprintf(storage+"130.1.1.%d.1", c) }
	o.add(ctl(1), gosnmp.Integer, Const(1))
	o.add(ctl(2), gosnmp.OctetString, Const("PERC H755 Front"))
	o.add(ctl(8), gosnmp.OctetString, Const("52.26.0-5179"))
	o.add(ctl(37), gosnmp.Integer, Const(3))
	o.add(ctl(38), gosnmp.Integer, Const(3))
	o.add(ctl(78), gosnmp.OctetString, Const("RAID.SL.3-1"))
	const diskMB = 1144064
	for n := 1; n <= 4; n++ {
		col := func(c int) string { return fmt.Sprintf(storage+"130.4.1.%d.%d", c, n) }
		used := diskMB * 9 / 10
		o.add(col(1), gosnmp.Integer, Const(n))
		o.add(col(2), gosnmp.OctetString, Const(fmt.Sprintf("Physical Disk 0:1:%d", n-1)))
		o.add(col(3), gosnmp.OctetString, Const("SEAGATE"))
		o.add(col(4), gosnmp.Integer, Const(3)) // online
		o.add(col(6), gosnmp.OctetString, Const("ST1200MM0099"))
		o.add(col(7), gosnmp.OctetString, Const(fmt.Sprintf("WFK%05d", int(unit(id.Seed+600+uint64(n))*1e5))))
		o.add(col(11), gosnmp.Integer, Const(diskMB))
		o.add(col(17), gosnmp.Integer, Const(used))
		o.add(col(19), gosnmp.Integer, Const(diskMB-used))
		o.add(col(24), gosnmp.Integer, Const(3))
		o.add(col(35), gosnmp.Integer, Const(2)) // a hard disk
		o.add(col(54), gosnmp.OctetString, Const(fmt.Sprintf("Disk.Bay.%d:Enclosure.Internal.0-1:RAID.SL.3-1", n-1)))
	}
	for n, v := range []struct {
		name   string
		sizeMB int
		layout int // r1(3), r5(4)
	}{{"OS", 228864, 3}, {"DATA", 3203584, 4}} {
		col := func(c int) string { return fmt.Sprintf(storage+"140.1.1.%d.%d", c, n+1) }
		o.add(col(1), gosnmp.Integer, Const(n+1))
		o.add(col(2), gosnmp.OctetString, Const(v.name))
		o.add(col(4), gosnmp.Integer, Const(2)) // online
		o.add(col(6), gosnmp.Integer, Const(v.sizeMB))
		o.add(col(13), gosnmp.Integer, Const(v.layout))
		o.add(col(20), gosnmp.Integer, Const(3))
		o.add(col(35), gosnmp.OctetString, Const(fmt.Sprintf("Disk.Virtual.%d:RAID.SL.3-1", n)))
	}

	// alertVariablesGroup: what alertTemperatureProbeWarning says.
	for i, v := range []struct {
		t gosnmp.Asn1BER
		r Reading
	}{
		{gosnmp.OctetString, Const("TMP0120")},
		{gosnmp.OctetString, Const("The system board inlet temperature is greater than the upper warning threshold.")},
		{gosnmp.Integer, Const(4)}, // nonCritical
		{gosnmp.OctetString, Const(tag)},
		{gosnmp.OctetString, Const(fqdn)},
		{gosnmp.OctetString, Const("iDRAC.Embedded.1#SystemBoardInletTemp")},
		{gosnmp.OctetString, Const("System Board Inlet Temp")},
		{gosnmp.OctetString, Const("System Board Inlet Temp")},
		{gosnmp.OctetString, Const(tag)},
		{gosnmp.OctetString, Const("Main System Chassis")},
		{gosnmp.OctetString, Const(id.Name)},
	} {
		o.addForNotify(fmt.Sprintf(idrac+"3.1.%d.0", i+1), v.t, v.r)
	}
	return o
}
