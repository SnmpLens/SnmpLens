package app

import (
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
	"SnmpLens/pkg/snmp"
)

// A bench cabled as a campus, mapped by the two tools over the wire: the network
// map follows LLDP from the core to every device cabled to it and beyond, each
// link reported by both of its ends; the port map of a Cisco access switch reads
// it VLAN by VLAN and gives its stations the addresses the core's ARP cache
// holds; the Aruba's says the VLAN of each station itself; and the desk phone is
// a phone, on a port that is not an uplink.
func TestABenchIsMappedAsOneNetwork(t *testing.T) {
	addrs := []string{simtest.OwnAddress(t, 31), simtest.OwnAddress(t, 32), simtest.OwnAddress(t, 33),
		simtest.OwnAddress(t, 34)}
	port := simtest.FreePort(t, addrs[0])
	device := func(id, name, model, address string) simulator.Device {
		engineID, err := simulator.NewEngineID(model, id)
		if err != nil {
			t.Fatal(err)
		}
		return simulator.Device{ID: id, Name: name, Model: model, Address: address, Port: port,
			Versions: []string{"v2c"}, Community: "public", EngineID: engineID, EngineBoots: 1}
	}
	devices := []simulator.Device{
		device("c0de0000000000a1", "core-01", "cisco-catalyst-9300", addrs[0]),
		device("acce5500000000a2", "sw-floor-2", "cisco-catalyst-24", addrs[1]),
		device("a7ba0000000000a3", "sw-floor-3", "aruba-2930f-24", addrs[2]),
		device("f0e00000000000a4", "phone-3-18", "ip-phone", addrs[3]),
	}
	end := func(i int, port string) simulator.LinkEnd {
		return simulator.LinkEnd{Device: devices[i].ID, Port: port}
	}
	links := []simulator.Link{
		{ID: "l1", A: end(1, "Gi0/1"), B: end(0, "Te1/1/1")},
		{ID: "l2", A: end(2, "25"), B: end(0, "Te1/1/2")},
		{ID: "l3", A: end(3, "eth0"), B: end(2, "18")},
	}
	if err := simulator.CheckLinks(devices, links); err != nil {
		t.Fatal(err)
	}
	plan := simulator.PlanBench(devices, links)
	for _, d := range devices {
		d.Wiring = plan[d.ID]
		simtest.Start(t, d)
	}

	a := &App{}
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient(nil)
	req := func(target string) snmp.SnmpRequest {
		return snmp.SnmpRequest{Targets: []string{target}, Community: "public", Version: "v2c", Port: port,
			Timeout: 2, Retries: 1}
	}

	// The map, from the core alone, two hops out.
	g, err := a.TopologyScan(TopologyRequest{Seeds: []snmp.SnmpRequest{req(addrs[0])}, Template: req(""), Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	kinds := map[string]string{}
	for _, n := range g.Nodes {
		if n.Managed {
			byName[n.Name], kinds[n.Name] = n.ID, n.Kind
		}
		if n.Name == "core-sw-01" {
			t.Errorf("the invented core is on the map: %+v", n)
		}
	}
	for _, d := range devices {
		if byName[d.Name] == "" {
			t.Errorf("%s was not walked: %+v", d.Name, g.Nodes)
		}
	}
	if kinds["phone-3-18"] != "phone" || kinds["core-01"] != "switch" {
		t.Errorf("kinds: %v", kinds)
	}
	both := 0
	for _, l := range g.Links {
		pair := map[string]bool{l.A: true, l.B: true}
		for _, want := range [][2]string{{"core-01", "sw-floor-2"}, {"core-01", "sw-floor-3"}, {"sw-floor-3", "phone-3-18"}} {
			if pair[byName[want[0]]] && pair[byName[want[1]]] && l.Both {
				both++
			}
		}
	}
	if both != 3 {
		t.Errorf("%d of the 3 cables were reported by both ends: %+v", both, g.Links)
	}

	// The Cisco access switch, VLAN by VLAN, with the core's ARP cache.
	m, err := a.PortMapScan(PortMapRequest{Switch: req(addrs[1]), Gateways: []snmp.SnmpRequest{req(addrs[0])}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != "bridge-per-vlan" {
		t.Errorf("source %q, want the switch read VLAN by VLAN", m.Source)
	}
	addressed := 0
	for _, p := range m.Ports {
		for _, s := range p.Stations {
			for _, ip := range s.IPs {
				if strings.HasPrefix(ip, "10.20.10.") && s.VLAN == 10 {
					addressed++
				}
			}
		}
		switch p.Name {
		case "Gi0/1":
			if p.Neighbour == nil || p.Neighbour.Name != "core-01" || !p.Uplink {
				t.Errorf("the uplink: %+v, uplink %v", p.Neighbour, p.Uplink)
			}
		case "Fa0/3":
			if p.Neighbour == nil || p.Neighbour.Kind != "phone" || p.Uplink {
				t.Errorf("the phone's port: %+v, uplink %v", p.Neighbour, p.Uplink)
			}
		}
	}
	if addressed == 0 {
		t.Error("no station in VLAN 10 was given its address by the core")
	}

	// The Aruba says the VLAN itself, and the phone cabled to it is a phone.
	m, err = a.PortMapScan(PortMapRequest{Switch: req(addrs[2]), Gateways: []snmp.SnmpRequest{req(addrs[0])}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != "q-bridge" {
		t.Errorf("the Aruba's source %q", m.Source)
	}
	for _, p := range m.Ports {
		if p.Name != "18" {
			continue
		}
		if p.Neighbour == nil || p.Neighbour.Name != "phone-3-18" || p.Neighbour.Kind != "phone" || p.Uplink {
			t.Errorf("port 18: %+v, uplink %v", p.Neighbour, p.Uplink)
		}
		if len(p.Stations) != 2 || p.Stations[0].VLAN != 30 || len(p.Stations[0].IPs) == 0 {
			t.Errorf("port 18's stations: %+v", p.Stations)
		}
	}
}

// A cable is kept with the devices, plugged into a running device without
// restarting it, and unplugged with a device deleted; a device whose port is in
// use cannot lose it.
func TestCablesAreKeptAndAppliedToRunningDevices(t *testing.T) {
	a, _ := simApp(t)
	coreDevice := simDevice()
	coreDevice.Name, coreDevice.Model = "core-01", "cisco-catalyst-9300"
	core := startSimulated(t, a, coreDevice)
	srvDevice := simDevice()
	srvDevice.Address, srvDevice.Port = "127.0.0.1", freeSimPort(t)
	srv, err := a.SimulatorSaveDevice(srvDevice)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := a.SimulatorPorts(core.ID)
	if err != nil || len(ports) == 0 || ports[0].Name != "Gi0/0" {
		t.Fatalf("the core's ports: %v %+v", err, ports)
	}
	bootsBefore := listedDevice(a, core.ID).EngineBoots
	cable := simulator.Link{A: simulator.LinkEnd{Device: core.ID, Port: "Gi1/0/1"},
		B: simulator.LinkEnd{Device: srv.ID, Port: "eth0"}}
	l, err := a.SimulatorConnect(cable)
	if err != nil || l.ID == "" {
		t.Fatalf("connect: %v %+v", err, l)
	}
	if _, err := a.SimulatorConnect(cable); err == nil {
		t.Error("a port took two cables")
	}
	if got := a.ListSimulatorLinks(); len(got) != 1 || got[0].ID != l.ID {
		t.Errorf("links: %+v", got)
	}
	a.sim.mu.Lock()
	wired := a.sim.wired[core.ID]
	a.sim.mu.Unlock()
	if wired == nil || wired.Ports["Gi1/0/1"].Peer.Name != "srv-01" {
		t.Errorf("the running core was not recabled: %+v", wired)
	}
	if boots := listedDevice(a, core.ID).EngineBoots; boots != bootsBefore {
		t.Errorf("the core restarted to be recabled: boots %d, then %d", bootsBefore, boots)
	}

	// A model with fewer ports would leave the cable with nowhere to go.
	fewer := coreDevice
	fewer.ID, fewer.Address, fewer.Port = core.ID, core.Address, core.Port
	fewer.Params = map[string]int{"ports": 4}
	moved := simulator.Link{A: simulator.LinkEnd{Device: core.ID, Port: "Gi1/0/8"}, B: simulator.LinkEnd{Device: srv.ID, Port: "eth0"}}
	if err := a.SimulatorDisconnect(l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SimulatorConnect(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SimulatorSaveDevice(fewer); err == nil {
		t.Error("a device lost the port a cable is in")
	}

	// Read back as another session would, then a device deleted takes its
	// cables with it.
	again := newSimulatorService(filepath.Dir(a.sim.path))
	if len(again.links) != 1 {
		t.Errorf("the cable was not kept: %+v", again.links)
	}
	if err := a.SimulatorDeleteDevice(srv.ID); err != nil {
		t.Fatal(err)
	}
	if got := a.ListSimulatorLinks(); len(got) != 0 {
		t.Errorf("a deleted device's cable remains: %+v", got)
	}
}

// A device a backup restores without the port a cable is in unplugs that cable,
// and says so; the backup's own cables are then restored. Left in, the cable
// failed every cable checked after it, each refused for a port nobody had asked
// for.
func TestARestoredDeviceThatLostAPortUnplugsItsCable(t *testing.T) {
	a, _ := simApp(t)
	coreDevice := simDevice()
	coreDevice.Name, coreDevice.Model = "core-01", "cisco-catalyst-9300"
	core, err := a.SimulatorSaveDevice(coreDevice)
	if err != nil {
		t.Fatal(err)
	}
	srvDevice := simDevice()
	srvDevice.Address = "127.0.0.3"
	srv, err := a.SimulatorSaveDevice(srvDevice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SimulatorConnect(simulator.Link{A: simulator.LinkEnd{Device: core.ID, Port: "Gi1/0/8"},
		B: simulator.LinkEnd{Device: srv.ID, Port: "eth0"}}); err != nil {
		t.Fatal(err)
	}

	a.sim.mu.Lock()
	fewer := a.sim.devices[a.sim.index(core.ID)]
	a.sim.mu.Unlock()
	fewer.Params = map[string]int{"ports": 4}
	var restored int
	problems := a.restoreSimulatedDevices([]simulator.Device{fewer}, nil, &restored)
	if restored != 1 || len(problems) != 1 || !strings.Contains(problems[0], "Gi1/0/8") {
		t.Fatalf("restored %d, said %q", restored, problems)
	}
	if got := a.ListSimulatorLinks(); len(got) != 0 {
		t.Errorf("the cable is still in a port the device no longer has: %+v", got)
	}
	again := newSimulatorService(filepath.Dir(a.sim.path))
	if len(again.links) != 0 {
		t.Errorf("the unplugged cable was kept in the file: %+v", again.links)
	}

	backup := simulator.Link{ID: simulator.NewLinkID(), A: simulator.LinkEnd{Device: core.ID, Port: "Gi1/0/2"},
		B: simulator.LinkEnd{Device: srv.ID, Port: "eth0"}}
	if problems := a.restoreSimulatorLinks([]simulator.Link{backup}); len(problems) != 0 {
		t.Errorf("the backup's cable was refused: %q", problems)
	}
	if got := a.ListSimulatorLinks(); len(got) != 1 || got[0].ID != backup.ID {
		t.Errorf("links: %+v", got)
	}
}
