package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"

	"SnmpLens/pkg/portmap"
	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
	"SnmpLens/pkg/snmp"

	"github.com/gosnmp/gosnmp"
)

// A map of two simulated devices over the wire: each is a node, so is every
// neighbour it names, and each neighbour is a link from the port it was heard on.
func TestAMapOfSimulatedDevices(t *testing.T) {
	sw, router := simtest.OwnAddress(t, 5), simtest.OwnAddress(t, 6)
	port := simtest.FreePort(t, sw)
	simtest.Start(t, simulator.Device{Address: sw, Port: port, Name: "access-1", Model: "cisco-catalyst-24"})
	simtest.Start(t, simulator.Device{Address: router, Port: port, Name: "edge-r1", Model: "cisco-isr-4331"})

	a := &App{}
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient(nil)
	req := func(target string) snmp.SnmpRequest {
		return snmp.SnmpRequest{Targets: []string{target}, Community: "public", Version: "v2c", Port: port, Timeout: 2, Retries: 1}
	}
	g, err := a.TopologyScan(TopologyRequest{Seeds: []snmp.SnmpRequest{req(sw), req(router)}})
	if err != nil {
		t.Fatal(err)
	}
	managed := 0
	for _, n := range g.Nodes {
		if n.Managed {
			managed++
			if n.Name != "access-1" && n.Name != "edge-r1" {
				t.Errorf("managed node %+v", n)
			}
		}
	}
	if managed != 2 || len(g.Nodes) < 4 || len(g.Links) < 2 {
		t.Fatalf("%d managed of %d nodes, %d links: %+v", managed, len(g.Nodes), len(g.Links), g)
	}
	for _, l := range g.Links {
		if l.A == "" || l.B == "" || l.APort == "" {
			t.Errorf("link %+v", l)
		}
	}
}

// Following neighbours: hop by hop, each address once, a target with its own
// identifiers and anything else with the defaults, and never past the depth.
func TestFollowingNeighboursIsBoundedAndUsesTheRightIdentifiers(t *testing.T) {
	// A chain: 10.0.0.1 hears .2, which hears .3, which hears .4; and .2 also
	// hears .1 back.
	walks := map[string]portmap.Walk{
		"10.0.0.1": chainDevice("r1", "10.0.0.2"),
		"10.0.0.2": chainDevice("r2", "10.0.0.3", "10.0.0.1"),
		"10.0.0.3": chainDevice("r3", "10.0.0.4"),
		"10.0.0.4": chainDevice("r4"),
	}
	var mu sync.Mutex
	asked := map[string]string{}
	walk := func(r snmp.SnmpRequest) (portmap.Walk, error) {
		mu.Lock()
		asked[r.Targets[0]] += r.Community + " "
		mu.Unlock()
		if w, ok := walks[r.Targets[0]]; ok {
			return w, nil
		}
		return nil, errors.New("no answer")
	}
	g, err := crawlTopology(context.Background(), TopologyRequest{
		Seeds:    []snmp.SnmpRequest{{Targets: []string{"10.0.0.1"}, Community: "seed"}},
		Template: snmp.SnmpRequest{Community: "defaults"},
		Known:    []snmp.SnmpRequest{{Targets: []string{"10.0.0.2"}, Community: "own"}},
		Depth:    2,
	}, walk, func(TopologyProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for a, c := range asked {
		got = append(got, a+"="+strings.TrimSpace(c))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "10.0.0.1=seed,10.0.0.2=own,10.0.0.3=defaults" {
		t.Errorf("asked %v", got)
	}
	managed := 0
	for _, n := range g.Nodes {
		if n.Managed {
			managed++
		}
	}
	if managed != 3 {
		t.Errorf("%d managed nodes: %+v", managed, g.Nodes)
	}
}

func TestADeviceThatDoesNotAnswerIsAWarning(t *testing.T) {
	g, err := crawlTopology(context.Background(), TopologyRequest{
		Seeds: []snmp.SnmpRequest{{Targets: []string{"192.0.2.9"}}},
	}, func(snmp.SnmpRequest) (portmap.Walk, error) { return nil, errors.New("timeout") }, func(TopologyProgress) {})
	if err != nil || len(g.Nodes) != 0 || len(g.Warnings) != 1 || !strings.Contains(g.Warnings[0], "192.0.2.9") {
		t.Errorf("graph %+v, err %v", g, err)
	}
}

// chainDevice is a router that names a neighbour at each address, over LLDP with
// its management address.
func chainDevice(name string, neighbours ...string) portmap.Walk {
	pdu := func(oid string, v any) gosnmp.SnmpPDU { return gosnmp.SnmpPDU{Name: "." + oid, Value: v} }
	w := portmap.Walk{
		pdu("1.3.6.1.2.1.1.5.0", []byte(name)),
		pdu("1.0.8802.1.1.2.1.3.1.0", 7),
		pdu("1.0.8802.1.1.2.1.3.2.0", []byte(name)),
	}
	for i, address := range neighbours {
		ifIndex := i + 1
		w = append(w, pdu(fmt.Sprintf("1.3.6.1.2.1.31.1.1.1.1.%d", ifIndex), []byte(fmt.Sprintf("ge-0/0/%d", i))))
		inst := fmt.Sprintf("0.%d.1", ifIndex)
		w = append(w,
			pdu("1.0.8802.1.1.2.1.4.1.1.4."+inst, 7),
			pdu("1.0.8802.1.1.2.1.4.1.1.5."+inst, []byte("peer-"+address)),
			pdu("1.0.8802.1.1.2.1.4.1.1.7."+inst, []byte("ge-0/0/9")),
			pdu("1.0.8802.1.1.2.1.4.2.1.3."+inst+".1.4."+strings.ReplaceAll(net.ParseIP(address).To4().String(), ".", "."), 2),
		)
	}
	return w
}
