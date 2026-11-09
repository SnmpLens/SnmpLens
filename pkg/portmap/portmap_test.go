package portmap

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

func pdu(oid string, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Value: v}
}

func macArcs(mac string) string {
	hw, _ := net.ParseMAC(mac)
	parts := make([]string, len(hw))
	for i, b := range hw {
		parts[i] = fmt.Sprint(b)
	}
	return strings.Join(parts, ".")
}

func mac(s string) []byte {
	hw, _ := net.ParseMAC(s)
	return hw
}

// A two-port switch: bridge ports 1 and 2 are ifIndex 10101 and 10102, so a
// map that read a bridge port as an ifIndex puts nothing anywhere.
func smallSwitch() Walk {
	return Walk{
		pdu(sysObjectID+".0", ".1.3.6.1.4.1.8072.3.2.10"),
		pdu(basePortIfIndex+".1", 10101),
		pdu(basePortIfIndex+".2", 10102),
		pdu(ifName+".10101", []byte("Gi1/0/1")),
		pdu(ifName+".10102", []byte("Gi1/0/2")),
		pdu(ifName+".1", []byte("Vlan1")), // not a bridge port
		pdu(ifAlias+".10101", []byte("desk 12")),
		pdu(ifOperStatus+".10101", 1),
		pdu(ifOperStatus+".10102", 2),
		pdu(ifHighSpeed+".10101", uint(1000)),
		pdu(qPvid+".1", uint(20)),
	}
}

func TestStationsArePlacedThroughTheBridgePortTable(t *testing.T) {
	sw := append(smallSwitch(),
		pdu(fdbPort+"."+macArcs("00:11:22:33:44:55"), 1),
		pdu(fdbStatus+"."+macArcs("00:11:22:33:44:55"), 3),
		pdu(fdbPort+"."+macArcs("00:aa:bb:cc:dd:ee"), 0), // the switch itself
		pdu(fdbStatus+"."+macArcs("00:aa:bb:cc:dd:ee"), 4),
		pdu(fdbPort+"."+macArcs("02:00:00:00:00:09"), 7), // a bridge port no interface answers
	)
	m := Build(Input{Switch: sw})
	if len(m.Ports) != 2 {
		t.Fatalf("ports = %+v", m.Ports)
	}
	p := m.Ports[0]
	if p.IfIndex != 10101 || p.BridgePort != 1 || p.Name != "Gi1/0/1" || p.Alias != "desk 12" || !p.Up ||
		p.SpeedMbps != 1000 || p.VLAN != 20 {
		t.Errorf("port = %+v", p)
	}
	if len(p.Stations) != 1 || p.Stations[0].MAC != "00:11:22:33:44:55" {
		t.Errorf("stations = %+v", p.Stations)
	}
	if m.Source != "bridge" || m.Stations != 2 || len(m.Unplaced) != 1 {
		t.Errorf("source %q, %d stations, unplaced %+v", m.Source, m.Stations, m.Unplaced)
	}
}

// Q-BRIDGE says which VLAN a station is on, so it is read first.
func TestQBridgeIsPreferredAndCarriesTheVLAN(t *testing.T) {
	sw := append(smallSwitch(),
		pdu(fdbPort+"."+macArcs("00:11:22:33:44:55"), 2),
		pdu(qFdbPort+".30."+macArcs("00:11:22:33:44:55"), 1),
		pdu(qFdbStatus+".30."+macArcs("00:11:22:33:44:55"), 3),
	)
	m := Build(Input{Switch: sw})
	if m.Source != "q-bridge" || len(m.Ports[0].Stations) != 1 || m.Ports[0].Stations[0].VLAN != 30 {
		t.Fatalf("map = %+v", m)
	}
	if len(m.Ports[1].Stations) != 0 {
		t.Errorf("BRIDGE-MIB's entry was used beside Q-BRIDGE's: %+v", m.Ports[1].Stations)
	}
}

// Cisco answers BRIDGE-MIB one VLAN at a time.
func TestAPerVLANWalkIsReadWithItsVLAN(t *testing.T) {
	m := Build(Input{Switch: smallSwitch(), ByVLAN: map[int]Walk{
		20: {pdu(basePortIfIndex+".1", 10101), pdu(fdbPort+"."+macArcs("00:11:22:33:44:55"), 1)},
		30: {pdu(basePortIfIndex+".2", 10102), pdu(fdbPort+"."+macArcs("00:11:22:33:44:66"), 2)},
	}})
	if m.Source != "bridge-per-vlan" || m.Stations != 2 {
		t.Fatalf("map = %+v", m)
	}
	if s := m.Ports[1].Stations; len(s) != 1 || s[0].VLAN != 30 {
		t.Errorf("stations on port 2 = %+v", s)
	}
}

func TestARPCachesGiveTheAddresses(t *testing.T) {
	sw := append(smallSwitch(), pdu(fdbPort+"."+macArcs("00:11:22:33:44:55"), 1))
	gateway := Walk{
		pdu(arpMedia+".3.192.0.2.10", mac("00:11:22:33:44:55")),
		pdu(arpPhysical+".3.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.10", mac("00:11:22:33:44:55")),
		pdu(arpPhysical+".3.1.4.192.0.2.10", mac("00:11:22:33:44:55")), // the same address twice
		pdu(arpPhysical+".3.1.5.1.2.3.4.5", mac("00:11:22:33:44:55")),  // a length that is not IPv4's
	}
	m := Build(Input{Switch: sw, ARP: []Walk{gateway}})
	got := m.Ports[0].Stations[0].IPs
	if strings.Join(got, ",") != "192.0.2.10,2001:db8::a" {
		t.Errorf("IPs = %v", got)
	}
}

