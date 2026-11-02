package app

import (
	"testing"
	"time"

	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/snmp"
)

var (
	trapAlice = snmp.V3Params{User: "alice", SecLevel: "AuthPriv", AuthProto: "SHA",
		AuthPass: "alice-auth-123", PrivProto: "AES", PrivPass: "alice-priv-123", ContextName: "vlan-100"}
	trapBob = snmp.V3Params{User: "bob", SecLevel: "AuthNoPriv", AuthProto: "SHA256",
		AuthPass: "bob-auth-1234"}
)

// headlessApp is an App whose SNMP client has no webview attached.
func headlessApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	// nil, deliberately: it is how this client says "no webview attached".
	//lint:ignore SA1012 nil means "headless"; see handleTrap's emit guard
	a.snmpClient = snmp.NewClient()
	return a
}

// The users a listener takes are remembered, so the next start — at login,
// before any window — accepts v3 notifications as well.
func TestTheTrapListenerRemembersTheUsersItTook(t *testing.T) {
	a := headlessApp(t)
	port := freeUDPPort(t)

	broken := snmp.V3Params{User: "dave", SecLevel: "AuthPriv", AuthProto: "SHA", AuthPass: "dave-auth-123"}
	info, err := a.StartTrapListener(snmp.TrapListenerRequest{Port: port, Users: []snmp.V3Params{trapAlice, broken}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.snmpClient.StopTrapListener()

	if info.Users != 1 || len(info.Refused) != 1 || info.Refused[0].User != "dave" {
		t.Fatalf("unexpected info %+v", info)
	}
	stored := a.loadTrapUsers()
	if len(stored) != 1 || stored[0].User != "alice" || stored[0].AuthPass != "alice-auth-123" {
		t.Fatalf("remembered %+v, want alice alone", stored)
	}
	// Only what receiving uses. A refused user would be refused again at every
	// login, and a context name selects nothing on the way in.
	if stored[0].ContextName != "" {
		t.Errorf("the context name was remembered: %+v", stored[0])
	}
}

// Applying users with nothing listening still remembers them: that is the
// upgrade path, and a profile edited while the listener is off.
func TestTrapUsersAreRememberedWithNothingListening(t *testing.T) {
	a := headlessApp(t)

	info, err := a.UpdateTrapUsers([]snmp.V3Params{trapAlice, trapBob})
	if err != nil || info.Restarted || info.Users != 2 {
		t.Fatalf("info %+v, err %v", info, err)
	}
	if got := a.loadTrapUsers(); len(got) != 2 {
		t.Fatalf("remembered %d users, want 2", len(got))
	}

	// The last v3 profile going deletes the entry rather than storing "[]".
	if _, err := a.UpdateTrapUsers(nil); err != nil {
		t.Fatal(err)
	}
	if raw, err := a.secrets.Get(secrets.TrapUsersRef()); err == nil {
		t.Errorf("an empty set left an entry behind: %q", raw)
	}
}

// The background start reads what was remembered. Before this it passed an
// empty V3Params, so a listener started at login dropped every v3 notification
// whatever the settings said.
func TestTheBackgroundStartUsesTheRememberedUsers(t *testing.T) {
	a := headlessApp(t)
	if _, err := a.UpdateTrapUsers([]snmp.V3Params{trapAlice, trapBob}); err != nil {
		t.Fatal(err)
	}

	port := freeUDPPort(t)
	a.serviceCfg = service.Config{AutoStartTrapListener: true, TrapPort: port}
	a.initBackgroundMode()
	defer a.snmpClient.StopTrapListener()
	if !a.snmpClient.TrapListenerRunning() {
		t.Fatal("the background start did not start the listener")
	}
	time.Sleep(200 * time.Millisecond) // let it bind

	// The same users again is not a change: the running set IS the remembered one.
	info, err := a.snmpClient.UpdateTrapUsers([]snmp.V3Params{trapBob, trapAlice})
	if err != nil {
		t.Fatal(err)
	}
	if info.Restarted {
		t.Error("the background start did not take the remembered users")
	}
}

// A session's connection can change its SNMP version as well as its
// credentials: a credential profile carries a version, and moving one from
// v2c to v3 changes how its sessions must speak.
func TestUpdatingAConnectionCanChangeTheVersion(t *testing.T) {
	a := newTestApp(t)

	id, err := a.MonitorCreateSession("1.3.6.1.2.1.1.3.0", []string{"10.0.0.1"}, 1000, "v2c", nil, "core",
		MonitorConnection{Port: 161, TimeoutSec: 2, Retries: 1, Community: "old-community", Profile: "p-core"})
	if err != nil {
		t.Fatal(err)
	}

	conn := MonitorConnection{Port: 161, TimeoutSec: 2, Retries: 1, Profile: "p-core",
		V3: snmp.V3Params{User: "ops", SecLevel: "AuthPriv", AuthProto: "SHA",
			AuthPass: "new-auth-123", PrivProto: "AES", PrivPass: "new-priv-123"}}
	if err := a.MonitorUpdateConnection(id, "v3", conn); err != nil {
		t.Fatal(err)
	}
	sess, err := a.findSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.SnmpVersion != "v3" {
		t.Errorf("version %q, want v3", sess.SnmpVersion)
	}
	if sess.Conn == nil || sess.Conn.Profile != "p-core" || sess.Conn.V3User != "ops" {
		t.Errorf("connection not replaced: %+v", sess.Conn)
	}
	creds := a.loadSessionCreds(id)
	if creds.AuthPass != "new-auth-123" || creds.Community != "" {
		t.Errorf("credentials not replaced: %+v", creds)
	}

	// Empty keeps the session's own version; nonsense is refused.
	if err := a.MonitorUpdateConnection(id, "", conn); err != nil {
		t.Fatal(err)
	}
	if sess, _ := a.findSession(id); sess.SnmpVersion != "v3" {
		t.Errorf("an empty version changed it to %q", sess.SnmpVersion)
	}
	if err := a.MonitorUpdateConnection(id, "v4", conn); err == nil {
		t.Error("an unknown SNMP version was accepted")
	}
}
