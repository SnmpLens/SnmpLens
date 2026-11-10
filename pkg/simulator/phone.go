package simulator

import (
	"net"
	"net/netip"
)

// A SIP desk phone: a network port, a PC port with a computer behind it, and an
// agent built on net-snmp, as an embedded one usually is — so it answers with
// net-snmp's sysObjectID and stands for no vendor's phone in particular. LLDP-MED
// says it is a telephone, and a bridge because of that PC port: a switch it is
// plugged into learns the phone and the computer on one port, and that port is
// neither an uplink nor a switch.
var ipPhone = model{
	ModelInfo:     ModelInfo{ID: "ip-phone", Category: "telephony"},
	enterprise:    8072, // an embedded net-snmp
	build:         buildIPPhone,
	notifications: []Notification{linkNotification(true, 1)}, // eth0, the network port
	attached: func(id Identity) map[string][]net.HardwareAddr {
		return map[string][]net.HardwareAddr{"eth1": {phonePC(id.Seed)}}
	},
}

// phoneDescr is what a desk phone says of itself, here and wherever a switch
// model invents one on a port.
const phoneDescr = "SIP desk phone, firmware 4.2.1 (build 2208)"

// phonePC is the computer plugged into a phone's PC port.
func phonePC(seed uint64) net.HardwareAddr { return peerMAC(seed, 910) }

func buildIPPhone(id Identity) []Object {
	var o objects
	addSystem(&o, id, phoneDescr, ".1.3.6.1.4.1.8072.3.2.10", "noc@example.com", "Desk", 72)
	lan := lanPrefix(id.Seed)
	ifs := []iface{
		{index: 1, descr: "eth0", alias: "network", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
			mac: deviceMAC(id.Seed, 0), up: true, inRate: 11_000, outRate: 13_000},
		{index: 2, descr: "eth1", alias: "PC", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
			mac: deviceMAC(id.Seed, 2), up: true, inRate: 90_000, outRate: 240_000},
		{index: 3, descr: "lo", ifType: ifTypeSoftwareLoopback, mtu: 65536, speed: 10_000_000, up: true,
			inRate: 200, outRate: 200},
	}
	pbx := hostIn(lan, 5)
	addStack(&o, id, stack{
		seed:     id.Seed,
		ifs:      ifs,
		addrs:    []ifAddr{{ifIndex: 1, prefix: addrIn(lan, 120+int(id.Seed%60)), neighbours: 1}},
		gateway:  hostIn(lan, 1),
		pps:      60,
		listen:   []uint16{80, 443, 5061},
		udp:      []uint16{161, 5060},
		sessions: []session{{5061, netip.AddrPortFrom(pbx, 5061)}}, // registered with the PBX over TLS
		lldp: &lldpInfo{name: id.Name, descr: phoneDescr, caps: capTelephone | capBridge, neighbours: []neighbour{
			{ifIndex: 1, name: "sw-floor-2", descr: catalystDescr, port: "Fa0/3", portDescr: "FastEthernet0/3",
				chassis: peerMAC(id.Seed, 900), caps: capBridge, addr: hostIn(lan, 2)},
		}},
	})
	return o
}
