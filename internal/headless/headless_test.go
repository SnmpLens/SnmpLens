package headless

import (
	"context"
	"errors"
	"testing"
	"time"

	"SnmpLens/pkg/dirlock"
	"SnmpLens/pkg/service"
)

// The supervision runs with no window on a directory, refuses a directory
// another one has open — two would poll every session twice and send every
// alert twice — and lets go of it when stopped.
func TestOneSupervisionPerDirectory(t *testing.T) {
	dir := t.TempDir()
	h, err := Start(Options{DataDir: dir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(Options{DataDir: dir}); !errors.Is(err, dirlock.ErrInUse) {
		t.Fatalf("a second supervision on the same directory: %v", err)
	}
	h.Stop()
	again, err := Start(Options{DataDir: dir})
	if err != nil {
		t.Fatalf("after the first stopped: %v", err)
	}
	again.Stop()
}

// service.json decides what starts: here the trap listener, on its port.
func TestTheTrapListenerStartsWhenTheDirectorySaysSo(t *testing.T) {
	dir := t.TempDir()
	if err := service.Save(dir, service.Config{AutoStartTrapListener: true, TrapPort: 0}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var h *Host
	started := make(chan struct{})
	go func() {
		var err error
		h, err = Start(Options{DataDir: dir})
		close(started)
		if err != nil {
			done <- err
			return
		}
		<-ctx.Done()
		h.Stop()
		done <- nil
	}()
	<-started
	if h == nil {
		t.Fatal(<-done)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !h.Engine().TrapListenerRunning() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.Engine().TrapListenerRunning() {
		t.Error("service.json asked for the trap listener and it is not listening")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
