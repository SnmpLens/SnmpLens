package app

// SnmpLens against its own simulator, one thing followed through several
// features at once: a device identified, a preset bound to it and polled; a
// threshold crossed, journalled and delivered to a webhook; a device going down
// and coming back; its notifications journalled and routed; a request it
// refuses coming back as a trap; a v3 session outliving the device's restart.
//
// Each feature has its own tests. These are for the seams between them, which
// no single package owns and which only a real agent on the other side of a real
// socket exercises.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
)

const liveUptime = "1.3.6.1.2.1.1.3.0"

var liveUser = snmp.V3Params{User: "ops", SecLevel: "AuthPriv",
	AuthProto: "SHA256", AuthPass: "authpass-1", PrivProto: "AES", PrivPass: "privpass-1"}

var liveSimUser = simulator.User{Name: "ops", SecLevel: "AuthPriv",
	AuthProto: "SHA256", AuthPass: "authpass-1", PrivProto: "AES", PrivPass: "privpass-1"}

// liveApp is an App wired as startup wires it — the poll clock persisting and
// evaluating, the event router, the notification dispatcher, the simulator, and
// a headless SNMP client journalling what it hears — with no window.
type liveApp struct {
	*App
	presets string

	mu     sync.Mutex
	points []monitor.Point
}

func newLiveApp(t *testing.T) *liveApp {
	t.Helper()
	a, presetDir := newBindApp(t)
	l := &liveApp{App: a, presets: presetDir}

	// Headless: a client whose context the Wails runtime did not issue ends the
	// process the first time it emits, which a received trap does.
	//lint:ignore SA1012 a nil context is how pkg/snmp tells a headless client
	a.snmpClient = snmp.NewClient()
	a.snmpClient.SetRecorder(events.RecorderFunc(a.recordEvent))
	a.trapEngineID = loadTrapEngineID(t.TempDir())
	a.snmpClient.SetTrapEngineID(a.trapEngineID)

	a.scheduler.StopAll()
	a.initScheduler()
	persist := a.scheduler.Persist
	a.scheduler.Persist = func(p []monitor.Point) {
		l.mu.Lock()
		l.points = append(l.points, p...)
		l.mu.Unlock()
		persist(p)
	}
	t.Cleanup(a.scheduler.StopAll)
	a.initEvaluator()
	a.router = newEventRouter(a)
	a.router.start()
	t.Cleanup(a.router.stop)
	a.initDispatcher()
	t.Cleanup(a.dispatcher.Stop)
	a.sim = newSimulatorService(t.TempDir())
	t.Cleanup(a.sim.fleet.StopAll)
	return l
}

