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
	"net"
	"os"
	"path/filepath"

	"SnmpLens/internal/engine"
	"SnmpLens/internal/remote"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
)

// Options say where and as what the supervision runs.
type Options struct {
	// DataDir holds monitoring.db, the secret store, service.json and the trap
	// listener's engine ID.
	DataDir string
	// Version is the application's.
	Version string
	// Emitter reaches whatever watches the engine; nil says nothing. Ignored
	// with Control, whose server is the emitter.
	Emitter engine.Emitter
	// Secrets is the store to supervise with; nil opens the data directory's.
	// The system service passes one bound to the computer (secrets.OpenMachine).
	Secrets secrets.Store
	// Control, when given, is where windows connect to drive the supervision
	// (internal/remote): the system service's named pipe.
	Control net.Listener
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
	srv *remote.Server
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
	eng := engine.New(engine.Options{DataDir: opts.DataDir, Config: cfg, Emitter: opts.Emitter,
		Secrets: opts.Secrets, Version: opts.Version})
	h := &Host{eng: eng}
	if opts.Control != nil {
		// The server is the engine's emitter: every window connected is told
		// what a window running the engine itself would be.
		h.srv = remote.NewServer(eng, opts.Version)
		eng.SetEmitter(h.srv)
	}
	if err := eng.Open(); err != nil {
		eng.Close()
		if opts.Control != nil {
			opts.Control.Close()
		}
		return nil, err
	}
	eng.Start()
	if h.srv != nil {
		go func() { _ = h.srv.Serve(opts.Control) }()
	}
	return h, nil
}

// Engine is the engine the host runs.
func (h *Host) Engine() *engine.Engine { return h.eng }

// Stop stops the supervision: the windows let go of first, then the trap
// listener, and the database last.
func (h *Host) Stop() {
	if h.srv != nil {
		h.srv.Close()
	}
	h.eng.Close()
}

// Windows is how many windows drive the supervision.
func (h *Host) Windows() int {
	if h.srv == nil {
		return 0
	}
	return h.srv.Windows()
}

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
