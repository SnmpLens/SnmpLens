package cli

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"SnmpLens/pkg/updater"
)

// The copy an update relaunches is told which process to wait for; anything
// else on that line must not make it wait for a process it was not given.
func TestAfterUpdateNamesTheProcessToWaitFor(t *testing.T) {
	for _, tc := range []struct {
		args []string
		pid  int
		ok   bool
	}{
		{[]string{updater.AfterUpdateFlag, "4242"}, 4242, true},
		{[]string{updater.AfterUpdateFlag}, 0, false},
		{[]string{updater.AfterUpdateFlag, "x"}, 0, false},
		{[]string{updater.AfterUpdateFlag, "-1"}, 0, false},
		{[]string{updater.AfterUpdateFlag, "0"}, 0, false},
		{nil, 0, false},
	} {
		pid, ok := afterUpdatePID(tc.args)
		if ok != tc.ok || (ok && pid != tc.pid) {
			t.Errorf("afterUpdatePID(%q) = %d, %v; want %d, %v", tc.args, pid, ok, tc.pid, tc.ok)
		}
	}
}

// The relaunched copy is the desktop application: Run waits for the copy that
// started it, then hands over.
func TestAfterUpdateStartsTheDesktopApplication(t *testing.T) {
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if handled, _ := Run([]string{updater.AfterUpdateFlag, strconv.Itoa(gone.Process.Pid)}); handled {
		t.Fatal("--after-update was taken for a command; the window would never open")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("waited for a copy that had already exited")
	}
}
