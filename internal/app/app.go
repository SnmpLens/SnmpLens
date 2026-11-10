package app

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"SnmpLens/internal/engine"
	"SnmpLens/internal/remote"
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/mib"
	"SnmpLens/pkg/network"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/tray"
	"SnmpLens/pkg/updater"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct holds our services and application context.
type App struct {
	// Backend is the supervision this window drives — the sessions, the event
	// journal, the notifications, the trap listener: its own engine, or the
	// system service's (internal/remote). It is EMBEDDED, so every method of it
	// is a method of App, which Wails binds without a forwarding stub apiece.
	remote.Backend
	// host is the same, with what the application reaches and the renderer
	// must not; link is the switch both go through, so that the window can
	// move between its own engine and the service's while calls are made.
	host remote.Host
	link *remote.Switch
	// svc is which supervision the window drives: the service's, or its own
	// engine (systemservice.go).
	svc serviceLink

	ctx              context.Context
	mibs             fs.FS
	presets          fs.FS
	persistentMibDir string
	mibService       *mib.Service
	// snmpClient sends what a person asks for. Polling and the trap listener
	// are the engine's, on a client of its own.
	snmpClient *snmp.Client
	secrets    secrets.Store
	// mibStatuses holds what the last load did with each MIB and the analysis
	// of each, for the one verdict every screen shows (mibstatus.go).
	mibStatuses mibStatusCache
	// lastExternal is the file last opened from outside the MIB directory, kept
	// so it can be read again in another encoding without a path from the
	// renderer (MibEditorReopenExternalAs).
	externalMu   sync.Mutex
	lastExternal externalFile
	// settingsKeyCache holds the renderer's sealing key. Every secrets.Get is
	// a whole-file read and decrypt, and a settings save seals one value per
	// sensitive field plus three per target override.
	settingsKeyCache []byte
	// Guards settingsKeyCache. Wails dispatches every bound method on its own
	// goroutine, and a settings load issues KeyStatus, AdoptKey and Open while
	// a save issues Seal — concurrently, on the same field.
	settingsKeyMu sync.Mutex
	updater       *updater.Service

	// restores holds the backup read and not yet restored (backup.go).
	restores backupState
	// portMaps is the port map being made (portmap.go).
	portMaps portMapState
	// topologies is the network map being drawn (topology.go).
	topologies topologyState

	// Background-mode state. configDir is the SnmpLens directory, kept here
	// because several subsystems need it after startup.
	configDir  string
	serviceCfg service.Config
	trayIcons  trayIcons
	tray       *tray.Controller
	trayLive   bool

	// sim holds and runs the simulated devices (simulator.go).
	sim *simulatorService
}

