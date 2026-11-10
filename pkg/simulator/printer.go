package simulator

import (
	"fmt"
	"time"

	"github.com/gosnmp/gosnmp"
)

// hpLaserJet is an HP LaserJet on its Jetdirect card, answering the Printer MIB
// (RFC 3805) as a walk of one finds it: the general group, the front door, two
// paper trays and the output bin, the marker and the pages it has printed, the
// black cartridge running low and the alert that says so, the languages it
// reads, the console's message; the printer as a device in the host resources,
// and a Jetdirect's sockets — raw printing, LPD, IPP, the web interface.
var hpLaserJet = model{
	ModelInfo:  ModelInfo{ID: "hp-laserjet", Category: "printing"},
	enterprise: 11, // HP
	build:      buildHPLaserJet,
	notifications: []Notification{{
		Name: "printerV2Alert",
		OID:  ".1.3.6.1.2.1.43.18.2.0.1",
		// prtAlertIndex, …SeverityLevel, …Group, …GroupIndex, …Location, …Code
		Objects: []string{
			".1.3.6.1.2.1.43.18.1.1.1.1.1",
			".1.3.6.1.2.1.43.18.1.1.2.1.1",
			".1.3.6.1.2.1.43.18.1.1.4.1.1",
			".1.3.6.1.2.1.43.18.1.1.5.1.1",
			".1.3.6.1.2.1.43.18.1.1.6.1.1",
			".1.3.6.1.2.1.43.18.1.1.7.1.1",
		},
	}},
}