// await waits until done holds over what session has polled so far.
func (l *liveApp) await(t *testing.T, session string, within time.Duration, done func([]monitor.Point) bool) []monitor.Point {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		l.mu.Lock()
		var got []monitor.Point
		for _, p := range l.points {
			if p.SessionID == session {
				got = append(got, p)
			}
		}
		l.mu.Unlock()
		if done(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sample(s) in %v, and not the ones awaited: %+v", len(got), within, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitEvents waits until done holds over the journal's events matching f.
func (l *liveApp) awaitEvents(t *testing.T, f events.Filter, within time.Duration, done func([]events.Event) bool) []events.Event {
	t.Helper()
	f.Limit = 500
	deadline := time.Now().Add(within)
	for {
		page, err := l.storage.QueryEvents(f)
		if err != nil {
			t.Fatal(err)
		}
		if done(page.Items) {
			return page.Items
		}
		if time.Now().After(deadline) {
			var lines []string
			for _, e := range page.Items {
				lines = append(lines, e.Kind+" "+e.Source+" "+e.OID)
			}
			t.Fatalf("the journal holds %d matching event(s), and not the ones awaited:\n%s",
				len(page.Items), strings.Join(lines, "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func some(n int) func([]events.Event) bool {
	return func(e []events.Event) bool { return len(e) >= n }
}

// listen starts the application's trap listener, accepting users, and returns
// its port. It binds on a goroutine of its own, hence the pause the listener's
// other tests take too.
func (l *liveApp) listen(t *testing.T, users []snmp.V3Params) int {
	t.Helper()
	port := freeUDPPort(t)
	if _, err := l.snmpClient.StartTrapListener(port, users); err != nil {
		t.Fatalf("cannot bind a trap listener: %v", err)
	}
	t.Cleanup(l.snmpClient.StopTrapListener)
	time.Sleep(400 * time.Millisecond)
	return port
}

func copyPresets(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(repoRoot, "presets", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// hookServer is a webhook receiver, keeping every body it is sent.
type hookServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newHookServer(t *testing.T) *hookServer {
	t.Helper()
	h := &hookServer{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, string(b))
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(h.Close)
	return h
}

// await waits for n bodies containing want.
func (h *hookServer) await(t *testing.T, n int, want string, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		h.mu.Lock()
		var got []string
		for _, b := range h.bodies {
			if strings.Contains(b, want) {
				got = append(got, b)
			}
		}
		total := len(h.bodies)
		h.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d webhook bodies contain %q in %v, want %d", len(got), total, want, within, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// route sends the events match selects to a webhook, as an operator sets it up
// in the settings.
func (l *liveApp) route(t *testing.T, match notify.RouteMatch) *hookServer {
	t.Helper()
	h := newHookServer(t)
	sink, err := l.NotifySaveSink(notify.SinkConfig{Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: h.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.storage.SaveRoute(notify.Route{Name: "test", Enabled: true, Priority: 1,
		Match: match, SinkIDs: []string{sink.ID}}); err != nil {
		t.Fatal(err)
	}
	return h
}

// ownAddress is 127.0.0.n where this machine answers on it, 127.0.0.1 where it
// does not (macOS without aliases): a device on an address of its own is how
// the journal's source is told from the listener's own.
func ownAddress(n int) string {
	address := "127.0.0." + string(rune('0'+n/10)) + string(rune('0'+n%10))
	c, err := net.ListenPacket("udp", net.JoinHostPort(address, "0"))
	if err != nil {
		return "127.0.0.1"
	}
	c.Close()
	return address
}

// Every model is identified as what it is, the preset written for it comes
// first, each preset it feeds binds — discovery included, through a target
// naming its port — and the first round of polling answers every OID the preset
// asks for. A healthy device raises none of the bands those presets carry.
func TestSnmpLensIdentifiesBindsAndPollsEachSimulatedModel(t *testing.T) {
	bundled := []string{"interfaces.json", "host-resources.json", "switch-drawing.json",
		"switch-ports.json", "cisco-generic.json", "ups.json"}
	for _, c := range []struct {
		model   string
		presets []string
		first   string // the preset that claims the device, if one does
	}{
		{"linux-server", []string{"interfaces.json", "host-resources.json"}, ""},
		{"windows-server", []string{"host-resources.json"}, ""},
		{"cisco-catalyst-24", []string{"switch-drawing.json", "switch-ports.json", "cisco-generic.json"}, "cisco-generic.json"},
		{"cisco-isr-4331", []string{"cisco-generic.json"}, "cisco-generic.json"},
		{"synology-nas", []string{"host-resources.json"}, ""},
		{"apc-smart-ups", []string{"ups.json"}, ""},
	} {
		t.Run(c.model, func(t *testing.T) {
			l := newLiveApp(t)
			copyPresets(t, l.presets, bundled...)
			d := simtest.Start(t, simulator.Device{Model: c.model, Name: "dev-" + c.model})

			id := l.IdentifyDevice(snmp.TestRequest{Target: d.Target(), Community: "public", Version: "v2c", Timeout: 2})
			if id.Error != "" || id.SysName != d.Name {
				t.Fatalf("identified as %+v", id)
			}
			switch {
			case c.first != "" && (id.Matched < 1 || id.Presets[0].File != c.first):
				t.Errorf("%d preset(s) claim it and %q comes first; want %s first", id.Matched, id.Presets[0].File, c.first)
			case c.first == "" && id.Matched != 0:
				t.Errorf("%d preset(s) claim a device none is written for", id.Matched)
			}

			for _, file := range c.presets {
				sess, err := l.PresetBind(file, d.Target(), "v2c", MonitorConnection{TimeoutSec: 2, Retries: 1, Community: "public"})
				if err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				oids := strings.Split(sess.OID, ",")
				round := l.await(t, sess.ID, 10*time.Second, func(p []monitor.Point) bool { return len(p) >= len(oids) })
				for _, p := range round[:len(oids)] {
					if p.Error != "" {
						t.Errorf("%s: %s answered %q", file, p.OID, p.Error)
					}
				}
				opened, err := l.storage.QueryEvents(events.Filter{Kinds: []string{events.KindThresholdOpened}, SessionID: sess.ID})
				if err != nil {
					t.Fatal(err)
				}
				if len(opened.Items) != 0 {
					t.Errorf("%s: a healthy %s raised %s", file, c.model, opened.Items[0].Summary)
				}
			}
		})
	}
}

// A band crossed on a simulated device is journalled, routed and delivered: the
// processor load of the Linux model swings from 4 to 55 %, and the band allows 1.
func TestAThresholdOnASimulatedDeviceReachesAWebhook(t *testing.T) {
	l := newLiveApp(t)
	hook := l.route(t, notify.RouteMatch{Kinds: []string{events.KindThresholdOpened}})
	d := simtest.Start(t, simulator.Device{})

	const load = "1.3.6.1.2.1.25.3.3.1.2.196608"
	limit := 1.0
	id, err := l.MonitorCreateSession(load, []string{d.Target()}, 300, "v2c",
		map[string]*storage.Thresholds{load: {Max: &limit, AlertEnabled: true}},
		"CPU", MonitorConnection{TimeoutSec: 2, Community: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.MonitorStart(id); err != nil {
		t.Fatal(err)
	}

	opened := l.awaitEvents(t, events.Filter{Kinds: []string{events.KindThresholdOpened}, SessionID: id}, 10*time.Second, some(1))
	if opened[0].OID != load {
		t.Errorf("the incident is about %q, not the processor load", opened[0].OID)
	}
	hook.await(t, 1, events.KindThresholdOpened, 10*time.Second)
}

// A device that stops answering is journalled as down, and as up again once it
// is back.
func TestASimulatedDeviceGoingDownAndComingBackIsJournalled(t *testing.T) {
	l := newLiveApp(t)
	d := simtest.Start(t, simulator.Device{})
	id, err := l.MonitorCreateSession(liveUptime, []string{d.Target()}, 300, "v2c", nil, "uptime",
		MonitorConnection{TimeoutSec: 1, Community: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.MonitorStart(id); err != nil {
		t.Fatal(err)
	}
	l.await(t, id, 8*time.Second, func(p []monitor.Point) bool { return len(p) > 0 && p[len(p)-1].Error == "" })

	d.Stop()
	l.awaitEvents(t, events.Filter{Kinds: []string{events.KindReachabilityDown}, SessionID: id}, 15*time.Second, some(1))
	d.Restart()
	l.awaitEvents(t, events.Filter{Kinds: []string{events.KindReachabilityUp}, SessionID: id}, 15*time.Second, some(1))
}

// A simulated switch's notifications, in every version, reach the journal as
// coming from the switch, and a route on the switch's address delivers them.
// It sends coldStart when it starts, and ciscoConfigManEvent on request —
// SNMPv1, v2c, a v3 trap as the switch's own engine, and a v3 INFORM to the
// engine ID the listener stands for, acknowledged.
func TestASimulatedSwitchsNotificationsAreJournalledAndRouted(t *testing.T) {
	l := newLiveApp(t)
	address := ownAddress(21)
	hook := l.route(t, notify.RouteMatch{Categories: []string{events.CategoryTrap}, Sources: []string{address + "/32"}})
	port := l.listen(t, []snmp.V3Params{liveUser})

	to := func(version string, inform bool) simulator.Destination {
		dest := simulator.Destination{Host: "127.0.0.1", Port: port, Version: version, Inform: inform}
		if version == "v3" {
			dest.User = "ops"
			if inform {
				dest.EngineID = l.TrapListenerEngineID()
			}
		} else {
			dest.Community = "traps"
		}
		return dest
	}
	saved, err := l.SimulatorSaveDevice(simulator.Device{
		Name: "sw-traps", Model: "cisco-catalyst-24", Address: address, Port: simtest.FreePort(t, address),
		Versions: []string{"v2c", "v3"}, Community: "public", Users: []simulator.User{liveSimUser},
		Traps: simulator.Traps{OnStart: true, Destinations: []simulator.Destination{
			to("v1", false), to("v2c", false), to("v3", false), to("v3", true),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SimulatorStartDevice(saved.ID); err != nil {
		t.Fatal(err)
	}

	const coldStart = ".1.3.6.1.6.3.1.1.5.1"
	fromSwitch := events.Filter{Categories: []string{events.CategoryTrap}, Source: address}
	l.awaitEvents(t, fromSwitch, 10*time.Second, func(e []events.Event) bool { return countOID(e, coldStart) >= 3 })

	deliveries, err := l.SimulatorSendTrap(saved.ID, "ciscoConfigManEvent")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deliveries {
		if d.Error != "" || d.Inform != d.Acknowledged {
			t.Errorf("%s: %+v", d.Destination, d)
		}
	}

	const configChange = ".1.3.6.1.4.1.9.9.43.2.0.1"
	got := l.awaitEvents(t, fromSwitch, 10*time.Second, func(e []events.Event) bool {
		return countOID(e, configChange) >= 3 && countOID(e, ".1.3.6.1.4.1.9.9.43.2") >= 4
	})
	informs := 0
	for _, e := range got {
		if e.Source != address {
			t.Errorf("journalled as coming from %q, not from the switch's %s", e.Source, address)
		}
		if e.Kind == events.KindTrapInform && e.OID == configChange {
			informs++
		}
	}
	if informs != 1 {
		t.Errorf("%d INFORM(s) journalled for the configuration change, want 1", informs)
	}
	// SNMPv1 carries no snmpTrapOID: its trap is journalled under its
	// enterprise, the CISCO-CONFIG-MAN-MIB's notification prefix.
	if n := countOID(got, ".1.3.6.1.4.1.9.9.43.2") - countOID(got, configChange); n != 1 {
		t.Errorf("%d SNMPv1 configuration change(s) journalled, want 1", n)
	}
	hook.await(t, 8, address, 10*time.Second)
}

func countOID(e []events.Event, prefix string) int {
	n := 0
	for _, x := range e {
		if strings.HasPrefix(x.OID, prefix) {
			n++
		}
	}
	return n
}

// A request the device refuses — SnmpLens asking with the wrong community — comes
// back as the device's authenticationFailure, journalled.
func TestARefusedRequestComesBackAsAuthenticationFailure(t *testing.T) {
	l := newLiveApp(t)
	port := l.listen(t, nil)
	d := simtest.Start(t, simulator.Device{Traps: simulator.Traps{OnAuthFailure: true,
		Destinations: []simulator.Destination{{ID: "nms", Host: "127.0.0.1", Port: port, Version: "v2c", Community: "public"}}}})

	res := l.snmpClient.Get([]string{d.Target()}, liveUptime, "not-the-community", "v2c", 0, 1, 0, snmp.V3Params{})
	if len(res) != 1 || res[0].Error == "" {
		t.Fatalf("a wrong community was answered: %+v", res)
	}
	l.awaitEvents(t, events.Filter{Categories: []string{events.CategoryTrap}, OID: ".1.3.6.1.6.3.1.1.5.5"},
		10*time.Second, some(1))
}

// An SNMPv3 session keeps polling a device that restarts: the device comes back
// one boot later with its clock started over, says so with coldStart, and the
// session's next readings are the new uptime rather than failures.
func TestAV3SessionOutlivesTheDevicesRestart(t *testing.T) {
	l := newLiveApp(t)
	port := l.listen(t, nil)
	d := simtest.Start(t, simulator.Device{Versions: []string{"v3"}, Users: []simulator.User{liveSimUser},
		Traps: simulator.Traps{OnStart: true, Destinations: []simulator.Destination{
			{ID: "nms", Host: "127.0.0.1", Port: port, Version: "v2c", Community: "public"}}}})

	id, err := l.MonitorCreateSession(liveUptime, []string{d.Target()}, 300, "v3", nil, "v3 uptime",
		MonitorConnection{TimeoutSec: 1, Retries: 1, V3: liveUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.MonitorStart(id); err != nil {
		t.Fatal(err)
	}
	up := func(p []monitor.Point, atLeast float64) bool {
		n := len(p)
		return n > 0 && p[n-1].Error == "" && p[n-1].Value != nil && *p[n-1].Value >= atLeast
	}
	before := l.await(t, id, 8*time.Second, func(p []monitor.Point) bool { return up(p, 100) })
	last := *before[len(before)-1].Value

	d.Restart()
	after := l.await(t, id, 10*time.Second, func(p []monitor.Point) bool {
		return len(p) > len(before) && up(p, 0) && *p[len(p)-1].Value < last
	})
	if n := len(after); after[n-1].Error != "" {
		t.Errorf("the session's last reading failed: %s", after[n-1].Error)
	}
	l.awaitEvents(t, events.Filter{Categories: []string{events.CategoryTrap}, OID: ".1.3.6.1.6.3.1.1.5.1"},
		10*time.Second, some(2))
}
