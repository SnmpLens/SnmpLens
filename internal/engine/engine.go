// Package engine is SnmpLens's supervision: the poll clock and its thresholds,
// the trap listener, the event journal, and the routing and delivery of
// notifications — everything that has to keep working with no window open.
//
// It imports nothing of Wails. A window runs it in its own process and passes
// it an Emitter that reaches the webview; the system service runs it with no
// window at all and passes one that reaches the windows connected to it. The
// Wails runtime ends the process when it is handed a context it did not issue,
// which is what a headless run would hand it: keeping the runtime out of this
// package is what makes that impossible rather than merely avoided.
package engine

import (
	"fmt"
	"log"
	"path/filepath"
	"sync"

	"SnmpLens/pkg/dirlock"
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
)

// Emitter is where the engine says what happened to whatever shows it: an
// event journalled, samples polled, a session that cannot keep up, a trap.
type Emitter interface {
	Emit(name string, data ...interface{})
}

// Options are what an engine is made from.
type Options struct {
	// DataDir holds monitoring.db, the trap listener's engine ID and, unless
	// Secrets is given, the secret store.
	DataDir string
	// Secrets is a store already open on DataDir, shared with the window that
	// runs the engine in its own process: two stores over one file would each
	// write the whole of it, and the last write would win. Nil opens one.
	Secrets secrets.Store
	// Config holds the background-mode preferences the engine acts on: the
	// trap listener started with it and on which port, the sessions that were
	// running resumed.
	Config service.Config
	// Emitter reaches whatever shows the engine; nil says nothing.
	Emitter Emitter
	// Version is the application's, which notification templates expose.
	Version string
}

// Engine is the supervision, open on one data directory.
type Engine struct {
	dataDir string
	cfg     service.Config
	emitter Emitter

	// snmpClient polls the sessions and runs the trap listener. It is the
	// engine's own: the window's client sends what a person asks for, and its
	// context is the Wails runtime's.
	snmpClient *snmp.Client
	storage    *storage.Storage
	evaluator  *monitor.Evaluator
	router     *eventRouter
	dispatcher *notify.Dispatcher
	secrets    secrets.Store
	scheduler  *monitor.Scheduler
	// trapEngineID is the trap listener's snmpEngineID (trapengine.go).
	trapEngineID []byte
	// onSessionsChanged is told when a session starts or stops: the window's
	// tray counts them.
	onSessionsChanged func()

	// release lets go of the data directory (pkg/dirlock).
	release   func()
	closeOnce sync.Once
}

// New makes an engine; Open opens it.
func New(opts Options) *Engine {
	eng := &Engine{dataDir: opts.DataDir, cfg: opts.Config, emitter: opts.Emitter, secrets: opts.Secrets}
	// Made here and not in Open, so an engine whose directory is another
	// process's still answers every call — with nothing — rather than panic.
	//lint:ignore SA1012 a nil context is how pkg/snmp tells a client no runtime is behind it
	eng.snmpClient = snmp.NewClient(nil)
	eng.snmpClient.SetEmitter(eng.emit)
	if opts.Version != "" {
		// The template vocabulary exposes the version; pkg/notify keeps it in a
		// package var rather than importing pkg/updater for one string.
		notify.AppVersion = opts.Version
	}
	return eng
}

// Open opens the database and the secret store, and starts what consumes the
// journal: the router, the outbox, the poll clock. Nothing that PRODUCES is
// started — the trap listener and the sessions wait for Start — so what was
// journalled and not routed before the last exit is replayed first, with
// nothing arriving to interleave with it on the watermark.
//
// An error says the directory is another process's (dirlock.ErrInUse), and
// nothing was opened, or that the database could not be opened. Either way the
// engine is still usable, as the window always was — it polls nothing and
// journals nothing — and a host that cannot do without it refuses to run.
func (eng *Engine) Open() error {
	// One engine per directory: a second would poll every session into the same
	// database and route every event to the same destinations a second time.
	release, err := dirlock.Lock(eng.dataDir)
	if err != nil {
		return fmt.Errorf("%s: %w", eng.dataDir, err)
	}
	eng.release = release
	var storeErr error
	store, err := storage.Init(filepath.Join(eng.dataDir, "monitoring.db"))
	if err != nil {
		storeErr = fmt.Errorf("the monitoring database: %w", err)
		log.Printf("WARNING: Failed to initialize monitoring storage: %v", err)
	} else {
		eng.storage = store
		eng.initEvaluator()
		eng.initDispatcher()

		// The router BEFORE the recorder is wired: recordEvent hands events to
		// it, and a producer must never find it absent and silently fall back.
		eng.router = newEventRouter(eng)
		eng.router.start()

		// Anything journalled but not routed before the last shutdown — a crash,
		// a kill, a flush that could not be written. Done before any producer
		// starts, so replay and live routing cannot interleave on the watermark.
		eng.replayUnrouted()

		// Traps are journalled by the SNMP client itself, in its listener
		// goroutine, before anything is emitted.
		eng.snmpClient.SetRecorder(events.RecorderFunc(eng.recordEvent))
	}

	// The secret store, INDEPENDENT of the database: a corrupt monitoring.db
	// must not lock anyone out of the credentials their sessions poll with.
	if eng.secrets == nil {
		if s, err := secrets.Open(eng.dataDir); err != nil {
			log.Printf("WARNING: secret storage unavailable, stored credentials cannot be read: %v", err)
		} else {
			eng.secrets = s
		}
	}

	// The trap listener's engine ID, before Start can start the listener: a
	// device sending it SNMPv3 INFORMs is configured with it.
	eng.trapEngineID = loadTrapEngineID(eng.dataDir)
	eng.snmpClient.SetTrapEngineID(eng.trapEngineID)

	// The poll clock lives here so that monitoring, thresholds and the
	// notification routes keep working with no window open.
	eng.initScheduler()
	return storeErr
}

