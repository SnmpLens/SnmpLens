package sysvc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/service"
)

// The service receives traps and resumes its sessions whatever the window's
// background preferences said — that is what it is for — on the window's trap
// port, or 162 when no window handed one over.
func TestTheServiceAlwaysListensAndResumes(t *testing.T) {
	cfg := serviceConfig(service.Config{TrapPort: 1162, RunInBackground: true, StartHidden: true}, true)
	if !cfg.AutoStartTrapListener || !cfg.AutoResumeMonitors || cfg.TrapPort != 1162 || cfg.RunInBackground || cfg.StartHidden {
		t.Errorf("from a window's: %+v", cfg)
	}
	if cfg := serviceConfig(service.Config{TrapPort: 1162}, false); cfg.TrapPort != 162 || !cfg.AutoStartTrapListener {
		t.Errorf("with none handed over: %+v", cfg)
	}
}

// What a window hands over holds credentials in the clear: it is gone once
// read, and no bundle at all is an empty one, the installer's.
func TestABundleIsReadOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	data, _ := json.Marshal(engine.Bundle{TrapEngineID: "8000000005010203040506070809"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := readBundle(path)
	if err != nil || b.TrapEngineID == "" {
		t.Fatalf("read %+v, %v", b, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("the bundle was left on disk")
	}
	if b, err := readBundle(""); err != nil || b.TrapEngineID != "" {
		t.Errorf("no bundle: %+v, %v", b, err)
	}
}

// An elevated step says what it did through a file, the only way it has.
func TestAnElevatedStepSaysWhatItDid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	WriteResult(path, Result{KeptHistory: true}, nil)
	if r, err := ReadResult(path); err != nil || !r.OK || !r.KeptHistory {
		t.Errorf("success read back as %+v, %v", r, err)
	}
	WriteResult(path, Result{}, errors.New("the port is taken"))
	if r, err := ReadResult(path); err != nil || r.OK || r.Error != "the port is taken" {
		t.Errorf("failure read back as %+v, %v", r, err)
	}
}
