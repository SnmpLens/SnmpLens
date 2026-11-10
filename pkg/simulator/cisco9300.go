package simulator

import (
	"fmt"
	"net/netip"
	"slices"
)

// The Catalyst 9300-24T as a campus core: a layer-3 switch whose VLAN
// interfaces are the gateways of the bench's subnets (bench.go), in IPv4 and in
// IPv6, so that its ARP cache — IP-MIB's ipNetToPhysicalTable as well as the
// older ipNetToMediaTable, the IPv6 neighbours in the first — holds every
// station the access switches cabled to it learnt. That is what the port
// mapper reads to give a MAC its address.
//
// Its front ports start at ifIndex 9, after the management port: bridge port
// 1 is not ifIndex 1, and reading one as the other puts every station on the
// wrong port — a mistake the 2960 model, numbered from 1 for the drawing
// preset, could never show. Its uplinks keep the same indexes whatever the
// number of front ports, as the 2960's do. BRIDGE-MIB is answered per VLAN, as
// IOS answers it.
//
// Cabled to nothing, it has four servers on its first ports and its uplinks
// dark: it is the device the others are cabled to, not one that invents them.
var catalyst9300 = model{
	ModelInfo: ModelInfo{ID: "cisco-catalyst-9300", Category: "network",
		Params: []ModelParam{{Name: "ports", Min: 4, Max: 24, Default: 24}}},
	enterprise: 9,
	build:      buildCatalyst9300,
	notifications: []Notification{
		linkNotification(false, c9300Uplink(4)), // the spare uplink, dark
		linkNotification(true, c9300Front(1)),   // the first server
		ciscoConfigManEvent,
	},
	vlanContexts: true,
}

// c9300Front is the ifIndex of front port n, and c9300Uplink of uplink n.
func c9300Front(n int) int  { return 8 + n }
func c9300Uplink(n int) int { return 40 + n }

// The VLANs the core routes, and what each is for.
var c9300VLANs = []struct {
	id   int
	name string
}{{1, "default"}, {10, "USERS"}, {20, "PRINTERS"}, {30, "VOICE"}, {40, "SERVERS"}}

