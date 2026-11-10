package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"SnmpLens/internal/engine"
	"SnmpLens/internal/remote"
	"SnmpLens/internal/sysvc"
	"SnmpLens/pkg/updater"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// --- Driving the system service ----------------------------------------------
//
// While the system service runs, it owns the supervision: the sessions, the
// journal, the destinations, the trap port. A window that kept a supervision of
// its own would poll every session a second time, send every alert twice, and
// fight the service for port 162. So the window drives the service instead,
// over its pipe: the sessions it edits are the service's, the journal it shows
// is the one the service keeps.
//
// When the service stops, or was never installed, the window runs its own
// supervision as before, and goes back to the service as soon as it runs again.
// Each switch tells the renderer, which reloads and reads everything afresh
// from whichever it now drives.

const (
	// linkPoll is how often the window looks for the service.
	linkPoll = 3 * time.Second
	// dialTimeout bounds one attempt to connect to the service's pipe.
	dialTimeout = 2 * time.Second
	// reconnectFor is how long a lost service is waited for before the window
	// runs its own supervision: long enough for a restart, which allowing an
	// account or an update is.
	reconnectFor = 15 * time.Second
	// elevatedTimeout bounds an administrator step, the prompt included.
	elevatedTimeout = 3 * time.Minute
)

// Why the window runs its own supervision although the service is installed.
const (
	reasonStopped     = "stopped"
	reasonDenied      = "denied"
	reasonVersion     = "version"
	reasonNotService  = "notService"
	reasonUnreachable = "unreachable"
)

// backendChanged tells the renderer the window now drives another supervision.
const backendChanged = "backend:changed"

// serviceLink is which supervision the window drives.
type serviceLink struct {
	mu sync.Mutex
	// client is set while the window drives the service; eng while it runs its
	// own supervision. Never both.
	client *remote.Client
	eng    *engine.Engine
	// reason and detail say why the window does not drive an installed service.
	reason, detail string
	// busy is set while this window installs, removes, starts or stops the
	// service: the supervision it drives is changing on purpose, and the
	// watcher leaves it alone.
	busy bool

	stop chan struct{}
	done chan struct{}
}

// SystemServiceLink is what the settings show of the system service.
type SystemServiceLink struct {
	// Supported is false where no service can be installed.
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	State     string `json:"state,omitempty"`
	// Driving is true while this window drives the service.
	Driving        bool   `json:"driving"`
	ServiceVersion string `json:"serviceVersion,omitempty"`
	// Reason says why this window does not drive the installed service:
	// stopped, denied, version, notService or unreachable; Detail says more.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Location says why this copy cannot install the service — a portable copy,
	// or one installed for one user — when it cannot.
	Location string `json:"location,omitempty"`
	// CanHandOver is true while this window drives a service that holds nothing
	// — the installer's, started on the defaults — and the supervision this
	// window ran before holds something to hand it.
	CanHandOver bool `json:"canHandOver,omitempty"`
}

// localDataDir is where the window's own supervision keeps its data.
func (a *App) localDataDir() string {
	if a.configDir != "" {
		return a.configDir
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "SnmpLens")
}

// localEngine is the window's own supervision while it runs one, nil while the
// window drives the service.
func (a *App) localEngine() *engine.Engine {
	a.svc.mu.Lock()
	defer a.svc.mu.Unlock()
	return a.svc.eng
}

// startLink chooses, at startup, what the window drives: the service when it
// runs and lets this account in, the window's own supervision otherwise.
func (a *App) startLink() {
	a.svc.stop = make(chan struct{})
	a.svc.done = make(chan struct{})
	a.link = remote.NewSwitch(a.newLocalEngine())
	a.host, a.Backend = a.link, a.link

	if c, reason, detail := a.dialService(); c != nil {
		a.useService(c)
	} else {
		a.setReason(reason, detail)
		a.openLocal()
	}
	go a.watchLink()
}

// stopLink lets go of whatever the window drives, as it closes.
func (a *App) stopLink() {
	if a.svc.stop != nil {
		close(a.svc.stop)
		<-a.svc.done
	}
	a.svc.mu.Lock()
	c, eng := a.svc.client, a.svc.eng
	a.svc.client, a.svc.eng = nil, nil
	a.svc.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if eng != nil {
		eng.Close()
	}
}

