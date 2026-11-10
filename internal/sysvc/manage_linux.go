//go:build linux

package sysvc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"SnmpLens/internal/engine"
	"SnmpLens/internal/headless"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
)

// startTimeout bounds the wait for the service to say it runs, or why not.
const startTimeout = 30 * time.Second

// Query asks systemd about the service. It needs no privilege.
func Query() (Info, error) {
	out, err := exec.Command("systemctl", "show", unitName, "--no-pager",
		"--property=LoadState,ActiveState,MainPID,ExecStart,User").Output()
	if err != nil {
		return Info{}, fmt.Errorf("ask systemd: %w", err)
	}
	return parseShow(out), nil
}

// parseShow reads `systemctl show` output.
func parseShow(out []byte) Info {
	props := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			props[k] = v
		}
	}
	info := Info{Installed: props["LoadState"] == "loaded"}
	if !info.Installed {
		return info
	}
	switch props["ActiveState"] {
	case "active", "reloading":
		info.State = "running"
	case "inactive", "failed":
		info.State = "stopped"
	case "activating":
		info.State = "starting"
	case "deactivating":
		info.State = "stopping"
	default:
		info.State = "unknown"
	}
	if pid, err := strconv.ParseUint(props["MainPID"], 10, 32); err == nil {
		info.PID = uint32(pid)
	}
	info.Executable = execStartPath(props["ExecStart"])
	info.Account = props["User"]
	return info
}

// execStartPath picks the program out of systemd's description of a command:
// "{ path=/usr/bin/x ; argv[]=/usr/bin/x service run ; ... }".
func execStartPath(s string) string {
	for _, field := range strings.Split(strings.Trim(s, "{} "), ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(field), "path="); ok {
			return v
		}
	}
	return ""
}

