package topology

import (
	"net"
	"strings"
	"testing"

	"SnmpLens/pkg/portmap"

	"github.com/gosnmp/gosnmp"
)

func pdu(oid string, v any) gosnmp.SnmpPDU { return gosnmp.SnmpPDU{Name: "." + oid, Value: v} }

func mac(s string) []byte {
	hw, _ := net.ParseMAC(s)
	return hw
}

const rem = "1.0.8802.1.1.2.1.4.1.1"

// neighbour is an lldpRemTable entry on local port ifIndex.
func neighbour(ifIndex string, idx string, name, port string, chassis []byte, caps byte) []gosnmp.SnmpPDU {
	inst := "0." + ifIndex + "." + idx
	return []gosnmp.SnmpPDU{
		pdu(rem+".4."+inst, 4), pdu(rem+".5."+inst, chassis),
		pdu(rem+".6."+inst, 5), pdu(rem+".7."+inst, []byte(port)),
		pdu(rem+".9."+inst, []byte(name)), pdu(rem+".12."+inst, []byte{caps}),
	}
}

func device(name, chassis string, caps byte, ports map[string]string, extra ...gosnmp.SnmpPDU) portmap.Walk {
	w := portmap.Walk{
		pdu(sysName+".0", []byte(name)),
		pdu("1.0.8802.1.1.2.1.3.1.0", 4),
		pdu("1.0.8802.1.1.2.1.3.2.0", mac(chassis)),
		pdu(lldpLocCapsOn+".0", []byte{caps}),
	}
	for idx, port := range ports {
		w = append(w, pdu("1.3.6.1.2.1.31.1.1.1.1."+idx, []byte(port)))
	}
	return append(w, extra...)
}

// Two switches that hear each other are two nodes and one link, though each
// names the other's port in its own way.
func TestTwoDevicesHearingEachOtherAreOneLink(t *testing.T) {
	access := device("access-1", "00:00:00:00:0a:01", 0x20, map[string]string{"49": "Gi1/0/49"},
		neighbour("49", "1", "core-sw-01.corp.example", "TenGigabitEthernet1/0/12", mac("00:00:00:00:0c:01"), 0x28)...)
	core := device("core-sw-01", "00:00:00:00:0c:01", 0x28, map[string]string{"12": "Te1/0/12"},
		neighbour("12", "1", "access-1", "GigabitEthernet1/0/49", mac("00:00:00:00:0a:01"), 0x20)...)
	g := Build([]Device{{Address: "10.0.0.2", Walk: access}, {Address: "10.0.0.1", Walk: core}})
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %+v", g.Nodes)
	}
	if len(g.Links) != 1 || !g.Links[0].Both {
		t.Fatalf("links = %+v", g.Links)
	}
	l := g.Links[0]
	if l.APort != "Gi1/0/49" || !samePort(l.BPort, "Te1/0/12") {
		t.Errorf("link = %+v", l)
	}
	for _, n := range g.Nodes {
		if !n.Managed || n.Kind != "switch" {
			t.Errorf("node = %+v", n)
		}
	}
}

// A neighbour nobody walked is a node all the same, met once however many
// devices hear it.
func TestANeighbourNobodyWalkedIsOneNode(t *testing.T) {
	ap := mac("00:00:00:00:ab:01")
	sw1 := device("sw1", "00:00:00:00:01:01", 0x20, map[string]string{"5": "Gi1/0/5"},
		neighbour("5", "1", "ap-hall", "eth0", ap, 0x30)...)
	sw2 := device("sw2", "00:00:00:00:01:02", 0x20, map[string]string{"7": "Gi1/0/7"},
		neighbour("7", "1", "AP-HALL.corp.example", "eth1", ap, 0x30)...)
	g := Build([]Device{{Address: "10.0.0.11", Walk: sw1}, {Address: "10.0.0.12", Walk: sw2}})
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes = %+v", g.Nodes)
	}
	var apNode Node
	for _, n := range g.Nodes {
		if !n.Managed {
			apNode = n
		}
	}
	if apNode.Kind != "ap" || apNode.Name != "ap-hall" {
		t.Errorf("the access point = %+v", apNode)
	}
	if len(g.Links) != 2 {
		t.Errorf("links = %+v", g.Links)
	}
}

