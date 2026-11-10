//go:build windows

package sysvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The pipe admits the system, administrators, the account serving it — which
// opens every instance it answers on against this very descriptor — and the
// operators, who may talk to it and nothing more.
func TestThePipeAdmitsTheServiceAndItsOperators(t *testing.T) {
	sddl := controlSDDL("S-1-5-80-1", []Operator{{SID: "S-1-5-21-1-2-3-1001"}})
	for _, want := range []string{"D:P", "(A;;GA;;;SY)", "(A;;GA;;;BA)", "(A;;GA;;;S-1-5-80-1)", "(A;;GRGW;;;S-1-5-21-1-2-3-1001)"} {
		if !strings.Contains(sddl, want) {
			t.Errorf("%s lacks %s", sddl, want)
		}
	}
	if _, err := windows.SecurityDescriptorFromString(sddl); err != nil {
		t.Errorf("%s does not parse: %v", sddl, err)
	}
	for _, users := range []bool{false, true} {
		d := dirSDDL("S-1-5-80-1", users)
		if _, err := windows.SecurityDescriptorFromString(d); err != nil {
			t.Errorf("%s does not parse: %v", d, err)
		}
		if strings.Contains(d, ";;;BU)") != users {
			t.Errorf("local users in %s: want %v", d, users)
		}
	}
}

// A copy ordinary users could replace would hand the service's account to
// whoever replaced it: only Program Files is accepted.
func TestOnlyProgramFilesMayHostTheService(t *testing.T) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		t.Skip(err)
	}
	if err := CheckLocation(filepath.Join(pf, "SnmpLens", "SnmpLens.exe")); err != nil {
		t.Errorf("Program Files: %v", err)
	}
	for _, exe := range []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "SnmpLens", "SnmpLens.exe"),
		filepath.Join(pf+" Evil", "SnmpLens.exe"),
		pf,
	} {
		if err := CheckLocation(exe); !errors.Is(err, ErrLocation) {
			t.Errorf("%s: %v", exe, err)
		}
	}
}

// An account is allowed once, by its SID in canonical form, named when it can
// be.
func TestAnOperatorIsAddedOnce(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if added, err := addOperator(dir, sid); err != nil || !added {
		t.Fatalf("first: %v %v", added, err)
	}
	if added, err := addOperator(dir, strings.ToLower(sid)); err != nil || added {
		t.Errorf("again: %v %v", added, err)
	}
	ops, err := loadOperators(dir)
	if err != nil || len(ops) != 1 || ops[0].SID != sid || ops[0].Name == "" {
		t.Errorf("operators: %+v, %v", ops, err)
	}
	if _, err := addOperator(dir, "not a SID"); err == nil {
		t.Error("an account that is no SID was allowed")
	}
}

// A window hands the service its credentials, so it checks that what answers on
// the pipe is the service's process and not one that took the name first.
func TestAWindowTalksOnlyToTheService(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf(`\\.\pipe\SnmpLens-test-%d`, os.Getpid())
	l, err := listenControl(path, sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	c, err := dialControl(path, time.Second, uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("the service's own process: %v", err)
	}
	c.Close()
	if _, err := dialControl(path, time.Second, uint32(os.Getpid())+1); !errors.Is(err, ErrNotService) {
		t.Errorf("another process on the pipe: %v", err)
	}
}
