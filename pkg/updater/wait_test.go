package updater

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHelperSleep is not a test: it is the short-lived process
// TestWaitForExit waits for, standing in for the copy that started an update.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("SNMPLENS_WAIT_HELPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(700 * time.Millisecond)
	os.Exit(0)
}

func TestWaitForExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "SNMPLENS_WAIT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it the way its real parent would, so it does not linger as a
	// zombie that still answers on Unix.
	go cmd.Wait()

	if WaitForExit(cmd.Process.Pid, 50*time.Millisecond) {
		t.Fatal("reported gone while the process was still running")
	}
	start := time.Now()
	if !WaitForExit(cmd.Process.Pid, 10*time.Second) {
		t.Fatal("the process exited, and WaitForExit timed out")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("returned %v after the process exited, not promptly", waited)
	}
}

func TestWaitForExitReturnsAtOnceForAProcessAlreadyGone(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if !WaitForExit(cmd.Process.Pid, 5*time.Second) {
		t.Fatal("a process that had exited was not reported gone")
	}
	if time.Since(start) > time.Second {
		t.Error("waited for a process that was already gone")
	}
}
