package app

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"SnmpLens/pkg/backup"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/storage"
	"SnmpLens/pkg/updater"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// A backup of the configuration: what the renderer keeps (handed over by it,
// opaque here) and what Go keeps — the alert destinations and routes, the
// monitoring sessions, the simulated devices and models, the presets and map
// backgrounds, the background-mode preferences, the trap listener's engine ID
// and, when asked, the MIBs added to the directory. Never the history: samples,
// events and query history belong to the machine they were taken on.
//
// Restoring adds and replaces and never deletes. The renderer's settings are one
// document and are replaced; everything else is matched by its ID or its file
// name. A monitoring session that already exists is left as it is, since the
// only way to "replace" one would be to drop its history with it. And before
// anything is written, the configuration as it stands is backed up on its own,
// with its secrets, into backups/ beside monitoring.db: the restore is undone by
// restoring that.
//
// Every file a backup brings goes through the importer that file would face if
// it were dropped on the window — the preset gate, imagegate, the model reader,
// the MIB import with its backups — so a backup is not a way around any of them.

const (
	backupSubdir = "backups"
	// automaticKept is how many backups made before a restore are kept.
	automaticKept = 5

	sectionRenderer = "renderer"
	sectionNotify   = "notify"
	sectionSessions = "sessions"
	sectionSim      = "simulator"
	sectionService  = "service"
	sectionTraps    = "traps"
)

// BackupExportRequest is what the renderer asks a backup to hold.
type BackupExportRequest struct {
	// Renderer is what the renderer keeps, its credentials taken out.
	Renderer json.RawMessage `json:"renderer"`
	// RendererSecrets are those credentials, by the key the renderer seals them under.
	RendererSecrets map[string]string `json:"rendererSecrets"`
	IncludeSecrets  bool              `json:"includeSecrets"`
	Passphrase      string            `json:"passphrase"`
	IncludeMibs     bool              `json:"includeMibs"`
}

// BackupContents counts what a backup holds, section by section.
type BackupContents struct {
	Settings   bool `json:"settings"`
	Sinks      int  `json:"sinks"`
	Routes     int  `json:"routes"`
	Sessions   int  `json:"sessions"`
	Devices    int  `json:"devices"`
	Models     int  `json:"models"`
	Presets    int  `json:"presets"`
	Assets     int  `json:"assets"`
	Mibs       int  `json:"mibs"`
	Service    bool `json:"service"`
	TrapEngine bool `json:"trapEngine"`
}

// BackupExportResult says where a backup went.
type BackupExportResult struct {
	Path     string         `json:"path"`
	Contents BackupContents `json:"contents"`
	Secrets  bool           `json:"secrets"`
}

// BackupSummary describes a backup that has been read and not yet restored.
type BackupSummary struct {
	// Token names it in BackupRestore: the file is kept here, never named again
	// by the renderer.
	Token      string         `json:"token"`
	FileName   string         `json:"fileName"`
	CreatedAt  string         `json:"createdAt"`
	AppVersion string         `json:"appVersion"`
	Secrets    bool           `json:"secrets"`
	Automatic  bool           `json:"automatic"`
	Contents   BackupContents `json:"contents"`
}

// BackupPoint is a backup SnmpLens made before a restore.
type BackupPoint struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	Size      int64  `json:"size"`
}

// BackupRestoreRequest restores the backup a summary named.
type BackupRestoreRequest struct {
	Token string `json:"token"`
	// Passphrase opens the secrets. Empty restores everything else.
	Passphrase string `json:"passphrase"`
	// The renderer's configuration as it stands, for the backup made first.
	CurrentRenderer        json.RawMessage   `json:"currentRenderer"`
	CurrentRendererSecrets map[string]string `json:"currentRendererSecrets"`
}

