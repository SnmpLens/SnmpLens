package simulator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/preset"

	"github.com/gosnmp/gosnmp"
)

// modelPresets names the bundled presets each model feeds. Every model is in
// it: a model added without saying what it answers is one nobody checked.
var modelPresets = map[string][]string{
	"linux-server":        {"interfaces.json", "host-resources.json"},
	"windows-server":      {"interfaces.json", "host-resources.json"},
	"cisco-catalyst-24":   {"interfaces.json", "switch-ports.json", "switch-drawing.json", "cisco-generic.json"},
	"cisco-catalyst-48":   {"interfaces.json", "switch-ports.json", "cisco-generic.json"},
	"cisco-isr-4331":      {"interfaces.json", "cisco-generic.json"},
	"cisco-catalyst-9300": {"interfaces.json", "switch-ports.json", "cisco-generic.json"},
	"aruba-2930f-24":      {"interfaces.json", "switch-ports.json"},
	"ip-phone":            {"interfaces.json"},
	"synology-nas":        {"interfaces.json", "host-resources.json"},
	"apc-smart-ups":       {"ups.json", "interfaces.json"},
	"hp-laserjet":         {"interfaces.json"},
	"environment-probe":   {"interfaces.json"},
	"dell-idrac9":         {"interfaces.json"},
	"mikrotik-rb4011":     {"interfaces.json", "host-resources.json"},
	"fortigate-60f":       {"interfaces.json"},
	"unifi-u6-pro":        {"interfaces.json", "host-resources.json"},
	"apc-rack-pdu":        {"interfaces.json"},
}

type presetFile struct {
	Match struct {
		SysObjectIDPrefix []string `json:"sysObjectIdPrefix"`
	} `json:"match"`
	Widgets []struct {
		Title    string   `json:"title"`
		OIDs     []string `json:"oids"`
		Discover *struct {
			Walk string `json:"walk"`
		} `json:"discover"`
	} `json:"widgets"`
}

func readPreset(t *testing.T, file string) presetFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "presets", file))
	if err != nil {
		t.Fatal(err)
	}
	var p presetFile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if len(p.Widgets) == 0 {
		t.Fatalf("%s has no widgets; the test reads the wrong shape", file)
	}
	return p
}

func sysObjectIDOf(t *testing.T, tr *tree) string {
	t.Helper()
	id, _ := parseOID(oidSysObjectID)
	e := tr.get(id)
	if e == nil {
		t.Fatal("no sysObjectID")
	}
	s, _ := e.val.read(clock{}).(string)
	return s
}

// Every OID a model's presets poll is one the model answers, for every
// instance the preset's own discovery walk finds on it. Read from the preset
// files themselves, so a preset that grows a widget a model cannot feed fails
// here rather than as an empty chart.
func TestEveryModelFeedsItsPresets(t *testing.T) {
	for _, m := range models {
		files, ok := modelPresets[m.ID]
		if !ok {
			t.Errorf("%s: say in modelPresets which bundled presets it feeds", m.ID)
			continue
		}
		tr, err := newTree(m.build(Identity{Name: "dev-01", Seed: 42}))
		if err != nil {
			t.Fatalf("%s: %v", m.ID, err)
		}
		for _, file := range files {
			for _, w := range readPreset(t, file).Widgets {
				instances := []string{""}
				if w.Discover != nil {
					column, err := parseOID(w.Discover.Walk)
					if err != nil {
						t.Fatal(err)
					}
					instances = nil
					for e := tr.next(column, nil); e != nil && e.oid.hasPrefix(column); e = tr.next(e.oid, nil) {
						instances = append(instances, strings.TrimPrefix(e.oid[len(column):].String(), "."))
					}
					if len(instances) == 0 {
						t.Errorf("%s, %s, %q: walking %s finds nothing", m.ID, file, w.Title, w.Discover.Walk)
						continue
					}
				}
				for _, template := range w.OIDs {
					for _, instance := range instances {
						name := strings.ReplaceAll(template, "{#}", instance)
						if id, err := parseOID(name); err != nil || tr.get(id) == nil {
							t.Errorf("%s, %s, %q: %s is not answered", m.ID, file, w.Title, name)
						}
					}
				}
			}
		}
	}
}

