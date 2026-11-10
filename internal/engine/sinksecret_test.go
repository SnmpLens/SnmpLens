package engine

import (
	"strings"
	"testing"

	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
)

// Saving a credential with no store to put it in must FAIL, loudly.
//
// The guard read `if incoming != "" && eng.secrets != nil`, so with no secret
// store — a locked keychain, a config directory that cannot be written — the
// typed token was silently dropped and the save reported success. The operator
// sees the destination saved, believes the token is held, and finds out at 03:00
// when the webhook posts unauthenticated or the SMTP AUTH is refused.
func TestSavingASecretWithNoStoreIsRefused(t *testing.T) {
	eng := newTestApp(t)
	eng.secrets = nil // as at startup when the protector could not be opened

	_, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "TOP-SECRET-TOKEN",
	})
	if err == nil {
		t.Fatal("the save reported success and discarded the credential")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "credential") {
		t.Errorf("the error does not say the credential is the problem: %v", err)
	}
}

// With no credential typed there is nothing to store, so no store is needed.
// A sink that authenticates with nothing must stay savable.
func TestSavingWithoutASecretNeedsNoStore(t *testing.T) {
	eng := newTestApp(t)
	eng.secrets = nil

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "open relay", Kind: notify.SinkSyslog, Enabled: true,
		Syslog: notify.SyslogConfig{Address: "127.0.0.1:514", Protocol: "udp"},
	})
	if err != nil {
		t.Fatalf("a sink with no credential was refused: %v", err)
	}
	if saved.HasSecret {
		t.Error("HasSecret is true with no store and no secret")
	}
}

// Deleting a destination must take its credential with it.
//
// DeleteSink removed the row and left the secret in DPAPI, the Keychain or the
// file store forever — a bearer token or an SMTP password the operator believes
// they deleted, outliving the thing that named it, under a key nothing in the
// app will ever look up again.
func TestDeletingASinkRemovesItsCredential(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  "TOP-SECRET-TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if eng.sinkSecret(saved.ID) == "" {
		t.Fatal("the credential was never stored, so this proves nothing")
	}

	if err := eng.NotifyDeleteSink(saved.ID); err != nil {
		t.Fatalf("NotifyDeleteSink: %v", err)
	}

	if got, _ := eng.secrets.Get(secrets.SinkRef(saved.ID)); got != "" {
		t.Errorf("the credential outlived the destination: %q", got)
	}
}

// A sink that never had a credential must still delete cleanly: Delete on a ref
// that was never set must not be treated as a failure.
func TestDeletingASinkWithNoCredentialSucceeds(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "plain", Kind: notify.SinkSyslog, Enabled: true,
		Syslog: notify.SyslogConfig{Address: "127.0.0.1:514", Protocol: "udp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.NotifyDeleteSink(saved.ID); err != nil {
		t.Errorf("deleting a sink with no credential failed: %v", err)
	}
}