// A neighbour matched to a device walked by name, when it gives no chassis the
// device gave, and by address when it gives neither.
func TestANeighbourFindsTheDeviceByNameThenAddress(t *testing.T) {
	router := portmap.Walk{pdu(sysName+".0", []byte("edge-r1")), pdu(sysServices+".0", 78),
		pdu(ipAdEntAddr+".192.0.2.1", "192.0.2.1")}
	sw := device("sw", "00:00:00:00:01:01", 0x20, map[string]string{"1": "Gi1/0/1", "2": "Gi1/0/2"},
		append(neighbour("1", "1", "edge-r1.corp.example", "Gi0/0/0", []byte("not-the-same-id"), 0x08),
			neighbour("2", "2", "", "Gi0/0/1", []byte("no-name-either"), 0x08)...)...)
	// The second neighbour's address, through lldpRemManAddrTable.
	sw = append(sw, pdu("1.0.8802.1.1.2.1.4.2.1.3.0.2.2.1.4.192.0.2.1", 2))
	g := Build([]Device{{Address: "10.0.0.1", Walk: sw}, {Address: "10.9.9.9", Walk: router}})
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %+v", g.Nodes)
	}
	if len(g.Links) != 2 {
		t.Errorf("links = %+v", g.Links)
	}
	for _, n := range g.Nodes {
		if n.Name == "edge-r1" && n.Kind != "router" {
			t.Errorf("sysServices 78 is a router: %+v", n)
		}
	}
}

func TestNextHopsAreTheNeighboursAddresses(t *testing.T) {
	sw := device("sw", "00:00:00:00:01:01", 0x20, map[string]string{"1": "Gi1/0/1", "2": "Gi1/0/2"},
		append(append(neighbour("1", "1", "core", "Te1/0/1", mac("00:00:00:00:0c:01"), 0x28),
			neighbour("2", "1", "dist", "Te1/0/2", mac("00:00:00:00:0c:02"), 0x28)...),
			pdu("1.0.8802.1.1.2.1.4.2.1.3.0.1.1.1.4.10.0.0.1", 2),
			pdu("1.0.8802.1.1.2.1.4.2.1.3.0.1.1.2.16.32.1.13.184.0.0.0.0.0.0.0.0.0.0.0.1", 2),
			// An arc above 255 is no octet: truncated, 10.0.0.258 would be 10.0.0.2.
			pdu("1.0.8802.1.1.2.1.4.2.1.3.0.2.1.1.4.10.0.0.258", 2))...)
	if got := strings.Join(NextHops(sw), ","); got != "10.0.0.1" {
		t.Errorf("next hops = %q", got)
	}
}

// A desk phone says bridge and telephone, and is a phone.
func TestAPhoneIsNotASwitch(t *testing.T) {
	if got := kindOf([]byte{0x24}, 0); got != "phone" {
		t.Errorf("bridge and telephone is %q", got)
	}
	if got := kindOf([]byte{0x28}, 0); got != "switch" {
		t.Errorf("bridge and router is %q", got)
	}
}

func TestPortNamesAreComparedShortened(t *testing.T) {
	for a, b := range map[string]string{
		"Gi1/0/1": "GigabitEthernet1/0/1", "Te1/1/1": "TenGigabitEthernet1/1/1", "eth0": "Ethernet0",
	} {
		if !samePort(a, b) {
			t.Errorf("%s and %s are not the same port", a, b)
		}
	}
	if samePort("Gi1/0/1", "Gi1/0/2") || samePort("Fa0/1", "Gi0/1") || samePort("", "") {
		t.Error("different ports read as one")
	}
}
