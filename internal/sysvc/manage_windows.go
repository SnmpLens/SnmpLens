//go:build windows

package sysvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// startTimeout bounds how long Install and Start wait for the service to say it
// runs, or that it does not.
const startTimeout = 30 * time.Second

// Query reads the service's state. It needs no administrator rights: only the
// right to look, which every local user has.
func Query() (Info, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return Info{}, err
	}
	defer windows.CloseServiceHandle(m)

	name, _ := windows.UTF16PtrFromString(Name)
	h, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return Info{}, nil
		}
		return Info{}, err
	}
	s := &mgr.Service{Name: Name, Handle: h}
	defer s.Close()

	info := Info{Installed: true, State: "unknown"}
	if st, err := s.Query(); err == nil {
		info.State = stateName(st.State)
		info.PID = st.ProcessId
	}
	if cfg, err := s.Config(); err == nil {
		info.Executable = binaryOf(cfg.BinaryPathName)
		info.Account = cfg.ServiceStartName
	}
	return info, nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.Running:
		return "running"
	case svc.StopPending:
		return "stopping"
	}
	return "unknown"
}

// binaryOf takes the program out of a service command line.
func binaryOf(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : 1+end]
		}
	}
	if i := strings.Index(strings.ToLower(cmd), ".exe"); i >= 0 {
		return cmd[:i+4]
	}
	return cmd
}

// Install installs the service, or updates an installed one, and starts it. It
// must run elevated.
//
// bundlePath and dbPath are what a window handed over — its credentials and
// preferences, and a copy of its monitoring database — and are deleted as soon
// as they are read. The installer gives neither, and the service starts with
// the defaults, empty until a window hands it a configuration. A service that
// has a history of its own keeps it (Result.KeptHistory). operatorSID, when
// given, is the account of the window that asked, which may then drive it.
func Install(exe, bundlePath, dbPath, operatorSID string) (Result, error) {
	var res Result
	if !IsElevated() {
		return res, fmt.Errorf("installing the service needs administrator rights")
	}
	if err := CheckLocation(exe); err != nil {
		return res, err
	}
	bundle, err := readBundle(bundlePath)
	if err != nil {
		return res, err
	}

	dataDir, err := DataDir()
	if err != nil {
		return res, err
	}
	statusDir, _ := StatusDir()
	logPath, _ := LogPath()

	m, err := mgr.Connect()
	if err != nil {
		return res, fmt.Errorf("open the service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(Name)
	switch {
	case err == nil:
		// Installed already: stop it, so its files can be rewritten, and point
		// it at this copy with these settings.
		if err := stopAndWait(s); err != nil {
			s.Close()
			return res, err
		}
		cfg, cerr := s.Config()
		if cerr != nil {
			s.Close()
			return res, cerr
		}
		applyConfig(&cfg, exe)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return res, fmt.Errorf("update the service: %w", err)
		}
	default:
		cfg := mgr.Config{}
		applyConfig(&cfg, exe)
		s, err = m.CreateService(Name, exe, cfg, "service", "run")
		if err != nil {
			return res, fmt.Errorf("create the service: %w", err)
		}
	}
	defer s.Close()

	// Restart after a failure — a port taken by something else at boot, say —
	// and keep retrying once a minute rather than giving up for good.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, uint32((24 * time.Hour).Seconds()))
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)

	sid, err := serviceSID()
	if err != nil {
		return res, err
	}
	for _, d := range []string{dataDir, filepath.Dir(logPath), statusDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return res, fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := secureDir(dataDir, sid, false); err != nil {
		return res, err
	}
	if err := secureDir(statusDir, sid, true); err != nil {
		return res, err
	}
	if operatorSID != "" {
		if _, err := addOperator(dataDir, operatorSID); err != nil {
			return res, err
		}
	}

	store, err := secrets.OpenMachine(dataDir)
	if err != nil {
		return res, fmt.Errorf("the service's credential store: %w", err)
	}
	cfg := serviceConfig(bundle.Config, bundlePath != "")
	if bundlePath == "" {
		// The installer's: a service installed before keeps what it had.
		if had, err := service.Load(dataDir); err == nil {
			cfg = serviceConfig(had, true)
		}
	}
	if res.KeptHistory, err = engine.ImportBundle(dataDir, store, bundle, cfg, dbPath); err != nil {
		return res, fmt.Errorf("write the service's configuration: %w", err)
	}

	if err := eventlog.InstallAsEventCreate(Name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "exists") {
		return res, fmt.Errorf("register the event log source: %w", err)
	}
	if err := allowThroughFirewall(exe); err != nil {
		return res, err
	}
	return res, startAndWait(s, statusDir)
}

func applyConfig(cfg *mgr.Config, exe string) {
	cfg.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
	cfg.StartType = mgr.StartAutomatic
	cfg.DelayedAutoStart = false
	cfg.ErrorControl = mgr.ErrorNormal
	cfg.DisplayName = DisplayName
	cfg.Description = Description
	cfg.ServiceStartName = Account
	cfg.Password = ""
	cfg.SidType = windows.SERVICE_SID_TYPE_UNRESTRICTED
	cfg.BinaryPathName = windows.EscapeArg(exe) + " service run"
}

// Uninstall stops and removes the service, its firewall rule and its event log
// source. It must run elevated.
//
// exportDir, when given, is a directory of the window's: the service's history
// and credentials are written there first — a copy of its database and a
// bundle — for the window to take back. The data directory goes only when purge
// is set, and never before the export has been written.
func Uninstall(purge bool, exportDir string) error {
	if !IsElevated() {
		return fmt.Errorf("removing the service needs administrator rights")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open the service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(Name)
	if err == nil {
		if err := stopAndWait(s); err != nil {
			s.Close()
			return err
		}
	}
	if exportDir != "" {
		if err := export(exportDir); err != nil {
			if s != nil {
				s.Close()
			}
			return fmt.Errorf("handing the history back: %w", err)
		}
	}
	if s != nil {
		if err := s.Delete(); err != nil {
			s.Close()
			return fmt.Errorf("remove the service: %w", err)
		}
		s.Close()
	}
	_ = eventlog.Remove(Name)
	removeFirewallRule()

	if purge {
		if dir, err := DataDir(); err == nil {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("delete %s: %w", dir, err)
			}
		}
	}
	return nil
}

// Export file names in the window's directory.
const (
	ExportedDatabase = "monitoring.db"
	ExportedBundle   = "bundle.json"
)

// export writes the stopped service's history and credentials into dir.
func export(dir string) error {
	dataDir, err := DataDir()
	if err != nil {
		return err
	}
	store, err := secrets.OpenMachine(dataDir)
	if err != nil {
		return err
	}
	b, err := engine.ExportBundleAt(dataDir, store)
	if err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ExportedBundle), data, 0o600); err != nil {
		return err
	}
	src := filepath.Join(dataDir, "monitoring.db")
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return copyFileTo(src, filepath.Join(dir, ExportedDatabase))
}