// newLocalEngine makes the supervision the window runs on its own directory.
func (a *App) newLocalEngine() *engine.Engine {
	eng := engine.New(engine.Options{
		DataDir: a.localDataDir(),
		Secrets: a.secrets,
		Config:  a.serviceCfg,
		Emitter: wailsEmitter{a.ctx},
		Version: a.GetAppVersion(),
	})
	eng.OnSessionsChanged(a.refreshTrayStatus)
	return eng
}

// openLocal starts the window's own supervision and drives it.
func (a *App) openLocal() {
	eng := a.newLocalEngine()
	if err := eng.Open(); err != nil {
		log.Printf("WARNING: %v", err)
	}
	eng.Start()
	a.svc.mu.Lock()
	a.svc.eng = eng
	a.svc.mu.Unlock()
	a.link.Set(eng)
	a.refreshTrayStatus()
}

// closeLocal stops the window's own supervision, if it runs one: before the
// service starts, which it would poll beside and fight for the trap port.
func (a *App) closeLocal() {
	a.svc.mu.Lock()
	eng := a.svc.eng
	a.svc.eng = nil
	a.svc.mu.Unlock()
	if eng != nil {
		eng.Close()
	}
}

// useService makes the window drive the service through c.
func (a *App) useService(c *remote.Client) {
	a.closeLocal()
	a.svc.mu.Lock()
	a.svc.client = c
	a.svc.reason, a.svc.detail = "", ""
	a.svc.mu.Unlock()
	a.link.Set(c)
	a.refreshTrayStatus()
}

// dropService lets go of the service, which stopped or was removed.
func (a *App) dropService() {
	a.svc.mu.Lock()
	c := a.svc.client
	a.svc.client = nil
	a.svc.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (a *App) setReason(reason, detail string) {
	a.svc.mu.Lock()
	a.svc.reason, a.svc.detail = reason, detail
	a.svc.mu.Unlock()
}

// dialService connects to the service when it runs and lets this account in. A
// nil client comes with why not, when the service is installed.
func (a *App) dialService() (*remote.Client, string, string) {
	if !sysvc.Supported() {
		return nil, "", ""
	}
	info, err := sysvc.Query()
	if err != nil || !info.Installed {
		return nil, "", ""
	}
	if info.State != "running" {
		return nil, reasonStopped, ""
	}
	conn, err := sysvc.DialControl(dialTimeout)
	switch {
	case errors.Is(err, sysvc.ErrAccessDenied):
		return nil, reasonDenied, ""
	case errors.Is(err, sysvc.ErrNotService):
		return nil, reasonNotService, ""
	case err != nil:
		return nil, reasonUnreachable, err.Error()
	}
	c, err := remote.NewClient(conn, updater.Version, a.forwardServiceEvent)
	var ve *remote.VersionError
	if errors.As(err, &ve) {
		return nil, reasonVersion, err.Error()
	}
	if err != nil {
		return nil, reasonUnreachable, err.Error()
	}
	return c, "", ""
}

// forwardServiceEvent hands the renderer what the service's engine emitted, as
// if this window's own engine had emitted it.
func (a *App) forwardServiceEvent(name string, data []json.RawMessage) {
	if a.ctx == nil {
		return
	}
	args := make([]interface{}, 0, len(data))
	for _, raw := range data {
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return
		}
		args = append(args, v)
	}
	runtime.EventsEmit(a.ctx, name, args...)
	if name == "monitor:state" {
		a.refreshTrayStatus()
	}
}

// watchLink follows the service: it drives it once it runs, and runs the
// window's own supervision once it has stopped.
func (a *App) watchLink() {
	defer close(a.svc.done)
	t := time.NewTicker(linkPoll)
	defer t.Stop()
	for {
		a.svc.mu.Lock()
		c, busy := a.svc.client, a.svc.busy
		a.svc.mu.Unlock()
		var lost <-chan struct{}
		if c != nil {
			lost = c.Done()
		}
		select {
		case <-a.svc.stop:
			return
		case <-t.C:
		case <-lost:
		}
		if busy {
			continue
		}
		a.followService()
	}
}

