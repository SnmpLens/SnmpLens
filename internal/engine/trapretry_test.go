package engine

import (
	"net"
	"testing"
	"time"

	"SnmpLens/pkg/service"
)

// A trap port taken when the supervision starts — by a window's own supervision
// about to hand over to the service — is taken again once it is free: the
// service does not stay deaf to traps for the rest of its run.
func TestATakenTrapPortIsTakenOnceFree(t *testing.T) {
	holder, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := holder.LocalAddr().(*net.UDPAddr).Port
	holder.Close()
	// Taken on every address the listener binds, as another process would.
	busy, err := net.ListenPacket("udp", net.JoinHostPort("", itoa(port)))
	if err != nil {
		t.Skip(err)
	}

	defer func(d time.Duration) { trapRetry = d }(trapRetry)
	trapRetry = 50 * time.Millisecond

	eng := New(Options{DataDir: t.TempDir(), Config: service.Config{AutoStartTrapListener: true, TrapPort: port}})
	if err := eng.Open(); err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	eng.Start()
	if eng.TrapListenerRunning() {
		busy.Close()
		t.Skip("the system let two sockets share the port")
	}
	busy.Close()
	deadline := time.Now().Add(3 * time.Second)
	for !eng.TrapListenerRunning() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !eng.TrapListenerRunning() {
		t.Fatal("the listener never took the port once it was free")
	}
}

func itoa(n int) string {
	b := []byte{}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
