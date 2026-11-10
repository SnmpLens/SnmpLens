package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
)

// A sink credential is write-only from the renderer: NotifyListSinks blanks
// Secret and the form shows "configured" from HasSecret without ever receiving
// the value. Two methods resolved that credential BY SINK ID while taking the
// destination from whatever the caller sent, which turned the pair into a read
// primitive over the store — name an existing sink's id, point the URL at your
// own server, collect the bearer token.
//
// This is the measurement, not the argument: a real receiver on a real socket,
// recording every Authorization header it is sent.

type collector struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) saw(secret string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.auth {
		if strings.Contains(a, secret) {
			return true
		}
	}
	return false
}

func (c *collector) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.auth)
}

const stolen = "TOP-SECRET-TOKEN"

func TestTestSinkWillNotSendAStoredCredentialElsewhere(t *testing.T) {
	eng := newTestApp(t)
	own := newCollector(t)
	attacker := newCollector(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: own.URL},
		Secret:  stolen,
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}

	// The attack: the sink's own id, somebody else's URL, no credential
	// supplied — exactly what the renderer can construct without ever being
	// able to READ the credential.
	err = eng.NotifyTestSink(notify.SinkConfig{
		ID: saved.ID, Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: attacker.URL},
	})
	if err == nil {
		t.Fatal("the test was accepted against a destination the credential was not stored for")
	}
	if attacker.saw(stolen) {
		t.Fatalf("the credential was delivered to the attacker: %v", attacker.auth)
	}
	if attacker.requests() != 0 {
		t.Errorf("the attacker received %d request(s); it should never have been contacted", attacker.requests())
	}
	// The refusal has to tell the operator what to do, because it fires on the
	// legitimate case too — editing a URL and pressing Test before saving.
	if !strings.Contains(strings.ToLower(err.Error()), "credential") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// The control: testing the destination the credential WAS stored for still
// authenticates. Without this, the fix could be "never resolve anything",
// which would break the one button an operator presses to check a sink.
func TestTestSinkStillAuthenticatesToItsOwnDestination(t *testing.T) {
	eng := newTestApp(t)
	own := newCollector(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: own.URL},
		Secret:  stolen,
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}

	if err := eng.NotifyTestSink(notify.SinkConfig{
		ID: saved.ID, Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: own.URL},
	}); err != nil {
		t.Fatalf("testing the sink's own destination was refused: %v", err)
	}
	if !own.saw(stolen) {
		t.Fatalf("the credential did not reach its own destination: %v", own.auth)
	}
}

// A sink that has not been saved yet has no stored credential to hand out, and
// must still be testable with one the operator just typed.
func TestTestSinkWorksForAnUnsavedSink(t *testing.T) {
	eng := newTestApp(t)
	dest := newCollector(t)

	if err := eng.NotifyTestSink(notify.SinkConfig{
		Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: dest.URL},
		Secret:  "typed-in-the-form",
	}); err != nil {
		t.Fatalf("testing a new sink was refused: %v", err)
	}
	if !dest.saw("typed-in-the-form") {
		t.Fatalf("the typed credential did not arrive: %v", dest.auth)
	}
}

// The durable half of the same defect: the SAVE path never compared the
// address it was given with the one the credential was stored against, so a
// renderer that cannot read a credential could still aim it. Pointing a sink
// somewhere new unbinds the credential.
func TestSavingANewDestinationUnbindsTheStoredCredential(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x"},
		Secret:  stolen,
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}
	if !saved.HasSecret {
		t.Fatal("the credential was not stored in the first place")
	}

	moved, err := eng.NotifySaveSink(notify.SinkConfig{
		ID: saved.ID, Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://attacker.example/collect"},
	})
	if err != nil {
		t.Fatalf("the save was refused rather than unbinding: %v", err)
	}
	if moved.HasSecret {
		t.Fatal("the credential followed the destination to a new address")
	}
	if got, _ := eng.secrets.Get(secrets.SinkRef(saved.ID)); got != "" {
		t.Fatalf("the credential is still in the store: %q", got)
	}
}