// unitFile is the service's unit: its own system user, the right to listen on
// ports below 1024 — the trap port, 162 — and nothing else, and the rest of
// the system out of reach.
func unitFile(exe string) string {
	return fmt.Sprintf(`# Written by SnmpLens (service install). Installing again rewrites it.
[Unit]
Description=%s
Documentation=https://github.com/SnmpLens/SnmpLens
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
ExecStart=%s service run
User=%s
Group=%s
StateDirectory=snmplens
StateDirectoryMode=0700
RuntimeDirectory=snmplens
RuntimeDirectoryMode=0755
RuntimeDirectoryPreserve=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
RestrictNamespaces=yes
LockPersonality=yes
SystemCallArchitectures=native
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, DisplayName, quoteExec(exe), serviceUser, serviceUser)
}

// quoteExec quotes a path for ExecStart when it needs it.
func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\"'\\") {
		return p
	}
	return strconv.Quote(p)
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureUser creates the service's system account if it is not there yet.
func ensureUser() (uid, gid int, err error) {
	if _, err := user.Lookup(serviceUser); err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return 0, 0, err
		}
		out, err := exec.Command("useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", dataDir, "--shell", "/usr/sbin/nologin", serviceUser).CombinedOutput()
		if err != nil {
			return 0, 0, fmt.Errorf("create the %s account: %v: %s", serviceUser, err, strings.TrimSpace(string(out)))
		}
	}
	u, err := user.Lookup(serviceUser)
	if err != nil {
		return 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, nil
}

// chownTree gives the service everything in its directory, which root wrote.
func chownTree(dir string, uid, gid int) error {
	return filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

// Install installs the service, or updates an installed one, and starts it. It
// must run as root. What a window handed over and the operator are as on
// Windows: see the Windows Install.
func Install(exe, bundlePath, dbPath, operator string) (Result, error) {
	var res Result
	if !IsElevated() {
		return res, fmt.Errorf("installing the service needs root: run it with sudo")
	}
	if !Supported() {
		return res, ErrUnsupported
	}
	if err := CheckLocation(exe); err != nil {
		return res, err
	}
	bundle, err := readBundle(bundlePath)
	if err != nil {
		return res, err
	}
	uid, gid, err := ensureUser()
	if err != nil {
		return res, err
	}
	// Installed already: stop it, so its files can be rewritten.
	if info, err := Query(); err == nil && info.Installed && info.State != "stopped" {
		if err := systemctl("stop", unitName); err != nil {
			return res, err
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return res, err
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return res, err
	}
	if operator != "" {
		if _, err := addOperator(dataDir, operator); err != nil {
			return res, err
		}
	}
	store, err := secrets.OpenMachine(dataDir)
	if err != nil {
		return res, fmt.Errorf("the service's credential store: %w", err)
	}
	cfg := serviceConfig(bundle.Config, bundlePath != "")
	if bundlePath == "" {
		if had, err := service.Load(dataDir); err == nil {
			cfg = serviceConfig(had, true)
		}
	}
	if res.KeptHistory, err = engine.ImportBundle(dataDir, store, bundle, cfg, dbPath); err != nil {
		return res, fmt.Errorf("write the service's configuration: %w", err)
	}
	if err := chownTree(dataDir, uid, gid); err != nil {
		return res, fmt.Errorf("give the service its directory: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(unitFile(exe)), 0o644); err != nil {
		return res, fmt.Errorf("write the unit: %w", err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return res, err
	}
	if err := systemctl("enable", unitName); err != nil {
		return res, err
	}
	return res, startAndWait()
}

// startAndWait starts the service and waits until it publishes a running
// status, or says why it could not start.
func startAndWait() error {
	_ = os.Remove(filepath.Join(runDir, statusFile))
	if err := systemctl("start", unitName); err != nil {
		return err
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if st, ok := ReadStatus(runDir); ok {
			switch {
			case st.State == "running":
				return nil
			case st.Error != "":
				return fmt.Errorf("the service could not start: %s", st.Error)
			}
		}
		if info, err := Query(); err == nil && info.State == "stopped" {
			return fmt.Errorf("the service stopped as it started; see journalctl -u %s", unitName)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the service did not report running within %s; see journalctl -u %s", startTimeout, unitName)
}

// Uninstall stops and removes the service. exportDir, when given, receives its
// history and credentials first, for the window to take back. Its data and its
// account go only when purge is set.
func Uninstall(purge bool, exportDir string) error {
	if !IsElevated() {
		return fmt.Errorf("removing the service needs root: run it with sudo")
	}
	info, err := Query()
	if err != nil {
		return err
	}
	if info.Installed && info.State != "stopped" {
		if err := systemctl("stop", unitName); err != nil {
			return err
		}
	}
	if exportDir != "" {
		if err := export(exportDir); err != nil {
			return fmt.Errorf("handing the history back: %w", err)
		}
	}
	if info.Installed {
		if err := systemctl("disable", unitName); err != nil {
			return err
		}
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if !purge {
		return nil
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return err
	}
	_ = os.RemoveAll(runDir)
	if out, err := exec.Command("userdel", serviceUser).CombinedOutput(); err != nil &&
		!strings.Contains(string(out), "does not exist") {
		return fmt.Errorf("remove the %s account: %v: %s", serviceUser, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Export file names in the window's directory.
const (
	ExportedDatabase = "monitoring.db"
	ExportedBundle   = "bundle.json"
)

// export writes the stopped service's history and credentials into dir, owned
// by the window's account that will read them.
func export(dir string) error {
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
	bundle := filepath.Join(dir, ExportedBundle)
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		return err
	}
	ownLikeDir(bundle)
	src := filepath.Join(dataDir, "monitoring.db")
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	db := filepath.Join(dir, ExportedDatabase)
	if err := os.WriteFile(db, raw, 0o600); err != nil {
		return err
	}
	ownLikeDir(db)
	return nil
}

// Start starts the installed service.
func Start() error {
	if !IsElevated() {
		return fmt.Errorf("starting the service needs root: run it with sudo")
	}
	return startAndWait()
}

// Stop stops it.
func Stop() error {
	if !IsElevated() {
		return fmt.Errorf("stopping the service needs root: run it with sudo")
	}
	return systemctl("stop", unitName)
}

// Run is the service's main under systemd, which starts it as `service run`: it
// supervises until told to stop. What it logs goes to the journal
// (journalctl -u snmplens).
func Run(version string) error {
	data, err := DataDir()
	if err != nil {
		return err
	}
	status, err := StatusDir()
	if err != nil {
		return err
	}
	store, err := secrets.OpenMachine(data)
	if err != nil {
		writeStatus(status, Status{State: "stopped", Version: version, Error: err.Error()})
		return fmt.Errorf("SnmpLens could not open its credential store: %w", err)
	}
	control, err := ListenControl()
	if err != nil {
		// Supervising matters more than being reachable from a window.
		log.Printf("windows cannot connect to the service: %v", err)
		control = nil
	}
	h, err := headless.Start(headless.Options{DataDir: data, Version: version, Secrets: store, Control: control})
	if err != nil {
		writeStatus(status, Status{State: "stopped", Version: version, Error: err.Error()})
		return fmt.Errorf("SnmpLens could not start: %w", err)
	}
	eng := h.Engine()
	log.Printf("SnmpLens %s started: %d monitoring session(s) polling, trap listener %s.",
		version, len(eng.MonitorRunning()), map[bool]string{true: "listening", false: "not listening"}[eng.TrapListenerRunning()])
	writeStatus(status, Status{State: "running", Version: version})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	h.Stop()
	writeStatus(status, Status{State: "stopped", Version: version})
	log.Printf("SnmpLens stopped.")
	return nil
}
