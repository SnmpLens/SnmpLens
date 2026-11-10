//go:build linux

package sysvc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What systemd says of the unit is read as it says it.
func TestSystemdIsReadAsItSays(t *testing.T) {
	info := parseShow([]byte("LoadState=loaded\nActiveState=active\nMainPID=4242\n" +
		"ExecStart={ path=/usr/bin/snmplens ; argv[]=/usr/bin/snmplens service run ; ignore_errors=no }\nUser=snmplens\n"))
	if !info.Installed || info.State != "running" || info.PID != 4242 || info.Executable != "/usr/bin/snmplens" || info.Account != "snmplens" {
		t.Errorf("read %+v", info)
	}
	if info := parseShow([]byte("LoadState=not-found\nActiveState=inactive\n")); info.Installed {
		t.Errorf("a unit that is not there: %+v", info)
	}
}

// The unit runs as its own user with the right to bind the trap port and
// nothing more, and quotes a path that needs it.
func TestTheUnitHoldsOnlyWhatItNeeds(t *testing.T) {
	unit := unitFile("/usr/bin/snmplens")
	for _, want := range []string{
		"ExecStart=/usr/bin/snmplens service run", "User=snmplens", "StateDirectory=snmplens",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		"NoNewPrivileges=yes", "ProtectSystem=strict",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit lacks %q", want)
		}
	}
	if got := quoteExec("/opt/Snmp Lens/snmplens"); got != `"/opt/Snmp Lens/snmplens"` {
		t.Errorf("quoted as %s", got)
	}
}

// serveLines answers every connection with a line, as the service's hello is.
func serveLines(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte(`{"hello":{"protocol":1,"version":"test"}}` + "\n"))
		c.Close()
	}
}

// A window hands the service its credentials, so it checks that what answers on
// the socket is the service's process.
func TestAWindowTalksOnlyToTheServiceOnLinux(t *testing.T) {
	dir := t.TempDir()
	l, err := listenControl(filepath.Join(dir, controlSocket), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveLines(l)

	c, err := dialControl(filepath.Join(dir, controlSocket), time.Second, uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("the service's own process: %v", err)
	}
	c.Close()
	if _, err := dialControl(filepath.Join(dir, controlSocket), time.Second, uint32(os.Getpid())+1); !errors.Is(err, ErrNotService) {
		t.Errorf("another process on the socket: %v", err)
	}
}

// An account that is no operator is told so, and an operator added later is let
// in without a restart.
func TestOnlyOperatorsStay(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root is always let in")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, controlSocket)
	raw, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l := &operatorListener{Listener: raw, dataDir: dir, self: -1}
	defer l.Close()
	go serveLines(l)

	if _, err := dialControl(path, time.Second, 0); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("an account that is no operator: %v", err)
	}
	uid, _ := CurrentUserSID()
	if _, err := addOperator(dir, uid); err != nil {
		t.Fatal(err)
	}
	c, err := dialControl(path, time.Second, 0)
	if err != nil {
		t.Fatalf("once allowed: %v", err)
	}
	c.Close()
}