// BackupRestoreResult is what was restored, and what the renderer must write.
type BackupRestoreResult struct {
	Renderer        json.RawMessage   `json:"renderer,omitempty"`
	RendererSecrets map[string]string `json:"rendererSecrets"`
	Restored        BackupContents    `json:"restored"`
	SecretsRestored bool              `json:"secretsRestored"`
	// Before is the name of the backup of what was there before.
	Before   string   `json:"before"`
	Problems []string `json:"problems"`
}

type backupSecrets struct {
	Renderer map[string]string `json:"renderer,omitempty"`
	Sinks    map[string]string `json:"sinks,omitempty"`
	Sessions map[string]string `json:"sessions,omitempty"`
	Devices  map[string]string `json:"devices,omitempty"`
}

type notifySection struct {
	Sinks  []notify.SinkConfig `json:"sinks"`
	Routes []notify.Route      `json:"routes"`
}

type simulatorSection struct {
	Devices []simulator.Device `json:"devices"`
}

type trapsSection struct {
	EngineID string `json:"engineID"`
}

type pendingBackup struct {
	token      string
	name       string
	archive    *backup.Archive
	passphrase string // known only for a backup SnmpLens made itself
}

type backupState struct {
	mu      sync.Mutex
	pending *pendingBackup
}

// --- export ---

// BackupExport asks where to save, and saves the configuration there.
func (a *App) BackupExport(req BackupExportRequest) (BackupExportResult, error) {
	if req.IncludeSecrets && len([]rune(req.Passphrase)) < backup.MinPassphrase {
		return BackupExportResult{}, fmt.Errorf("the passphrase must be at least %d characters", backup.MinPassphrase)
	}
	if a.ctx == nil {
		return BackupExportResult{}, fmt.Errorf("no window")
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Back up the configuration",
		DefaultFilename: "SnmpLens-" + time.Now().Format("2006-01-02") + backup.Extension,
		Filters:         []runtime.FileFilter{{DisplayName: "SnmpLens backup", Pattern: "*" + backup.Extension}},
	})
	if err != nil || dest == "" {
		return BackupExportResult{}, err
	}
	if !strings.EqualFold(filepath.Ext(dest), backup.Extension) {
		dest += backup.Extension
	}
	data, contents, err := a.buildBackup(req)
	if err != nil {
		return BackupExportResult{}, err
	}
	// A file holding credentials, even sealed, is its owner's alone to read.
	perm := os.FileMode(0o644)
	if req.IncludeSecrets {
		perm = 0o600
	}
	if err := os.WriteFile(dest, data, perm); err != nil {
		return BackupExportResult{}, fmt.Errorf("writing %s: %w", filepath.Base(dest), err)
	}
	return BackupExportResult{Path: dest, Contents: contents, Secrets: req.IncludeSecrets}, nil
}