// NewApp creates a new App application struct.
func NewApp(mibs, presets fs.FS) *App {
	return &App{
		mibs:    mibs,
		presets: presets,
		updater: updater.NewService("SnmpLens", "SnmpLens"),
	}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.snmpClient = snmp.NewClient(ctx)
	a.updater.SetContext(ctx)
	// An update quits for real: the window's own close hides it to the tray.
	a.updater.SetQuit(a.QuitApp)

	// 1. Determine/Create the persistent MIB directory
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("Failed to get user config dir: %v", err)
	}
	a.persistentMibDir = filepath.Join(configDir, "SnmpLens", "mibs")

	if err := os.MkdirAll(a.persistentMibDir, 0755); err != nil {
		log.Fatalf("Failed to create persistent MIB directory: %v", err)
	}

	// Always make sure the bundled standard MIBs are present. They define the
	// base types (SNMPv2-SMI, SNMPv2-TC, ...) that nearly every other MIB
	// IMPORTS from, so without them user-supplied MIBs fail to load. Running
	// this on every startup (not just first run) self-heals an empty or
	// partially-populated MIB directory.
	a.ensureStandardMibs()
	a.ensureBundledPresets()

	// 2. Initialize gosmi and our MIB service
	log.Printf("Setting MIB search path to: %s", a.persistentMibDir)
	mib.InitPath(a.persistentMibDir)
	a.mibService = mib.NewService(a.persistentMibDir)

	// The secret store, INDEPENDENT of the database: a corrupt monitoring.db
	// must not lock anyone out of their own community string. The engine
	// shares it — two stores over one file would each write the whole of it.
	if store, err := secrets.Open(filepath.Join(configDir, "SnmpLens")); err != nil {
		log.Printf("WARNING: secret storage unavailable, stored credentials cannot be read: %v", err)
	} else {
		a.secrets = store
		log.Printf("Secret storage backend: %s", store.Backend())
	}

	// 3. The supervision: the system service's when it runs and lets this
	// account in, else the window's own — the monitoring database, the
	// journal, the routes, the poll clock, the trap listener (systemservice.go).
	if a.configDir == "" {
		a.configDir = filepath.Join(configDir, "SnmpLens")
	}
	a.startLink()

	// The simulated devices are read, and those set to start with the
	// application are started — after the secret store, which holds their
	// communities and passphrases. The others answer once someone starts them.
	a.sim = newSimulatorService(filepath.Join(configDir, "SnmpLens"))
	// Which column is a RowStatus is in the MIBs, and the agents have none: a
	// row is created and destroyed as the loaded MIBs describe it.
	a.sim.fleet.RowStatus = a.mibService.RowStatusColumn
	a.startAutoSimulated()

	// 4. Load core MIBs
	coreMibs := []string{"SNMPv2-SMI", "SNMPv2-TC"}
	failed := mib.LoadCore(coreMibs...)
	for _, mibName := range coreMibs {
		if err, bad := failed[mibName]; bad {
			log.Printf("ERROR: Failed to load core MIB '%s': %v.", mibName, err)
		} else {
			log.Printf("Successfully loaded core MIB: %s", mibName)
		}
	}

	// 5. Tray and hide-on-close: the tray decides whether closing the window
	// may merely hide it.
	a.initBackgroundMode()
	a.refreshTrayStatus()
}

// useEngine makes eng the supervision this window runs and drives, with no
// service to follow: what a test wires.
func (a *App) useEngine(eng *engine.Engine) {
	a.svc.mu.Lock()
	a.svc.eng = eng
	a.svc.mu.Unlock()
	a.link = remote.NewSwitch(eng)
	a.host, a.Backend = a.link, a.link
}

// wailsEmitter reaches the webview through the Wails runtime, which accepts
// only the context it issued.
type wailsEmitter struct{ ctx context.Context }

func (w wailsEmitter) Emit(name string, data ...interface{}) {
	if w.ctx != nil {
		runtime.EventsEmit(w.ctx, name, data...)
	}
}

// ensureStandardMibs copies any bundled standard MIB that is missing from the
// persistent directory. Files that already exist are left untouched so user
// edits and additions are preserved.
func (a *App) ensureStandardMibs() {
	mibFiles, err := bundledDir(a.mibs, "mibs")
	if err != nil {
		log.Printf("ERROR: Failed to read embedded mibs directory: %v", err)
		return
	}

	var extracted []string
	for _, mibFile := range mibFiles {
		if mibFile.IsDir() {
			continue
		}
		fileName := mibFile.Name()
		destPath := filepath.Join(a.persistentMibDir, fileName)
		if _, err := os.Stat(destPath); err == nil {
			continue // already present
		}

		// An fs.FS always uses forward slashes, even on Windows — never filepath.Join here.
		content, err := bundledFile(a.mibs, "mibs/"+fileName)
		if err != nil {
			log.Printf("Warning: Failed to read embedded MIB file %s: %v", fileName, err)
			continue
		}
		if err := os.WriteFile(destPath, content, 0644); err != nil {
			log.Printf("Warning: Failed to write standard MIB file %s: %v", fileName, err)
			continue
		}
		extracted = append(extracted, fileName)
	}

	if len(extracted) > 0 {
		log.Printf("Extracted %d standard MIB(s) to %s: %v", len(extracted), a.persistentMibDir, extracted)
	}
}

