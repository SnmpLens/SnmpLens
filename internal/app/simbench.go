package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"

	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/simulator"

)

// A bench of simulated devices as a file (pkg/simulator, DeviceFile): exported
// with or without the passwords — the renderer asks, and the file says which —
// and imported as NEW devices of this machine, each with an ID, an engine and,
// when the address it names is taken, an address of its own. Duplicating a
// device is the same making of a new one, from a device rather than a file.

// SimulatorDeviceImportResult is what became of one device of a file.
type SimulatorDeviceImportResult struct {
	// Name is the device's, or the file's when the file as a whole was refused.
	Name     string                  `json:"name"`
	ID       string                  `json:"id,omitempty"`
	Success  bool                    `json:"success"`
	Error    string                  `json:"error,omitempty"`
	Warnings []SimulatorModelWarning `json:"warnings"`
}

// SimulatorExportDevicesDialog writes simulated devices — those named, or every
// one when none is — to a file the user names, with their passwords only when
// withSecrets says so. It returns where the file went, or "" when the dialog was
// cancelled.
func (a *App) SimulatorExportDevicesDialog(ids []string, withSecrets bool) (string, error) {
	data, name, err := a.exportDevices(ids, withSecrets)
	if err != nil {
		return "", err
	}
	dest, err := a.saveFile("Export simulated devices", name, fileFilter("Simulated devices (JSON)", "*.json"))
	if err != nil || dest == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(dest), ".json") {
		dest += ".json"
	}
	// A file holding passwords is its owner's alone to read.
	perm := os.FileMode(0o644)
	if withSecrets {
		perm = 0o600
	}
	if err := os.WriteFile(dest, data, perm); err != nil {
		return "", fmt.Errorf("writing %s: %w", filepath.Base(dest), err)
	}
	return dest, nil
}

// exportDevices is the file the devices are exported as, and the name it is
// offered under.
func (a *App) exportDevices(ids []string, withSecrets bool) ([]byte, string, error) {
	s := a.sim
	if s == nil {
		return nil, "", errNoSimulator
	}
	s.mu.Lock()
	var chosen []simulator.Device
	for _, d := range s.devices {
		if len(ids) == 0 || slices.Contains(ids, d.ID) {
			chosen = append(chosen, d)
		}
	}
	s.mu.Unlock()
	switch {
	case len(chosen) == 0:
		return nil, "", errors.New("there is no simulated device to export")
	case len(ids) > 0 && len(chosen) != len(ids):
		return nil, "", errors.New("a device to export is no longer there")
	}
	if withSecrets {
		for i := range chosen {
			sec, err := a.simulatorSecrets(chosen[i].ID)
			if err != nil {
				return nil, "", err
			}
			chosen[i] = chosen[i].WithSecrets(sec)
		}
	}
	data, err := simulator.ExportDevices(chosen, withSecrets)
	name := "simulated-devices.json"
	if len(chosen) == 1 {
		name = simulator.Slug(chosen[0].Name, "simulated-device") + ".json"
	}
	return data, name, err
}

// ImportSimulatedDevicesDialog asks for files of simulated devices and imports
// them.
func (a *App) ImportSimulatedDevicesDialog() ([]SimulatorDeviceImportResult, error) {
	paths, err := a.openFiles("Import simulated devices", fileFilter("Simulated devices (JSON)", "*.json"))
	if err != nil {
		return nil, err
	}
	// Cancelled is an empty list, never null: the caller reports each result.
	results := []SimulatorDeviceImportResult{}
	for _, p := range paths {
		raw, err := readAtMost(p, simulator.MaxDeviceFileBytes)
		if err != nil {
			results = append(results, SimulatorDeviceImportResult{Name: filepath.Base(p), Error: err.Error(),
				Warnings: []SimulatorModelWarning{}})
			continue
		}
		results = append(results, a.importDevices(filepath.Base(p), raw)...)
	}
	return results, nil
}