// buildBackup gathers the configuration into a backup file.
func (a *App) buildBackup(req BackupExportRequest) ([]byte, BackupContents, error) {
	var c BackupContents
	sections := map[string]any{}
	files := map[string]map[string][]byte{}
	sec := backupSecrets{Renderer: req.RendererSecrets, Sinks: map[string]string{},
		Sessions: map[string]string{}, Devices: map[string]string{}}

	if len(req.Renderer) > 0 {
		if !json.Valid(req.Renderer) {
			return nil, c, errors.New("the interface's configuration is not JSON")
		}
		sections[sectionRenderer] = req.Renderer
		c.Settings = true
	}

	if a.engineStorage() != nil {
		sinks, err := a.engineStorage().ListSinks()
		if err != nil {
			return nil, c, fmt.Errorf("reading the alert destinations: %w", err)
		}
		routes, err := a.engineStorage().ListRoutes()
		if err != nil {
			return nil, c, fmt.Errorf("reading the alert routes: %w", err)
		}
		for i := range sinks {
			sinks[i].Secret = ""
			if s := a.storedSinkSecret(sinks[i].ID); s != "" {
				sec.Sinks[sinks[i].ID] = s
			}
		}
		sections[sectionNotify] = notifySection{Sinks: nonNil(sinks), Routes: nonNil(routes)}
		c.Sinks, c.Routes = len(sinks), len(routes)

		sessions, err := a.engineStorage().ListSessions()
		if err != nil {
			return nil, c, fmt.Errorf("reading the monitoring sessions: %w", err)
		}
		for _, s := range sessions {
			if a.secrets == nil {
				break
			}
			if raw, err := a.secrets.Get(secrets.SessionRef(s.ID)); err == nil {
				sec.Sessions[s.ID] = raw
			}
		}
		sections[sectionSessions] = nonNil(sessions)
		c.Sessions = len(sessions)
	}

	if s := a.sim; s != nil {
		s.mu.Lock()
		devices := slices.Clone(s.devices)
		modelDir := s.modelDir
		s.mu.Unlock()
		for _, d := range devices {
			if a.secrets == nil {
				break
			}
			if raw, err := a.secrets.Get(secrets.SimulatorDeviceRef(d.ID)); err == nil {
				sec.Devices[d.ID] = raw
			}
		}
		sections[sectionSim] = simulatorSection{Devices: nonNil(devices)}
		c.Devices = len(devices)
		models, _ := readFolder(modelDir, func(string) bool { return true })
		files["simulator-models"] = models
		c.Models = countModels(models)
	}

	if dir, err := a.presetDir(); err == nil {
		files["presets"], c.Presets = readFolder(dir, func(string) bool { return true })
	}
	if dir, err := a.assetDir(); err == nil {
		files["assets"], c.Assets = readFolder(dir, func(string) bool { return true })
	}
	if req.IncludeMibs && a.persistentMibDir != "" {
		files["mibs"], c.Mibs = readFolder(a.persistentMibDir, func(name string) bool {
			_, bundled := a.bundledContent(name)
			return !bundled
		})
	}
	for k, v := range files {
		if len(v) == 0 {
			delete(files, k)
		}
	}

	if a.configDir != "" {
		sections[sectionService] = a.serviceCfg
		c.Service = true
	}
	if a.eng != nil && len(a.eng.TrapEngineID()) > 0 {
		sections[sectionTraps] = trapsSection{EngineID: hex.EncodeToString(a.eng.TrapEngineID())}
		c.TrapEngine = true
	}

	var sealed any
	if req.IncludeSecrets {
		sealed = sec
	}
	var buf bytes.Buffer
	if err := backup.Write(&buf, backup.Manifest{AppVersion: updater.Version}, sections, files, sealed, req.Passphrase); err != nil {
		return nil, c, err
	}
	return buf.Bytes(), c, nil
}

// readFolder reads the plain files of dir that keep says to, by name.
func readFolder(dir string, keep func(name string) bool) (map[string][]byte, int) {
	out := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, 0
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || !keep(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			log.Printf("backup: %s is left out: %v", name, err)
			continue
		}
		out[name] = data
	}
	return out, len(out)
}

// countModels counts the models among a model folder's files, its icons apart.
func countModels(files map[string][]byte) int {
	n := 0
	for name := range files {
		if ext := filepath.Ext(name); ext == keptFile || ext == keptPackage {
			n++
		}
	}
	return n
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// --- inspect ---

// BackupInspect asks for a backup file and says what it holds.
func (a *App) BackupInspect() (BackupSummary, error) {
	if a.ctx == nil {
		return BackupSummary{}, fmt.Errorf("no window")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Restore a configuration",
		Filters: []runtime.FileFilter{{DisplayName: "SnmpLens backup", Pattern: "*" + backup.Extension}},
	})
	if err != nil || path == "" {
		return BackupSummary{}, err
	}
	data, err := readAtMost(path, maxBackupBytes)
	if err != nil {
		return BackupSummary{}, err
	}
	return a.holdBackup(filepath.Base(path), data, "")
}

// maxBackupBytes bounds the file read before anything in it is looked at.
const maxBackupBytes = 600 << 20

