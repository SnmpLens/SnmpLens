//go:build windows

package sysvc

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"SnmpLens/internal/headless"
	"SnmpLens/pkg/secrets"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// Event identifiers in the Application log.
const (
	eventStarted = 1
	eventStopped = 2
	eventFailed  = 3
)

// logLimit is the size the service's log reaches before it starts again, the
// previous one kept beside it.
const logLimit = 5 << 20

// IsService reports whether this process was started by the service manager.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// Run is the service's main: it hands control to the service manager and
// returns when the service has stopped.
func Run(version string) error {
	return svc.Run(Name, &handler{version: version})
}

type handler struct {
	version string
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	elog, _ := eventlog.Open(Name)
	if elog != nil {
		defer elog.Close()
	}
	report := func(id uint32, isError bool, msg string) {
		if elog == nil {
			return
		}
		if isError {
			_ = elog.Error(id, msg)
		} else {
			_ = elog.Info(id, msg)
		}
	}
	closeLog := serviceLog()
	defer closeLog()

	statusDir, _ := StatusDir()
	fail := func(msg string) (bool, uint32) {
		log.Print(msg)
		report(eventFailed, true, msg)
		writeStatus(statusDir, Status{State: "stopped", Version: h.version, Error: msg})
		// The service manager restarts a service that failed; the event log and
		// the published status say why, so the retries are not silent.
		return true, 1
	}

	dataDir, err := DataDir()
	if err != nil {
		return fail("SnmpLens could not find its data directory: " + err.Error())
	}
	store, err := secrets.OpenMachine(dataDir)
	if err != nil {
		return fail("SnmpLens could not open its credential store: " + err.Error())
	}
	// Without its pipe the service still supervises; windows just cannot reach
	// it, and the event log says why.
	control, err := ListenControl()
	if err != nil {
		log.Printf("windows cannot connect: %v", err)
		report(eventFailed, true, "SnmpLens supervises, but windows cannot connect to it: "+err.Error())
		control = nil
	}
	host, err := headless.Start(headless.Options{DataDir: dataDir, Version: h.version, Secrets: store, Control: control})
	if err != nil {
		return fail("SnmpLens could not start: " + err.Error())
	}
	eng := host.Engine()
	started := fmt.Sprintf("SnmpLens %s started: %d monitoring session(s) polling, trap listener %s.",
		h.version, len(eng.MonitorRunning()), map[bool]string{true: "listening", false: "not listening"}[eng.TrapListenerRunning()])
	log.Print(started)
	report(eventStarted, false, started)
	writeStatus(statusDir, Status{State: "running", Version: h.version})

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			status <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			host.Stop()
			report(eventStopped, false, "SnmpLens stopped.")
			writeStatus(statusDir, Status{State: "stopped", Version: h.version})
			return false, 0
		}
	}
	host.Stop()
	return false, 0
}

// serviceLog sends the log to the service's own file, starting it again once it
// passes logLimit with the previous one kept. A log that cannot be opened goes
// nowhere rather than stopping the service: supervising matters more than its
// diary.
func serviceLog() func() {
	path, err := LogPath()
	if err != nil {
		return func() {}
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > logLimit {
		_ = os.Rename(path, path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	return func() { f.Close() }
}