// importDevices imports the devices of a file as new devices of this machine:
// each is given an ID, an engine and, when another device answers where it
// says, an address of its own. A device that cannot be made here — a model this
// machine does not have, an address off loopback — is refused on its own, and
// the others are kept. None is started.
//
// A file that says it left the passwords out has any it holds anyway ignored:
// what a file says of itself is what is believed. Its devices are checked for
// everything but their secrets, and kept to be given them.
func (a *App) importDevices(file string, raw []byte) []SimulatorDeviceImportResult {
	fail := func(err error) []SimulatorDeviceImportResult {
		return []SimulatorDeviceImportResult{{Name: file, Error: err.Error(), Warnings: []SimulatorModelWarning{}}}
	}
	s := a.sim
	if s == nil {
		return fail(errNoSimulator)
	}
	if a.secrets == nil {
		return fail(errors.New("the credential store is unavailable, so a simulated device cannot keep its community and passphrases"))
	}
	f, err := simulator.ParseDeviceFile(raw)
	if err != nil {
		return fail(err)
	}
	withSecrets := f.Secrets == simulator.SecretsIncluded

	s.mu.Lock()
	defer s.mu.Unlock()
	taken := make([]string, 0, len(s.devices)+len(f.Devices))
	for _, d := range s.devices {
		taken = append(taken, d.Listen())
	}
	results := make([]SimulatorDeviceImportResult, 0, len(f.Devices))
	next := slices.Clone(s.devices)
	var stored []string
	for _, fd := range f.Devices {
		d := fd.Device()
		res := SimulatorDeviceImportResult{Name: d.Name, Warnings: []SimulatorModelWarning{}}
		refuse := func(err error) {
			res.Error = err.Error()
			results = append(results, res)
		}
		if !withSecrets {
			d = d.WithoutSecrets()
		}
		d.ID = simulator.NewDeviceID()
		engineID, err := simulator.NewEngineID(d.Model, d.ID)
		if err != nil {
			refuse(err)
			continue
		}
		d.EngineID, d.EngineBoots = engineID, 0
		for i := range d.Traps.Destinations {
			if d.Traps.Destinations[i].ID == "" {
				d.Traps.Destinations[i].ID = simulator.NewDestinationID()
			}
		}
		if slices.Contains(taken, d.Listen()) {
			was := d.Listen()
			moved := suggestAddress(goruntime.GOOS, taken, canBindUDP)
			d.Address, d.Port = moved.Address, moved.Port
			res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "addressMoved",
				Detail: was + " → " + net.JoinHostPort(d.Address, strconv.Itoa(d.Port))})
		}
		check := d.Validate
		if !withSecrets {
			check = d.ValidateWithoutSecrets
		}
		if err := check(); err != nil {
			refuse(err)
			continue
		}
		if withSecrets {
			sec, err := json.Marshal(d.Secrets())
			if err != nil {
				refuse(err)
				continue
			}
			if err := a.secrets.Set(secrets.SimulatorDeviceRef(d.ID), string(sec)); err != nil {
				refuse(fmt.Errorf("keeping the device's credentials: %w", err))
				continue
			}
			stored = append(stored, d.ID)
		} else if d.Validate() != nil {
			// Complete without secrets is a device that needs none.
			res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "noSecrets"})
		}
		taken = append(taken, d.Listen())
		next = append(next, d.WithoutSecrets())
		res.ID, res.Success = d.ID, true
		results = append(results, res)
	}
	if len(next) == len(s.devices) {
		return results
	}
	if err := s.write(next); err != nil {
		for _, id := range stored {
			_ = a.secrets.Delete(secrets.SimulatorDeviceRef(id))
		}
		for i := range results {
			if results[i].Success {
				results[i].Success, results[i].ID = false, ""
				results[i].Error = fmt.Sprintf("saving the simulated devices: %v", err)
			}
		}
		return results
	}
	s.devices = next
	a.emitSimulatorChanged()
	return results
}

// SimulatorDuplicateDevice makes a copy of a device under the name given — the
// renderer's, in the user's language —, with an ID, an engine and an address of
// its own, and the passwords of the one it copies. The copy is not started.
func (a *App) SimulatorDuplicateDevice(id, name string) (SimulatedDevice, error) {
	s := a.sim
	if s == nil {
		return SimulatedDevice{}, errNoSimulator
	}
	if a.secrets == nil {
		return SimulatedDevice{}, errors.New("the credential store is unavailable, so a simulated device cannot keep its community and passphrases")
	}
	sec, err := a.simulatorSecrets(id)
	if err != nil {
		return SimulatedDevice{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return SimulatedDevice{}, fmt.Errorf("there is no simulated device %q", id)
	}
	d := s.devices[i].WithSecrets(sec)
	d.Name = clipText(strings.TrimSpace(name), simulator.MaxDeviceName)
	d.ID = simulator.NewDeviceID()
	if d.EngineID, err = simulator.NewEngineID(d.Model, d.ID); err != nil {
		return SimulatedDevice{}, err
	}
	d.EngineBoots = 0
	taken := make([]string, len(s.devices))
	for j, other := range s.devices {
		taken[j] = other.Listen()
	}
	at := suggestAddress(goruntime.GOOS, taken, canBindUDP)
	d.Address, d.Port = at.Address, at.Port
	// A copy of a device still waiting for its passwords waits for them too.
	if err := d.Validate(); err != nil && d.ValidateWithoutSecrets() != nil {
		return SimulatedDevice{}, err
	}
	raw, err := json.Marshal(d.Secrets())
	if err != nil {
		return SimulatedDevice{}, err
	}
	ref := secrets.SimulatorDeviceRef(d.ID)
	if err := a.secrets.Set(ref, string(raw)); err != nil {
		return SimulatedDevice{}, fmt.Errorf("keeping the device's credentials: %w", err)
	}
	next := append(slices.Clone(s.devices), d.WithoutSecrets())
	if err := s.write(next); err != nil {
		_ = a.secrets.Delete(ref)
		return SimulatedDevice{}, fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.devices = next
	a.emitSimulatorChanged()
	return s.view(next[len(next)-1]), nil
}

// SimulatorRestartDevice restarts a running device as a device reboots: its
// uptime and counters from zero, its boots one higher — which tells a manager
// holding its clock to resynchronise —, and coldStart sent if it sends one when
// it starts.
func (a *App) SimulatorRestartDevice(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("there is no simulated device %q", id)
	}
	if !s.fleet.Stop(id) {
		return fmt.Errorf("%q is not running", s.devices[i].Name)
	}
	err := a.startSimulatedLocked(s, i)
	a.emitSimulatorChanged()
	return err
}