// followService moves the window to whichever supervision it should drive now.
func (a *App) followService() {
	a.svc.mu.Lock()
	c, busy := a.svc.client, a.svc.busy
	a.svc.mu.Unlock()
	if busy {
		return
	}
	if c != nil {
		select {
		case <-c.Done():
		default:
			return // still driving the service
		}
		// The service went: a restart comes back within reconnectFor.
		a.dropService()
		deadline := time.Now().Add(reconnectFor)
		for time.Now().Before(deadline) {
			if next, _, _ := a.dialService(); next != nil {
				a.useService(next)
				a.emitBackendChanged()
				return
			}
			select {
			case <-a.svc.stop:
				return
			case <-time.After(time.Second):
			}
		}
		reason, detail := reasonUnreachable, ""
		if _, r, d := a.dialService(); r != "" {
			reason, detail = r, d
		}
		a.setReason(reason, detail)
		a.openLocal()
		a.emitBackendChanged()
		return
	}
	next, reason, detail := a.dialService()
	if next == nil {
		a.setReason(reason, detail)
		if a.localEngine() == nil {
			a.openLocal()
			a.emitBackendChanged()
		}
		return
	}
	a.useService(next)
	a.emitBackendChanged()
}

func (a *App) emitBackendChanged() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, backendChanged)
	}
}

// drivingService reports whether the window drives the service.
func (a *App) drivingService() bool {
	a.svc.mu.Lock()
	defer a.svc.mu.Unlock()
	return a.svc.client != nil
}

// setBusy marks the window as changing the service itself.
func (a *App) setBusy(busy bool) {
	a.svc.mu.Lock()
	a.svc.busy = busy
	a.svc.mu.Unlock()
}

// SystemServiceStatus says whether the system service is installed and runs,
// whether this window drives it, and when not, why.
func (a *App) SystemServiceStatus() SystemServiceLink {
	link := SystemServiceLink{Supported: sysvc.Supported()}
	if !link.Supported {
		return link
	}
	if exe, err := executable(); err == nil {
		if err := sysvc.CheckLocation(exe); err != nil {
			link.Location = err.Error()
		}
	}
	if info, err := sysvc.Query(); err == nil {
		link.Installed, link.State = info.Installed, info.State
	}
	a.svc.mu.Lock()
	c := a.svc.client
	if c != nil {
		link.Driving = true
		link.ServiceVersion = c.Hello().Version
	} else if link.Installed {
		link.Reason, link.Detail = a.svc.reason, a.svc.detail
	}
	a.svc.mu.Unlock()
	if c != nil {
		link.CanHandOver = a.canHandOver(c)
	}
	return link
}

// canHandOver reports whether the service c holds nothing and this window's
// own supervision, idle on its directory, holds something.
func (a *App) canHandOver(c *remote.Client) bool {
	if sessions, err := c.MonitorLoadSessions(); err != nil || len(sessions) > 0 {
		return false
	}
	if sinks, err := c.NotifyListSinks(); err != nil || len(sinks) > 0 {
		return false
	}
	has, err := engine.HoldsSomething(a.localDataDir())
	return err == nil && has
}

// SystemServiceHandOver hands the supervision this window ran before to the
// service it drives, which holds nothing yet — the one the installer set up:
// the sessions, destinations, routes, trap users and history kept in this
// window's directory. Through the administrator prompt; the service restarts
// with them.
func (a *App) SystemServiceHandOver() (SystemServiceLink, error) {
	if !a.drivingService() {
		return a.SystemServiceStatus(), errors.New("this window does not drive the service")
	}
	a.setBusy(true)
	defer a.setBusy(false)
	tmp, err := os.MkdirTemp("", "snmplens-service-")
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	defer os.RemoveAll(tmp)
	local := a.localDataDir()
	b, err := engine.ExportBundleAt(local, a.secrets)
	if err != nil {
		return a.SystemServiceStatus(), fmt.Errorf("preparing what to hand over: %w", err)
	}
	b.Config = a.serviceCfg
	data, err := json.Marshal(b)
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	bundle, db := filepath.Join(tmp, sysvc.ExportedBundle), filepath.Join(tmp, sysvc.ExportedDatabase)
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		return a.SystemServiceStatus(), err
	}
	if err := engine.SnapshotAt(local, db); err != nil {
		return a.SystemServiceStatus(), err
	}
	res, err := a.runServiceCommand("install", "--bundle", bundle, "--db", db)
	a.dropService()
	a.awaitService()
	if err == nil && res.KeptHistory {
		err = errors.New("the service had a history of its own by then, and kept it")
	}
	return a.SystemServiceStatus(), err
}