// And with no secret store at all, deleting the destination must still work —
// the row is the user's actual intent.
func TestDeletingASinkWithNoStoreStillRemovesIt(t *testing.T) {
	eng := newTestApp(t)
	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "plain", Kind: notify.SinkSyslog, Enabled: true,
		Syslog: notify.SyslogConfig{Address: "127.0.0.1:514", Protocol: "udp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.secrets = nil

	if err := eng.NotifyDeleteSink(saved.ID); err != nil {
		t.Errorf("the destination could not be deleted: %v", err)
	}
	sinks, err := eng.storage.ListSinks()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sinks {
		if s.ID == saved.ID {
			t.Error("the destination is still there")
		}
	}
}

// Deleting a destination must unbind it from every rule that named it.
//
// The id was left in each route's SinkIDs: the rule then rendered a raw UUID in
// the list — sinkName() falls back to the id — and every matching event queued
// a delivery nothing could resolve. A rule pointing at one live destination and
// one dead id looks like it works, and half of it does not.
func TestDeletingASinkUnbindsItFromRoutes(t *testing.T) {
	eng := newTestApp(t)

	gone, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "old", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://eng.example/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stays, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "kept", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://b.example/x"},
	})
	if err != nil {
		t.Fatal(err)
	}

	both, err := eng.storage.SaveRoute(notify.Route{
		Name: "both", Enabled: true, SinkIDs: []string{gone.ID, stays.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	only, err := eng.storage.SaveRoute(notify.Route{
		Name: "only the one being deleted", Enabled: true, SinkIDs: []string{gone.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.NotifyDeleteSink(gone.ID); err != nil {
		t.Fatal(err)
	}

	routes, err := eng.NotifyListRoutes()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]notify.Route{}
	for _, r := range routes {
		byID[r.ID] = r
	}

	for _, r := range routes {
		for _, id := range r.SinkIDs {
			if id == gone.ID {
				t.Errorf("route %q still names the deleted destination", r.Name)
			}
		}
	}
	if got := byID[both.ID].SinkIDs; len(got) != 1 || got[0] != stays.ID {
		t.Errorf("the surviving destination was lost from the rule: %v", got)
	}
	if !byID[both.ID].Enabled {
		t.Error("a rule that still has a destination was disabled")
	}

	// A rule left with nothing is disabled, not deleted: it carries the match
	// conditions, which are the part that took thought.
	left, ok := byID[only.ID]
	if !ok {
		t.Fatal("the rule was deleted along with its destination")
	}
	if left.Enabled {
		t.Error("a rule with no destinations left is still enabled, so it matches " +
			"events and delivers to nothing")
	}
}

// A list binding that could not be read must be reported, not answered with an
// empty configuration that reads as "you have none".
func TestListingWithNoDatabaseIsAnError(t *testing.T) {
	eng := &Engine{}

	if _, err := eng.NotifyListSinks(); err == nil {
		t.Error("NotifyListSinks answered an empty list and no error, which the " +
			"settings page renders as a fresh install")
	}
	if _, err := eng.NotifyListRoutes(); err == nil {
		t.Error("NotifyListRoutes answered an empty list and no error")
	}
	if _, err := eng.NotifyListDeliveries("", 10); err == nil {
		t.Error("NotifyListDeliveries answered an empty list and no error")
	}
}

// A syslog TLS sink that can never deliver must be refused at save time.
//
// NotifySaveSink gates the message template and the webhook payload precisely
// so a broken sink is not discovered at 03:00, and checked none of the TLS
// material. Measured, all four surface only at send and none is classified
// permanent, so every routed event retried six times and dead-lettered.
func TestASyslogSinkWithUnusableTLSMaterialIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		cfg    notify.SyslogConfig
		secret string
	}{
		{
			name: "a CA bundle that is not PEM",
			cfg: notify.SyslogConfig{
				Address: "collector.example.com:6514", Protocol: "tls",
				CACert: "this is not a certificate",
			},
		},
		{
			name: "a client key with no certificate",
			cfg: notify.SyslogConfig{
				Address: "collector.example.com:6514", Protocol: "tls",
			},
			secret: "-----BEGIN PRIVATE KEY-----\nnot really\n-----END PRIVATE KEY-----\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eng := newTestApp(t)
			_, err := eng.NotifySaveSink(notify.SinkConfig{
				Name: "collector", Kind: notify.SinkSyslog, Enabled: true,
				Syslog: c.cfg, Secret: c.secret,
			})
			if err == nil {
				t.Error("saved a destination that can never deliver; every routed " +
					"event will retry six times and dead-letter")
			}
		})
	}
}

// A syslog sink with no TLS at all, and one over plain TCP, must still save.
func TestAPlainSyslogSinkStillSaves(t *testing.T) {
	eng := newTestApp(t)
	for _, proto := range []string{"udp", "tcp"} {
		if _, err := eng.NotifySaveSink(notify.SinkConfig{
			Name: "collector " + proto, Kind: notify.SinkSyslog, Enabled: true,
			Syslog: notify.SyslogConfig{Address: "127.0.0.1:514", Protocol: proto},
		}); err != nil {
			t.Errorf("a %s syslog sink was refused: %v", proto, err)
		}
	}
}

// A rule whose source pattern matches nothing must be refused, not saved to
// deliver silence.
func TestARouteWithAMalformedSourcePatternIsRefused(t *testing.T) {
	eng := newTestApp(t)

	if _, err := eng.NotifySaveRoute(notify.Route{
		Name: "the whole estate", Enabled: true,
		Match: notify.RouteMatch{Sources: []string{"10.0.0/8"}},
	}); err == nil {
		t.Error("a rule whose source pattern matches nothing was accepted")
	}

	if _, err := eng.NotifySaveRoute(notify.Route{
		Name: "the whole estate", Enabled: true,
		Match: notify.RouteMatch{Sources: []string{"10.0.0.0/8", "sw-*"}},
	}); err != nil {
		t.Errorf("a correct rule was refused: %v", err)
	}
}
