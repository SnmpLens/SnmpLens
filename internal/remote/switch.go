package remote

import "sync"

// Switch is a Host that forwards to another, which can be replaced while calls
// are made: the window drives its own engine, then the service once it runs,
// then its own again when the service stops. The methods in switch_gen.go each
// forward one call to whichever is current.
type Switch struct {
	mu  sync.RWMutex
	cur Host
}

// NewSwitch starts on h.
func NewSwitch(h Host) *Switch { return &Switch{cur: h} }

// Current is the host calls go to now.
func (s *Switch) Current() Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Set makes later calls go to h. Calls already under way finish where they
// started.
func (s *Switch) Set(h Host) {
	s.mu.Lock()
	s.cur = h
	s.mu.Unlock()
}