// SystemServiceInstall installs the system service, or updates the installed
// one, through the administrator prompt. A window running its own supervision
// hands it over: the sessions, the destinations and routes, the trap users and
// engine ID, the credentials, and a copy of the monitoring database with its
// history. The window's own supervision stops before the service starts.
func (a *App) SystemServiceInstall() (SystemServiceLink, error) {
	if !sysvc.Supported() {
		return a.SystemServiceStatus(), sysvc.ErrUnsupported
	}
	exe, err := executable()
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	if err := sysvc.CheckLocation(exe); err != nil {
		return a.SystemServiceStatus(), err
	}
	a.setBusy(true)
	defer a.setBusy(false)

	// A window driving the service updates it: there is nothing to hand over,
	// the service's configuration is the one it edits.
	if a.drivingService() {
		_, err := a.runServiceCommand("install")
		a.dropService()
		a.awaitService()
		return a.SystemServiceStatus(), err
	}

	tmp, err := os.MkdirTemp("", "snmplens-service-")
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	defer os.RemoveAll(tmp)
	args := []string{"install"}
	if eng := a.localEngine(); eng != nil && eng.Storage() != nil {
		b, err := eng.ExportBundle()
		if err != nil {
			return a.SystemServiceStatus(), fmt.Errorf("preparing what to hand over: %w", err)
		}
		data, err := json.Marshal(b)
		if err != nil {
			return a.SystemServiceStatus(), err
		}
		bundle, db := filepath.Join(tmp, sysvc.ExportedBundle), filepath.Join(tmp, sysvc.ExportedDatabase)
		if err := os.WriteFile(bundle, data, 0o600); err != nil {
			return a.SystemServiceStatus(), err
		}
		if err := eng.Storage().SnapshotTo(db); err != nil {
			return a.SystemServiceStatus(), err
		}
		args = append(args, "--bundle", bundle, "--db", db)
	}
	if sid, err := sysvc.CurrentUserSID(); err == nil {
		args = append(args, "--operator", sid)
	}

	// Stopped BEFORE the service starts: two supervisions would poll every
	// session twice, send every alert twice, and fight for the trap port.
	a.closeLocal()
	if _, err := a.runServiceCommand(args...); err != nil {
		a.openLocal()
		a.emitBackendChanged()
		return a.SystemServiceStatus(), err
	}
	a.awaitService()
	return a.SystemServiceStatus(), nil
}

// SystemServiceUninstall removes the system service through the administrator
// prompt. With bringBack, its history and credentials come back to this window
// first — the window's database before the service is kept beside it — and
// with purge its data directory goes too.
func (a *App) SystemServiceUninstall(bringBack, purge bool) (SystemServiceLink, error) {
	if !sysvc.Supported() {
		return a.SystemServiceStatus(), sysvc.ErrUnsupported
	}
	a.setBusy(true)
	defer a.setBusy(false)

	tmp, err := os.MkdirTemp("", "snmplens-service-")
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	defer os.RemoveAll(tmp)
	args := []string{"uninstall"}
	if bringBack {
		args = append(args, "--export", tmp)
		// The window's own database is about to be replaced: its supervision
		// lets go of it first.
		a.closeLocal()
	}
	if purge {
		args = append(args, "--purge")
	}
	_, err = a.runServiceCommand(args...)
	if err != nil {
		if !a.drivingService() && a.localEngine() == nil {
			a.openLocal()
		}
		return a.SystemServiceStatus(), err
	}
	a.dropService()
	a.closeLocal()
	var takeErr error
	if bringBack {
		takeErr = a.takeBack(tmp)
	}
	a.setReason("", "")
	a.openLocal()
	a.emitBackendChanged()
	return a.SystemServiceStatus(), takeErr
}