// --- Frontend Exposed Methods ---

// GetPersistentMibDirectory returns the path where users can store their MIBs.
func (a *App) GetPersistentMibDirectory() string {
	return a.persistentMibDir
}

// LoadAllMibs loads all MIBs from the persistent directory.
func (a *App) LoadAllMibs() ([]*mib.Node, error) {
	return a.mibService.LoadAll()
}

// LoadEnabledMibs loads only the specified (enabled) MIBs from the persistent directory.
func (a *App) LoadEnabledMibs(enabledFiles []string) ([]*mib.Node, error) {
	// Same rule as LoadMibsWithDiagnostics: the store returns everything when
	// nothing has been chosen, so an empty list is a deliberate "none".
	if len(enabledFiles) == 0 {
		log.Println("Every MIB is disabled; loading none")
		return []*mib.Node{}, nil
	}
	return a.mibService.LoadSpecific(enabledFiles)
}

// LoadMibsWithDiagnostics loads MIBs and returns both tree and per-file load diagnostics.
func (a *App) LoadMibsWithDiagnostics(enabledFiles []string) mib.MibLoadResponse {
	// An empty list means empty, here as everywhere else.
	//
	// The store defaults an unknown MIB to ENABLED, so it returns the whole
	// directory when the user has expressed no preference; [] therefore only
	// arrives when every file was explicitly switched off. Treating that as
	// "load everything" turned Disable All into the maximal tree — the exact
	// opposite of what was asked.
	if len(enabledFiles) == 0 {
		log.Println("Every MIB is disabled; loading none")
	}
	response := a.mibService.LoadWithDiagnostics(enabledFiles)
	a.mibStatuses.rememberLoads(response.Diagnostics)
	return response
}

// GetOidDetails translates a numeric OID into its MIB details.
func (a *App) GetOidDetails(oid string) mib.OidDetails {
	return a.mibService.Translate(oid)
}

// ResolveOid returns detailed MIB info for a single OID, including enum values.
func (a *App) ResolveOid(oid string) mib.OidInfo {
	return a.mibService.ResolveOid(oid)
}

// ResolveOids returns detailed MIB info for a batch of OIDs.
func (a *App) ResolveOids(oids []string) map[string]mib.OidInfo {
	return a.mibService.ResolveOids(oids)
}

// SnmpGet performs a concurrent SNMP GET operation.
func (a *App) SnmpGet(req snmp.SnmpRequest) []*snmp.BulkResult {
	return a.snmpClient.Get(req.Targets, req.OID, req.Community, req.Version, req.Port, req.Timeout, req.Retries, req.V3)
}

// SnmpSet performs a concurrent SNMP SET operation.
func (a *App) SnmpSet(req snmp.SetRequest) []*snmp.BulkResult {
	results := a.snmpClient.Set(req.Targets, req.OID, req.Community, req.Value, req.ValueType, req.Version, req.Port, req.Timeout, req.Retries, req.V3)
	a.auditFailedSets(req, results)
	return results
}

// auditFailedSets journals SETs that were refused.
//
// A SET is the one operation that CHANGES a device, so a refused one is worth
// keeping: it is either a permissions problem or someone attempting a change
// they should not. Only failures are recorded, and never the value that was
// being written — an audit trail must not become somewhere passwords or
// community strings accumulate in the clear.
func (a *App) auditFailedSets(req snmp.SetRequest, results []*snmp.BulkResult) {
	if !a.serviceCfg.AuditFailedSets || a.host == nil {
		return
	}
	for _, r := range results {
		if r == nil || r.Error == "" {
			continue
		}
		a.host.Journal(events.Event{
			Category: events.CategoryOperation,
			Kind:     events.KindOperationFailed,
			Severity: "warning",
			Source:   r.Target,
			OID:      req.OID,
			TitleKey: "events.kind." + events.KindOperationFailed,
			Summary:  fmt.Sprintf("SET %s on %s was refused: %s", req.OID, r.Target, r.Error),
		})
	}
}

