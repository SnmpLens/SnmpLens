package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/mib"
	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/network"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
	"SnmpLens/pkg/tray"
	"SnmpLens/pkg/updater"

	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct holds our services and application context.
type App struct {
	ctx              context.Context
	mibs             fs.FS
	presets          fs.FS
	persistentMibDir string
	mibService       *mib.Service
	snmpClient       *snmp.Client
	storage          *storage.Storage
	evaluator        *monitor.Evaluator
	router           *eventRouter
	dispatcher       *notify.Dispatcher
	secrets          secrets.Store
	// mibStatuses holds what the last load did with each MIB and the analysis
	// of each, for the one verdict every screen shows (mibstatus.go).
	mibStatuses mibStatusCache
	// settingsKeyCache holds the renderer's sealing key. Every secrets.Get is
	// a whole-file read and decrypt, and a settings save seals one value per
	// sensitive field plus three per target override.
	settingsKeyCache []byte
	// Guards settingsKeyCache. Wails dispatches every bound method on its own
	// goroutine, and a settings load issues KeyStatus, AdoptKey and Open while
	// a save issues Seal — concurrently, on the same field.
	settingsKeyMu sync.Mutex
	updater       *updater.Service

	// Background-mode state. configDir is the SnmpLens directory, kept here
	// because several subsystems need it after startup.
	configDir  string
	serviceCfg service.Config
	trayIcons  trayIcons
	tray       *tray.Controller
	trayLive   bool
	trapsOn    bool
	scheduler  *monitor.Scheduler

	// sim holds and runs the simulated devices (simulator.go).
	sim *simulatorService
	// trapEngineID is the trap listener's snmpEngineID (trapengine.go).
	trapEngineID []byte
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

	// 3. Initialize SQLite storage for monitoring data
	dbPath := filepath.Join(configDir, "SnmpLens", "monitoring.db")
	store, err := storage.Init(dbPath)
	if err != nil {
		log.Printf("WARNING: Failed to initialize monitoring storage: %v", err)
	} else {
		a.storage = store
		a.initEvaluator()
		a.initDispatcher()

		// The router BEFORE the recorder is wired: recordEvent hands events to
		// it, and a producer must never find it absent and silently fall back.
		a.router = newEventRouter(a)
		a.router.start()

		// Anything journalled but not routed before the last shutdown — a crash,
		// a kill, a flush that could not be written. Done before any producer
		// starts, so replay and live routing cannot interleave on the watermark.
		a.replayUnrouted()

		// Traps are journalled by the SNMP client itself, in its listener
		// goroutine, before anything is emitted to the webview.
		a.snmpClient.SetRecorder(events.RecorderFunc(a.recordEvent))
	}

	// The secret store, INDEPENDENT of the database.
	//
	// It used to open only inside the else branch above, so a corrupt
	// monitoring.db took the credentials with it. Sink secrets could survive
	// that; the SNMP credentials that now live here cannot, and locking
	// someone out of their own community string because a history database
	// failed to open is not a trade anyone would choose.
	if store, err := secrets.Open(filepath.Join(configDir, "SnmpLens")); err != nil {
		log.Printf("WARNING: secret storage unavailable, stored credentials cannot be read: %v", err)
	} else {
		a.secrets = store
		log.Printf("Secret storage backend: %s", store.Backend())
	}

	// The simulated devices are read, and those set to start with the
	// application are started — after the secret store, which holds their
	// communities and passphrases. The others answer once someone starts them.
	a.sim = newSimulatorService(filepath.Join(configDir, "SnmpLens"))
	// Which column is a RowStatus is in the MIBs, and the agents have none: a
	// row is created and destroyed as the loaded MIBs describe it.
	a.sim.fleet.RowStatus = a.mibService.RowStatusColumn
	a.startAutoSimulated()

	// The trap listener's engine ID, before initBackgroundMode can start the
	// listener: a device sending it SNMPv3 INFORMs is configured with it.
	a.trapEngineID = loadTrapEngineID(filepath.Join(configDir, "SnmpLens"))
	a.snmpClient.SetTrapEngineID(a.trapEngineID)

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

	// The template vocabulary exposes the version; pkg/notify keeps it in a
	// package var rather than importing pkg/updater for one string.
	notify.AppVersion = a.GetAppVersion()

	// 5. The poll clock. It lives in Go so that monitoring, thresholds and the
	// notification routes keep working with the window closed — which is the
	// entire point of background mode.
	a.initScheduler()

	// 6. Tray, hide-on-close and the trap listener, in that order: the tray
	// decides whether closing the window may merely hide it.
	a.initBackgroundMode()

	// 7. Pick up what was running when we last exited.
	a.resumeActiveSessions()
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
	if !a.serviceCfg.AuditFailedSets || a.storage == nil {
		return
	}
	for _, r := range results {
		if r == nil || r.Error == "" {
			continue
		}
		a.journalEvent(events.Event{
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

// journalEvent records an event, logging rather than propagating a failure:
// every caller is on a path whose real work already succeeded or already
// reported its own error.
func (a *App) journalEvent(e events.Event) {
	if err := a.recordEvent(e, ""); err != nil {
		log.Printf("WARNING: could not journal a %s event: %v", e.Kind, err)
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

// StopTrapListener stops the active trap listener.
func (a *App) StopTrapListener() {
	a.snmpClient.StopTrapListener()
}

// ListMibFiles returns a list of MIB file names in the specified directory.
func (a *App) ListMibFiles(dirPath string) ([]string, error) {
	return mib.ListMibFiles(dirPath)
}

// MibImportResult holds per-file results for a MIB import operation.
// maxImportBytes bounds what ImportMibFiles will read into memory. The largest
// MIBs in circulation are a couple of megabytes.
const maxImportBytes = 16 << 20

type MibImportResult struct {
	FileName string `json:"fileName"`
	Success  bool   `json:"success"`
	Skipped  bool   `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ImportMibFiles copies the given files (or directories, recursively) into the
// persistent MIB directory. It returns per-file results so the frontend can
// report failures.
func (a *App) ImportMibFiles(filePaths []string) []MibImportResult {
	var results []MibImportResult

	for _, src := range filePaths {
		info, err := os.Stat(src)
		if err != nil {
			results = append(results, MibImportResult{
				FileName: filepath.Base(src),
				Success:  false,
				Error:    fmt.Sprintf("stat error: %v", err),
			})
			continue
		}

		if info.IsDir() {
			// Walk the directory recursively, importing regular files only
			filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil // skip dirs and errors silently
				}
				results = append(results, a.importSingleFile(path))
				return nil
			})
		} else {
			results = append(results, a.importSingleFile(src))
		}
	}
	return results
}

// importSingleFile copies one file into the persistent MIB directory.
// If an identical file already exists, it is skipped.
func (a *App) importSingleFile(src string) MibImportResult {
	name := filepath.Base(src)
	dst := filepath.Join(a.persistentMibDir, name)

	// Bounded before it is read. os.ReadFile on a renderer-named path is an
	// unbounded allocation, so a path to a large file — or to a device that
	// never ends — is a way to take the process down without sending a single
	// packet. The largest MIB anyone ships is a couple of megabytes; the
	// benchmark in pkg/mib uses 185 KB.
	if info, err := os.Stat(src); err == nil && info.Size() > maxImportBytes {
		return MibImportResult{FileName: name, Success: false,
			Error: fmt.Sprintf("this file is %d MB; a MIB is text and nothing legitimate is over %d MB",
				info.Size()/(1<<20), maxImportBytes/(1<<20))}
	}

	srcData, err := os.ReadFile(src)
	if err != nil {
		log.Printf("ImportMibFiles: failed to read %s: %v", src, err)
		return MibImportResult{FileName: name, Success: false, Error: fmt.Sprintf("read error: %v", err)}
	}

	// A standard MIB that ships in the binary is never overwritten from here.
	//
	// This is not an exotic case: vendor archives routinely include their own
	// SNMPv2-SMI, SNMPv2-TC and SNMPv2-CONF, and nearly every other MIB imports
	// from those three. Replacing one with a copy that differs or is truncated
	// breaks the tree for everything, and it STAYS broken across restarts,
	// because ensureStandardMibs restores only files that are ABSENT
	// (`if _, err := os.Stat(destPath); err == nil { continue }`) — a replaced
	// one is never repaired. So one drag-and-drop of a vendor zip could take
	// out MIB resolution for good, with nothing saying what happened.
	//
	// Refused rather than backed up: the MIB editor already owns deliberate
	// change to a bundled file, with its own backup directory and
	// MibEditorRestoreBundled to undo it. An import is a bulk operation nobody
	// reads the results of unless something goes wrong, which is exactly when
	// this needs to be loud. The refusal reaches the per-file error list the
	// import dialog already shows.
	if _, bundled := a.bundledContent(name); bundled {
		log.Printf("ImportMibFiles: refused to replace the bundled %s", name)
		return MibImportResult{
			FileName: name,
			Success:  false,
			Error: fmt.Sprintf("%s ships with SnmpLens and nearly every MIB imports from it; "+
				"replacing it would break MIB resolution. Use the MIB editor if you really mean to change it.", name),
		}
	}

	// Only a MIB gets into the MIB directory.
	//
	// This is a security boundary as much as a usability one. This method reads
	// any absolute path the renderer names, and MibEditorRead hands the content
	// of anything in that directory back — so the two calls together are an
	// arbitrary-file-read primitive over the bridge. What decides how much that
	// is worth is what is allowed to land here. A private key, a password file,
	// a browser profile: not a MIB, not admitted, never readable.
	//
	// It is NOT a validity check: a MIB with a syntax error is precisely what
	// the diagnosis feature exists for. The refusal reuses pkg/mib's own
	// description of the files people download by mistake, so an HTML page or a
	// PDF is still named as what it is — now without leaving a copy behind.
	if summary, hint, reject := mib.ImportRejection(srcData); reject {
		msg := summary
		if hint != "" {
			msg += " — " + hint
		}
		log.Printf("ImportMibFiles: refused %s (%s)", name, summary)
		return MibImportResult{FileName: name, Success: false, Error: msg}
	}

	// Check if the destination already has an identical file
	if dstData, err := os.ReadFile(dst); err == nil {
		if bytes.Equal(srcData, dstData) {
			log.Printf("ImportMibFiles: skipped %s (already exists)", name)
			return MibImportResult{FileName: name, Success: true, Skipped: true}
		}
	}

	if err := os.WriteFile(dst, srcData, 0644); err != nil {
		log.Printf("ImportMibFiles: failed to write %s: %v", dst, err)
		return MibImportResult{FileName: name, Success: false, Error: fmt.Sprintf("write error: %v", err)}
	}
	log.Printf("ImportMibFiles: imported %s", name)
	return MibImportResult{FileName: name, Success: true}
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
	// The trap listener goes FIRST, and it was not being stopped at all.
	//
	// It is the one producer fed from outside: a datagram arriving during
	// teardown runs handleTrap on gosnmp's goroutine, which journals and routes
	// through storage that the lines below are about to close. Stopping every
	// producer before any consumer is the only ordering in which the rest of
	// this function's reasoning holds.
	if a.snmpClient != nil {
		a.snmpClient.StopTrapListener()
	}
	// The simulated devices with it. They touch nothing below, but they hold
	// sockets, and an application that is closing should stop answering.
	if a.sim != nil {
		a.sim.fleet.StopAll()
	}

	// Then the router: every producer is stopped, so what it has queued is all
	// there will ever be. Anything it cannot write stays above the watermark
	// and is replayed on the next launch.
	if a.router != nil {
		a.router.stop()
	}

	// Then the poll clock: its goroutines write samples through storage,
	// so letting them run into a closed database would log failures for work
	// that was actually fine.
	if a.scheduler != nil {
		a.scheduler.StopAll()
	}
	a.tray.Stop()
	// Stop the dispatcher BEFORE closing storage: it queries the outbox on its
	// own goroutine, and a drain racing a closed database would log spurious
	// failures against deliveries that are actually fine.
	if a.dispatcher != nil {
		a.dispatcher.Stop()
	}
	if a.storage != nil {
		if err := a.storage.Close(); err != nil {
			log.Printf("Error closing monitoring storage: %v", err)
		}
	}
}

// --- Monitoring Storage Methods ---

// MonitorSaveDataPoints queues data points for batch insertion.
func (a *App) MonitorSaveDataPoints(points []storage.DataPoint) {
	if a.storage == nil {
		return
	}
	a.storage.QueueDataPoints(points)
}

// MonitorLoadSessions returns all persisted monitoring sessions.
func (a *App) MonitorLoadSessions() ([]storage.Session, error) {
	if a.storage == nil {
		return []storage.Session{}, nil
	}
	return a.storage.ListSessions()
}

// MonitorLoadSessionData loads recent data points for a session.
func (a *App) MonitorLoadSessionData(sessionID string, limit int) ([]storage.DataPoint, error) {
	if a.storage == nil {
		return []storage.DataPoint{}, nil
	}
	return a.storage.QueryDataPoints(sessionID, "", "", limit)
}

// MonitorLoadHistoricalData loads data points for a specific time range.
func (a *App) MonitorLoadHistoricalData(sessionID, from, to string) ([]storage.DataPoint, error) {
	if a.storage == nil {
		return []storage.DataPoint{}, nil
	}
	return a.storage.QueryDataPoints(sessionID, from, to, 0)
}

// MonitorDeleteSession removes a session and all its data.
// MonitorDeleteSession removes a session AND the credentials it named.
//
// Deleting the row alone left the session's community and v3 passphrases in
// DPAPI, the Keychain or the file store forever, under a key nothing in the
// app will ever look up again — the same defect NotifyDeleteSink had, in the
// other place that writes to pkg/secrets. Session credentials are deliberately
// kept out of monitoring.db (storage.SessionConn holds only what is safe to
// read in a copied database), which is exactly why deleting the database row
// cannot be the whole of it.
//
// The credential goes first, for the reason NotifyDeleteSink gives: it is the
// part that cannot be retried once the row is gone. If the store refuses — a
// locked keychain — the session stays and the operator can try again after
// unlocking. With no store at all there is nothing that could have been
// written, so the deletion proceeds.
func (a *App) MonitorDeleteSession(sessionID string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if a.secrets != nil {
		if err := a.secrets.Delete(secrets.SessionRef(sessionID)); err != nil {
			return fmt.Errorf("the session was kept: its stored credentials could not be removed: %w", err)
		}
	}
	return a.storage.DeleteSession(sessionID)
}

// MonitorGetStats returns aggregate statistics for a monitoring session.
func (a *App) MonitorGetStats(sessionID string) (storage.SessionStats, error) {
	if a.storage == nil {
		return storage.SessionStats{}, nil
	}
	return a.storage.GetSessionStats(sessionID)
}

// MonitorCleanup deletes data older than the specified number of days.
func (a *App) MonitorCleanup(daysToKeep int) (int64, error) {
	if a.storage == nil {
		return 0, fmt.Errorf("storage not initialized")
	}
	n, removedSessions, err := a.storage.Cleanup(time.Duration(daysToKeep) * 24 * time.Hour)
	// The rows are gone; their credentials must go with them. Best effort and
	// logged rather than returned: retention has already done the part the
	// operator asked for, and a locked keychain is not a reason to report the
	// cleanup as failed.
	if a.secrets != nil {
		for _, id := range removedSessions {
			if delErr := a.secrets.Delete(secrets.SessionRef(id)); delErr != nil {
				log.Printf("monitor: session %s was removed but its credentials could not be: %v", id, delErr)
			}
		}
	}
	return n, err
}

// MonitorLoadBuckets returns a session's data aggregated into fixed-width time
// buckets (bucketSec seconds), so long ranges render from a bounded number of
// points instead of every raw sample.
func (a *App) MonitorLoadBuckets(sessionID, from, to string, bucketSec int) ([]storage.Bucket, error) {
	if a.storage == nil {
		return []storage.Bucket{}, nil
	}
	return a.storage.QueryBuckets(sessionID, from, to, bucketSec)
}

// MonitorImportLegacyData imports data from localStorage migration.
func (a *App) MonitorImportLegacyData(sessions []storage.Session, points map[string][]storage.DataPoint) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.ImportLocalStorageData(sessions, points)
}

// MonitorUpdateSession updates a session's active status.
func (a *App) MonitorUpdateSession(sessionID string, active bool, stoppedAt string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.UpdateSession(sessionID, active, stoppedAt)
}

// --- Query History Persistence ---

// SaveHistoryEntry persists a single query-history entry to SQLite.
func (a *App) SaveHistoryEntry(entry map[string]interface{}) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.SaveHistory(entry)
}

// LoadHistory returns all persisted query-history entries, newest first.
func (a *App) LoadHistory() ([]map[string]interface{}, error) {
	if a.storage == nil {
		return []map[string]interface{}{}, nil
	}
	return a.storage.LoadHistory()
}

// DeleteHistoryEntry removes a single query-history entry by id.
func (a *App) DeleteHistoryEntry(id string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.DeleteHistoryEntry(id)
}

// DeleteHistoryEntries removes several query-history entries at once.
func (a *App) DeleteHistoryEntries(ids []string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.DeleteHistoryEntries(ids)
}

// ClearHistory removes every query-history entry.
func (a *App) ClearHistory() error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.ClearHistory()
}

// CountHistory returns the number of persisted query-history entries.
func (a *App) CountHistory() (int, error) {
	if a.storage == nil {
		return 0, nil
	}
	return a.storage.CountHistory()
}

// ImportHistoryEntries bulk-inserts query-history entries (localStorage
// migration and JSON import from the UI).
func (a *App) ImportHistoryEntries(entries []map[string]interface{}) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.ImportHistoryEntries(entries)
}

// --- Event journal ---

// recordEvent persists an event and tells any open window about it. It is the
// single Recorder implementation handed to every producer.
//
// Persist first, notify second: the runtime event is a UI hint and is dropped
// when no window is listening, whereas the row is the record of what happened.
func (a *App) recordEvent(e events.Event, payload string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	saved, err := a.storage.InsertEvent(e, payload)
	if err != nil {
		return err
	}

	// Hand the event to the router rather than routing it here.
	//
	// For a trap, "here" is gosnmp's UDP receive loop — strictly serial, so
	// every millisecond costs datagrams. Routing was measured at 1.30 ms per
	// event against 0.97 ms for the insert itself, essentially all of it a
	// second write transaction.
	//
	// The INSERT above stays synchronous: gosnmp acknowledges an INFORM after
	// this handler returns, and acknowledging a confirmed notification before it
	// is durably journalled would be a lie.
	//
	// A full queue falls back to routing INLINE — never dropping. That is
	// exactly what this did before the router existed, so the worst case is the
	// behaviour we already had.
	if a.router == nil || !a.router.accept(saved) {
		a.routeEvent(saved)
	}

	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "event:new", saved)
	}
	return nil
}

// deadLetterSink names the sink a dead-letter event is ABOUT, or "".
//
// Read from the event rather than passed alongside it, so any future path that
// records one is protected without having to remember to.
func deadLetterSink(e events.Event) string {
	if e.Kind != events.KindSystemSinkDeadLetter {
		return ""
	}
	// "sinkId", not "sink". `sink` carries the sink's NAME, because that is what
	// an operator reads in "Delivery to {sink} given up" — and comparing a name
	// against the ids in a route silently disabled this guard the moment the
	// message was made legible. The event now carries both; the id is the one
	// that means something to the router.
	if id, ok := e.Params["sinkId"].(string); ok && id != "" {
		return id
	}
	// Events journalled before the id was recorded carry only the name, which
	// was also the id back when they were written.
	if id, ok := e.Params["sink"].(string); ok {
		return id
	}
	return ""
}

// deadLetterEvent builds the event announcing that a delivery was given up on.
//
// Extracted so a test can drive the same construction production does. The
// version of this test that built the event by hand passed throughout the
// window in which the guard above was broken, because a hand-written event puts
// whatever the test author expects into Params.
func (a *App) deadLetterEvent(q notify.Queued, err error) events.Event {
	name := a.sinkName(q.SinkID)
	return events.Event{
		Category: events.CategorySystem,
		Kind:     events.KindSystemSinkDeadLetter,
		Severity: events.SevMajor.String(),
		TitleKey: "events.kind." + events.KindSystemSinkDeadLetter,
		Params: map[string]any{
			// The NAME, because "{sink}" is what the operator reads. A raw UUID
			// names nothing they have ever seen, in the one message telling them
			// an alert never arrived.
			"sink": name,
			// And the ID, because the routing guard needs to compare it against
			// the ids a rule holds.
			"sinkId":   q.SinkID,
			"attempts": q.Attempts + 1,
			"error":    err.Error(),
		},
		Summary: "Delivery to " + name + " given up: " + err.Error(),
	}
}

// routedGroupsFor decides where an event goes and renders it for each
// destination, WITHOUT writing anything.
//
// Separated from the write so the same decision can be made on the producer's
// goroutine or on the router's, and replayed at startup, from one piece of code.
func (a *App) routedGroupsFor(e events.Event) ([]storage.RoutedGroup, error) {
	// A read that FAILS is not the same as a configuration with nothing in it.
	//
	// Returning nil for both made a transient failure indistinguishable from
	// "no rule matched", and the router then recorded the watermark past the
	// event — so it was never delivered and never replayed, which is the one
	// outcome the watermark exists to prevent.
	routes, err := a.storage.ListRoutes()
	if err != nil {
		return nil, fmt.Errorf("could not read the routing rules: %w", err)
	}
	if len(routes) == 0 {
		return nil, nil
	}
	// Evaluated at the EVENT's own time, in the local zone.
	//
	// Routing then depends only on the event, which is what lets it be replayed
	// after a crash and reach the same answer. The zone conversion is not
	// cosmetic: every producer writes Ts in UTC while quiet hours are wall-clock
	// times an operator typed, so comparing them directly rotates every window by
	// the machine's UTC offset.
	sinkIDs := notify.SelectAt(routes, e, time.Local)

	// A dead letter must never go back to the sink that produced it.
	//
	// The failure becomes an event, the event is routed, and a catch-all rule
	// — the natural thing to write — selects the sink that just failed. That
	// delivery fails, produces another dead letter, and so on: one unreachable
	// collector grows the event journal and the outbox without bound, and each
	// cycle is another six attempts against a machine that is not answering.
	//
	// It still goes to the OTHER sinks, which is the useful case: "the mail
	// relay is down" is exactly what you want to hear over syslog.
	if failing := deadLetterSink(e); failing != "" {
		kept := sinkIDs[:0]
		for _, id := range sinkIDs {
			if id != failing {
				kept = append(kept, id)
			}
		}
		sinkIDs = kept
	}

	if len(sinkIDs) == 0 {
		return nil, nil
	}

	// Redaction is decided per sink, so group by the rendering it needs rather
	// than rendering once for everyone.
	sinks, err := a.storage.ListSinks()
	if err != nil {
		return nil, fmt.Errorf("could not read the destinations: %w", err)
	}
	configBySink := map[string]notify.SinkConfig{}
	for _, s := range sinks {
		configBySink[s.ID] = s
	}

	// Render once per sink, then group the identical results. Sinks usually
	// share a rendering, and each group is one transaction on the synchronous
	// insert path; grouping by the OUTPUT rather than by a guessed key means a
	// new template variable can never silently merge two sinks that should
	// have differed.
	type group struct {
		event   events.Event
		subject string
		body    string
		sinkIDs []string
	}
	groups := map[string]*group{}

	for _, id := range sinkIDs {
		cfg, known := configBySink[id]

		// A sink that is switched off is not written to, and a route naming a
		// sink that no longer exists queues nothing.
		//
		// Only the dispatcher used to look at Enabled — where a disabled sink
		// cannot be resolved and the delivery is dead-lettered, and a dead
		// letter is a MAJOR system event. So switching the mail sink off for
		// the weekend answered EVERY event with a major alarm saying the mail
		// sink could not be reached, which is the opposite of what the switch
		// means. A missing sink took the same path to the same place, having
		// never had any chance of being delivered.
		if !known || !cfg.Enabled {
			continue
		}

		// Masking happens BEFORE templating, always. A template can name
		// fields the built-in rendering never showed — dedupKey, params — so
		// masking inside the renderer would leave exactly those uncovered.
		ev := e
		if cfg.Redact {
			ev = notify.RedactEvent(e)
		}
		subject, body := a.renderForSink(ev, cfg)

		key := fmt.Sprintf("%t|%q|%q", cfg.Redact, subject, body)
		g, ok := groups[key]
		if !ok {
			g = &group{event: ev, subject: subject, body: body}
			groups[key] = g
		}
		g.sinkIDs = append(g.sinkIDs, id)
	}

	out := make([]storage.RoutedGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, storage.RoutedGroup{
			Event: g.event, SinkIDs: g.sinkIDs, Subject: g.subject, Body: g.body,
		})
	}
	return out, nil
}

// routeEvent routes an event and queues its deliveries immediately.
//
// The inline path: used when the router's queue is full, and by tests. It is
// what the whole application did before routing moved to a background worker,
// so falling back to it can never be worse than not having one.
func (a *App) routeEvent(e events.Event) {
	groups, err := a.routedGroupsFor(e)
	if err != nil {
		log.Printf("notify: could not route event %s: %v", e.ID, err)
		return
	}
	for _, g := range groups {
		if err := a.storage.EnqueueDeliveries(g.Event, g.SinkIDs, g.Subject, g.Body); err != nil {
			log.Printf("notify: could not queue deliveries: %v", err)
		}
	}
	if a.dispatcher != nil {
		a.dispatcher.Wake()
	}
}

// renderForSink applies the sink's template, falling back to the built-in
// rendering if anything goes wrong.
//
// The recover is not decoration. This runs on the trap-listener and the
// monitor-scheduler goroutines; a panic in a formatting routine would take
// down the background monitoring that pkg/monitor exists to keep alive. A bug
// here must cost formatting, never the alert.
func (a *App) renderForSink(ev events.Event, cfg notify.SinkConfig) (subject, body string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("notify: template for sink %q panicked, using the default rendering: %v", cfg.Name, r)
			subject, body = notify.Render(ev, false) // ev is already redacted
		}
	}()
	// The escaping follows the format the body is DECLARED as, because the body
	// carries values that arrived from the network unauthenticated: one quote in
	// a trap's OID turns a hand-written JSON payload into a parse error at the
	// receiver, and one "<" ends a paragraph in an HTML mail and takes the rest
	// of the message with it.
	switch {
	case cfg.Kind == notify.SinkWebhook &&
		strings.EqualFold(strings.TrimSpace(cfg.Webhook.PayloadMode), notify.PayloadTemplate):
		return notify.RenderWebhookTemplate(cfg.Webhook, ev, cfg.Name, cfg.Template)
	case cfg.Kind == notify.SinkEmail &&
		strings.EqualFold(strings.TrimSpace(cfg.Email.Format), notify.EmailFormatHTML):
		return notify.RenderHTMLTemplate(ev, cfg.Name, cfg.Template)
	}
	return notify.RenderTemplate(ev, cfg.Name, cfg.Template)
}

// EventsQuery returns one page of the journal, newest first.
func (a *App) EventsQuery(filter events.Filter) (events.Page, error) {
	if a.storage == nil {
		return events.Page{Items: []events.Event{}}, nil
	}
	return a.storage.QueryEvents(filter)
}

// EventsPayload returns an event's bulk detail, loaded only when opened.
func (a *App) EventsPayload(id string) (string, error) {
	if a.storage == nil {
		return "", nil
	}
	return a.storage.EventPayload(id)
}

// EventsCounts feeds the unacknowledged badge without fetching rows.
func (a *App) EventsCounts() (events.Counts, error) {
	if a.storage == nil {
		return events.Counts{UnackedBySev: map[string]int{}, UnackedByCatego: map[string]int{}}, nil
	}
	return a.storage.EventCounts()
}

// EventsAck acknowledges specific events.
func (a *App) EventsAck(ids []string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.AckEvents(ids)
}

// EventsAckAll acknowledges everything matching a filter.
func (a *App) EventsAckAll(filter events.Filter) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.AckAllEvents(filter)
}

// EventsDelete removes events and their payloads.
func (a *App) EventsDelete(ids []string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.DeleteEvents(ids)
}

// EventsClear empties the journal.
func (a *App) EventsClear() error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.ClearEvents()
}

// --- Threshold and reachability evaluation ---

// generateEventCorrID returns the id that ties a resolution event back to the
// opening event of the same incident.
func generateEventCorrID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("corr-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("corr-%x", b)
}

// initEvaluator wires the breach evaluator to the journal and restores any
// episode that was still open when the app last closed. Without the restore, a
// restart would re-announce an incident that had already been reported.
func (a *App) initEvaluator() {
	ev := monitor.NewEvaluator()
	ev.Record = a.recordEvent
	ev.NewCorrID = func() string { return generateEventCorrID() }
	ev.SaveEpi = func(r monitor.EpisodeRecord) error {
		return a.storage.SaveEpisode(storage.Episode{
			DedupKey:  r.DedupKey,
			Kind:      r.Kind,
			SessionID: r.SessionID,
			Target:    r.Target,
			OID:       r.OID,
			FirstSeen: r.FirstSeen,
			FiredAt:   r.FiredAt,
			CorrID:    r.CorrID,
		})
	}
	ev.DeleteEpi = a.storage.DeleteEpisode

	if saved, err := a.storage.LoadEpisodes(); err == nil {
		records := make([]monitor.EpisodeRecord, 0, len(saved))
		for _, e := range saved {
			records = append(records, monitor.EpisodeRecord{
				DedupKey:  e.DedupKey,
				Kind:      e.Kind,
				SessionID: e.SessionID,
				Target:    e.Target,
				OID:       e.OID,
				FirstSeen: e.FirstSeen,
				FiredAt:   e.FiredAt,
				CorrID:    e.CorrID,
			})
		}
		ev.Restore(records)
	} else {
		log.Printf("WARNING: failed to restore open incidents: %v", err)
	}

	a.evaluator = ev
}

// --- Notification routing ---

// initDispatcher starts the outbox drain loop.
func (a *App) initDispatcher() {
	resolve := func(sinkID string) (notify.Sink, bool) {
		sinks, err := a.storage.ListSinks()
		if err != nil {
			return nil, false
		}
		for _, cfg := range sinks {
			if cfg.ID != sinkID {
				continue
			}
			if !cfg.Enabled {
				return nil, false
			}
			return notify.Build(cfg, a.sinkSecret(cfg.ID))
		}
		return nil, false
	}

	d := notify.NewDispatcher(a.storage, resolve, 15*time.Second)
	d.OnResult = func(q notify.Queued, err error, dead bool) {
		if err == nil || !dead {
			return
		}
		// A dead letter is the only signal an operator gets that a
		// notification never arrived, so it becomes an event of its own.
		a.recordEvent(a.deadLetterEvent(q, err), "")
	}
	d.Start()
	a.dispatcher = d
}

// sinkName resolves a destination's id to what the operator called it, falling
// back to the id when it has been deleted — which is itself informative.
func (a *App) sinkName(sinkID string) string {
	if a.storage == nil {
		return sinkID
	}
	sinks, err := a.storage.ListSinks()
	if err != nil {
		return sinkID
	}
	for _, s := range sinks {
		if s.ID == sinkID && strings.TrimSpace(s.Name) != "" {
			return s.Name
		}
	}
	return sinkID
}

// sinkSecret returns a sink's credential from secure storage. Secrets are kept
// out of the sink config so it can be exported, logged or read from the
// database without leaking one.
func (a *App) sinkSecret(sinkID string) string {
	if a.secrets == nil || sinkID == "" {
		return ""
	}
	v, err := a.secrets.Get(secrets.SinkRef(sinkID))
	if err != nil {
		return ""
	}
	return v
}

// storedSink returns the destination currently on file under id.
//
// There is no GetSink, and adding one for this would put a second read path
// against notify_sinks; the list is a handful of rows read on a settings
// screen, not a hot path.
func (a *App) storedSink(id string) (notify.SinkConfig, bool) {
	if a.storage == nil || id == "" {
		return notify.SinkConfig{}, false
	}
	sinks, err := a.storage.ListSinks()
	if err != nil {
		return notify.SinkConfig{}, false
	}
	for _, s := range sinks {
		if s.ID == id {
			return s, true
		}
	}
	return notify.SinkConfig{}, false
}

// boundSinkSecret returns the stored credential for cfg.ID, but ONLY if cfg
// still names the destination that credential was stored against.
//
// See notify.Destination for why this exists: resolving by id while taking the
// address from the caller is the one path that turns a write-only credential
// store into a read primitive.
func (a *App) boundSinkSecret(cfg notify.SinkConfig) (string, error) {
	stored, ok := a.storedSink(cfg.ID)
	if !ok {
		// Nothing on file under that id: a sink being tested before it is
		// saved. There is no stored credential to hand out, and the caller
		// supplying one in cfg.Secret is the normal path for a new sink.
		return "", nil
	}
	if !notify.SameDestination(stored, cfg) {
		return "", fmt.Errorf("this destination differs from the saved one, so the stored credential " +
			"cannot be used with it. Enter the credential to test the new destination")
	}
	return a.sinkSecret(cfg.ID), nil
}

// SecretsBackend names the protection actually in use on this machine, so the
// settings UI can state what is true here rather than what was hoped for.
func (a *App) SecretsBackend() string {
	if a.secrets == nil {
		return "unavailable"
	}
	return a.secrets.Backend()
}

// errNoStorage is what the notification bindings answer when monitoring.db
// could not be opened.
//
// They used to answer an EMPTY LIST and a nil error, so the settings page
// rendered "no destinations yet" — visually identical to a fresh install. An
// operator whose database failed to open saw their entire notification
// configuration as GONE, and only found out it was still on disk when a save
// answered "storage not initialized". Nothing is a fact; it must not be
// reported as one when it is really "I could not look".
var errNoStorage = errors.New("the monitoring database is not open, so the notification configuration cannot be read")

// NotifyListSinks returns the configured destinations. Credentials are never
// included — only whether one is on file.
func (a *App) NotifyListSinks() ([]notify.SinkConfig, error) {
	if a.storage == nil {
		return []notify.SinkConfig{}, errNoStorage
	}
	sinks, err := a.storage.ListSinks()
	if err != nil {
		return sinks, err
	}
	for i := range sinks {
		sinks[i].Secret = ""
		sinks[i].HasSecret = a.sinkSecret(sinks[i].ID) != ""
	}
	return sinks, nil
}

// NotifySaveSink creates or updates a destination.
//
// The credential is split off and stored separately: SinkConfig.Secret is
// write-only transport, and the config that reaches the database must never
// carry it.
func (a *App) NotifySaveSink(cfg notify.SinkConfig) (notify.SinkConfig, error) {
	// Refuse a broken template here rather than at 03:00. Rendering itself
	// cannot fail — it runs at enqueue, where an error would lose the alert
	// instead of spoiling its formatting — so this is the only gate.
	if errs := notify.ValidateTemplate(cfg.Template); len(errs) > 0 {
		return notify.SinkConfig{}, fmt.Errorf("%s", errs[0].Error())
	}
	// A webhook that sends its template as the payload must produce JSON. Find
	// out here rather than at 03:00, when the only symptom is a receiver
	// rejecting a body nobody can see.
	if err := notify.ValidatePayloadTemplate(cfg); err != nil {
		return notify.SinkConfig{}, err
	}
	// The same reasoning, for syslog over TLS: a certificate whose key does not
	// match, a secret that is not PEM, a certificate cleared while its key stays
	// in the store. All four surface only at send, none is classified permanent,
	// so every routed event retried six times and dead-lettered — starting at
	// the first incident after the save.
	//
	// The key comes from the store when the form did not carry one, because an
	// edit that changes the address must be checked against the key already
	// held rather than against nothing.
	if cfg.Kind == notify.SinkSyslog {
		key := cfg.Secret
		if key == "" && cfg.ID != "" {
			key = a.sinkSecret(cfg.ID)
		}
		if err := notify.ValidateTLSMaterial(cfg.Syslog, key); err != nil {
			return notify.SinkConfig{}, err
		}
	}
	if a.storage == nil {
		return cfg, fmt.Errorf("storage not initialized")
	}

	incoming := cfg.Secret
	cfg.Secret = ""

	// Pointing an EXISTING sink at a new destination is the durable version of
	// the same problem NotifyTestSink has: the save path never compared the
	// address it was given with the address the credential was stored against,
	// so a renderer that cannot read a credential could still aim it. Read the
	// previous row BEFORE writing over it.
	previous, hadRow := a.storedSink(cfg.ID)
	rebound := hadRow && !notify.SameDestination(previous, cfg)

	saved, err := a.storage.SaveSink(cfg)
	if err != nil {
		return saved, err
	}

	// A credential does not follow its destination. Deleted rather than
	// refused: a collector really does move, and refusing the save would leave
	// the operator editing a form they cannot submit, holding a credential
	// they cannot read back. HasSecret then comes back false, which is what
	// the form shows, and the renderer says so out loud.
	if rebound && incoming == "" && a.secrets != nil {
		if err := a.secrets.Delete(secrets.SinkRef(saved.ID)); err != nil {
			log.Printf("NotifySaveSink: could not unbind the credential of %s: %v", saved.ID, err)
		}
	}

	// An empty field means "leave the stored credential alone", not "clear it":
	// the UI never receives the current value, so it cannot echo it back.
	if incoming != "" {
		// A credential with nowhere to go is a FAILURE, not a no-op. The guard
		// used to be `incoming != "" && a.secrets != nil`, so with no protector
		// — a locked keychain, a config directory that cannot be written — the
		// typed token was silently dropped and the save reported success. The
		// operator sees the destination saved, believes the token is held, and
		// finds out at 03:00 when the webhook posts unauthenticated.
		if a.secrets == nil {
			return saved, fmt.Errorf("saved the destination, but its credential could not be stored: %s", a.SecretsBackend())
		}
		if err := a.secrets.Set(secrets.SinkRef(saved.ID), incoming); err != nil {
			return saved, fmt.Errorf("saved the destination but could not store its credential: %w", err)
		}
	}
	saved.Secret = ""
	saved.HasSecret = a.sinkSecret(saved.ID) != ""
	return saved, nil
}

// NotifyClearSinkSecret removes a destination's stored credential.
func (a *App) NotifyClearSinkSecret(sinkID string) error {
	if a.secrets == nil {
		return fmt.Errorf("secret storage unavailable")
	}
	return a.secrets.Delete(secrets.SinkRef(sinkID))
}

// NotifyDeleteSink removes a destination AND the credential it named.
//
// Deleting the row alone left the bearer token or SMTP password in DPAPI, the
// Keychain or the file store forever — a credential the operator believes they
// deleted, outliving the thing that named it, under a key nothing in the app
// will ever look up again.
//
// The credential goes first, because that is the part that cannot be retried
// once the row is gone: if the store refuses — a locked keychain — the
// destination stays and the operator can try again after unlocking. With no
// store at all there is nothing that could have been written, so the deletion
// proceeds.
func (a *App) NotifyDeleteSink(id string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if a.secrets != nil {
		if err := a.secrets.Delete(secrets.SinkRef(id)); err != nil {
			return fmt.Errorf("the destination was kept: its stored credential could not be removed: %w", err)
		}
	}
	if err := a.storage.DeleteSink(id); err != nil {
		return err
	}

	// And unbind it from every rule that named it.
	//
	// Deleting the destination left its id in each route's SinkIDs. The rule
	// then rendered a raw UUID in the list — sinkName() falls back to the id —
	// and every matching event queued a delivery that nothing could ever
	// resolve. A rule pointing at one live destination and one dead id looks
	// like it works and half of it does not.
	//
	// Best effort AFTER the delete: the destination is gone either way, and a
	// rule that still names it is a cosmetic and routing problem, not a reason
	// to refuse the operator's action.
	if err := a.unbindSinkFromRoutes(id); err != nil {
		log.Printf("notify: the destination was deleted but some rules still name it: %v", err)
	}
	return nil
}

// unbindSinkFromRoutes removes a deleted sink's id from every routing rule.
//
// A rule left with NO destinations is disabled rather than deleted: it carries
// the operator's match conditions, which are the part that took thought, and
// silently removing their rules is worse than leaving one they can re-point.
func (a *App) unbindSinkFromRoutes(sinkID string) error {
	routes, err := a.storage.ListRoutes()
	if err != nil {
		return err
	}
	for _, r := range routes {
		kept := make([]string, 0, len(r.SinkIDs))
		for _, id := range r.SinkIDs {
			if id != sinkID {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(r.SinkIDs) {
			continue
		}
		r.SinkIDs = kept
		if len(kept) == 0 {
			r.Enabled = false
		}
		if _, err := a.storage.SaveRoute(r); err != nil {
			return err
		}
	}
	return nil
}

// NotifyTestSink sends a synthetic event so the operator can verify a
// destination without waiting for a real incident.
func (a *App) NotifyTestSink(cfg notify.SinkConfig) error {
	// Resolve the stored credential BEFORE building the sink: Build captures
	// the secret by value, so fetching it afterwards left every test of an
	// already-saved sink authenticating with nothing. The symptom was the
	// confusing one — "Test fails but the alerts themselves arrive".
	//
	// Looking the credential up by sink id is BOUND to the destination stored
	// under that id. Without that, this method is a read primitive over a
	// write-only store: name an existing sink's id, point the URL at your own
	// server, press Test, receive the bearer token or the SMTP password.
	//
	// The comment that used to stand here accepted it, on the grounds that
	// anyone able to drive the renderer "could simply read the sink list
	// instead". The line above in NotifyListSinks — sinks[i].Secret = "" —
	// disproves that: the store is deliberately write-only from the renderer,
	// and this was the one hole in it.
	secret := cfg.Secret
	if secret == "" {
		bound, err := a.boundSinkSecret(cfg)
		if err != nil {
			return err
		}
		secret = bound
	}
	sink, ok := notify.Build(cfg, secret)
	if !ok {
		return fmt.Errorf("unknown sink kind %q", cfg.Kind)
	}
	sample := events.Event{
		ID:       "test-" + generateEventCorrID(),
		Ts:       time.Now().UTC().Format(time.RFC3339),
		Category: events.CategorySystem,
		Kind:     events.KindSystemListenerStarted,
		Severity: events.SevInfo.String(),
		State:    events.StateOneshot,
		Source:   "SnmpLens",
		TitleKey: "events.kind." + events.KindSystemListenerStarted,
		Summary:  "SnmpLens test notification",
	}
	// Render through the SAME path as a real delivery, with the template as it
	// stands in the form. Otherwise the one button an operator presses to check
	// a template is the one path that never exercises it.
	ev := sample
	if cfg.Redact {
		ev = notify.RedactEvent(sample)
	}
	subject, body := a.renderForSink(ev, cfg)
	return sink.Send(ev, subject, body)
}

// NotifyTemplateVariables returns the template vocabulary for the editor.
//
// Generated from Go rather than mirrored in JavaScript: a hand-copied list is
// a list that drifts, and the first symptom would be a variable the UI offers
// and the renderer does not know.
func (a *App) NotifyTemplateVariables() []notify.VariableDoc {
	return notify.TemplateVariables()
}

// NotifyValidateTemplate reports every problem with a template at once, so the
// editor can mark them all rather than one per save attempt.
func (a *App) NotifyValidateTemplate(tpl notify.MessageTemplate) []notify.TemplateError {
	errs := notify.ValidateTemplate(tpl)
	if errs == nil {
		return []notify.TemplateError{}
	}
	return errs
}

// TemplatePreview is what the editor shows while a template is being written.
type TemplatePreview struct {
	Subject string                 `json:"subject"`
	Body    string                 `json:"body"`
	Errors  []notify.TemplateError `json:"errors"`
	// Json is set when the sink posts its template as the payload. The editor
	// shows what will actually be sent rather than a plain-text approximation
	// of it, because the two differ exactly where mistakes live: escaping.
	Json bool `json:"json"`
	// JsonValid says whether the rendered body parses. Not "valid template" —
	// a template can look like JSON with its placeholders still in place and
	// stop being JSON the moment one of them expands.
	JsonValid bool `json:"jsonValid"`
	// JsonError is the parse failure, verbatim.
	JsonError string `json:"jsonError,omitempty"`
	// Bytes is the size of the body as it will go on the wire.
	Bytes int `json:"bytes"`
	// ContentType is the header a webhook will actually send. The header and the
	// body have to agree, and an operator who has just changed the body format
	// needs to see that they do.
	ContentType string `json:"contentType,omitempty"`
	// Format says WHAT the body below is, so the editor can label and highlight
	// it: "syslog", "email", "json", "xml", "form" or "text".
	//
	// It exists because the preview used to show a rendered subject and body for
	// every kind, which is only what a webhook sends. A syslog sink sends one
	// RFC5424 line with a priority, a timestamp and structured data in front of
	// the message, and a mail sink sends headers — including the From, the
	// recipients and the Content-Type the operator is configuring three fields
	// above. Neither was visible until something arrived at the far end.
	Format string `json:"format"`
}

// NotifyPreviewTemplate renders a template against a canned event.
//
// A preview is not a nicety here: buildMessage silently substitutes the event
// summary for an empty subject, so a template that renders to nothing looks
// exactly like one that works. The sample carries real Params, which the test
// notification does not, so {{params.*}} can actually be seen.
func (a *App) NotifyPreviewTemplate(cfg notify.SinkConfig, kind string) TemplatePreview {
	out := TemplatePreview{Errors: a.NotifyValidateTemplate(cfg.Template)}

	ev := notify.SampleEvent(kind)
	if cfg.Redact {
		ev = notify.RedactEvent(ev)
	}

	// The SAME call the dispatcher makes. It used to be a second copy of the
	// mode choice sitting eight hundred lines away from the first, which is how
	// a preview comes to show something the sink would never send — and the
	// escaping is decided here, so a divergence would be invisible until a
	// receiver rejected a real alert.
	out.Subject, out.Body = a.renderForSink(ev, cfg)

	// Then: what does this transport actually put on the wire?
	switch cfg.Kind {
	case notify.SinkWebhook:
		// The REQUEST, not the text inside it. In envelope mode the rendered
		// text is one field of the object the receiver gets, and the shape is
		// exactly what is being configured.
		out.Format = notify.PreviewBodyFormat(cfg.Webhook)
		out.ContentType = cfg.Webhook.ContentType()
		// Json stays for the editor's monospace treatment, which suits every one
		// of these bodies; the specific check below is what differs.
		out.Json = true
		payload, err := notify.PreviewPayload(cfg.Webhook, ev, out.Subject, out.Body)
		if err != nil {
			// The failure IS the answer here: a body that will not go on the
			// wire is what the operator is trying to see. Show the rendered text
			// with the reason beside it, rather than nothing.
			out.JsonError = err.Error()
			out.Bytes = len(out.Body)
			return out
		}
		out.Body = string(payload)
		out.Bytes = len(payload)
		out.JsonValid = true
		return out

	case notify.SinkSyslog:
		// One line, with everything in front of the message that a collector
		// indexes on: the priority computed from facility and severity, the
		// timestamp, the host and app names, the event kind as MSGID, and the
		// structured data — which is the part the operator can now switch off
		// and until now had no way to see.
		out.Format = "syslog"
		out.Body = notify.FormatRFC5424(cfg.Syslog, ev, out.Body)
		// Subject is KEPT although the line does not carry one. It is still what
		// the template rendered, the editor still marks errors against it, and a
		// syslog sink whose subject template is broken should say so rather than
		// looking fine because the field it is checked against was blanked.
		out.Bytes = len(out.Body)
		return out

	case notify.SinkEmail:
		// The whole message, headers included. The Content-Type is the point:
		// it is what the body format setting changes, and reading it is the only
		// way to be sure the mail will render as markup rather than as markup
		// printed out.
		out.Format = "email"
		out.Body = notify.PreviewMessage(cfg.Email, ev, out.Subject, out.Body)
		out.Bytes = len(out.Body)
		return out
	}

	out.Format = "text"
	out.Bytes = len(out.Body)
	return out
}

// NotifyListRoutes returns the routing rules.
func (a *App) NotifyListRoutes() ([]notify.Route, error) {
	if a.storage == nil {
		return []notify.Route{}, errNoStorage
	}
	return a.storage.ListRoutes()
}

// NotifySaveRoute creates or updates a routing rule.
func (a *App) NotifySaveRoute(r notify.Route) (notify.Route, error) {
	if a.storage == nil {
		return r, fmt.Errorf("storage not initialized")
	}
	// A source pattern that cannot match anything is a mistake, and every
	// failure path in matching ends at "no match" with no error — so a rule
	// meant to cover the whole estate delivered nothing and neither the save
	// nor the rule list said a word. "10.0.0/8" is the typo that does it.
	if errs := notify.ValidateSourcePatterns(r.Match.Sources); len(errs) > 0 {
		return r, fmt.Errorf("%s", errs[0])
	}
	return a.storage.SaveRoute(r)
}

// NotifyDeleteRoute removes a routing rule.
func (a *App) NotifyDeleteRoute(id string) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return a.storage.DeleteRoute(id)
}

// NotifyListDeliveries returns the delivery log. state may be "", "pending",
// "sent" or "dead".
func (a *App) NotifyListDeliveries(state string, limit int) ([]storage.Delivery, error) {
	if a.storage == nil {
		return []storage.Delivery{}, errNoStorage
	}
	return a.storage.ListDeliveries(state, limit)
}

// NotifyRetryDelivery puts a dead letter back in the queue.
func (a *App) NotifyRetryDelivery(id int64) error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if err := a.storage.RetryDelivery(id); err != nil {
		return err
	}
	if a.dispatcher != nil {
		a.dispatcher.Wake()
	}
	return nil
}

// NotifyDefaultBody is the starting point offered when a sink is switched to a
// body format that has one.
//
// Served from Go rather than duplicated in the editor, so the text the operator
// starts from is byte for byte the one the sink falls back to when the template
// is empty. An unknown format returns "", and the editor offers no button.
func (a *App) NotifyDefaultBody(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return notify.DefaultJSONPayload
	case "html":
		return notify.DefaultHTMLPayload
	default:
		return ""
	}
}
