// Package headless runs SnmpLens's supervision with no window: the poll clock,
// the thresholds, the trap listener and the notifications (internal/engine),
// on a data directory, until it is told to stop. It is what `SnmpLens
// --headless` runs in a terminal or under a service manager, and what the
// system service runs.
package headless

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/service"
)

// Options say where and as what the supervision runs.
type Options struct {
	// DataDir holds monitoring.db, the secret store, service.json and the trap
	// listener's engine ID.
	DataDir string
	// Version is the application's.
	Version string
	// Emitter reaches whatever watches the engine; nil says nothing.
	Emitter engine.Emitter
}

// DefaultDataDir is the desktop application's own directory, so that a
// headless run on the same machine and account supervises what the window set
// up.
func DefaultDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SnmpLens"), nil
}

// Host is the supervision running with no window.
type Host struct {
	eng *engine.Engine
}

// Start opens the engine on the data directory and starts it: the trap
// listener when service.json says it starts with SnmpLens, and the sessions
// running at the last exit when it says they resume. It refuses a directory
// another SnmpLens has open, and a database that cannot be opened: a
// supervision that journals nothing would run without saying so.
func Start(opts Options) (*Host, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("no data directory")
	}
	cfg, err := service.Load(opts.DataDir)
	if err != nil {
		log.Printf("WARNING: could not read %s, using defaults: %v", service.Path(opts.DataDir), err)
	}
	eng := engine.New(engine.Options{DataDir: opts.DataDir, Config: cfg, Emitter: opts.Emitter, Version: opts.Version})
	if err := eng.Open(); err != nil {
		eng.Close()
		return nil, err
	}
	eng.Start()
	return &Host{eng: eng}, nil
}

// Engine is the engine the host runs.
func (h *Host) Engine() *engine.Engine { return h.eng }

// Stop stops the supervision: the trap listener first, the database last.
func (h *Host) Stop() { h.eng.Close() }

// Run starts the supervision and stops it when ctx is done.
func Run(ctx context.Context, opts Options) error {
	h, err := Start(opts)
	if err != nil {
		return err
	}
	log.Printf("SnmpLens %s supervises from %s with no window: %d session(s) polling, trap listener %s",
		opts.Version, opts.DataDir, len(h.eng.MonitorRunning()), onOff(h.eng.TrapListenerRunning()))
	<-ctx.Done()
	h.Stop()
	log.Printf("SnmpLens stopped.")
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