func copyFileTo(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// Start starts the installed service and waits until it runs, or fails.
func Start() error {
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	statusDir, _ := StatusDir()
	return startAndWait(s, statusDir)
}

// Stop stops the installed service and waits until it has.
func Stop() error {
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return stopAndWait(s)
}

// Allow lets an account drive the service from a window, and restarts the
// service if it runs so its pipe lets that account in. It must run elevated.
func Allow(sid string) error {
	if !IsElevated() {
		return fmt.Errorf("allowing an account needs administrator rights")
	}
	dir, err := DataDir()
	if err != nil {
		return err
	}
	added, err := addOperator(dir, sid)
	if err != nil || !added {
		return err
	}
	m, s, err := open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if st, err := s.Query(); err != nil || st.State != svc.Running {
		return nil
	}
	if err := stopAndWait(s); err != nil {
		return err
	}
	statusDir, _ := StatusDir()
	return startAndWait(s, statusDir)
}

func open() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("open the service manager: %w", err)
	}
	s, err := m.OpenService(Name)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("the service is not installed")
	}
	return m, s, nil
}

func stopAndWait(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop the service: %w", err)
		}
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if st, err := s.Query(); err == nil && st.State == svc.Stopped {
			return nil
		}
	}
	return fmt.Errorf("the service did not stop within %s", startTimeout)
}

// startAndWait starts the service and waits for the verdict. A service that
// stops again while starting says why in its published status, which is what
// is returned: "it stopped" alone would leave the operator guessing.
func startAndWait(s *mgr.Service, statusDir string) error {
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the service: %w", err)
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		st, err := s.Query()
		if err != nil {
			continue
		}
		switch st.State {
		case svc.Running:
			return nil
		case svc.Stopped:
			if hs, ok := ReadStatus(statusDir); ok && hs.Error != "" {
				return fmt.Errorf("the service stopped while starting: %s", hs.Error)
			}
			return fmt.Errorf("the service stopped while starting; see the Application event log")
		}
	}
	return fmt.Errorf("the service did not start within %s", startTimeout)
}

// netsh runs a firewall command without flashing a console window.
func netsh(args ...string) error {
	cmd := exec.Command("netsh", append([]string{"advfirewall", "firewall"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// allowThroughFirewall lets the service's trap listener hear UDP on domain and
// private networks — not public ones, where a laptop should not be taking
// notifications from strangers. It replaces any earlier rule of ours.
func allowThroughFirewall(exe string) error {
	removeFirewallRule()
	err := netsh("add", "rule", "name="+FirewallRule, "dir=in", "action=allow",
		"program="+exe, "protocol=UDP", "enable=yes", "profile=domain,private",
		"description=Lets the SnmpLens service receive SNMP traps. Added when the service was installed.")
	if err != nil {
		return fmt.Errorf("add the firewall rule: %w", err)
	}
	return nil
}

func removeFirewallRule() {
	_ = netsh("delete", "rule", "name="+FirewallRule)
}