// Editing anything else — the name, the template, the timeout — must leave the
// credential alone, or every save becomes a re-entry of the password.
func TestSavingAnUnrelatedChangeKeepsTheCredential(t *testing.T) {
	eng := newTestApp(t)

	saved, err := eng.NotifySaveSink(notify.SinkConfig{
		Name: "NOC", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x", Timeout: 10},
		Secret:  stolen,
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}

	again, err := eng.NotifySaveSink(notify.SinkConfig{
		ID: saved.ID, Name: "NOC renamed", Kind: notify.SinkWebhook, Enabled: true,
		Webhook: notify.WebhookConfig{URL: "https://hooks.example.com/x", Timeout: 30},
	})
	if err != nil {
		t.Fatalf("NotifySaveSink: %v", err)
	}
	if !again.HasSecret {
		t.Fatal("an unrelated edit dropped the credential")
	}
}

// What counts as the destination, per kind. Each pair differs in exactly one
// field, and these are the comparisons both paths above rely on.
func TestDestinationDistinguishesWhatItMust(t *testing.T) {
	wh := func(url string) notify.SinkConfig {
		return notify.SinkConfig{Kind: notify.SinkWebhook, Webhook: notify.WebhookConfig{URL: url}}
	}
	em := func(host string, port int, enc string) notify.SinkConfig {
		return notify.SinkConfig{Kind: notify.SinkEmail,
			Email: notify.EmailConfig{Host: host, Port: port, Encryption: enc}}
	}
	sl := func(proto, addr string) notify.SinkConfig {
		return notify.SinkConfig{Kind: notify.SinkSyslog,
			Syslog: notify.SyslogConfig{Protocol: proto, Address: addr}}
	}

	cases := []struct {
		name string
		a, b notify.SinkConfig
		want bool
	}{
		{"a different webhook host is a different destination", wh("https://a.example/x"), wh("https://b.example/x"), false},
		{"a different path is too", wh("https://a.example/x"), wh("https://a.example/y"), false},
		{"https to http is a downgrade, not the same place", wh("https://a.example/x"), wh("http://a.example/x"), false},
		{"surrounding whitespace is not a move", wh("https://a.example/x"), wh("  https://a.example/x  "), true},
		{"a placeholder URL is compared UNSUBSTITUTED", wh("https://hooks.slack.com/{{secret}}"), wh("https://hooks.slack.com/{{secret}}"), true},
		{"a different SMTP host", em("mail.a", 587, "starttls"), em("mail.b", 587, "starttls"), false},
		{"a different port", em("mail.a", 587, "starttls"), em("mail.a", 465, "starttls"), false},
		{"dropping encryption puts the password on the wire in the clear", em("mail.a", 587, "starttls"), em("mail.a", 587, "none"), false},
		{"host case does not matter", em("Mail.A", 587, "starttls"), em("mail.a", 587, "starttls"), true},
		{"a different collector", sl("tls", "a:6514"), sl("tls", "b:6514"), false},
		{"a different transport", sl("tls", "a:6514"), sl("tcp", "a:6514"), false},
		{"the same collector", sl("TLS", "A:6514"), sl("tls", "a:6514"), true},
	}
	for _, c := range cases {
		if got := notify.SameDestination(c.a, c.b); got != c.want {
			t.Errorf("%s: SameDestination = %v, want %v", c.name, got, c.want)
		}
	}

	// A kind nothing can build binds to nothing, including itself — otherwise
	// two unknown kinds would compare equal on two empty strings.
	unknown := notify.SinkConfig{Kind: "carrier-pigeon"}
	if _, ok := notify.Destination(unknown); ok {
		t.Error("an unknown kind reported a destination")
	}
	if notify.SameDestination(unknown, unknown) {
		t.Error("an unknown kind matched itself")
	}
}

// Every kind Build accepts must have a destination, or a sink added later gets
// the old behaviour by default: a credential bound to nothing.
func TestEveryBuildableKindHasADestination(t *testing.T) {
	for _, kind := range []string{notify.SinkSyslog, notify.SinkWebhook, notify.SinkEmail} {
		cfg := notify.SinkConfig{Kind: kind}
		if _, ok := notify.Build(cfg, ""); !ok {
			t.Fatalf("%s is no longer buildable; this test is out of date", kind)
		}
		if _, ok := notify.Destination(cfg); !ok {
			t.Errorf("%s can be built but has no destination", kind)
		}
	}
}