func buildCatalyst9300(id Identity) []Object {
	var o objects
	addSystem(&o, id, coreDescr+"\r\nTechnical Support: http://www.cisco.com/techsupport\r\n"+
		"Copyright (c) 1986-2023 by Cisco Systems, Inc.\r\nCompiled Fri 20-Oct-23 10:44 by mcpre",
		".1.3.6.1.4.1.9.1.2435",                   // ciscoCat930024T
		"noc@example.com", "Core room, rack A", 6) // a bridge that routes
	serial := fmt.Sprintf("FOC%04dX%03d", 2300+int(unit(id.Seed+81)*200), int(unit(id.Seed+82)*1000))
	n := id.count("ports", 24)

	ifs := []iface{{index: 1, descr: "GigabitEthernet0/0", name: "Gi0/0", alias: "out-of-band",
		ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, 1), adminDown: true}}
	var servers, users []int
	for p := 1; p <= n; p++ {
		f := iface{index: c9300Front(p), descr: fmt.Sprintf("GigabitEthernet1/0/%d", p), name: fmt.Sprintf("Gi1/0/%d", p),
			ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, uint64(8+p))}
		if p <= 4 {
			f.up, f.alias = true, fmt.Sprintf("srv-%02d", p)
			f.inRate = 400_000 + 2_000_000*unit(id.Seed+8100+uint64(p))
			f.outRate = 900_000 + 4_000_000*unit(id.Seed+8200+uint64(p))
			servers = append(servers, f.index)
		} else {
			users = append(users, f.index)
		}
		ifs = append(ifs, f)
	}
	var uplinks []int
	for u := 1; u <= 4; u++ {
		ifs = append(ifs, iface{index: c9300Uplink(u), descr: fmt.Sprintf("TenGigabitEthernet1/1/%d", u),
			name: fmt.Sprintf("Te1/1/%d", u), ifType: ifTypeEthernet, mtu: 1500, speed: 10_000_000_000,
			mac: deviceMAC(id.Seed, uint64(40+u))})
		uplinks = append(uplinks, c9300Uplink(u))
	}
	// A VLAN interface for each VLAN routed, with the switch's base MAC — the
	// one its engine ID and its LLDP chassis carry.
	var addrs, addrs6 []ifAddr
	var vtp []vtpVLAN
	for i, v := range c9300VLANs {
		index := 60 + i
		ifs = append(ifs, iface{index: index, descr: fmt.Sprintf("Vlan%d", v.id), name: fmt.Sprintf("Vl%d", v.id),
			ifType: ifTypePropVirtual, mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, 0), up: true,
			inRate: 60_000, outRate: 40_000})
		addrs = append(addrs, ifAddr{ifIndex: index, prefix: addrIn(benchPrefix(v.id), 1), neighbours: 4})
		addrs6 = append(addrs6, ifAddr{ifIndex: index, prefix: netip.PrefixFrom(host6In(benchPrefix6(v.id), 1), 64)})
		vtp = append(vtp, vtpVLAN{v.id, v.name, index})
	}

	// Every physical port is a bridge port, numbered by its place among them.
	var bridged []int
	for _, f := range ifs {
		if f.ifType == ifTypeEthernet && f.index != 1 {
			bridged = append(bridged, f.index)
		}
	}
	portOf := func(ifIndexes ...int) []int {
		out := make([]int, len(ifIndexes))
		for i, x := range ifIndexes {
			out[i] = slices.Index(bridged, x) + 1
		}
		return out
	}
	vlans := []vlanInfo{{id: 1, name: "default", untagged: portOf(uplinks...)}}
	for _, v := range c9300VLANs[1:] {
		info := vlanInfo{id: v.id, name: v.name, tagged: portOf(uplinks...)}
		switch v.id {
		case 10:
			info.untagged = portOf(users...)
		case 40:
			info.untagged = portOf(servers...)
		}
		vlans = append(vlans, info)
	}

	entity := []physical{{index: 1000, class: classChassis, relPos: -1, descr: "Cisco Catalyst 9300 Switch Stack",
		name: "Switch 1", model: "C9300-24T", serial: serial, hwRev: "V02", fwRev: "17.9.4a", swRev: "17.9.4a",
		mfg: "Cisco Systems, Inc."},
		{index: 1001, container: 1000, class: classPowerSupply, relPos: 1, descr: "Switch 1 - Power Supply A",
			name: "Switch 1 - Power Supply A", model: "PWR-C1-350WAC-P", mfg: "Cisco Systems, Inc.", fru: true},
		{index: 1002, container: 1000, class: classFan, relPos: 1, descr: "Switch 1 - FAN 1", name: "Switch 1 - FAN 1"},
		{index: 1003, container: 1000, class: classSensor, relPos: 1, descr: "Switch 1 - Inlet Temp Sensor",
			name: "Switch 1 - Inlet Temp Sensor"},
	}
	for k, f := range ifs {
		if f.ifType == ifTypeEthernet {
			entity = append(entity, physical{index: 1100 + k, container: 1000, class: classPort, relPos: k,
				descr: f.descr, name: f.ifName(), ifIndex: f.index})
		}
	}

	addStack(&o, id, stack{
		seed:        id.Seed,
		ifs:         ifs,
		addrs:       addrs,
		addrs6:      addrs6,
		gateway:     hostIn(benchPrefix(1), 254), // the edge router
		router:      true,
		ttl:         255,
		pps:         400,
		listen:      []uint16{22, 443},
		udp:         []uint16{123, 161},
		sessions:    []session{{22, netip.AddrPortFrom(hostIn(benchPrefix(40), 50), 51234)}},
		entity:      entity,
		physicalARP: true,
		bridge: &bridgeInfo{
			ports: bridged, vlans: vlans,
			stations: 1, perVLAN: true,
		},
		lldp: &lldpInfo{name: id.Name, descr: coreDescr, caps: capBridge | capRouter},
	})
	addCiscoCPU(&o, id.Seed, 1000, 4, 18, 8_000_000)
	addCiscoMemory(&o, id.Seed, []memPool{
		{1, "Processor", 1_582_000_000, [2]float64{0.31, 0.34}},
		{2, "reserve Processor", 102_000, [2]float64{0.01, 0.01}},
	})
	addCiscoEnv(&o, id.Seed, []envTemp{{"Switch 1 - Inlet Temp Sensor", 28, 33, 56}},
		[]string{"Switch 1 - FAN 1"}, []string{"Switch 1 - Power Supply A"})
	addCDP(&o, id.Name, cdpOf(id, ifs, nil))
	addVTP(&o, "CAMPUS", vtp)
	addCiscoConfigHistory(&o)
	return o
}
