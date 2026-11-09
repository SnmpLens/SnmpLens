package app

import (
	"strings"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
	"SnmpLens/pkg/snmp"
)

// A port map of a simulated Catalyst, over the wire: its BRIDGE-MIB puts the
// stations on ports through dot1dBasePortIfIndex, its LLDP names the neighbour
// on the uplink, and the uplink carries the rest of the network.
func TestAPortMapOfASimulatedSwitch(t *testing.T) {
	address := simtest.OwnAddress(t, 4)
	port := simtest.FreePort(t, address)
	simtest.Start(t, simulator.Device{Address: address, Port: port, Name: "access-1", Model: "cisco-catalyst-24"})

	a := &App{}
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient(nil)
	m, err := a.PortMapScan(PortMapRequest{Switch: snmp.SnmpRequest{
		Targets: []string{address}, Community: "public", Version: "v2c", Port: port, Timeout: 2, Retries: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Cisco || m.Source == "" {
		t.Errorf("cisco %v, source %q", m.Cisco, m.Source)
	}
	if len(m.Ports) < 24 {
		t.Fatalf("%d ports", len(m.Ports))
	}
	stations, uplinks, named := 0, 0, 0
	for _, p := range m.Ports {
		stations += len(p.Stations)
		if p.Uplink {
			uplinks++
		}
		if p.Neighbour != nil && p.Neighbour.Name != "" {
			named++
		}
		if p.Name == "" || p.IfIndex == 0 || p.BridgePort == 0 {
			t.Errorf("port = %+v", p)
		}
		for _, s := range p.Stations {
			if strings.Count(s.MAC, ":") != 5 {
				t.Errorf("station %q on %s", s.MAC, p.Name)
			}
		}
	}
	if stations == 0 || stations != m.Stations {
		t.Errorf("%d stations on the ports, %d counted", stations, m.Stations)
	}
	for _, p := range m.Ports {
		n := ""
		if p.Neighbour != nil {
			n = p.Neighbour.Name + " " + p.Neighbour.Port
		}
		t.Logf("%-8s if%-3d bp%-3d up=%-5v vlan=%-3d stations=%-3d uplink=%-5v %s", p.Name, p.IfIndex, p.BridgePort, p.Up, p.VLAN, len(p.Stations), p.Uplink, n)
	}
	if uplinks == 0 || named == 0 {
		t.Errorf("%d uplinks, %d ports with a named neighbour", uplinks, named)
	}
}

// Only one map at a time, and the second is told so rather than queued.
func TestOnePortMapAtATime(t *testing.T) {
	a := &App{}
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient(nil)
	a.portMaps.cancel = func() {}
	if _, err := a.PortMapScan(PortMapRequest{Switch: snmp.SnmpRequest{Targets: []string{"127.0.0.1"}}}); err == nil {
		t.Error("a second scan started")
	}
	if _, err := (&App{snmpClient: a.snmpClient}).PortMapScan(PortMapRequest{}); err == nil {
		t.Error("a scan of no switch started")
	}
}

func TestACiscoVLANIsAskedForByCommunityOrContext(t *testing.T) {
	v2 := inVLAN(snmp.SnmpRequest{Version: "v2c", Community: "public"}, 20)
	v3 := inVLAN(snmp.SnmpRequest{Version: "v3", V3: snmp.V3Params{User: "ops"}}, 20)
	if v2.Community != "public@20" || v3.V3.ContextName != "vlan-20" || v3.V3.User != "ops" {
		t.Errorf("v2c %q, v3 %+v", v2.Community, v3.V3)
	}
}
