package remote

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"SnmpLens/internal/engine"
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
)

// service is an engine open on a temporary directory, served the way the
// system service serves it: the server is the engine's emitter.
func service(t *testing.T, version string) (*engine.Engine, *Server) {
	t.Helper()
	eng := engine.New(engine.Options{DataDir: t.TempDir()})
	srv := NewServer(eng, version)
	eng.SetEmitter(srv)
	if err := eng.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Close()
		eng.Close()
	})
	return eng, srv
}

// connect is a window connected to srv over an in-memory pipe.
func connect(t *testing.T, srv *Server, version string, onEvent func(string, []json.RawMessage)) (*Client, error) {
	t.Helper()
	a, b := net.Pipe()
	go srv.ServeConn(a)
	c, err := NewClient(b, version, onEvent)
	if err == nil {
		t.Cleanup(c.Close)
	}
	return c, err
}

// A window drives the service's engine as it would its own: what it saves is
// saved there, a credential goes in and never comes back out, an error the
// engine returns comes back as one, and what the engine journals reaches the
// window as the event a window in the same process would have had.
func TestAWindowDrivesTheServicesEngine(t *testing.T) {
	_, srv := service(t, "v9.9.9")
	var mu sync.Mutex
	var seen []string
	c, err := connect(t, srv, "v9.9.9", func(name string, _ []json.RawMessage) {
		mu.Lock()
		seen = append(seen, name)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	saved, err := c.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"}, Secret: "TOP-SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Secret != "" {
		t.Fatalf("saved = %+v", saved)
	}
	sinks, err := c.NotifyListSinks()
	if err != nil || len(sinks) != 1 || sinks[0].ID != saved.ID || sinks[0].Secret != "" {
		t.Fatalf("listed = %+v, %v", sinks, err)
	}

	var remote *RemoteError
	if err := c.MonitorStart("no-such-session"); !errors.As(err, &remote) || !strings.Contains(err.Error(), "no-such-session") {
		t.Errorf("an engine's error came back as %v", err)
	}

	c.Journal(events.Event{Category: events.CategorySystem, Kind: events.KindSystemInfo, Severity: "info",
		TitleKey: "events.kind.system.info", Summary: "journalled from a window"})
	page, err := c.EventsQuery(events.Filter{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Summary != "journalled from a window" {
		t.Fatalf("the journal = %+v, %v", page.Items, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := strings.Join(seen, ",")
		mu.Unlock()
		if strings.Contains(got, "event:new") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the window was told %q, not of the event", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A window refuses a service of another version before sending it anything:
// they are one executable, and a mismatch means the installer ran for one of
// them only.
func TestAWindowRefusesAnotherVersion(t *testing.T) {
	_, srv := service(t, "v1.0.0")
	_, err := connect(t, srv, "v1.0.1", nil)
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Service != "v1.0.0" || ve.Window != "v1.0.1" {
		t.Fatalf("connecting across versions: %v", err)
	}
}

// A lost service is said once, through Done, and a call that cannot report it
// returns nothing rather than hanging.
func TestALostServiceIsSaidThroughDone(t *testing.T) {
	_, srv := service(t, "")
	c, err := connect(t, srv, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the window did not notice the service go")
	}
	if !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("why: %v", c.Err())
	}
	if got := c.MonitorRunning(); len(got) != 0 {
		t.Errorf("a call on a lost service answered %v", got)
	}
	if _, err := c.EventsCounts(); !errors.Is(err, ErrClosed) {
		t.Errorf("a call with an error result: %v", err)
	}
}

// The switch sends each call to whichever engine is current, and a window
// moved to the service is driving the service's journal, not its own.
func TestTheSwitchFollowsTheCurrentEngine(t *testing.T) {
	local := engine.New(engine.Options{DataDir: t.TempDir()})
	if err := local.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(local.Close)
	_, srv := service(t, "")
	c, err := connect(t, srv, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	sw := NewSwitch(local)
	sw.Journal(events.Event{Category: events.CategorySystem, Kind: events.KindSystemInfo, Severity: "info",
		TitleKey: "events.kind.system.info", Summary: "local"})
	sw.Set(c)
	sw.Journal(events.Event{Category: events.CategorySystem, Kind: events.KindSystemInfo, Severity: "info",
		TitleKey: "events.kind.system.info", Summary: "service"})

	for _, x := range []struct {
		name string
		h    Host
		want string
	}{{"the window's own", local, "local"}, {"the service's", c, "service"}} {
		page, err := x.h.EventsQuery(events.Filter{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].Summary != x.want {
			t.Errorf("%s journal = %+v, %v", x.name, page.Items, err)
		}
	}
}
