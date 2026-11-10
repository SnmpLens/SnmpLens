// Package sysvc runs SnmpLens's supervision as a system service: a process the
// system starts at boot, before anyone signs in, under an account of its own,
// with its database and credentials in a machine-wide directory only
// administrators and the service can enter. It polls the monitoring sessions,
// evaluates their thresholds, receives traps and sends the notifications — the
// engine a window would run, with nobody signed in.
//
// Installing it needs an administrator, once. The desktop application does the
// rest unprivileged — it prepares what to hand over, then runs only the steps
// that need elevation in a separate, elevated copy of itself — so the window
// never runs elevated and the prompt names exactly what is being done.
package sysvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/service"
)

const (
	// Name is the service's name in the service manager.
	Name = "SnmpLens"
	// DisplayName is what the service manager shows.
	DisplayName = "SnmpLens supervision"
	// Description says what it is to someone who did not install it.
	Description = "Polls the SnmpLens monitoring sessions, receives SNMP traps and sends their notifications, from boot, with nobody signed in."
)

// ErrUnsupported is returned where there is no service to install.
var ErrUnsupported = errors.New("the system service is available on Windows and on Linux with systemd; elsewhere, run SnmpLens --headless under the platform's service manager")

// ErrCancelled means the user declined the administrator prompt.
var ErrCancelled = errors.New("the administrator prompt was declined")

// ErrAccessDenied means this account is not one of the service's operators.
var ErrAccessDenied = errors.New("this account is not allowed to drive the service")

// ErrNotService means something other than the service holds its channel.
var ErrNotService = errors.New("the program answering on the service's channel is not the SnmpLens service")

// Operator is an account allowed to drive the service from a window.
type Operator struct {
	// SID identifies the account: its SID on Windows.
	SID  string `json:"sid"`
	Name string `json:"name,omitempty"`
}

// operatorsFile lists the accounts allowed to drive the service from a window,
// in the service's own directory.
const operatorsFile = "operators.json"

func loadOperators(dir string) ([]Operator, error) {
	data, err := os.ReadFile(filepath.Join(dir, operatorsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ops []Operator
	if err := json.Unmarshal(data, &ops); err != nil {
		return nil, fmt.Errorf("read %s: %w", operatorsFile, err)
	}
	return ops, nil
}

func saveOperators(dir string, ops []Operator) error {
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, operatorsFile), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", operatorsFile, err)
	}
	return nil
}

// readBundle reads what the window prepared, and deletes the file at once: it
// holds credentials in the clear. No file is an empty bundle — the installer's,
// which has no window's configuration to hand over.
func readBundle(path string) (engine.Bundle, error) {
	if path == "" {
		return engine.Bundle{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return engine.Bundle{}, fmt.Errorf("read the configuration to install: %w", err)
	}
	_ = os.Remove(path)
	var b engine.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return engine.Bundle{}, fmt.Errorf("read the configuration to install: %w", err)
	}
	return b, nil
}

// serviceConfig is what the service supervises with: the window's trap port,
// the trap listener started and the sessions resumed whatever the window's
// background preferences said — receiving with nobody signed in is what the
// service is for. Without a window's, the defaults: port 162.
func serviceConfig(from service.Config, given bool) service.Config {
	cfg := service.Defaults()
	if given {
		cfg = from
	}
	cfg.AutoStartTrapListener = true
	cfg.AutoResumeMonitors = true
	cfg.RunInBackground, cfg.StartHidden = false, false
	if cfg.TrapPort == 0 {
		cfg.TrapPort = 162
	}
	return cfg
}

// Info is the service manager's view of the service.
type Info struct {
	Installed bool `json:"installed"`
	// State is stopped, starting, running, stopping, or unknown.
	State      string `json:"state"`
	Executable string `json:"executable,omitempty"`
	Account    string `json:"account,omitempty"`
	// PID is the service's process while it runs.
	PID uint32 `json:"pid,omitempty"`
}

// Result is what an elevated command reports back to the window that started
// it. The elevated copy has no console and no window, so a file is the only way
// to say more than an exit code.
type Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// KeptHistory says the service had a history of its own and kept it, rather
	// than taking the window's.
	KeptHistory bool `json:"keptHistory,omitempty"`
}

// WriteResult records the outcome of an elevated command. A failure to write it
// is not reported: the exit code still says whether it worked.
func WriteResult(path string, r Result, err error) {
	if path == "" {
		return
	}
	r.OK = err == nil
	if err != nil {
		r.Error = err.Error()
	}
	data, _ := json.Marshal(r)
	if os.WriteFile(path, data, 0o600) == nil {
		ownLikeDir(path)
	}
}

// RunsFromCommandLine reports whether the service manager starts the service as
// `SnmpLens service run` — systemd does — rather than recognisably as a
// service, as Windows' does.
func RunsFromCommandLine() bool { return runFromCommandLine }

// ReadResult reads what an elevated command recorded.
func ReadResult(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, err
	}
	return r, nil
}

// Status is what the service publishes of itself where any local user may read
// it: whether it runs, and why it stopped when it did — never what it supervises.
type Status struct {
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

const statusFile = "status.json"

func writeStatus(dir string, s Status) {
	if dir == "" {
		return
	}
	data, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(dir, statusFile), data, 0o644)
}

// ReadStatus reads what the service published.
func ReadStatus(dir string) (Status, bool) {
	data, err := os.ReadFile(filepath.Join(dir, statusFile))
	if err != nil {
		return Status{}, false
	}
	var s Status
	if json.Unmarshal(data, &s) != nil {
		return Status{}, false
	}
	return s, true
}
