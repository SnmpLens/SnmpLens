package simulator

import (
	"fmt"
	"math"
	"time"

	"github.com/gosnmp/gosnmp"
)

// apcSmartUPS is a three-phase APC Smart-UPS on its network management card,
// answering the standard UPS-MIB (RFC 1628) — the MIB the "UPS (RFC 1628)"
// preset polls, three output lines included, which is why it is the
// three-phase model — and APC's PowerNet-MIB beside it, which most tools read
// an APC by. The two say the same things, from the same readings, each in its
// own units: minutes remaining and a TimeTicks run time, volts and tenths of a
// volt. It runs on mains, charged.
var apcSmartUPS = model{
	ModelInfo:  ModelInfo{ID: "apc-smart-ups", Category: "power"},
	enterprise: 318, // APC
	build:      buildAPCSmartUPS,
	notifications: []Notification{{
		Name: "upsTrapOnBattery",
		OID:  ".1.3.6.1.2.1.33.2.1",
		// upsEstimatedMinutesRemaining, upsSecondsOnBattery, upsConfigLowBattTime
		Objects: []string{".1.3.6.1.2.1.33.1.2.3.0", ".1.3.6.1.2.1.33.1.2.2.0", ".1.3.6.1.2.1.33.1.9.7.0"},
	}},
}

// scaled is what r reads times factor, as a figure of type t: the same reading
// in other units.
func scaled(t gosnmp.Asn1BER, r Reading, factor float64) Reading {
	return derived{typ: t, f: func(c clock) any { return as(t, math.Round(number(r, c)*factor)) }}
}