func buildHPLaserJet(id Identity) []Object {
	var o objects
	addSystem(&o, id, "HP ETHERNET MULTI-ENVIRONMENT,ROM none,JETDIRECT,JD153,EEPROM JSI23900051,CIDATE 06/17/2019",
		".1.3.6.1.4.1.11.2.3.9.1", // hpNetPrinter
		"helpdesk@example.com", "Floor 1, copy room", 72)
	lan := lanPrefix(id.Seed)
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			{index: 1, descr: "HP ETHERNET MULTI-ENVIRONMENT", ifType: ifTypeEthernet, mtu: 1500,
				speed: 1_000_000_000, mac: deviceMAC(id.Seed, 1), up: true, inRate: 1_500, outRate: 900},
		},
		addrs:   []ifAddr{{ifIndex: 1, prefix: addrIn(lan, 200+int(id.Seed%20))}},
		gateway: hostIn(lan, 1),
		pps:     12,
		listen:  []uint16{80, 443, 515, 631, 9100},
		udp:     []uint16{161, 427, 5353},
		modules: []sysOR{moduleHostResources},
	})

	// Host resources: the printer as a device, its memory, and nothing running
	// that a printer would say.
	const model = "HP LaserJet Pro M404dn"
	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: 524288, // 512 MiB
		storage: []storageArea{
			{1, hrStorageRam, "RAM", 1024, 524288, 0.38, 0.44, 30 * time.Minute},
			{2, hrStorageFlashMemory, "Flash", 1024, 4194304, 0.21, 0.21, 24 * time.Hour},
		},
		devices: []hrDevice{{index: 1, kind: hrDevicePrinter, descr: model}},
		boot:    1,
	})

	const prt = "1.3.6.1.2.1.43."
	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 40000 + n} }
	// prtGeneralTable
	o.add(prt+"5.1.1.1.1", gosnmp.Counter32, Const(uint32(3)))
	o.add(prt+"5.1.1.2.1", gosnmp.Integer, Const(1))
	o.add(prt+"5.1.1.4.1", gosnmp.OctetString, Const(""))
	o.add(prt+"5.1.1.5.1", gosnmp.OctetString, Const(""))
	o.add(prt+"5.1.1.16.1", gosnmp.OctetString, Const(model))
	o.add(prt+"5.1.1.17.1", gosnmp.OctetString, Const(fmt.Sprintf("PHB%07d", int(unit(id.Seed+41)*1e7))))
	// prtCoverTable: the front door, closed.
	o.add(prt+"6.1.1.2.1.1", gosnmp.OctetString, Const("Front Door"))
	o.add(prt+"6.1.1.3.1.1", gosnmp.Integer, Const(4)) // coverClosed
	// prtInputTable: the manual feed and the cassette, A4 in micrometres.
	for n, t := range []struct {
		name      string
		kind, max int
		level     [2]float64
	}{
		{"Tray 1", 4, 100, [2]float64{0, 0}},    // sheetFeedManual, empty
		{"Tray 2", 3, 250, [2]float64{60, 230}}, // sheetFeedAutoRemovableTray
	} {
		col := func(c int) string { return fmt.Sprintf(prt+"8.2.1.%d.1.%d", c, n+1) }
		o.add(col(2), gosnmp.Integer, Const(t.kind))
		o.add(col(3), gosnmp.Integer, Const(4)) // micrometers
		o.add(col(4), gosnmp.Integer, Const(297000))
		o.add(col(5), gosnmp.Integer, Const(210000))
		o.add(col(8), gosnmp.Integer, Const(8)) // sheets
		o.add(col(9), gosnmp.Integer, Const(t.max))
		o.add(col(10), gosnmp.Integer, IntegerGauge(t.level[0], t.level[1], swing(10+uint64(n), 3*time.Hour)))
		o.add(col(11), gosnmp.Integer, Const(0)) // available and idle
		o.add(col(12), gosnmp.OctetString, Const("A4"))
		o.add(col(13), gosnmp.OctetString, Const(t.name))
		o.add(col(15), gosnmp.OctetString, Const(""))
		o.add(col(18), gosnmp.OctetString, Const(t.name))
	}
	// prtOutputTable: the face-down bin.
	out := func(c int) string { return fmt.Sprintf(prt+"9.2.1.%d.1.1", c) }
	o.add(out(2), gosnmp.Integer, Const(4)) // unRemovableBin
	o.add(out(3), gosnmp.Integer, Const(8)) // sheets
	o.add(out(4), gosnmp.Integer, Const(150))
	o.add(out(5), gosnmp.Integer, IntegerGauge(90, 150, swing(20, 2*time.Hour)))
	o.add(out(6), gosnmp.Integer, Const(0))
	o.add(out(7), gosnmp.OctetString, Const("Face Down Bin"))
	// The marker, and the pages it has printed: about fourteen an hour, and
	// fewer since it was last switched on.
	pages := Swing{Depth: 0.9, Period: 2 * time.Hour, Seed: id.Seed + 40000}
	o.add(prt+"10.2.1.2.1.1", gosnmp.Integer, Const(4)) // electrophotographicLaser
	o.add(prt+"10.2.1.3.1.1", gosnmp.Integer, Const(7)) // counted in impressions
	o.add(prt+"10.2.1.4.1.1", gosnmp.Counter32, Counter(0.004, uint64(18_000+40_000*unit(id.Seed+42)), pages))
	o.add(prt+"10.2.1.5.1.1", gosnmp.Counter32, Counter(0.004, 0, pages))
	o.add(prt+"10.2.1.6.1.1", gosnmp.Integer, Const(1))  // one colorant
	o.add(prt+"10.2.1.15.1.1", gosnmp.Integer, Const(0)) // prtMarkerStatus: available and idle
	// The black cartridge, at 17 to 18 percent.
	supply := func(c int) string { return fmt.Sprintf(prt+"11.1.1.%d.1.1", c) }
	o.add(supply(1), gosnmp.Integer, Const(1))
	o.add(supply(2), gosnmp.Integer, Const(1))
	o.add(supply(3), gosnmp.Integer, Const(1))
	o.add(supply(4), gosnmp.Integer, Const(3)) // supplyThatIsConsumed
	o.add(supply(5), gosnmp.Integer, Const(3)) // toner
	o.add(supply(6), gosnmp.OctetString, Const("Black Cartridge HP 59A"))
	o.add(supply(7), gosnmp.Integer, Const(19)) // in percent
	o.add(supply(8), gosnmp.Integer, Const(100))
	o.add(supply(9), gosnmp.Integer, IntegerGauge(17, 18, Swing{Period: 6 * time.Hour, Seed: id.Seed + 40001}))
	// prtMarkerColorantTable: black, which the marker uses.
	o.add(prt+"12.1.1.3.1.1", gosnmp.Integer, Const(3)) // process
	o.add(prt+"12.1.1.4.1.1", gosnmp.OctetString, Const("black"))
	// prtInterpreterTable: the languages the printer reads.
	for n, l := range []struct {
		family         int
		version, descr string
	}{
		{5, "", "PJL"},
		{3, "6", "PCL 6"},
		{47, "3.0", "PCL XL"},
		{6, "3", "PostScript 3 emulation"},
		{54, "1.7", "PDF"},
	} {
		col := func(c int) string { return fmt.Sprintf(prt+"15.1.1.%d.1.%d", c, n+1) }
		o.add(col(2), gosnmp.Integer, Const(l.family))
		o.add(col(3), gosnmp.OctetString, Const(""))
		o.add(col(4), gosnmp.OctetString, Const(l.version))
		o.add(col(5), gosnmp.OctetString, Const(l.descr))
	}
	// prtConsoleDisplayBufferTable: what the panel says.
	o.add(prt+"16.5.1.2.1.1", gosnmp.OctetString, Const("Ready"))
	// The alert the cartridge raised, which printerV2Alert carries.
	alert := func(c int) string { return fmt.Sprintf(prt+"18.1.1.%d.1.1", c) }
	o.add(alert(1), gosnmp.Integer, Const(1))
	o.add(alert(2), gosnmp.Integer, Const(4))    // severity: warning
	o.add(alert(3), gosnmp.Integer, Const(4))    // training: trained
	o.add(alert(4), gosnmp.Integer, Const(11))   // group: markerSupplies
	o.add(alert(5), gosnmp.Integer, Const(1))    // the supply's index
	o.add(alert(6), gosnmp.Integer, Const(-2))   // location: unknown
	o.add(alert(7), gosnmp.Integer, Const(1104)) // markerTonerAlmostEmpty
	o.add(alert(8), gosnmp.OctetString, Const("Black cartridge low"))
	o.add(alert(9), gosnmp.TimeTicks, Const(uint32(0)))
	return o
}
