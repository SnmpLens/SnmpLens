//go:build linux

package sysvc

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// On Linux the service is a systemd unit running as a system user of its own.
// systemd creates its directories (StateDirectory, RuntimeDirectory) and tells
// the process where they are; these are where it puts them.
const (
	unitName = "snmplens.service"
	unitPath = "/etc/systemd/system/" + unitName
	// serviceUser is the system account the service runs as.
	serviceUser = "snmplens"
	dataDir     = "/var/lib/snmplens"
	// runDir holds what anyone may read — the status — and the socket windows
	// connect to.
	runDir = "/run/snmplens"
)

// runFromCommandLine: systemd starts the service as `SnmpLens service run`.
const runFromCommandLine = true

// Supported reports whether systemd runs this system.
func Supported() bool {
	fi, err := os.Stat("/run/systemd/system")
	return err == nil && fi.IsDir()
}

// DataDir is the service's database and credentials, readable by the service
// and root only.
func DataDir() (string, error) {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return d, nil
	}
	return dataDir, nil
}

// StatusDir is where the service publishes its status, for anyone to read.
func StatusDir() (string, error) {
	if d := os.Getenv("RUNTIME_DIRECTORY"); d != "" {
		return d, nil
	}
	return runDir, nil
}

// CheckLocation refuses an executable an ordinary user could replace: the
// service would run whatever is put there. Every directory up to the root, and
// the file itself, must belong to root and be writable by nobody else — as an
// installed package's are, and a copy unpacked in a home directory's are not.
func CheckLocation(exe string) error {
	path, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("the service can only run a copy that only root can change, as an installed "+
				"package is; %s is not: install SnmpLens's package, or copy it under /usr/local as root", p)
		}
		if p == "/" {
			return nil
		}
	}
}

// ownLikeDir gives a file root wrote to the owner of its directory: what an
// elevated step writes for the window that asked — its result, the history it
// hands back — which that window could not otherwise read.
func ownLikeDir(path string) {
	if os.Geteuid() != 0 {
		return
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
