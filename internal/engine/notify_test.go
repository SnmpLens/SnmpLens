package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/storage"
)

// newTestApp wires just enough of App to exercise the notification bindings:
// the storage and the secret store, with no GUI and no Wails context.
func newTestApp(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()

	st, err := storage.Init(filepath.Join(dir, "monitoring.db"))
	if err != nil {
		t.Fatalf("storage.Init: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sec, err := secrets.Open(dir)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}

	return &Engine{storage: st, secrets: sec}
}

// Saving a sink must move the credential out of the configuration and into the
// secret store, and never hand it back.
func TestNotifySaveSinkMovesTheCredentialOut(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "TOP-SECRET-TOKEN",
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}
	if saved.Secret != "" {
		t.Error("the credential was handed back to the caller")
	}
	if !saved.HasSecret {
		t.Error("HasSecret is false, so the UI would show the sink as unconfigured")
	}
	if got := eng.sinkSecret(saved.ID); got != "TOP-SECRET-TOKEN" {
		t.Errorf("the credential did not reach the secret store: %q", got)
	}

	list, err := eng.NotifyListSinks()
	if err != nil {
		t.Fatalf("NotifyListSinks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sinks", len(list))
	}
	if list[0].Secret != "" || list[0].Webhook.Token != "" {
		t.Error("a credential came back through the listing")
	}
	if !list[0].HasSecret {
		t.Error("HasSecret is false in the listing")
	}
}

// Testing an ALREADY-SAVED sink must authenticate with its stored credential.
//
// It did not: notify.Build captures the secret by value, and the lookup ran
// after the sink had already been built with an empty one. The symptom was the
// confusing kind — the Test button failed while the real alerts arrived.
func TestNotifyTestSinkUsesTheStoredCredential(t *testing.T) {
	eng := newTestApp(t)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: srv.URL},
		Secret:  "STORED-TOKEN",
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}

	// The renderer never receives the secret, so this is exactly what the UI
	// sends when the user presses Test on a saved sink: no credential.
	if saved.Secret != "" {
		t.Fatal("this test is meaningless if the caller still holds the secret")
	}
	if err := eng.NotifyTestSink(saved); err != nil {
		t.Fatalf("NotifyTestSink: %v", err)
	}
	if gotAuth != "Bearer STORED-TOKEN" {
		t.Errorf("the test request authenticated with %q; the stored credential was not used", gotAuth)
	}
}

// A credential typed into the form but not yet saved must be used as-is, so
// the button can validate a new sink before it is created.
func TestNotifyTestSinkUsesAnUnsavedCredential(t *testing.T) {
	eng := newTestApp(t)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	err := eng.NotifyTestSink(notify.SinkConfig{
		Kind:    notify.SinkWebhook,
		Webhook: notify.WebhookConfig{URL: srv.URL},
		Secret:  "TYPED-TOKEN",
	})
	if err != nil {
		t.Fatalf("NotifyTestSink: %v", err)
	}
	if gotAuth != "Bearer TYPED-TOKEN" {
		t.Errorf("authenticated with %q, want the typed credential", gotAuth)
	}
}

// Clearing a credential must remove it, not leave the old one usable.
func TestNotifyClearSinkSecret(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "STORED-TOKEN",
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}
	if err := eng.NotifyClearSinkSecret(saved.ID); err != nil {
		t.Fatalf("NotifyClearSinkSecret: %v", err)
	}
	if got := eng.sinkSecret(saved.ID); got != "" {
		t.Errorf("the credential survived being cleared: %q", got)
	}
	list, _ := eng.NotifyListSinks()
	if len(list) == 1 && list[0].HasSecret {
		t.Error("the UI would still show a credential on file")
	}
}

// Saving a sink again without supplying a credential must keep the stored one,
// or every edit to an unrelated field would silently disarm the sink.
func TestNotifySaveSinkKeepsTheCredentialOnEdit(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "STORED-TOKEN",
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}

	saved.Name = "NOC renamed"
	saved.Secret = "" // the renderer never has the credential to send back
	again, err := eng.NotifySaveSink(saved)
	if err != nil {
		t.Fatalf("second NotifySaveSink: %v", err)
	}
	if !again.HasSecret {
		t.Error("HasSecret went false after an unrelated edit")
	}
	if got := eng.sinkSecret(saved.ID); got != "STORED-TOKEN" {
		t.Errorf("the credential was lost on edit: %q", got)
	}
}

