package app

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/snmp"
)

// A device is recorded as a model: the walk kept as a package under an id made
// from the name, the agent's own objects left out and counted, and a device
// made from the model answering what the recorded one answered, under its own
// name. Exported, the model imports back as itself.
func TestADeviceIsRecordedAsAModel(t *testing.T) {
	a := modelApp(t)
	if a.snmpClient == nil {
		a.snmpClient = snmp.NewClient()
	}
	src := simDevice()
	src.Name, src.Model, src.Versions, src.Users, src.Traps = "sw-source", "cisco-catalyst-24", []string{"v2c"}, nil, simulator.Traps{}
	source := startSimulated(t, a, src)

	res, err := a.SimulatorRecordDevice(SimulatorRecordRequest{
		SnmpRequest: snmp.SnmpRequest{Targets: []string{fmt.Sprintf("%s:%d", source.Address, source.Port)},
			Community: src.Community, Version: "v2c", Timeout: 2, Retries: 1},
		Name: "Floor switch", Vendor: "Cisco", Category: "network", Description: "Recorded in a test.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.Replaced || res.ModelID != "custom:floor-switch" ||
		len(res.Warnings) != 1 || res.Warnings[0].Key != "walkAgentOwned" {
		t.Fatalf("%+v", res)
	}
	if got := dirNames(t, a.sim.modelDir); !slices.Equal(got, []string{"floor-switch.zip"}) {
		t.Errorf("the model directory holds %v", got)
	}
	if m := listedModel(a, "custom:floor-switch"); m == nil || m.Vendor != "Cisco" || m.Category != "network" {
		t.Fatalf("listed as %+v", m)
	}

	replica := simDevice()
	replica.Name, replica.Model, replica.Port = "sw-copy", "custom:floor-switch", src.Port+1
	replica.Versions, replica.Users, replica.Traps = []string{"v2c"}, nil, simulator.Traps{}
	copied := startSimulated(t, a, replica)
	for _, oid := range []string{".1.3.6.1.2.1.1.1.0", ".1.3.6.1.2.1.1.2.0", ".1.3.6.1.2.1.2.2.1.2.2", ".1.3.6.1.2.1.47.1.1.1.1.2.1"} {
		want := simGet(t, source, src.Community, oid)
		got := simGet(t, copied, replica.Community, oid)
		if got.Type != want.Type || !reflect.DeepEqual(got.Value, want.Value) {
			t.Errorf("%s answers %v %v, and the recorded device %v %v", oid, got.Type, got.Value, want.Type, want.Value)
		}
	}
	if v := simGet(t, copied, replica.Community, ".1.3.6.1.2.1.1.5.0"); string(v.Value.([]byte)) != "sw-copy" {
		t.Errorf("sysName answers %v", v.Value)
	}

	a.sim.mu.Lock()
	data, err := a.sim.exportModel("floor-switch")
	a.sim.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	again := onlyResult(t, a.importSimulatorModels([]string{writeModelFile(t, "floor-switch.zip", data)}))
	if !again.Success || !again.Replaced || again.ModelID != "custom:floor-switch" ||
		again.File != "floor-switch.zip › floor-switch/model.json" {
		t.Errorf("imported back as %+v", again)
	}
}

// A lone model exports as a package folder holding it as model.json, beside
// its icon under the name it gives it.
func TestALoneModelExportsAsAPackage(t *testing.T) {
	a := modelApp(t)
	onlyResult(t, a.importSimulatorModels([]string{writeZip(t, "acme.zip",
		zipEntry{"custom-model.json", exampleModel(t)}, zipEntry{"acme-crac.png", pngIcon(t, 32)})}))
	a.sim.mu.Lock()
	data, err := a.sim.exportModel("acme-crac")
	a.sim.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	res := onlyResult(t, a.importSimulatorModels([]string{writeModelFile(t, "export.zip", data)}))
	if !res.Success || !res.Icon || res.ModelID != "custom:acme-crac" || res.File != "export.zip › acme-crac/model.json" {
		t.Errorf("%+v", res)
	}
	if got := dirNames(t, a.sim.modelDir); !slices.Equal(got, []string{"acme-crac.png", "acme-crac.zip"}) {
		t.Errorf("the model directory holds %v", got)
	}
}
