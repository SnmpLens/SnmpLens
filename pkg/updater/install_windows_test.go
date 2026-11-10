//go:build windows

package updater

import "testing"

// A copy is updated by the installer only when it IS the installed copy: a
// portable executable beside an installation replaces itself, rather than run
// the installer against the other copy — or against the one the service runs.
func TestOnlyTheInstalledCopyRunsTheInstaller(t *testing.T) {
	registered := []string{`C:\Program Files (x86)\SnmpLens\`, `C:\Users\op\AppData\Local\SnmpLens`}
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{`C:\Program Files (x86)\SnmpLens`, true},
		{`c:\program files (x86)\snmplens`, true},
		{`C:\Users\op\AppData\Local\SnmpLens`, true},
		{`C:\Users\op\Downloads`, false},
		{`C:\Program Files (x86)\SnmpLens Portable`, false},
	} {
		if got := installedAt(c.dir, registered); got != c.want {
			t.Errorf("%s: installed = %v", c.dir, got)
		}
	}
	// Nothing registered: Program Files is taken for an installation.
	if !installedAt(`C:\Program Files\SnmpLens`, nil, `C:\Program Files`) {
		t.Error("a copy in Program Files with nothing registered")
	}
	if installedAt(`D:\tools`, nil, `C:\Program Files`) {
		t.Error("a portable copy with nothing registered")
	}
}