// What LLDP numbers a port is the agent's choice; each way is resolved.
func TestLLDPNeighboursFindTheirPort(t *testing.T) {
	sw := append(smallSwitch(),
		// By name: lldpLocPortNum 7 is named Gi1/0/2.
		pdu(lldpLocPort+".2.7", 5),
		pdu(lldpLocPort+".3.7", []byte("Gi1/0/2")),
		pdu(lldpRem+".9.0.7.1", []byte("core-sw")),
		pdu(lldpRem+".6.0.7.1", 5),
		pdu(lldpRem+".7.0.7.1", []byte("Te1/1/1")),
		pdu(lldpRem+".12.0.7.1", []byte{0x28}), // bridge and router
		// By bridge port: 1 is no ifIndex here, and bridge port 1 is.
		pdu(lldpRem+".9.0.1.1", []byte("ap-hall")),
		pdu(lldpRem+".4.0.1.1", 4),
		pdu(lldpRem+".5.0.1.1", mac("00:0c:29:aa:bb:cc")),
		pdu(lldpRem+".12.0.1.1", []byte{0x30}), // an access point, which bridges
	)
	m := Build(Input{Switch: sw})
	if n := m.Ports[1].Neighbour; n == nil || n.Name != "core-sw" || n.Port != "Te1/1/1" || n.Protocol != "lldp" {
		t.Errorf("port 2's neighbour = %+v", n)
	}
	if n := m.Ports[0].Neighbour; n == nil || n.Name != "ap-hall" || n.Chassis != "00:0c:29:aa:bb:cc" {
		t.Errorf("port 1's neighbour = %+v", n)
	}
	if m.Ports[1].Neighbour.Kind != "switch" || m.Ports[0].Neighbour.Kind != "ap" {
		t.Errorf("kinds: %q, %q", m.Ports[1].Neighbour.Kind, m.Ports[0].Neighbour.Kind)
	}
	if !m.Ports[1].Uplink || m.Ports[0].Uplink {
		t.Errorf("uplinks: to a switch %v, to an access point %v", m.Ports[1].Uplink, m.Ports[0].Uplink)
	}
}

func TestCDPNamesANeighbourLLDPDoesNot(t *testing.T) {
	sw := append(smallSwitch(),
		pdu(cdpCache+".6.10102.1", []byte("dist-1.example")),
		pdu(cdpCache+".7.10102.1", []byte("GigabitEthernet0/48")),
		pdu(cdpCache+".3.10102.1", 1),
		pdu(cdpCache+".4.10102.1", []byte{192, 0, 2, 1}),
		pdu(cdpCache+".9.10102.1", []byte{0, 0, 0, 0x29}), // router, switch, IGMP
	)
	m := Build(Input{Switch: sw})
	n := m.Ports[1].Neighbour
	if n == nil || n.Protocol != "cdp" || n.Name != "dist-1.example" || n.Address != "192.0.2.1" || n.Kind != "switch" {
		t.Errorf("neighbour = %+v", n)
	}
}

func TestManyStationsMakeAnUplink(t *testing.T) {
	sw := smallSwitch()
	for i := range ManyStations {
		sw = append(sw, pdu(fdbPort+fmt.Sprintf(".0.17.34.51.68.%d", i), 2))
	}
	sw = append(sw, pdu(fdbPort+"."+macArcs("00:11:22:33:44:55"), 1))
	m := Build(Input{Switch: sw})
	if m.Ports[0].Uplink || !m.Ports[1].Uplink {
		t.Errorf("uplinks: port 1 %v, port 2 %v", m.Ports[0].Uplink, m.Ports[1].Uplink)
	}
}

func TestCiscoIsRecognisedAndItsVLANsListed(t *testing.T) {
	sw := Walk{pdu(sysObjectID+".0", ".1.3.6.1.4.1.9.1.716")}
	if !IsCisco(sw) || IsCisco(smallSwitch()) {
		t.Error("IsCisco")
	}
	// 911 is not 9: a prefix compared as text says it is.
	if IsCisco(Walk{pdu(sysObjectID+".0", ".1.3.6.1.4.1.911.1")}) {
		t.Error("enterprise 911 was taken for Cisco")
	}
	vtp := Walk{
		pdu(VTPVlanState+".1.1", 1), pdu(VTPVlanState+".1.20", 1), pdu(VTPVlanState+".1.30", 2),
		pdu(VTPVlanState+".1.1002", 1), pdu(VTPVlanState+".1.1005", 1),
	}
	if got := fmt.Sprint(CiscoVLANs(vtp)); got != "[1 20]" {
		t.Errorf("VLANs = %s", got)
	}
}

func TestAMalformedIndexIsSkippedNotMisread(t *testing.T) {
	sw := append(smallSwitch(),
		pdu(fdbPort+".1.2.3", 1),                             // five arcs short of a MAC
		pdu(basePortIfIndex+".x", 10101),                     // not a number
		pdu(arpMedia+".3.192.0.2", mac("00:11:22:33:44:55")), // three octets
	)
	m := Build(Input{Switch: sw})
	if m.Stations != 0 {
		t.Errorf("stations = %d", m.Stations)
	}
}
