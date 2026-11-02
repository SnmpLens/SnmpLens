package snmp

import (
	"strings"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
)

// Every operation of the Operations tab against a simulated Catalyst, read
// back through SnmpLens's own client. The target names its port and the port
// field says 161: the target's port wins, as it does everywhere.
func TestTheOperationsAgainstASimulatedSwitch(t *testing.T) {
	d := simtest.Start(t, simulator.Device{Model: "cisco-catalyst-24", Name: "sw-floor-2", WriteCommunity: "private"})
	c := NewClient()
	targets := []string{d.Target()}
	answer := func(t *testing.T, res []*BulkResult) *Result {
		t.Helper()
		if len(res) != 1 || res[0].Error != "" || res[0].Result == nil {
			t.Fatalf("%+v", res[0])
		}
		return res[0].Result
	}

	t.Run("GET", func(t *testing.T) {
		r := answer(t, c.Get(targets, "1.3.6.1.2.1.1.5.0", "public", "v2c", 161, 2, 0, V3Params{}))
		if r.Value != "sw-floor-2" {
			t.Errorf("sysName = %v", r.Value)
		}
	})
	t.Run("GETNEXT", func(t *testing.T) {
		r := answer(t, c.GetNext(targets, "1.3.6.1.2.1.1.5.0", "public", "v2c", 161, 2, 0, V3Params{}))
		if r.Oid != ".1.3.6.1.2.1.1.6.0" || r.Value != "Wiring closet, floor 2" {
			t.Errorf("after sysName: %s = %v", r.Oid, r.Value)
		}
	})
	t.Run("WALK", func(t *testing.T) {
		rows, _ := answer(t, c.Walk(targets, "1.3.6.1.2.1.2.2.1.2", "public", "v2c", 161, 2, 0, V3Params{})).Value.([]*Result)
		// 24 Fast Ethernet ports, two gigabit uplinks and VLAN 1.
		if len(rows) != 27 || rows[0].Value != "FastEthernet0/1" || rows[26].Value != "Vlan1" {
			t.Fatalf("walked %d interface(s): %+v", len(rows), rows)
		}
	})
	t.Run("GETBULK", func(t *testing.T) {
		rows, _ := answer(t, c.GetBulk(targets, "1.3.6.1.2.1.2.2.1.2", "public", "v2c", 161, 2, 0, 0, 10, V3Params{})).Value.([]interface{})
		if len(rows) != 10 {
			t.Fatalf("%d row(s) for ten repetitions", len(rows))
		}
		if r, _ := rows[9].(*Result); r == nil || r.Value != "FastEthernet0/10" {
			t.Errorf("the tenth row is %+v", rows[9])
		}
	})
	t.Run("SET", func(t *testing.T) {
		// The read community reads and nothing more, as a rocommunity does.
		res := c.Set(targets, "1.3.6.1.2.1.1.5.0", "public", "refused", "OctetString", "v2c", 161, 2, 0, V3Params{})
		if len(res) != 1 || !strings.Contains(strings.ToLower(res[0].Error), "noaccess") {
			t.Errorf("a SET with the read community came back as %+v", res[0])
		}
		res = c.Set(targets, "1.3.6.1.2.1.1.5.0", "private", "renamed", "OctetString", "v2c", 161, 2, 0, V3Params{})
		if len(res) != 1 || res[0].Error != "" {
			t.Fatalf("a SET with the write community came back as %+v", res[0])
		}
		if r := answer(t, c.Get(targets, "1.3.6.1.2.1.1.5.0", "public", "v2c", 161, 2, 0, V3Params{})); r.Value != "renamed" {
			t.Errorf("sysName read back as %v", r.Value)
		}
	})
}

// A scan of a loopback range finds the simulated devices on it, each named and
// identified as what it is — the sysObjectID a preset is matched on.
func TestDiscoveryFindsTheSimulatedDevices(t *testing.T) {
	server, sw := simtest.OwnAddress(t, 2), simtest.OwnAddress(t, 3)
	port := simtest.FreePort(t, server)
	simtest.Start(t, simulator.Device{Address: server, Port: port, Name: "scan-server"})
	simtest.Start(t, simulator.Device{Address: sw, Port: port, Name: "scan-switch", Model: "cisco-catalyst-24"})

	// Headless: the scan reports its progress to a window only when there is one.
	//lint:ignore SA1012 a nil context is how this package tells a headless client
	c := NewClient()
	found := map[string]DiscoveryResult{}
	for _, r := range c.Discover("127.0.0.0/29", "public", "v2c", port, 1, V3Params{}) {
		if r.Reachable {
			found[r.IP] = r
		}
	}
	for ip, want := range map[string][2]string{
		server: {"scan-server", ".1.3.6.1.4.1.8072.3.2.10"},
		sw:     {"scan-switch", ".1.3.6.1.4.1.9.1.716"},
	} {
		if r, ok := found[ip]; !ok || r.SysName != want[0] || r.SysObjectID != want[1] {
			t.Errorf("%s: found %v as %+v, want %s (%s)", ip, ok, r, want[0], want[1])
		}
	}
}