// SnmpGetNext performs a concurrent SNMP GETNEXT operation.
func (a *App) SnmpGetNext(req snmp.SnmpRequest) []*snmp.BulkResult {
	return a.snmpClient.GetNext(req.Targets, req.OID, req.Community, req.Version, req.Port, req.Timeout, req.Retries, req.V3)
}

// SnmpGetBulk performs a concurrent SNMP GETBULK operation.
func (a *App) SnmpGetBulk(req snmp.GetBulkRequest) []*snmp.BulkResult {
	return a.snmpClient.GetBulk(req.Targets, req.OID, req.Community, req.Version, req.Port, req.Timeout, req.Retries, req.NonRepeaters, req.MaxRepetitions, req.V3)
}

// SnmpWalk performs a concurrent SNMP WALK operation.
func (a *App) SnmpWalk(req snmp.SnmpRequest) []*snmp.BulkResult {
	return a.snmpClient.Walk(req.Targets, req.OID, req.Community, req.Version, req.Port, req.Timeout, req.Retries, req.V3)
}

// TestConnection tests the SNMP connection to a target by fetching sysDescr.0
func (a *App) TestConnection(req snmp.TestRequest) *snmp.BulkResult {
	results := a.snmpClient.Get([]string{req.Target}, "1.3.6.1.2.1.1.1.0", req.Community, req.Version, req.Port, req.Timeout, 1, req.V3)
	if len(results) > 0 {
		return results[0]
	}
	return &snmp.BulkResult{Target: req.Target, Error: "No response"}
}

// SnmpDiscover scans a CIDR range for SNMP-responsive devices.
func (a *App) SnmpDiscover(req snmp.DiscoverRequest) []snmp.DiscoveryResult {
	return a.snmpClient.Discover(req.CIDR, req.Community, req.Version, req.Port, req.Timeout, req.V3)
}

// SnmpSetMultiple writes several varbinds in a single SET.
//
// Row creation needs this: RFC 3416 makes a SET atomic across its varbinds, so
// a row and its RowStatus arrive together or not at all. Setting the columns
// one at a time asks the agent to accept a row that is incomplete at every
// step, and leaves half of one behind when it refuses.
func (a *App) SnmpSetMultiple(req snmp.SetMultiRequest) []*snmp.BulkResult {
	results := a.snmpClient.SetMultiple(req.Targets, req.Vars, req.Community, req.Version, req.Port, req.Timeout, req.Retries, req.V3)
	if len(req.Vars) > 0 {
		// One audit entry per refused request, not per varbind: the SET is
		// atomic, so a dozen entries would describe one event a dozen times.
		audit := snmp.SetRequest{SnmpRequest: req.SnmpRequest}
		audit.OID = req.Vars[0].Oid
		a.auditFailedSets(audit, results)
	}
	return results
}

// MibDiagnose explains why a MIB file does or does not load.
//
// Separate from loading because it costs a re-read and a re-parse: it is what
// you ask for when something is wrong, and what the import dialog asks for on
// your behalf when a file has already failed.
func (a *App) MibDiagnose(fileName string) mib.LoadDiagnosis {
	return a.mibService.Diagnose(fileName)
}

// MibTable returns the conceptual table containing oid — the table itself, its
// row, or any column of it.
func (a *App) MibTable(oid string) (*mib.TableInfo, error) {
	return a.mibService.Table(oid)
}

// MibDecodeIndexes splits each row instance into the values the INDEX clause
// declares. Batched because a table has as many instances as it has rows, and
// one bridge call each would cost more than the walk did.
func (a *App) MibDecodeIndexes(tableOid string, instances []string) []mib.DecodedIndex {
	return a.mibService.DecodeIndexes(tableOid, instances)
}

// MibEncodeIndex builds a row's instance sub-OID from one value per INDEX
// object, which is how a new row gets its identity.
func (a *App) MibEncodeIndex(tableOid string, values []string) (string, error) {
	return a.mibService.EncodeIndex(tableOid, values)
}