// Start starts what produces: the trap listener when the preferences say it
// starts with SnmpLens, and the sessions that were running at the last exit.
func (eng *Engine) Start() {
	if eng.cfg.AutoStartTrapListener {
		port := eng.cfg.TrapPort
		// The users last applied from a window. This may run before any window
		// exists, and without them a background start used to hear v1 and v2c
		// and drop every v3 notification.
		info, err := eng.snmpClient.StartTrapListener(port, eng.loadTrapUsers())
		if err != nil {
			log.Printf("WARNING: could not auto-start the trap listener on port %d: %v", port, err)
			eng.RecordSystemEvent(events.KindSystemInfo, "major",
				"The trap listener could not bind its port at startup: "+err.Error())
		} else {
			log.Printf("trap listener auto-started on port %d with %d SNMPv3 users", port, info.Users)
			for _, r := range info.Refused {
				log.Printf("WARNING: the trap listener did not take SNMPv3 user %q: %s", r.User, r.Reason)
			}
		}
	}
	eng.resumeActiveSessions()
}

// Close stops the engine: every producer before any consumer, and the
// database last.
func (eng *Engine) Close() {
	eng.closeOnce.Do(eng.close)
}

func (eng *Engine) close() {
	// The trap listener FIRST. It is the one producer fed from outside: a
	// datagram arriving during teardown runs handleTrap on gosnmp's goroutine,
	// which journals and routes through storage the lines below are about to
	// close. Stopping every producer before any consumer is the only ordering
	// in which the rest of this holds.
	if eng.snmpClient != nil {
		eng.snmpClient.StopTrapListener()
	}
	// Then the router: every producer is stopped, so what it has queued is all
	// there will ever be. Anything it cannot write stays above the watermark
	// and is replayed on the next start.
	if eng.router != nil {
		eng.router.stop()
	}
	// Then the poll clock: its goroutines write samples through storage, and
	// letting them run into a closed database would log failures for work that
	// was actually fine.
	if eng.scheduler != nil {
		eng.scheduler.StopAll()
	}
	// The dispatcher BEFORE the storage: it queries the outbox on its own
	// goroutine, and a drain racing a closed database would log spurious
	// failures against deliveries that are actually fine.
	if eng.dispatcher != nil {
		eng.dispatcher.Stop()
	}
	if eng.storage != nil {
		if err := eng.storage.Close(); err != nil {
			log.Printf("Error closing monitoring storage: %v", err)
		}
	}
	if eng.release != nil {
		eng.release()
	}
}

// emit tells whatever shows the engine.
func (eng *Engine) emit(name string, data ...interface{}) {
	if eng.emitter != nil {
		eng.emitter.Emit(name, data...)
	}
}

// SetEmitter makes e what the engine tells: a service makes the engine first,
// then the server that serves it, which is the emitter. Set before Open: what
// Open starts reads it from goroutines of its own.
func (eng *Engine) SetEmitter(e Emitter) { eng.emitter = e }

// DataDir is the directory the engine is open on.
func (eng *Engine) DataDir() string { return eng.dataDir }

// Secrets is the engine's secret store, nil when it could not be opened.
func (eng *Engine) Secrets() secrets.Store { return eng.secrets }

// Storage is the engine's database, nil when it could not be opened. A window
// running the engine in its own process reads it for what it backs up.
func (eng *Engine) Storage() *storage.Storage { return eng.storage }

// TrapListenerRunning reports whether the trap listener is listening.
func (eng *Engine) TrapListenerRunning() bool {
	return eng.snmpClient != nil && eng.snmpClient.TrapListenerRunning()
}

// StopTrapListener stops listening for SNMP traps.
func (eng *Engine) StopTrapListener() {
	if eng.snmpClient != nil {
		eng.snmpClient.StopTrapListener()
	}
}

// Journal records an event that happened outside the engine — a refused SET,
// a MIB that failed to load — logging rather than propagating a failure: every
// caller is on a path whose real work already succeeded or already reported
// its own error.
func (eng *Engine) Journal(e events.Event) {
	if err := eng.recordEvent(e, ""); err != nil {
		log.Printf("WARNING: could not journal a %s event: %v", e.Kind, err)
	}
}

// RecordSystemEvent journals a system-category event, ignoring the error: it
// is used on paths that are already reporting a problem.
func (eng *Engine) RecordSystemEvent(kind, severity, summary string) {
	if eng.storage == nil {
		return
	}
	_ = eng.recordEvent(events.Event{
		Category: events.CategorySystem,
		Kind:     kind,
		Severity: severity,
		Summary:  summary,
		TitleKey: "events.kind." + kind,
	}, "")
}

// OnSessionsChanged has fn called whenever a session starts or stops. Set
// before Start.
func (eng *Engine) OnSessionsChanged(fn func()) { eng.onSessionsChanged = fn }

// sessionsChanged tells the window a session started or stopped.
func (eng *Engine) sessionsChanged() {
	if eng.onSessionsChanged != nil {
		eng.onSessionsChanged()
	}
	eng.emit("monitor:state")
}

// secretsBackend names the protection actually in use, so an error can say
// what is true here rather than what was hoped for.
func (eng *Engine) secretsBackend() string {
	if eng.secrets == nil {
		return "unavailable"
	}
	return eng.secrets.Backend()
}

// SetConfig replaces the background-mode preferences the engine acts on. It
// starts and stops nothing: they are read by the next Start.
func (eng *Engine) SetConfig(cfg service.Config) { eng.cfg = cfg }
