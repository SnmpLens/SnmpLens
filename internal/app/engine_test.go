package app

import (
	"encoding/json"
	"net"
	"testing"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/secrets"
)

// testEngine opens an engine on dir for a, sharing a's secret store as startup
// does, and closes it when the test ends. emitter may be nil.
func testEngine(t *testing.T, a *App, dir string, emitter engine.Emitter) *engine.Engine {
	t.Helper()
	eng := engine.New(engine.Options{DataDir: dir, Secrets: a.secrets, Emitter: emitter})
	if err := eng.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	a.useEngine(eng)
	return eng
}

// sessionCommunity is the community kept for a session in the secret store.
func sessionCommunity(t *testing.T, a *App, id string) string {
	t.Helper()
	raw, err := a.secrets.Get(secrets.SessionRef(id))
	if err != nil {
		return ""
	}
	var creds struct {
		Community string `json:"community"`
	}
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		t.Fatal(err)
	}
	return creds.Community
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
