package simulator

import (
	"fmt"
	"net"
	"net/netip"
)

// The Aruba 2930F-24G-4SFP+ (JL253A), an access switch that is not a Cisco: its
// ports are named by their number, it answers Q-BRIDGE-MIB's dot1qTpFdbTable —
// the VLAN of every station, which BRIDGE-MIB cannot say — and it speaks LLDP and
// no CDP. Its VLANs are a users' one on the first sixteen ports and a voice one
// on the next eight, where a desk phone sits; its SFP+ ports are trunks, the
// first towards the core. Its bridge ports are its port numbers, which is why
// the model has no fewer than its 24: a bridge port is numbered by its place.
var aruba2930F = model{
	ModelInfo:  ModelInfo{ID: "aruba-2930f-24", Category: "network"},
	enterprise: 11, // Hewlett Packard, whose arc Aruba's switches are under
	build:      buildAruba2930F,
	notifications: []Notification{
		linkNotification(false, 28), // the last SFP+ port, dark
		linkNotification(true, 25),  // the uplink
	},
}

const arubaDescr = "Aruba JL253A 2930F-24G-4SFP+ Switch, revision WC.16.11.0012, ROM WC.16.01.0010 (/ws/swbuildm/rel_tacoma_qaoff/code/build/lvm(swbuildm_rel_tacoma_qaoff_rel_tacoma))"

// arubaVoice is the first port of the voice VLAN, and the phone on it.
const arubaVoice = 17

func buildAruba2930F(id Identity) []Object {
	var o objects
	addSystem(&o, id, arubaDescr, ".1.3.6.1.4.1.11.2.3.7.11.181.18", // arubaSwitchJL253A
		"noc@example.com", "Wiring closet, floor 3", 2)
	lan := lanPrefix(id.Seed)
	const n = 24
	serial := fmt.Sprintf("CN%02dHKX%03d", 80+int(unit(id.Seed+91)*19), int(unit(id.Seed+92)*1000))

	var ifs []iface
	var users, voice []int
	for p := 1; p <= n; p++ {
		f := iface{index: p, descr: fmt.Sprint(p), ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
			mac: deviceMAC(id.Seed, uint64(p))}
		switch r := unit(id.Seed + 9000 + uint64(p)); {
		case p == arubaVoice:
			f.up, f.alias, f.inRate, f.outRate = true, "desk 3-17 phone", 14_000, 28_000
		case r < 0.05:
			f.adminDown = true
		case r < 0.6:
			f.up, f.alias = true, fmt.Sprintf("desk 3-%02d", p)
			f.inRate = 5_000 + 140_000*unit(id.Seed+9100+uint64(p))
			f.outRate = 9_000 + 320_000*unit(id.Seed+9200+uint64(p))
		}
		if p < arubaVoice {
			users = append(users, p)
		} else {
			voice = append(voice, p)
		}
		ifs = append(ifs, f)
	}
	var trunks []int
	for p := 25; p <= 28; p++ {
		f := iface{index: p, descr: fmt.Sprint(p), ifType: ifTypeEthernet, mtu: 1500, speed: 10_000_000_000,
			mac: deviceMAC(id.Seed, uint64(p))}
		if p == 25 {
			f.up, f.alias, f.inRate, f.outRate = true, "uplink core-sw-01", 5_200_000, 2_100_000
		}
		ifs = append(ifs, f)
		trunks = append(trunks, p)
	}
	// The management address is on the default VLAN's interface, which an
	// ArubaOS switch numbers after its ports.
	ifs = append(ifs, iface{index: 4097, descr: "DEFAULT_VLAN", ifType: ifTypePropVirtual, mtu: 1500,
		speed: 1_000_000_000, mac: deviceMAC(id.Seed, 0), up: true, inRate: 1_500, outRate: 1_200})

	// The ports are the bridge's ports, and numbered as the interfaces are.
	var bridged []int
	for _, f := range ifs {
		if f.ifType == ifTypeEthernet {
			bridged = append(bridged, f.index)
		}
	}
	vlans := []vlanInfo{{id: 1, name: "DEFAULT_VLAN", untagged: trunks}, {id: 10, name: "USERS", untagged: users,
		tagged: trunks}}
	vlans = append(vlans, vlanInfo{id: 30, name: "VOICE", untagged: voice, tagged: trunks})

	// Cabled to nothing, it hangs from the core every simulated access switch
	// hangs from, and a desk phone is plugged into its first voice port.
	phoneMAC, pcMAC := peerMAC(id.Seed, 904), peerMAC(id.Seed, 905)
	fixed := map[int][]net.HardwareAddr{25: {fictionalCoreChassis}}
	invented := []neighbour{{ifIndex: 25, name: fictionalCoreName, descr: coreDescr, port: "Te1/1/2",
		portDescr: "TenGigabitEthernet1/1/2", chassis: fictionalCoreChassis, caps: capBridge | capRouter,
		addr: hostIn(lan, 1)}}
	fixed[arubaVoice] = []net.HardwareAddr{phoneMAC, pcMAC}
	invented = append(invented, neighbour{ifIndex: arubaVoice, name: "phone-3-17", descr: phoneDescr,
		port: "eth0", portDescr: "eth0", chassis: phoneMAC, caps: capTelephone | capBridge})

	entity := []physical{{index: 1, class: classChassis, relPos: -1, descr: "Aruba JL253A 2930F-24G-4SFP+ Switch",
		name: "JL253A", model: "JL253A", serial: serial, hwRev: "", fwRev: "WC.16.01.0010", swRev: "WC.16.11.0012",
		mfg: "Aruba"}}
	for _, f := range ifs {
		if f.ifType == ifTypeEthernet {
			entity = append(entity, physical{index: 100 + f.index, container: 1, class: classPort, relPos: f.index,
				descr: f.descr, name: f.ifName(), ifIndex: f.index})
		}
	}

	addStack(&o, id, stack{
		seed:     id.Seed,
		ifs:      ifs,
		addrs:    []ifAddr{{ifIndex: 4097, prefix: addrIn(lan, 3+int(id.Seed%5)), neighbours: 3}},
		gateway:  hostIn(lan, 1),
		pps:      70,
		listen:   []uint16{22, 80},
		udp:      []uint16{161},
		sessions: []session{{22, netip.AddrPortFrom(hostIn(lan, 50), 57002)}},
		entity:   entity,
		bridge: &bridgeInfo{
			ports: bridged, vlans: vlans,
			stations: 1, uplinkStations: 30, uplink: 25, root: fictionalCoreChassis,
			fixed: fixed, qFDB: true,
		},
		lldp: &lldpInfo{name: id.Name, descr: arubaDescr, caps: capBridge, neighbours: invented},
	})
	return o
}
