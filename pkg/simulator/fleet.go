package simulator

import (
	"errors"
	"sync"
)

// ErrRunning is returned for a device that is already running.
var ErrRunning = errors.New("simulator: the device is already running")

// Fleet runs simulated devices, at most one agent per device.
//
// It keeps nothing of its own: a device's configuration, its secrets and its
// boot count belong to whoever calls it — which is also what lets the fleet be
// tested without a file or a keychain.
type Fleet struct {
	mu     sync.Mutex
	agents map[string]*Agent
	// RowStatus is given to every device the fleet starts: see
	// Config.RowStatus. Set before the first start.
	RowStatus func(instance string) (column string, ok bool)
}

// NewFleet returns a fleet running nothing.
func NewFleet() *Fleet { return &Fleet{agents: map[string]*Agent{}} }

// Start runs d. d.EngineBoots is the count for THIS run: the caller raises it
// and keeps it before calling, since a device that starts again must come back
// with a higher count and the fleet has nowhere to keep one.
func (f *Fleet) Start(d Device) error {
	if err := d.Validate(); err != nil {
		return err
	}
	cfg, err := d.config()
	if err != nil {
		return err
	}
	cfg.RowStatus = f.RowStatus
	// Outside the lock: localising the users' keys hashes a megabyte each.
	a, err := NewAgent(cfg)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agents == nil {
		f.agents = map[string]*Agent{}
	}
	if f.agents[d.ID] != nil {
		return ErrRunning
	}
	if err := a.Start(); err != nil {
		return err
	}
	f.agents[d.ID] = a
	return nil
}

// Stop stops a device, reporting whether it was running.
func (f *Fleet) Stop(id string) bool {
	f.mu.Lock()
	a := f.agents[id]
	delete(f.agents, id)
	f.mu.Unlock()
	if a == nil {
		return false
	}
	a.Stop()
	return true
}

// StopAll stops every device.
func (f *Fleet) StopAll() {
	f.mu.Lock()
	agents := f.agents
	f.agents = map[string]*Agent{}
	f.mu.Unlock()
	for _, a := range agents {
		a.Stop()
	}
}

// Notify has a running device send the notification named name now; see
// Agent.Notify.
func (f *Fleet) Notify(id, name string) ([]Delivery, error) {
	f.mu.Lock()
	a := f.agents[id]
	f.mu.Unlock()
	if a == nil {
		return nil, ErrNotRunning
	}
	return a.Notify(name)
}

// Rewire has a running device answer as d describes it now — recabled on the
// bench — without restarting it (Agent.Rewire). A device that is not running
// has nothing to rewire: it is built as it is cabled when it starts.
func (f *Fleet) Rewire(d Device) error {
	f.mu.Lock()
	a := f.agents[d.ID]
	f.mu.Unlock()
	if a == nil {
		return ErrNotRunning
	}
	objs, contexts, err := d.views()
	if err != nil {
		return err
	}
	return a.Rewire(objs, contexts)
}

// SetFaults changes what a running device does wrong, without restarting it;
// see Agent.SetFaults.
func (f *Fleet) SetFaults(id string, faults Faults) error {
	f.mu.Lock()
	a := f.agents[id]
	f.mu.Unlock()
	if a == nil {
		return ErrNotRunning
	}
	return a.SetFaults(faults)
}

// Status reports whether a device is running, and its counters if it is.
func (f *Fleet) Status(id string) (Stats, bool) {
	f.mu.Lock()
	a := f.agents[id]
	f.mu.Unlock()
	if a == nil {
		return Stats{}, false
	}
	return a.Stats(), true
}