// routeEvent renders per sink and groups identical results. Getting the group
// key wrong is not a performance bug: it would deliver a plain rendering to a
// sink whose whole purpose is to mask addresses.
func TestRouteEventKeepsRedactedAndTemplatedRenderingsApart(t *testing.T) {
	eng := newTestApp(t)

	mk := func(name string, redact bool, tpl notify.MessageTemplate) string {
		s, err := eng.NotifySaveSink(notify.SinkConfig{
			Name: name, Kind: notify.SinkWebhook, Enabled: true, Redact: redact,
			Webhook:  notify.WebhookConfig{URL: "https://hooks.example.com/" + name},
			Template: tpl,
		})
		if err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
		return s.ID
	}

	plainDefault := mk("plain", false, notify.MessageTemplate{})
	maskedDefault := mk("masked", true, notify.MessageTemplate{})
	plainCustom := mk("custom", false, notify.MessageTemplate{Subject: "S {{source}}", Body: "B {{source}}"})
	maskedCustom := mk("maskedCustom", true, notify.MessageTemplate{Subject: "S {{source}}", Body: "B {{source}}"})

	// One route per sink, matching everything.
	for _, id := range []string{plainDefault, maskedDefault, plainCustom, maskedCustom} {
		if _, err := eng.NotifySaveRoute(notify.Route{
			Name: "all-" + id, Enabled: true, SinkIDs: []string{id},
		}); err != nil {
			t.Fatalf("save route: %v", err)
		}
	}

	saved, err := eng.storage.InsertEvent(events.Event{
		Category: events.CategoryThreshold, Kind: events.KindThresholdOpened,
		Severity: "major", Source: "10.0.0.1",
		TitleKey: "events.kind." + events.KindThresholdOpened,
		Summary:  "ifInOctets above 900 on 10.0.0.1",
		DedupKey: "threshold|s1|10.0.0.1|1.3.6",
	}, "")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	eng.routeEvent(saved)

	deliveries, err := eng.storage.ListDeliveries("pending", 50)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 4 {
		t.Fatalf("got %d deliveries, want one per sink", len(deliveries))
	}

	bySink := map[string]storage.Delivery{}
	for _, d := range deliveries {
		bySink[d.SinkID] = d
	}

	// The two masking sinks must carry nothing real, in the rendering OR in
	// the event snapshot the webhook will serialise.
	for _, id := range []string{maskedDefault, maskedCustom} {
		d := bySink[id]
		for _, field := range []string{d.Subject, d.Body, d.Event.Source, d.Event.Summary, d.Event.DedupKey} {
			if strings.Contains(field, "10.0.0.1") {
				t.Errorf("sink %s masks, but %q went to the queue", id, field)
			}
		}
	}
	// ...and the two plain sinks must still carry the real one.
	for _, id := range []string{plainDefault, plainCustom} {
		if !strings.Contains(bySink[id].Body, "10.0.0.1") {
			t.Errorf("sink %s does not mask, but its body lost the address: %q", id, bySink[id].Body)
		}
	}
	// The custom templates must actually have been applied.
	if bySink[plainCustom].Subject != "S 10.0.0.1" {
		t.Errorf("the custom subject was not used: %q", bySink[plainCustom].Subject)
	}
	if bySink[maskedCustom].Subject != "S 10.x.x.1" {
		t.Errorf("the masked custom subject is wrong: %q", bySink[maskedCustom].Subject)
	}
	// And the default sinks must still get the built-in rendering.
	if !strings.HasPrefix(bySink[plainDefault].Subject, "[SnmpLens]") {
		t.Errorf("the default rendering changed: %q", bySink[plainDefault].Subject)
	}
}

// A template that cannot be saved cannot be routed with.
func TestNotifySaveSinkRefusesABrokenTemplate(t *testing.T) {
	eng := newTestApp(t)
	_, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook,
		Webhook:  notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Template: notify.MessageTemplate{Body: "{{nope}}"},
	})
	if err == nil {
		t.Fatal("a template naming an unknown variable was saved")
	}
	if !strings.Contains(err.Error(), "unknown variable") {
		t.Errorf("the error should name the problem: %v", err)
	}
}

// The preview is what tells an operator their template works before an
// incident does. It must render against an event with real params.
func TestNotifyPreviewTemplate(t *testing.T) {
	eng := newTestApp(t)

	p := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkEmail, Name: "NOC mail",
		Template: notify.MessageTemplate{
			Subject: "[{{severityUpper}}] {{source}}",
			Body:    "bound={{params.bound}} value={{value|n/a}}",
		},
	}, "threshold")

	if len(p.Errors) != 0 {
		t.Fatalf("a valid template previewed with errors: %+v", p.Errors)
	}
	if !strings.HasPrefix(p.Subject, "[MAJOR]") {
		t.Errorf("subject = %q", p.Subject)
	}
	if !strings.Contains(p.Body, "bound=900") {
		t.Errorf("the sample has no usable params: %q", p.Body)
	}

	masked := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkEmail, Name: "s", Redact: true,
		Template: notify.MessageTemplate{Body: "{{source}}"},
	}, "threshold")
	if strings.Contains(masked.Body, "10.0.0.1") {
		t.Errorf("the masked preview shows a real address: %q", masked.Body)
	}

	broken := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkEmail, Name: "s",
		Template: notify.MessageTemplate{Body: "{{#severity}}x"},
	}, "threshold")
	if len(broken.Errors) == 0 {
		t.Error("an unclosed block previewed without an error")
	}
}