// The preset matched to Cisco equipment claims a simulated Catalyst and a
// simulated ISR, as it would the real ones — identified by their sysObjectID,
// read the way SnmpLens reads it when a target is added — and no other model.
func TestTheCiscoPresetClaimsTheSimulatedCiscos(t *testing.T) {
	prefixes := readPreset(t, "cisco-generic.json").Match.SysObjectIDPrefix
	if len(prefixes) == 0 {
		t.Fatal("cisco-generic.json claims nothing; the test reads the wrong shape")
	}
	for _, m := range models {
		tr, err := newTree(m.build(Identity{Name: "dev-01", Seed: 1}))
		if err != nil {
			t.Fatalf("%s: %v", m.ID, err)
		}
		sysObjectID := sysObjectIDOf(t, tr)
		cisco := strings.HasPrefix(m.ID, "cisco-")
		if claimed := preset.MatchDepth(prefixes, sysObjectID) > 0; claimed != cisco {
			t.Errorf("%s (%s): the Cisco preset claims it: %v, want %v", m.ID, sysObjectID, claimed, cisco)
		}
	}
}

// Every model goes out over the wire whole: every value encodes and a walk
// visits every object a request may read. A value gosnmp cannot encode would
// otherwise fail the one response that carries it, as a timeout.
func TestEveryModelServesAWalk(t *testing.T) {
	for _, m := range models {
		t.Run(m.ID, func(t *testing.T) {
			objects := m.build(Identity{Name: "dev-01", Seed: 7})
			a := startAgent(t, Config{Versions: []string{"v2c"}, Community: "public", Objects: objects})
			// Everything a request may read: the model's objects less those only a
			// notification carries, and the agent's own.
			readable := len(a.tree.Load().entries)
			g := connect(t, manager(a, gosnmp.Version2c, "public"))
			n := 0
			// Both subtrees a device answers in: LLDP-MIB lives under
			// iso.std.iso8802, outside .1.3.6.1, as it does on a real device.
			for _, root := range []string{".1.0.8802", ".1.3.6.1"} {
				if err := g.BulkWalk(root, func(gosnmp.SnmpPDU) error {
					n++
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if n != readable {
				t.Errorf("walked %d objects of %d", n, readable)
			}
			res, err := g.Get([]string{".1.3.6.1.2.1.1.5.0"})
			if err != nil || text(res.Variables[0]) != "dev-01" {
				t.Errorf("sysName: %v, %v", res, err)
			}
			if s := a.Stats(); s.Faults != 0 {
				t.Errorf("%d answers failed to encode", s.Faults)
			}
		})
	}
}

// Every model makes a device: a category the interface files it under, and an
// engine ID in its vendor's MAC format.
func TestEveryModelMakesADevice(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Models() {
		if seen[m.ID] {
			t.Errorf("%s is listed twice", m.ID)
		}
		seen[m.ID] = true
		if m.Category == "" {
			t.Errorf("%s has no category", m.ID)
		}
		if _, err := NewEngineID(m.ID, "one"); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
	}
}

// Two devices of one model are two different machines.
func TestTwoDevicesOfOneModelDiffer(t *testing.T) {
	one := linuxServer.build(Identity{Name: "a", Seed: 1})
	two := linuxServer.build(Identity{Name: "b", Seed: 2})
	mac := func(objects []Object) string {
		for _, o := range objects {
			if o.OID == "1.3.6.1.2.1.2.2.1.6.2" {
				return string(o.Value.read(clock{}).([]byte))
			}
		}
		return ""
	}
	if mac(one) == "" || mac(one) == mac(two) {
		t.Error("two simulated servers share eth0's MAC address")
	}
}