// takeBack makes what the service exported into dir the window's own: its
// database — the window's from before kept beside it — and its credentials.
func (a *App) takeBack(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, sysvc.ExportedBundle))
	if err != nil {
		return fmt.Errorf("the service's history did not come back: %w", err)
	}
	var b engine.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return fmt.Errorf("the service's history did not come back: %w", err)
	}
	local := a.localDataDir()
	db := filepath.Join(dir, sysvc.ExportedDatabase)
	if _, err := os.Stat(db); err == nil {
		aside := fmt.Sprintf(".before-service-%s", time.Now().Format("20060102-150405"))
		for _, suffix := range []string{"", "-wal", "-shm"} {
			from := filepath.Join(local, "monitoring.db"+suffix)
			if _, err := os.Stat(from); err == nil {
				if err := os.Rename(from, filepath.Join(local, "monitoring.db"+aside+suffix)); err != nil {
					return fmt.Errorf("keeping this window's history aside: %w", err)
				}
			}
		}
	} else {
		db = ""
	}
	if _, err := engine.ImportBundle(local, a.secrets, b, a.serviceCfg, db); err != nil {
		return fmt.Errorf("taking the service's history back: %w", err)
	}
	return nil
}

// SystemServiceStart starts the installed service through the administrator
// prompt; the window drives it as soon as it runs.
func (a *App) SystemServiceStart() (SystemServiceLink, error) {
	a.setBusy(true)
	defer a.setBusy(false)
	// The window's own supervision lets go of the trap port first.
	a.closeLocal()
	if _, err := a.runServiceCommand("start"); err != nil {
		a.openLocal()
		a.emitBackendChanged()
		return a.SystemServiceStatus(), err
	}
	a.awaitService()
	return a.SystemServiceStatus(), nil
}

// SystemServiceStop stops the service through the administrator prompt; the
// window runs its own supervision once it has.
func (a *App) SystemServiceStop() (SystemServiceLink, error) {
	a.setBusy(true)
	defer a.setBusy(false)
	if _, err := a.runServiceCommand("stop"); err != nil {
		return a.SystemServiceStatus(), err
	}
	a.dropService()
	a.setReason(reasonStopped, "")
	if a.localEngine() == nil {
		a.openLocal()
	}
	a.emitBackendChanged()
	return a.SystemServiceStatus(), nil
}

// SystemServiceAllow lets this window's account drive the service, through the
// administrator prompt. The service restarts so that its pipe lets the account
// in, and the window drives it once it is back.
func (a *App) SystemServiceAllow() (SystemServiceLink, error) {
	sid, err := sysvc.CurrentUserSID()
	if err != nil {
		return a.SystemServiceStatus(), err
	}
	a.setBusy(true)
	defer a.setBusy(false)
	if _, err := a.runServiceCommand("allow", "--operator", sid); err != nil {
		return a.SystemServiceStatus(), err
	}
	a.awaitService()
	return a.SystemServiceStatus(), nil
}

// awaitService drives the service once it answers, or runs the window's own
// supervision when it does not within reconnectFor.
func (a *App) awaitService() {
	deadline := time.Now().Add(reconnectFor)
	for time.Now().Before(deadline) {
		if c, _, _ := a.dialService(); c != nil {
			a.useService(c)
			a.emitBackendChanged()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	_, reason, detail := a.dialService()
	a.setReason(reason, detail)
	if !a.drivingService() && a.localEngine() == nil {
		a.openLocal()
	}
	a.emitBackendChanged()
}

// runServiceCommand runs `SnmpLens service <args>` elevated, and says what it
// did.
func (a *App) runServiceCommand(args ...string) (sysvc.Result, error) {
	exe, err := executable()
	if err != nil {
		return sysvc.Result{}, err
	}
	resultFile, err := os.CreateTemp("", "snmplens-service-*.json")
	if err != nil {
		return sysvc.Result{}, err
	}
	resultPath := resultFile.Name()
	resultFile.Close()
	os.Remove(resultPath)
	defer os.Remove(resultPath)

	full := append([]string{"service"}, args...)
	full = append(full, "--result", resultPath)
	code, err := sysvc.RunElevated(exe, full, elevatedTimeout)
	if err != nil {
		return sysvc.Result{}, err
	}
	res, rerr := sysvc.ReadResult(resultPath)
	switch {
	case rerr == nil && !res.OK:
		return res, errors.New(res.Error)
	case rerr == nil:
		return res, nil
	case code != 0:
		return sysvc.Result{}, fmt.Errorf("the administrator step failed (exit status %d)", code)
	}
	return sysvc.Result{OK: true}, nil
}

// executable is this program, links resolved.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}