// ListAutomaticBackups lists the backups SnmpLens made before each restore,
// newest first.
func (a *App) ListAutomaticBackups() []BackupPoint {
	out := []BackupPoint{}
	dir := a.automaticDir()
	if dir == "" {
		return out
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != backup.Extension {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupPoint{Name: e.Name(), CreatedAt: info.ModTime().UTC().Format(time.RFC3339), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// BackupInspectAutomatic reads one of the backups SnmpLens made before a
// restore. Its passphrase is in this machine's credential store.
func (a *App) BackupInspectAutomatic(name string) (BackupSummary, error) {
	path, err := a.automaticPath(name)
	if err != nil {
		return BackupSummary{}, err
	}
	data, err := readAtMost(path, maxBackupBytes)
	if err != nil {
		return BackupSummary{}, err
	}
	passphrase := ""
	if a.secrets != nil {
		passphrase, _ = a.secrets.Get(secrets.BackupRef(name))
	}
	return a.holdBackup(name, data, passphrase)
}

func (a *App) holdBackup(name string, data []byte, passphrase string) (BackupSummary, error) {
	ar, err := backup.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return BackupSummary{}, err
	}
	token := randomToken()
	a.restores.mu.Lock()
	a.restores.pending = &pendingBackup{token: token, name: name, archive: ar, passphrase: passphrase}
	a.restores.mu.Unlock()
	return BackupSummary{
		Token: token, FileName: name, CreatedAt: ar.Manifest.CreatedAt, AppVersion: ar.Manifest.AppVersion,
		Secrets: ar.HasSecrets(), Automatic: passphrase != "", Contents: contentsOf(ar),
	}, nil
}

func contentsOf(ar *backup.Archive) BackupContents {
	var c BackupContents
	_, c.Settings = ar.Sections[sectionRenderer]
	var n notifySection
	if ok, _ := ar.Section(sectionNotify, &n); ok {
		c.Sinks, c.Routes = len(n.Sinks), len(n.Routes)
	}
	var sessions []storage.Session
	if ok, _ := ar.Section(sectionSessions, &sessions); ok {
		c.Sessions = len(sessions)
	}
	var sim simulatorSection
	if ok, _ := ar.Section(sectionSim, &sim); ok {
		c.Devices = len(sim.Devices)
	}
	c.Models = countModels(ar.Files["simulator-models"])
	c.Presets, c.Assets, c.Mibs = len(ar.Files["presets"]), len(ar.Files["assets"]), len(ar.Files["mibs"])
	_, c.Service = ar.Sections[sectionService]
	_, c.TrapEngine = ar.Sections[sectionTraps]
	return c
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b) // never fails since Go 1.24
	return hex.EncodeToString(b)
}

// --- restore ---

// BackupRestore restores the backup BackupInspect or BackupInspectAutomatic
// read, after backing up what is there now.
func (a *App) BackupRestore(req BackupRestoreRequest) (BackupRestoreResult, error) {
	a.restores.mu.Lock()
	p := a.restores.pending
	if p == nil || p.token != req.Token {
		a.restores.mu.Unlock()
		return BackupRestoreResult{}, errors.New("choose the backup again: this one is no longer open")
	}
	a.restores.pending = nil
	a.restores.mu.Unlock()

	passphrase := req.Passphrase
	if p.passphrase != "" {
		passphrase = p.passphrase
	}
	// The secrets are opened BEFORE anything is changed: a wrong passphrase is
	// said while there is still nothing to undo.
	var sec backupSecrets
	opened := false
	if p.archive.HasSecrets() && passphrase != "" {
		if err := p.archive.Open(passphrase, &sec); err != nil {
			a.restores.mu.Lock()
			a.restores.pending = p // the person may try another passphrase
			a.restores.mu.Unlock()
			return BackupRestoreResult{}, err
		}
		opened = true
	}

	before, err := a.backupBeforeRestore(req.CurrentRenderer, req.CurrentRendererSecrets)
	if err != nil {
		return BackupRestoreResult{}, fmt.Errorf("the configuration as it stands could not be backed up first, so nothing was restored: %w", err)
	}
	res := a.restoreArchive(p.archive, sec)
	res.Before = before
	res.SecretsRestored = opened
	return res, nil
}

// backupBeforeRestore saves the configuration as it stands, secrets included,
// sealed under a passphrase only this machine's credential store knows.
func (a *App) backupBeforeRestore(renderer json.RawMessage, rendererSecrets map[string]string) (string, error) {
	dir := a.automaticDir()
	if dir == "" {
		return "", errors.New("the configuration directory is not ready")
	}
	if a.secrets == nil {
		return "", errors.New("the credential store is unavailable")
	}
	key := make([]byte, 32)
	rand.Read(key) // never fails since Go 1.24
	passphrase := base64.RawURLEncoding.EncodeToString(key)
	data, _, err := a.buildBackup(BackupExportRequest{
		Renderer: renderer, RendererSecrets: rendererSecrets,
		IncludeSecrets: true, Passphrase: passphrase, IncludeMibs: true,
	})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "before-restore-" + time.Now().UTC().Format("20060102-150405.000") + backup.Extension
	if err := a.secrets.Set(secrets.BackupRef(name), passphrase); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		_ = a.secrets.Delete(secrets.BackupRef(name))
		return "", err
	}
	a.pruneAutomatic()
	return name, nil
}

// pruneAutomatic keeps the newest automaticKept backups made before a restore.
func (a *App) pruneAutomatic() {
	points := a.ListAutomaticBackups()
	for _, p := range points[min(len(points), automaticKept):] {
		if path, err := a.automaticPath(p.Name); err == nil {
			_ = os.Remove(path)
		}
		if a.secrets != nil {
			_ = a.secrets.Delete(secrets.BackupRef(p.Name))
		}
	}
}

func (a *App) automaticDir() string {
	if a.configDir == "" {
		return ""
	}
	return filepath.Join(a.configDir, backupSubdir)
}

// automaticPath turns a name from ListAutomaticBackups into a path, or refuses.
func (a *App) automaticPath(name string) (string, error) {
	dir := a.automaticDir()
	if dir == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") ||
		filepath.Ext(name) != backup.Extension {
		return "", fmt.Errorf("there is no backup %q", name)
	}
	return filepath.Join(dir, name), nil
}

// restoreArchive writes what the backup holds. A section that fails is a
// problem reported, and the others are restored all the same.
func (a *App) restoreArchive(ar *backup.Archive, sec backupSecrets) BackupRestoreResult {
	res := BackupRestoreResult{RendererSecrets: map[string]string{}, Problems: []string{}}
	problem := func(format string, args ...any) { res.Problems = append(res.Problems, fmt.Sprintf(format, args...)) }

	if raw, ok := ar.Sections[sectionRenderer]; ok {
		res.Renderer = raw
		res.Restored.Settings = true
		if sec.Renderer != nil {
			res.RendererSecrets = sec.Renderer
		}
	}

	tmp, err := os.MkdirTemp("", "snmplens-restore-")
	if err != nil {
		problem("the files could not be unpacked: %v", err)
	} else {
		defer os.RemoveAll(tmp)
	}
	unpack := func(folder string, keep func(name string) bool) []string {
		var paths []string
		if tmp == "" {
			return nil
		}
		for _, name := range sortedNames(ar.Files[folder]) {
			if !keep(name) {
				continue
			}
			p := filepath.Join(tmp, folder, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
				if err := os.WriteFile(p, ar.Files[folder][name], 0o600); err == nil {
					paths = append(paths, p)
				}
			}
		}
		return paths
	}

	// The models before the devices: a device names its model.
	if a.sim != nil {
		isModel := func(name string) bool {
			ext := filepath.Ext(name)
			return ext == keptFile || ext == keptPackage
		}
		for _, r := range a.importSimulatorModels(unpack("simulator-models", isModel)) {
			if r.Success {
				res.Restored.Models++
			} else {
				problem("model %s: %s", r.File, r.Error)
			}
		}
		a.restoreModelIcons(ar.Files["simulator-models"])
	}
	for _, r := range a.ImportPresetFiles(unpack("presets", func(string) bool { return true })) {
		if r.Success {
			res.Restored.Presets++
		} else {
			problem("preset %s: %s", r.FileName, r.Error)
		}
	}
	for _, r := range a.ImportMapAssets(unpack("assets", func(string) bool { return true })) {
		if r.Success {
			res.Restored.Assets++
		} else {
			problem("background %s: %s", r.FileName, r.Error)
		}
	}
	if mibs := unpack("mibs", func(string) bool { return true }); len(mibs) > 0 {
		for _, r := range a.ImportMibFiles(mibs) {
			if r.Success {
				res.Restored.Mibs++
			} else if r.Error != "" {
				problem("MIB %s: %s", r.FileName, r.Error)
			}
		}
	}

	var n notifySection
	if ok, err := ar.Section(sectionNotify, &n); err != nil {
		problem("%v", err)
	} else if ok {
		for _, s := range n.Sinks {
			s.Secret = sec.Sinks[s.ID]
			if _, err := a.NotifySaveSink(s); err != nil {
				problem("destination %q: %v", s.Name, err)
			} else {
				res.Restored.Sinks++
			}
		}
		for _, r := range n.Routes {
			if _, err := a.NotifySaveRoute(r); err != nil {
				problem("route %q: %v", r.Name, err)
			} else {
				res.Restored.Routes++
			}
		}
	}

	var sessions []storage.Session
	if ok, err := ar.Section(sectionSessions, &sessions); err != nil {
		problem("%v", err)
	} else if ok && a.engineStorage() != nil {
		existing, _ := a.engineStorage().ListSessions()
		have := map[string]bool{}
		for _, s := range existing {
			have[s.ID] = true
		}
		for _, s := range sessions {
			if s.ID == "" || have[s.ID] {
				continue // already here, with its history
			}
			if err := a.engineStorage().InsertSession(s); err != nil {
				problem("monitoring session %q: %v", s.Name, err)
				continue
			}
			if raw, ok := sec.Sessions[s.ID]; ok && a.secrets != nil {
				if err := a.secrets.Set(secrets.SessionRef(s.ID), raw); err != nil {
					problem("monitoring session %q: its credentials: %v", s.Name, err)
				}
			}
			res.Restored.Sessions++
		}
	}

	var sim simulatorSection
	if ok, err := ar.Section(sectionSim, &sim); err != nil {
		problem("%v", err)
	} else if ok {
		for _, msg := range a.restoreSimulatedDevices(sim.Devices, sec.Devices, &res.Restored.Devices) {
			problem("%s", msg)
		}
	}

	var cfg service.Config
	if ok, err := ar.Section(sectionService, &cfg); err != nil {
		problem("%v", err)
	} else if ok && a.configDir != "" {
		if _, err := a.ServiceSetConfig(cfg); err != nil {
			problem("background mode: %v", err)
		} else {
			res.Restored.Service = true
		}
	}

	var traps trapsSection
	if ok, err := ar.Section(sectionTraps, &traps); err != nil {
		problem("%v", err)
	} else if ok {
		if err := a.restoreTrapEngineID(traps.EngineID); err != nil {
			problem("trap listener: %v", err)
		} else {
			res.Restored.TrapEngine = true
		}
	}
	return res
}

// restoreModelIcons keeps the icons of the models now kept, through the same
// gate an imported icon passes: a backup's icon is a file somebody may have
// written like any other.
func (a *App) restoreModelIcons(files map[string][]byte) {
	s := a.sim
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, name := range sortedNames(files) {
		ext := filepath.Ext(name)
		slug := strings.TrimSuffix(name, ext)
		if ext == keptFile || ext == keptPackage {
			continue
		}
		if !isFile(filepath.Join(s.modelDir, slug+keptFile)) && !isFile(filepath.Join(s.modelDir, slug+keptPackage)) {
			continue // the model did not come back, so neither does its icon
		}
		meta, err := checkIcon(files[name])
		if err != nil || iconExtensions[meta.Format] != ext {
			continue
		}
		if err := writeFileAtomic(filepath.Join(s.modelDir, name), files[name]); err == nil {
			changed = true
		}
	}
	if changed {
		a.emitSimulatorModelsChanged()
	}
}

// restoreSimulatedDevices adds the devices the backup holds and replaces those
// it shares an ID with, keeping their engine IDs: managers localise their keys to
// them. A device that exists here keeps the higher of the two boot counts, so a
// manager holding its clock is not handed one that went back.
func (a *App) restoreSimulatedDevices(devices []simulator.Device, raw map[string]string, restored *int) []string {
	s := a.sim
	if s == nil || len(devices) == 0 {
		return nil
	}
	var problems []string
	s.mu.Lock()
	defer s.mu.Unlock()
	next := slices.Clone(s.devices)
	for _, d := range devices {
		d = d.WithoutSecrets()
		var ds simulator.DeviceSecrets
		hasSecrets := false
		if r, ok := raw[d.ID]; ok && json.Unmarshal([]byte(r), &ds) == nil {
			hasSecrets = true
		}
		check := d.ValidateWithoutSecrets()
		if hasSecrets {
			check = d.WithSecrets(ds).Validate()
		}
		if d.ID == "" || check != nil {
			problems = append(problems, fmt.Sprintf("simulated device %q: %v", d.Name, check))
			continue
		}
		i := slices.IndexFunc(next, func(o simulator.Device) bool { return o.ID == d.ID })
		clash := slices.IndexFunc(next, func(o simulator.Device) bool { return o.ID != d.ID && o.Listen() == d.Listen() })
		if clash >= 0 {
			problems = append(problems, fmt.Sprintf("simulated device %q: %q already answers on %s", d.Name, next[clash].Name, d.Listen()))
			continue
		}
		if hasSecrets && a.secrets != nil {
			if err := a.secrets.Set(secrets.SimulatorDeviceRef(d.ID), raw[d.ID]); err != nil {
				problems = append(problems, fmt.Sprintf("simulated device %q: its credentials: %v", d.Name, err))
				continue
			}
		}
		if i >= 0 {
			s.fleet.Stop(d.ID)
			d.EngineBoots = max(d.EngineBoots, next[i].EngineBoots)
			next[i] = d
		} else {
			next = append(next, d)
		}
		*restored++
	}
	if err := s.write(next); err != nil {
		return append(problems, fmt.Sprintf("saving the simulated devices: %v", err))
	}
	s.devices = next
	a.emitSimulatorChanged()
	return problems
}

// restoreTrapEngineID keeps the engine ID the backup names. Devices sending
// acknowledged v3 notifications to SnmpLens localise their keys to it, so moving
// SnmpLens to another machine without it is reconfiguring every one of them. It
// is used from the listener's next start.
func (a *App) restoreTrapEngineID(engineID string) error {
	id, err := hex.DecodeString(strings.TrimSpace(engineID))
	if err != nil || len(id) < 5 || len(id) > 32 {
		return errors.New("the backup's engine ID is not one")
	}
	if a.eng == nil {
		return errors.New("the configuration directory is not ready")
	}
	return a.eng.SetTrapEngineID(id)
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// engineStorage is the database of the engine this window runs, nil when there
// is none: what a backup reads its sessions, destinations and routes from.
func (a *App) engineStorage() *storage.Storage {
	if a.eng == nil {
		return nil
	}
	return a.eng.Storage()
}

// storedSinkSecret is the credential kept for a destination, "" when none is.
func (a *App) storedSinkSecret(sinkID string) string {
	if a.secrets == nil {
		return ""
	}
	v, err := a.secrets.Get(secrets.SinkRef(sinkID))
	if err != nil {
		return ""
	}
	return v
}
