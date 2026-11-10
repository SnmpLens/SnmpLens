package app

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"

	"SnmpLens/pkg/simulator"
)

// The bench's cables (pkg/simulator, bench.go): kept in simulator.json beside
// the devices, checked against their ports, and applied to a running device
// without restarting it — a switch does not reboot when a cable is plugged in,
// and its uptime and counters are what the tests on the bench watch.

// unplugged is a cable that no longer fits the devices, and why.
type unplugged struct {
	link simulator.Link
	err  error
}

// keptLinks are the links that still fit devices, one at a time: a cable whose
// device or port has gone is dropped, with what was wrong with it, and the rest
// are kept.
func keptLinks(devices []simulator.Device, links []simulator.Link) ([]simulator.Link, []unplugged) {
	out := []simulator.Link{}
	var dropped []unplugged
	c := simulator.NewLinkChecker(devices)
	for _, l := range links {
		if err := c.Add(l); err != nil {
			dropped = append(dropped, unplugged{l, err})
			continue
		}
		out = append(out, l)
	}
	return out, dropped
}

// settleBenchLocked unplugs the cables the devices no longer have a port for,
// and recables the running devices as the bench then stands. Saving a device
// refuses to leave a cable in a port it no longer has; a device restored from
// a backup, or a model imported again with fewer interfaces, cannot be refused,
// and left in, one such cable failed every cable checked after it — a backup's
// were all refused, naming a port nobody had asked for. What was unplugged is
// returned, to be said; a failure to save it is returned as an error.
func (s *simulatorService) settleBenchLocked() ([]unplugged, error) {
	links, dropped := keptLinks(s.devices, s.links)
	if len(dropped) > 0 {
		if err := s.writeBench(s.devices, links); err != nil {
			return nil, fmt.Errorf("saving the simulated devices: %w", err)
		}
		s.links = links
	}
	s.rewireLocked()
	return dropped, nil
}

// settleModelsLocked settles the bench once models were imported, and says each
// cable it unplugged beside the import of a model at one of its ends.
func (s *simulatorService) settleModelsLocked(results []SimulatorModelImportResult) {
	dropped, err := s.settleBenchLocked()
	if err != nil {
		log.Printf("simulator: %v", err)
	}
	modelOf := make(map[string]string, len(s.devices))
	for _, d := range s.devices {
		modelOf[d.ID] = d.Model
	}
	for _, u := range dropped {
		said := false
		for r := range results {
			res := &results[r]
			if !res.Success || !res.Replaced {
				continue
			}
			if modelOf[u.link.A.Device] == res.ModelID || modelOf[u.link.B.Device] == res.ModelID {
				res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "cableUnplugged", Detail: u.err.Error()})
				said = true
			}
		}
		if !said {
			log.Printf("simulator: a cable is unplugged: %v", u.err)
		}
	}
}

// planLocked is the wiring of every cabled device as the bench stands.
func (s *simulatorService) planLocked() map[string]*simulator.Wiring {
	return simulator.PlanBench(s.devices, s.links)
}

// rewireLocked applies the bench as it stands to every running device whose
// wiring it changes, and to no other: what a SET wrote to a device is not lost
// to a cable plugged in elsewhere.
func (s *simulatorService) rewireLocked() {
	plan := s.planLocked()
	for _, d := range s.devices {
		if _, running := s.fleet.Status(d.ID); !running {
			continue
		}
		w := plan[d.ID]
		if reflect.DeepEqual(w, s.wired[d.ID]) {
			continue
		}
		d.Wiring = w
		if err := s.fleet.Rewire(d); err != nil {
			log.Printf("simulator: %s could not be recabled: %v", d.Name, err)
			continue
		}
		s.wired[d.ID] = w
	}
}

