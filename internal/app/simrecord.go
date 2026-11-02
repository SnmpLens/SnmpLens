package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/snmp"

	"github.com/gosnmp/gosnmp"
)

// Recording a device: SnmpLens walks a real one and keeps what it answered as a
// package (simulator.RecordedPackage), a custom model like any imported one.
// And any custom model can be exported as the package it is, to be edited —
// an oids file beside a recorded walk makes a recorded constant move — and
// imported again, or passed on.

// SimulatorRecordRequest is what a recording takes: the device and how to reach
// it, built as every request is (snmp.SnmpRequest, with one target), and what
// the model it makes is to be called.
type SimulatorRecordRequest struct {
	snmp.SnmpRequest
	Name        string `json:"name"`
	Description string `json:"description"`
	Vendor      string `json:"vendor"`
	Category    string `json:"category"`
}

// recordRoots are the subtrees a device answers in: LLDP-MIB lives under
// 1.0.8802, outside .1.3.6.1.
var recordRoots = []string{".1.3.6.1", ".1.0.8802"}

// recordProgressEvery is how many objects pass between two progress events.
const recordProgressEvery = 250

var errRecordingStopped = errors.New("the recording was stopped, and nothing of it was kept")

// recorder is the recording under way, if one is. A recording holds none of
// the simulator's lock while it walks — a large device takes minutes — so it
// is guarded apart.
type recorder struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (r *recorder) begin(cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return false
	}
	r.cancel = cancel
	return true
}

func (r *recorder) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// SimulatorRecordDevice walks a device and keeps what it answered as a custom
// model, replacing a model of the same name as importing it again would, and
// returns what became of it as an import does. One recording runs at a time,
// and SimulatorCancelRecording stops it.
func (a *App) SimulatorRecordDevice(req SimulatorRecordRequest) (SimulatorModelImportResult, error) {
	s := a.sim
	if s == nil {
		return SimulatorModelImportResult{}, errNoSimulator
	}
	if len(req.Targets) != 1 || strings.TrimSpace(req.Targets[0]) == "" {
		return SimulatorModelImportResult{}, errors.New("a recording walks one device")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !s.rec.begin(cancel) {
		cancel()
		return SimulatorModelImportResult{}, errors.New("a device is already being recorded")
	}
	defer s.rec.end()

	var rec simulator.Recording
	err := a.snmpClient.Record(ctx, req.SnmpRequest, recordRoots, func(pdu gosnmp.SnmpPDU) error {
		if err := rec.Add(pdu); err != nil {
			return err
		}
		if n := rec.Objects(); n%recordProgressEvery == 0 {
			a.emitRecording(n)
		}
		return nil
	})
	switch {
	case errors.Is(err, context.Canceled):
		return SimulatorModelImportResult{}, errRecordingStopped
	case err != nil:
		return SimulatorModelImportResult{}, err
	}
	files, m, err := simulator.RecordedPackage(simulator.RecordedModel{Name: req.Name, Description: req.Description,
		Vendor: req.Vendor, Category: req.Category}, &rec)
	if err != nil {
		return SimulatorModelImportResult{}, err
	}
	packed, err := repack(files)
	if err != nil {
		return SimulatorModelImportResult{}, err
	}

	res := SimulatorModelImportResult{File: req.Targets[0], Warnings: []SimulatorModelWarning{}}
	if n := rec.LeftOut(); n > 0 {
		res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "walkAgentOwned", Detail: strconv.Itoa(n)})
	}
	noIcon := func(string) (*modelIcon, *SimulatorModelWarning) { return nil, nil }
	s.mu.Lock()
	defer s.mu.Unlock()
	res = s.keepModel(res, m, noIcon, keptPackage, packed)
	s.loadModels()
	a.restartModelDevicesLocked(s, &res)
	a.emitSimulatorModelsChanged()
	return res, nil
}

// SimulatorCancelRecording stops the recording under way, if one is. Nothing of
// it is kept.
func (a *App) SimulatorCancelRecording() {
	if s := a.sim; s != nil {
		s.rec.end()
	}
}

// emitRecording tells the renderer how many objects the recording has kept.
func (a *App) emitRecording(objects int) {
	a.emit("simulator:recording", objects)
}

// SimulatorExportModelDialog writes a custom model to a ZIP the user names, as
// a package folder — its model file, its other files, and its icon under the
// name the model gives it — which imports back as the same model. It returns
// where the file went, or "" when the dialog was cancelled.
func (a *App) SimulatorExportModelDialog(id string) (string, error) {
	s := a.sim
	if s == nil {
		return "", errNoSimulator
	}
	slug, ok := simulator.CustomModelSlug(id)
	if !ok {
		return "", fmt.Errorf("%q is not a custom model", id)
	}
	s.mu.Lock()
	data, err := s.exportModel(slug)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	dest, err := a.saveFile("Export simulator model", slug + ".zip", fileFilter("Simulator model package (ZIP)", "*.zip"))
	if err != nil || dest == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(dest), ".zip") {
		dest += ".zip"
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", filepath.Base(dest), err)
	}
	return dest, nil
}

// exportModel is the ZIP a kept model is exported as: a folder named after the
// model, holding model.json and the rest of its package, and its icon.
func (s *simulatorService) exportModel(slug string) ([]byte, error) {
	var files []simulator.PackageFile
	var icon string
	switch s.keptFormOf(slug) {
	case keptPackage:
		kept, err := keptPackageFiles(filepath.Join(s.modelDir, slug+keptPackage))
		if err != nil {
			return nil, err
		}
		m, _, err := simulator.ParseCustomPackage(kept)
		if err != nil {
			return nil, err
		}
		files, icon = kept, m.Icon()
	case keptFile:
		raw, err := readAtMost(filepath.Join(s.modelDir, slug+keptFile), simulator.MaxCustomModelBytes)
		if err != nil {
			return nil, err
		}
		m, err := simulator.ParseCustomModel(bytes.TrimPrefix(raw, utf8BOM))
		if err != nil {
			return nil, err
		}
		files, icon = []simulator.PackageFile{{Name: simulator.PackageModelFile, Data: raw}}, m.Icon()
	default:
		return nil, fmt.Errorf("no custom model %q is kept", slug)
	}
	if icon != "" {
		for _, ext := range []string{".png", ".jpg", ".gif"} {
			if data, err := readAtMost(filepath.Join(s.modelDir, slug+ext), maxModelIconBytes); err == nil {
				files = append(files, simulator.PackageFile{Name: icon, Data: data})
				break
			}
		}
	}
	return repackUnder(slug, files)
}

// keptFormOf is the form a model is kept in — the newer, should a stopped
// import have left both, as loadModels takes it — or "" when it is not kept.
func (s *simulatorService) keptFormOf(slug string) string {
	form, newest := "", time.Time{}
	for _, f := range []string{keptFile, keptPackage} {
		st, err := os.Stat(filepath.Join(s.modelDir, slug+f))
		if err == nil && !st.IsDir() && (form == "" || st.ModTime().After(newest)) {
			form, newest = f, st.ModTime()
		}
	}
	return form
}
