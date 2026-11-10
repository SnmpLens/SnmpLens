package app

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/backup"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/storage"
)

// newBackupApp is an App with a configuration directory, as startup gives one.
func newBackupApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	sec, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mibDir := filepath.Join(dir, "mibs")
	if err := os.MkdirAll(mibDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{secrets: sec, configDir: dir, persistentMibDir: mibDir, sim: newSimulatorService(dir)}
	t.Cleanup(func() { a.sim.fleet.StopAll() })
	testEngine(t, a, dir, nil)
	return a
}

const backupPassphrase = "a long enough passphrase"

// fillForBackup gives an App something of everything a backup holds.
func fillForBackup(t *testing.T, a *App) (sinkID, sessionID, deviceID string) {
	t.Helper()
	sink, err := a.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "WEBHOOK-TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.NotifySaveRoute(notify.Route{Name: "everything", Enabled: true, SinkIDs: []string{sink.ID}}); err != nil {
		t.Fatal(err)
	}
	sessionID, err = a.eng.Storage().CreateSession("uplinks", "1.3.6.1.2.1.1.3.0", []string{"10.0.0.1"}, 5000, "v2c",
		"2026-11-01T00:00:00Z", nil, &storage.SessionConn{Port: 161, TimeoutSec: 2, Retries: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.secrets.Set(secrets.SessionRef(sessionID), `{"community":"SESSION-COMMUNITY"}`); err != nil {
		t.Fatal(err)
	}

	dev, err := a.SimulatorSaveDevice(simDevice())
	if err != nil {
		t.Fatal(err)
	}

	presetDir, _ := a.presetDir()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "presets", "interfaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presetDir, "interfaces.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	assetDir, _ := a.assetDir()
	writePNG(t, filepath.Join(assetDir, "rack.png"), 64, 32)

	// Not RunInBackground: restoring that starts a real tray icon, which macOS
	// refuses to create anywhere but on the main thread of a real application.
	a.serviceCfg = service.Config{AutoResumeMonitors: true, TrapPort: 1162}
	if err := a.eng.SetTrapEngineID([]byte{0x80, 0, 0, 0, 5, 1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Fatal(err)
	}
	return sink.ID, sessionID, dev.ID
}

func exportFrom(t *testing.T, a *App, withSecrets bool) []byte {
	t.Helper()
	req := BackupExportRequest{
		Renderer:        json.RawMessage(`{"settings":{"targets":"10.0.0.1","community":""}}`),
		RendererSecrets: map[string]string{"community": "RENDERER-COMMUNITY"},
		IncludeSecrets:  withSecrets,
	}
	if withSecrets {
		req.Passphrase = backupPassphrase
	}
	data, _, err := a.buildBackup(req)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestABackupRestoresEverythingItHoldsOnAnotherMachine(t *testing.T) {
	src := newBackupApp(t)
	sinkID, sessionID, deviceID := fillForBackup(t, src)
	data := exportFrom(t, src, true)

	dst := newBackupApp(t)
	sum, err := dst.holdBackup("x.snmplens", data, "")
	if err != nil {
		t.Fatal(err)
	}
	want := BackupContents{Settings: true, Sinks: 1, Routes: 1, Sessions: 1, Devices: 1, Presets: 1, Assets: 1,
		Service: true, TrapEngine: true}
	if sum.Contents != want || !sum.Secrets || sum.Automatic {
		t.Fatalf("summary = %+v", sum)
	}

	res, err := dst.BackupRestore(BackupRestoreRequest{Token: sum.Token, Passphrase: backupPassphrase,
		CurrentRenderer: json.RawMessage(`{"settings":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	if res.Restored != want || !res.SecretsRestored {
		t.Fatalf("restored = %+v, secrets %v", res.Restored, res.SecretsRestored)
	}
	if res.RendererSecrets["community"] != "RENDERER-COMMUNITY" || !strings.Contains(string(res.Renderer), "10.0.0.1") {
		t.Fatalf("renderer = %s %v", res.Renderer, res.RendererSecrets)
	}

	if got := dst.storedSinkSecret(sinkID); got != "WEBHOOK-TOKEN" {
		t.Errorf("the destination's token = %q", got)
	}
	if routes, _ := dst.eng.Storage().ListRoutes(); len(routes) != 1 || routes[0].SinkIDs[0] != sinkID {
		t.Errorf("routes = %+v", routes)
	}
	sessions, _ := dst.eng.Storage().ListSessions()
	if len(sessions) != 1 || sessions[0].ID != sessionID || sessions[0].Active {
		t.Errorf("sessions = %+v", sessions)
	}
	if got := sessionCommunity(t, dst, sessionID); got != "SESSION-COMMUNITY" {
		t.Errorf("the session's community = %q", got)
	}
	if len(dst.sim.devices) != 1 || dst.sim.devices[0].ID != deviceID || dst.sim.devices[0].EngineID != src.sim.devices[0].EngineID {
		t.Errorf("devices = %+v", dst.sim.devices)
	}
	if ds, _ := dst.simulatorSecrets(deviceID); ds.Community != "s3cr3t-community" {
		t.Errorf("the device's community = %q", ds.Community)
	}
	presetDir, _ := dst.presetDir()
	if _, err := os.Stat(filepath.Join(presetDir, "interfaces.json")); err != nil {
		t.Errorf("preset: %v", err)
	}
	assetDir, _ := dst.assetDir()
	if _, err := os.Stat(filepath.Join(assetDir, "rack.png")); err != nil {
		t.Errorf("background: %v", err)
	}
	if !dst.serviceCfg.AutoResumeMonitors || dst.serviceCfg.TrapPort != 1162 {
		t.Errorf("service = %+v", dst.serviceCfg)
	}
	if got := dst.TrapListenerEngineID(); got != src.TrapListenerEngineID() {
		t.Errorf("engine ID = %s, want %s", got, src.TrapListenerEngineID())
	}
	if kept := reopenedEngineID(t, dst.configDir); string(kept) != string(src.eng.TrapEngineID()) {
		t.Errorf("the engine ID kept on disk = %x", kept)
	}
}

// What was there is backed up first, with its secrets, and this machine can open
// that backup without being asked for anything.
func TestARestoreCanBeUndone(t *testing.T) {
	src := newBackupApp(t)
	fillForBackup(t, src)
	data := exportFrom(t, src, true)

	dst := newBackupApp(t)
	mine, err := dst.NotifySaveSink(notify.SinkConfig{Name: "mine", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/mine"}, Secret: "MY-TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := dst.holdBackup("x.snmplens", data, "")
	res, err := dst.BackupRestore(BackupRestoreRequest{Token: sum.Token, Passphrase: backupPassphrase,
		CurrentRenderer:        json.RawMessage(`{"settings":{"targets":"192.0.2.1"}}`),
		CurrentRendererSecrets: map[string]string{"community": "MY-COMMUNITY"}})
	if err != nil {
		t.Fatal(err)
	}
	points := dst.ListAutomaticBackups()
	if len(points) != 1 || points[0].Name != res.Before {
		t.Fatalf("automatic backups = %+v, before = %q", points, res.Before)
	}
	before, err := dst.BackupInspectAutomatic(res.Before)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Automatic || before.Contents.Sinks != 1 {
		t.Fatalf("the backup made first = %+v", before)
	}
	undo, err := dst.BackupRestore(BackupRestoreRequest{Token: before.Token, CurrentRenderer: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !undo.SecretsRestored || undo.RendererSecrets["community"] != "MY-COMMUNITY" ||
		!strings.Contains(string(undo.Renderer), "192.0.2.1") {
		t.Fatalf("undo = %+v", undo)
	}
	if got := dst.storedSinkSecret(mine.ID); got != "MY-TOKEN" {
		t.Errorf("my own destination's token = %q", got)
	}
}

// A wrong passphrase is said before anything changes, and the backup stays open
// for another try.
func TestAWrongPassphraseChangesNothing(t *testing.T) {
	src := newBackupApp(t)
	fillForBackup(t, src)
	data := exportFrom(t, src, true)

	dst := newBackupApp(t)
	sum, _ := dst.holdBackup("x.snmplens", data, "")
	_, err := dst.BackupRestore(BackupRestoreRequest{Token: sum.Token, Passphrase: "not the passphrase at all"})
	if !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("err = %v", err)
	}
	if sinks, _ := dst.eng.Storage().ListSinks(); len(sinks) != 0 {
		t.Errorf("a wrong passphrase restored %d destinations", len(sinks))
	}
	if points := dst.ListAutomaticBackups(); len(points) != 0 {
		t.Errorf("a wrong passphrase made a backup: %+v", points)
	}
	if _, err := dst.BackupRestore(BackupRestoreRequest{Token: sum.Token, Passphrase: backupPassphrase}); err != nil {
		t.Errorf("the right passphrase after a wrong one: %v", err)
	}
}

// Without the passphrase, everything but the secrets.
func TestWithoutThePassphraseEverythingButTheSecretsIsRestored(t *testing.T) {
	src := newBackupApp(t)
	sinkID, sessionID, _ := fillForBackup(t, src)
	data := exportFrom(t, src, true)

	dst := newBackupApp(t)
	sum, _ := dst.holdBackup("x.snmplens", data, "")
	res, err := dst.BackupRestore(BackupRestoreRequest{Token: sum.Token})
	if err != nil {
		t.Fatal(err)
	}
	if res.SecretsRestored || len(res.RendererSecrets) != 0 || res.Restored.Sinks != 1 || res.Restored.Sessions != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := dst.storedSinkSecret(sinkID); got != "" {
		t.Errorf("a token came back without the passphrase: %q", got)
	}
	if got := sessionCommunity(t, dst, sessionID); got != "" {
		t.Errorf("a community came back without the passphrase: %q", got)
	}
}

// A session that exists is left with its history rather than replaced.
func TestASessionThatExistsKeepsItsHistory(t *testing.T) {
	a := newBackupApp(t)
	_, sessionID, _ := fillForBackup(t, a)
	data := exportFrom(t, a, false)
	sum, _ := a.holdBackup("x.snmplens", data, "")
	res, err := a.BackupRestore(BackupRestoreRequest{Token: sum.Token})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored.Sessions != 0 {
		t.Errorf("an existing session was restored over: %+v", res.Restored)
	}
	if sessions, _ := a.eng.Storage().ListSessions(); len(sessions) != 1 || sessions[0].ID != sessionID {
		t.Errorf("sessions = %+v", sessions)
	}
}

// Without secrets, the file holds none and says so; a token is not anywhere in it.
func TestABackupWithoutSecretsHoldsNone(t *testing.T) {
	a := newBackupApp(t)
	fillForBackup(t, a)
	data := exportFrom(t, a, false)
	for _, s := range []string{"WEBHOOK-TOKEN", "SESSION-COMMUNITY", "s3cr3t-community", "RENDERER-COMMUNITY"} {
		if strings.Contains(string(data), s) {
			t.Errorf("%s is in a backup made without secrets", s)
		}
	}
	sum, err := a.holdBackup("x.snmplens", data, "")
	if err != nil || sum.Secrets {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
}

// Only a name the backups folder holds is opened.
func TestOnlyAnAutomaticBackupIsOpenedByName(t *testing.T) {
	a := newBackupApp(t)
	for _, name := range []string{"../monitoring.db", "..\\sinks.secrets", ".hidden.snmplens", "x.json"} {
		if _, err := a.BackupInspectAutomatic(name); err == nil {
			t.Errorf("%q was opened", name)
		}
	}
}

// reopenedEngineID is the trap listener's engine ID as kept on disk in dir,
// not as held in memory.
func reopenedEngineID(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "trap-engine-id"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