// ListSimulatorLinks lists the cables between the simulated devices.
//
// Named List… for tools/genbridge.mjs, which gives such a binding an empty
// ARRAY as its screenshot fixture.
func (a *App) ListSimulatorLinks() []simulator.Link {
	out := []simulator.Link{}
	s := a.sim
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(out, s.links...)
}

// SimulatorPorts lists the ports of a simulated device a cable can go into, as
// the device is saved.
func (a *App) SimulatorPorts(id string) ([]simulator.Port, error) {
	s := a.sim
	if s == nil {
		return []simulator.Port{}, errNoSimulator
	}
	s.mu.Lock()
	i := s.index(id)
	var d simulator.Device
	if i >= 0 {
		d = s.devices[i]
	}
	s.mu.Unlock()
	if i < 0 {
		return []simulator.Port{}, fmt.Errorf("there is no simulated device %q", id)
	}
	ports, err := simulator.Ports(d)
	if ports == nil {
		ports = []simulator.Port{}
	}
	return ports, err
}

// SimulatorConnect plugs a cable between two ports of two simulated devices,
// and returns it with the ID it was given. The devices at its ends that are
// running hear each other at once.
func (a *App) SimulatorConnect(l simulator.Link) (simulator.Link, error) {
	s := a.sim
	if s == nil {
		return simulator.Link{}, errNoSimulator
	}
	l.ID = simulator.NewLinkID()
	l.A.Port, l.B.Port = strings.TrimSpace(l.A.Port), strings.TrimSpace(l.B.Port)
	s.mu.Lock()
	defer s.mu.Unlock()
	links := append(slices.Clone(s.links), l)
	if err := simulator.CheckLinks(s.devices, links); err != nil {
		return simulator.Link{}, err
	}
	if err := s.writeBench(s.devices, links); err != nil {
		return simulator.Link{}, fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.links = links
	s.rewireLocked()
	a.emitSimulatorChanged()
	return l, nil
}

// SimulatorDisconnect unplugs a cable.
func (a *App) SimulatorDisconnect(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.links, func(l simulator.Link) bool { return l.ID == id })
	if i < 0 {
		return errors.New("that cable is no longer there")
	}
	links := slices.Delete(slices.Clone(s.links), i, i+1)
	if err := s.writeBench(s.devices, links); err != nil {
		return fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.links = links
	s.rewireLocked()
	a.emitSimulatorChanged()
	return nil
}

// restoreSimulatorLinks adds the cables a backup holds and replaces those it
// shares an ID with, as the devices are restored. A cable that does not fit the
// devices as they now are is said, and the others are kept.
func (a *App) restoreSimulatorLinks(links []simulator.Link) []string {
	s := a.sim
	if s == nil || len(links) == 0 {
		return nil
	}
	var problems []string
	s.mu.Lock()
	defer s.mu.Unlock()
	next := slices.Clone(s.links)
	for _, l := range links {
		candidate := slices.DeleteFunc(slices.Clone(next), func(o simulator.Link) bool { return o.ID == l.ID })
		candidate = append(candidate, l)
		if err := simulator.CheckLinks(s.devices, candidate); err != nil {
			problems = append(problems, fmt.Sprintf("a cable: %v", err))
			continue
		}
		next = candidate
	}
	if err := s.writeBench(s.devices, next); err != nil {
		return append(problems, fmt.Sprintf("saving the simulated devices: %v", err))
	}
	s.links = next
	s.rewireLocked()
	a.emitSimulatorChanged()
	return problems
}

// previewWiring is where the device being edited sits on the bench, as the
// editor holds it: the cables to it as they are, its peers as they are saved.
func (a *App) previewWiring(d simulator.Device) *simulator.Wiring {
	s := a.sim
	if s == nil || d.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	devices := slices.Clone(s.devices)
	i := slices.IndexFunc(devices, func(o simulator.Device) bool { return o.ID == d.ID })
	if i < 0 {
		return nil
	}
	devices[i] = d.WithoutSecrets()
	return simulator.PlanBench(devices, s.links)[d.ID]
}
