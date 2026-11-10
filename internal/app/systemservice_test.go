package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"SnmpLens/internal/engine"
	"SnmpLens/internal/sysvc"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
)

// With no service, the window runs its own supervision and drives it, and lets
// go of it when it closes.
func TestTheWindowRunsItsOwnSupervisionWithoutAService(t *testing.T) {
	if info, err := sysvc.Query(); err == nil && info.Installed {
		t.Skip("a SnmpLens service is installed on this machine")
	}
	dir := t.TempDir()
	sec, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{secrets: sec, configDir: dir}
	a.startLink()
	eng := a.localEngine()
	if eng == nil || eng.Storage() == nil {
		a.stopLink()
		t.Fatal("no supervision of the window's own")
	}
	if _, err := a.NotifySaveRoute(notify.Route{Name: "everything", Enabled: true}); err != nil {
		t.Error(err)
	}
	if st := a.SystemServiceStatus(); st.Driving || st.Installed {
		t.Errorf("status: %+v", st)
	}
	a.stopLink()
	if a.localEngine() != nil {
		t.Error("the window's supervision outlived the window")
	}
	// Let go of: another can open the directory now.
	again := engine.New(engine.Options{DataDir: dir})
	if err := again.Open(); err != nil {
		t.Fatalf("the directory was not let go of: %v", err)
	}
	again.Close()
}

// A service removed hands its history and credentials back: they become the
// window's own, and what the window had before is kept beside them.
func TestTheServicesHistoryComesBack(t *testing.T) {
	// The service's supervision, exported as the elevated step does.
	svcDir, exportDir := t.TempDir(), t.TempDir()
	service := engine.New(engine.Options{DataDir: svcDir})
	if err := service.Open(); err != nil {
		t.Fatal(err)
	}
	sink, err := service.NotifySaveSink(notify.SinkConfig{Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"}, Secret: "WEBHOOK-TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.ExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(exportDir, sysvc.ExportedBundle), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Storage().SnapshotTo(filepath.Join(exportDir, sysvc.ExportedDatabase)); err != nil {
		t.Fatal(err)
	}
	service.Close()

	// The window, with a database of its own from before the service.
	local := t.TempDir()
	before := engine.New(engine.Options{DataDir: local})
	if err := before.Open(); err != nil {
		t.Fatal(err)
	}
	before.Close()
	sec, err := secrets.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{secrets: sec, configDir: local}
	if err := a.takeBack(exportDir); err != nil {
		t.Fatal(err)
	}

	kept, _ := filepath.Glob(filepath.Join(local, "monitoring.db.before-service-*"))
	if len(kept) == 0 {
		t.Error("the window's history from before the service was not kept")
	}
	after := engine.New(engine.Options{DataDir: local, Secrets: sec})
	if err := after.Open(); err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	sinks, err := after.NotifyListSinks()
	if err != nil || len(sinks) != 1 || sinks[0].ID != sink.ID {
		t.Fatalf("the window's destinations: %+v, %v", sinks, err)
	}
	if got, _ := sec.Get(secrets.SinkRef(sink.ID)); got != "WEBHOOK-TOKEN" {
		t.Errorf("the destination's token came back as %q", got)
	}
}