// The preview must show what will actually be POSTed, because the plain and
// JSON renderings differ exactly where the mistakes live: escaping.
func TestPreviewShowsTheRealJSONPayload(t *testing.T) {
	eng := newTestApp(t)

	p := eng.NotifyPreviewTemplate(jsonSink(`{"text":"{{summary}}","sev":"{{severity}}"}`), "threshold")

	if !p.Json {
		t.Error("the preview does not know it is a JSON payload")
	}
	if !p.JsonValid {
		t.Errorf("a valid payload was reported invalid: %s / %s", p.JsonError, p.Body)
	}
	if p.Bytes != len(p.Body) {
		t.Errorf("bytes = %d, body is %d", p.Bytes, len(p.Body))
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(p.Body), &parsed); err != nil {
		t.Fatalf("the previewed body is not what it claims: %v", err)
	}
	if parsed["sev"] != "major" {
		t.Errorf("values were not rendered: %+v", parsed)
	}
}

// A broken payload must say so in the preview rather than at 03:00.
func TestPreviewReportsInvalidJSON(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(jsonSink(`{"unclosed": `), "threshold")
	if p.JsonValid {
		t.Fatal("invalid JSON previewed as valid")
	}
	if p.JsonError == "" {
		t.Error("no reason given")
	}
}

// The mode has to be stable: with nothing written yet it must still produce
// JSON, not the plain-text default posted to an endpoint expecting an object.
func TestEmptyTemplateInJSONModeStillProducesJSON(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(jsonSink(""), "threshold")
	if !p.JsonValid {
		t.Fatalf("an empty template produced non-JSON: %s\n%s", p.JsonError, p.Body)
	}
	var parsed map[string]string
	json.Unmarshal([]byte(p.Body), &parsed)
	if parsed["summary"] == "" || parsed["severity"] == "" {
		t.Errorf("the default payload is missing the basics: %+v", parsed)
	}
}

// Hostile content from a trap must not be able to break the payload, and the
// preview must show that it does not.
func TestPreviewEscapesHostileContent(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(jsonSink(`{"oid":"{{oid}}"}`), "trap")
	if !p.JsonValid {
		t.Errorf("a trap sample broke the payload: %s", p.JsonError)
	}
}

// Plain mode must be untouched by any of this.
func TestPreviewPlainModeIsUnchanged(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkEmail, Name: "s",
		Template: notify.MessageTemplate{Body: "{{summary}}"},
	}, "threshold")
	if p.Json || p.JsonValid || p.JsonError != "" {
		t.Errorf("plain mode reported JSON state: %+v", p)
	}
	if strings.Contains(p.Body, `\"`) {
		t.Errorf("plain mode was escaped: %q", p.Body)
	}
}

// jsonSink is a webhook that posts its template as the payload.
func jsonSink(body string) notify.SinkConfig {
	return notify.SinkConfig{
		Kind: notify.SinkWebhook, Name: "NOC",
		Webhook:  notify.WebhookConfig{URL: "https://hooks.example.com/x", PayloadMode: notify.PayloadTemplate},
		Template: notify.MessageTemplate{Body: body},
	}
}

// In envelope mode the preview must show the OBJECT the receiver gets, not the
// text inside one of its fields: the shape is what is being configured.
func TestPreviewShowsTheEnvelopeForAWebhook(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkWebhook, Name: "NOC",
		Webhook:  notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Template: notify.MessageTemplate{Body: "just some text"},
	}, "threshold")

	if !p.Json || !p.JsonValid {
		t.Fatalf("the envelope preview is not reported as JSON: %+v", p)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(p.Body), &env); err != nil {
		t.Fatalf("the preview is not the envelope: %v\n%s", err, p.Body)
	}
	if env["sender"] != "SnmpLens" {
		t.Errorf("envelope = %v", env)
	}
	if env["body"] != "just some text" {
		t.Errorf("the rendered text is not inside the envelope: %v", env["body"])
	}
}

// An email preview stays plain text: there is no envelope to show.
func TestPreviewStaysPlainForEmail(t *testing.T) {
	eng := newTestApp(t)
	p := eng.NotifyPreviewTemplate(notify.SinkConfig{
		Kind: notify.SinkEmail, Name: "s",
		Template: notify.MessageTemplate{Body: "{{summary}}"},
	}, "threshold")
	if p.Json {
		t.Errorf("an email preview claimed to be JSON: %+v", p)
	}
}
