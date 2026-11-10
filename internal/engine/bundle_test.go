package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/snmp"
)

// What a window supervised moves to another supervision whole: its sessions and
// their history in the database copy, their credentials, the destinations'
// tokens, the trap users and the listener's engine ID — and a service that has
// a history of its own keeps it.
func TestASupervisionMovesWhole(t *testing.T) {
	from := New(Options{DataDir: t.TempDir()})
	if err := from.Open(); err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	session, err := from.MonitorCreateSession("1.3.6.1.2.1.1.3.0", []string{"10.0.0.1"}, 5000, "v2c", nil, "uplinks",
		MonitorConnection{Port: 161, Community: "SESSION-COMMUNITY"})
	if err != nil {
		t.Fatal(err)
	}
	sink, err := from.NotifySaveSink(notify.SinkConfig{Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"}, Secret: "WEBHOOK-TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := from.UpdateTrapUsers([]snmp.V3Params{{User: "ops", SecLevel: "AuthNoPriv", AuthProto: "SHA", AuthPass: "authpass-1"}}); err != nil {
		t.Fatal(err)
	}

	b, err := from.ExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "monitoring.db")
	if err := from.Storage().SnapshotTo(snapshot); err != nil {
		t.Fatal(err)
	}

	to := t.TempDir()
	store, err := secrets.OpenMachine(to)
	if err != nil {
		t.Fatal(err)
	}
	cfg := service.Config{AutoStartTrapListener: true, AutoResumeMonitors: true, TrapPort: 162}
	kept, err := ImportBundle(to, store, b, cfg, snapshot)
	if err != nil || kept {
		t.Fatalf("import: kept %v, %v", kept, err)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Error("the copy holding the history was left behind")
	}

	installed := New(Options{DataDir: to, Secrets: store, Config: cfg})
	if err := installed.Open(); err != nil {
		t.Fatal(err)
	}
	defer installed.Close()
	sessions, _ := installed.MonitorLoadSessions()
	if len(sessions) != 1 || sessions[0].ID != session {
		t.Fatalf("the service's sessions: %+v", sessions)
	}
	if got := installed.loadSessionCreds(session); got.Community != "SESSION-COMMUNITY" {
		t.Errorf("the session's community = %q", got.Community)
	}
	if got := installed.sinkSecret(sink.ID); got != "WEBHOOK-TOKEN" {
		t.Errorf("the destination's token = %q", got)
	}
	if users := installed.loadTrapUsers(); len(users) != 1 || users[0].AuthPass != "authpass-1" {
		t.Errorf("the trap users = %+v", users)
	}
	if installed.TrapListenerEngineID() != from.TrapListenerEngineID() {
		t.Errorf("engine ID %s, the window's %s", installed.TrapListenerEngineID(), from.TrapListenerEngineID())
	}
	if saved, _ := os.ReadFile(service.Path(to)); !strings.Contains(string(saved), `"autoStartTrapListener": true`) {
		t.Errorf("service.json = %s", saved)
	}

	// Installed again over a service that has its history: it keeps it.
	again := filepath.Join(t.TempDir(), "monitoring.db")
	if err := from.Storage().SnapshotTo(again); err != nil {
		t.Fatal(err)
	}
	if kept, err := ImportBundle(to, store, Bundle{}, cfg, again); err != nil || !kept {
		t.Errorf("a service with a history: kept %v, %v", kept, err)
	}
}

// A bundle is a file: it may carry sessions', destinations' and the trap
// users' credentials into a store, and nothing else of it.
func TestABundleCarriesOnlyWhatItMay(t *testing.T) {
	dir := t.TempDir()
	store, err := secrets.OpenMachine(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{secrets.SettingsKeyRef(), "session//snmp", "session/a/b/snmp", "sink/x/other"} {
		_, err := ImportBundle(dir, store, Bundle{Secrets: map[string]string{ref: "x"}}, service.Defaults(), "")
		if err == nil {
			t.Errorf("a bundle wrote %q", ref)
		}
	}
	for _, ref := range []string{secrets.SessionRef("s1"), secrets.SinkRef("k1"), secrets.TrapUsersRef()} {
		if _, err := ImportBundle(dir, store, Bundle{Secrets: map[string]string{ref: "x"}}, service.Defaults(), ""); err != nil {
			t.Errorf("a bundle could not write %q: %v", ref, err)
		}
	}
}