func buildAPCSmartUPS(id Identity) []Object {
	var o objects
	serial := fmt.Sprintf("5A%04dT%05d", 2100+int(unit(id.Seed+1)*4), int(unit(id.Seed+2)*100000))
	addSystem(&o, id, "APC Web/SNMP Management Card (MB:v4.1.0 PF:v6.9.6 PN:apc_hw05_aos_696.bin AF1:v6.9.6 "+
		"AN1:apc_hw05_sumx_696.bin MN:SUVTP10KH3B4S HR:05 SN: "+serial+" MD:03/14/2021) "+
		"(Embedded PowerNet SNMP Agent SW v2.2 compatible)",
		".1.3.6.1.4.1.318.1.3.17.1", // smartUPS3Phase10kVA
		"facilities@example.com", "Server room, UPS bay", 72)
	lan := lanPrefix(id.Seed)
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			{index: 1, descr: "eth0", ifType: ifTypeEthernet, mtu: 1500, speed: 100_000_000,
				mac: deviceMAC(id.Seed, 1), up: true, inRate: 400, outRate: 700},
		},
		addrs:   []ifAddr{{ifIndex: 1, prefix: addrIn(lan, 230+int(id.Seed%10))}},
		gateway: hostIn(lan, 1),
		pps:     8,
		listen:  []uint16{21, 22, 80, 443},
		udp:     []uint16{161},
	})

	const ups = "1.3.6.1.2.1.33.1."
	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 20000 + n} }
	minutes := IntegerGauge(38, 44, swing(0, 20*time.Minute))
	charge := IntegerGauge(99, 100, swing(1, 40*time.Minute))
	volts := IntegerGauge(2180, 2200, swing(2, 15*time.Minute)) // tenths of a volt
	celsius := IntegerGauge(24, 27, swing(3, 3*time.Hour))
	// upsIdent
	o.add(ups+"1.1.0", gosnmp.OctetString, Const("APC"))
	o.add(ups+"1.2.0", gosnmp.OctetString, Const("Smart-UPS VT 10kVA"))
	o.add(ups+"1.3.0", gosnmp.OctetString, Const("UPS 09.1 / ID=1015"))
	o.add(ups+"1.4.0", gosnmp.OctetString, Const("v6.9.6"))
	o.add(ups+"1.5.0", gosnmp.OctetString, Const(id.Name))
	o.add(ups+"1.6.0", gosnmp.OctetString, Const("Racks A1 to A4"))
	// upsBattery
	o.add(ups+"2.1.0", gosnmp.Integer, Const(2)) // upsBatteryStatus: normal
	o.add(ups+"2.2.0", gosnmp.Integer, Const(0)) // upsSecondsOnBattery
	o.add(ups+"2.3.0", gosnmp.Integer, minutes)
	o.add(ups+"2.4.0", gosnmp.Integer, charge)
	o.add(ups+"2.5.0", gosnmp.Integer, volts)
	o.add(ups+"2.6.0", gosnmp.Integer, Const(0)) // 0.1 A: not discharging
	o.add(ups+"2.7.0", gosnmp.Integer, celsius)
	// upsInput, one line per phase
	inVolts := make([]Reading, 3)
	o.add(ups+"3.1.0", gosnmp.Counter32, Const(uint32(0))) // upsInputLineBads
	o.add(ups+"3.2.0", gosnmp.Integer, Const(3))
	for line := 1; line <= 3; line++ {
		n := 10 + uint64(line)*8
		col := func(c int) string { return fmt.Sprintf(ups+"3.3.1.%d.%d", c, line) }
		inVolts[line-1] = IntegerGauge(228, 234, swing(n+1, 9*time.Minute))
		o.add(col(1), gosnmp.Integer, Const(line))
		o.add(col(2), gosnmp.Integer, IntegerGauge(499, 501, swing(n, 3*time.Minute)))     // 0.1 Hz
		o.add(col(3), gosnmp.Integer, inVolts[line-1])                                     // V RMS
		o.add(col(4), gosnmp.Integer, IntegerGauge(95, 140, swing(n+2, 7*time.Minute)))    // 0.1 A RMS
		o.add(col(5), gosnmp.Integer, IntegerGauge(2100, 3100, swing(n+3, 7*time.Minute))) // W
	}
	// upsOutput, one line per phase, each loaded differently
	loads := make([]Reading, 3)
	o.add(ups+"4.1.0", gosnmp.Integer, Const(3)) // upsOutputSource: normal, which is mains
	o.add(ups+"4.2.0", gosnmp.Integer, Const(500))
	o.add(ups+"4.3.0", gosnmp.Integer, Const(3))
	for i, load := range [][2]float64{{34, 42}, {41, 48}, {28, 37}} {
		line := i + 1
		n := 50 + uint64(line)*8
		col := func(c int) string { return fmt.Sprintf(ups+"4.4.1.%d.%d", c, line) }
		loads[i] = IntegerGauge(load[0], load[1], swing(n+2, 6*time.Minute))
		o.add(col(1), gosnmp.Integer, Const(line))
		o.add(col(2), gosnmp.Integer, Const(230))
		o.add(col(3), gosnmp.Integer, IntegerGauge(80+load[0], 80+load[1], swing(n, 6*time.Minute)))   // 0.1 A RMS
		o.add(col(4), gosnmp.Integer, IntegerGauge(load[0]*55, load[1]*55, swing(n+1, 6*time.Minute))) // W
		o.add(col(5), gosnmp.Integer, loads[i])                                                        // %
	}
	o.add(ups+"6.1.0", gosnmp.Gauge32, Const(uint32(0))) // upsAlarmsPresent
	// upsConfig: 230 V and 50 Hz in and out, 10 kVA, 8 kW, two minutes' warning
	// of a low battery, the alarm on, and the transfer points.
	for i, v := range []int{230, 50, 230, 50, 10000, 8000, 2, 2, 160, 282} {
		o.add(fmt.Sprintf(ups+"9.%d.0", i+1), gosnmp.Integer, Const(v))
	}

	// PowerNet-MIB's upsIdent, upsBattery, upsInput and upsOutput.
	const pn = "1.3.6.1.4.1.318.1.1.1."
	averageLoad := derived{typ: gosnmp.Gauge32, f: func(c clock) any {
		return uint32(math.Round((number(loads[0], c) + number(loads[1], c) + number(loads[2], c)) / 3))
	}}
	o.add(pn+"1.1.1.0", gosnmp.OctetString, Const("Smart-UPS VT 10kVA"))
	o.add(pn+"1.1.2.0", gosnmp.OctetString, Const(id.Name))
	o.add(pn+"1.2.1.0", gosnmp.OctetString, Const("UPS 09.1 / ID=1015"))
	o.add(pn+"1.2.2.0", gosnmp.OctetString, Const("03/14/2021"))
	o.add(pn+"1.2.3.0", gosnmp.OctetString, Const(serial))
	o.add(pn+"1.2.5.0", gosnmp.OctetString, Const("SUVTP10KH3B4S"))
	o.add(pn+"1.2.6.0", gosnmp.OctetString, Const("05"))
	o.add(pn+"2.1.1.0", gosnmp.Integer, Const(2))                // upsBasicBatteryStatus: normal
	o.add(pn+"2.1.2.0", gosnmp.TimeTicks, Const(uint32(0)))      // upsBasicBatteryTimeOnBattery
	o.add(pn+"2.1.3.0", gosnmp.OctetString, Const("09/02/2021")) // upsBasicBatteryLastReplaceDate
	o.add(pn+"2.2.1.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, charge, 1))
	o.add(pn+"2.2.2.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, celsius, 1))
	o.add(pn+"2.2.3.0", gosnmp.TimeTicks, scaled(gosnmp.TimeTicks, minutes, 6000)) // run time, in hundredths
	o.add(pn+"2.2.4.0", gosnmp.Integer, Const(1))                                  // no battery needs replacing
	o.add(pn+"2.2.5.0", gosnmp.Integer, Const(2))
	o.add(pn+"2.2.6.0", gosnmp.Integer, Const(0))
	o.add(pn+"2.2.7.0", gosnmp.Integer, Const(216))
	o.add(pn+"2.2.8.0", gosnmp.Integer, scaled(gosnmp.Integer, volts, 0.1))
	o.add(pn+"2.3.1.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, charge, 10))
	o.add(pn+"2.3.2.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, celsius, 10))
	o.add(pn+"2.3.3.0", gosnmp.Integer, Const(2160))
	o.add(pn+"2.3.4.0", gosnmp.Integer, volts)
	o.add(pn+"3.1.1.0", gosnmp.Integer, Const(3))                                // upsBasicInputPhase
	o.add(pn+"3.2.1.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, inVolts[0], 1))   // upsAdvInputLineVoltage
	o.add(pn+"3.2.2.0", gosnmp.Gauge32, Const(uint32(236)))                      // highest in the last minute
	o.add(pn+"3.2.3.0", gosnmp.Gauge32, Const(uint32(226)))                      // lowest
	o.add(pn+"3.2.4.0", gosnmp.Gauge32, Const(uint32(50)))                       // Hz
	o.add(pn+"3.2.5.0", gosnmp.Integer, Const(1))                                // no transfer
	o.add(pn+"3.3.1.0", gosnmp.Gauge32, scaled(gosnmp.Gauge32, inVolts[0], 10))  // tenths of a volt
	o.add(pn+"3.3.4.0", gosnmp.Gauge32, Const(uint32(500)))                      // tenths of a hertz
	o.add(pn+"4.1.1.0", gosnmp.Integer, Const(2))                                // upsBasicOutputStatus: onLine
	o.add(pn+"4.1.2.0", gosnmp.Integer, Const(3))                                // upsBasicOutputPhase
	o.add(pn+"4.2.1.0", gosnmp.Gauge32, Const(uint32(230)))                      // upsAdvOutputVoltage
	o.add(pn+"4.2.2.0", gosnmp.Gauge32, Const(uint32(50)))                       // upsAdvOutputFrequency
	o.add(pn+"4.2.3.0", gosnmp.Gauge32, averageLoad)                             // upsAdvOutputLoad, %
	o.add(pn+"4.2.4.0", gosnmp.Gauge32, Gauge(12, 16, swing(90, 6*time.Minute))) // upsAdvOutputCurrent, A
	return o
}