// SendInform sends an acknowledged notification and reports the answer.
func (a *App) SendInform(target string, port int, community, version, trapOid string, variables []snmp.TrapVariable) snmp.InformResult {
	return a.snmpClient.SendInform(target, port, community, version, trapOid, variables)
}

// SendTrap sends an SNMP trap to a target.
func (a *App) SendTrap(target string, port int, community, version, trapOid string, variables []snmp.TrapVariable) error {
	return a.snmpClient.SendTrap(target, port, community, version, trapOid, variables)
}

// ListMibFiles returns a list of MIB file names in the specified directory.
func (a *App) ListMibFiles(dirPath string) ([]string, error) {
	return mib.ListMibFiles(dirPath)
}

// BrowseDialog opens a directory picker dialog and returns the selected path.
func (a *App) BrowseDialog() (string, error) {
	selection, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select MIB Directory",
	})
	if err != nil {
		return "", err
	}
	return selection, nil
}

// --- Network Tools ---

// NetworkPing executes a ping command against a target.
func (a *App) NetworkPing(target string, count int) (network.PingResult, error) {
	return network.Ping(target, count)
}

// NetworkTraceroute executes a traceroute command, emitting progress events per hop.
func (a *App) NetworkTraceroute(target string) ([]network.TracerouteHop, error) {
	return network.Traceroute(a.ctx, target)
}

// --- SNMP Debug Methods ---

// SnmpSetDebug enables or disables SNMP packet debug logging.
func (a *App) SnmpSetDebug(enabled bool) {
	a.snmpClient.SetDebugMode(enabled)
}

// SnmpGetDebugLog returns the current SNMP debug log buffer.
func (a *App) SnmpGetDebugLog() []snmp.DebugEntry {
	return a.snmpClient.GetDebugLog()
}

// SnmpClearDebugLog clears the SNMP debug log buffer.
func (a *App) SnmpClearDebugLog() {
	a.snmpClient.ClearDebugLog()
}

// --- Auto-update Methods ---

// GetAppVersion returns the running application version (or "dev" for local builds).
func (a *App) GetAppVersion() string {
	return updater.Version
}

// CheckForUpdate queries GitHub Releases and reports whether a newer version exists.
func (a *App) CheckForUpdate() updater.UpdateInfo {
	info, err := a.updater.CheckForUpdate()
	if err != nil {
		log.Printf("Update check failed: %v", err)
	}
	return withCheckError(info, err)
}

// withCheckError puts the reason a check failed where the renderer can see it.
//
// Separate from the bridge method so it can be tested without a network: the
// Service holds its http.Client unexported, so there is no seam in package
// main, and the interesting part is not the HTTP call — it is that the error
// stops being dropped.
//
// Available is forced false as well. A check that failed cannot have found an
// update, and leaving whatever the zero value happened to be would let a
// failure offer a download.
func withCheckError(info updater.UpdateInfo, err error) updater.UpdateInfo {
	if err == nil {
		return info
	}
	info.Error = err.Error()
	info.Available = false
	return info
}

// DownloadAndApplyUpdate downloads, verifies and applies the pending update. On a
// successful self-apply the app relaunches and exits, so this may not return.
func (a *App) DownloadAndApplyUpdate() error {
	return a.updater.DownloadAndApply()
}

// OpenURL opens a URL in the user's default browser.
func (a *App) OpenURL(url string) {
	runtime.BrowserOpenURL(a.ctx, url)
}

// shutdown is called when the app is closing.
func (a *App) shutdown(ctx context.Context) {
	// The simulated devices first. They touch nothing below, but they hold
	// sockets, and an application that is closing should stop answering.
	if a.sim != nil {
		a.sim.fleet.StopAll()
	}
	a.tray.Stop()
	// Then the supervision: the service let go of, or the window's own engine
	// closed — its producers before its consumers.
	a.stopLink()
}

// SecretsBackend names the protection actually in use on this machine, so the
// settings UI can state what is true here rather than what was hoped for.
func (a *App) SecretsBackend() string {
	if a.secrets == nil {
		return "unavailable"
	}
	return a.secrets.Backend()
}
