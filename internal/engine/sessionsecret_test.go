package engine

import (
	"testing"
	"time"

	"SnmpLens/pkg/secrets"
)

// Deleting a monitoring session must take its credentials with it.
//
// A session's community and v3 passphrases live in pkg/secrets under
// SessionRef(id), deliberately NOT in monitoring.db — storage.SessionConn holds
// only what is safe to read in a copied database. Which is exactly why deleting
// the database row cannot be the whole of it: the row goes and the credential
// stays, under a key nothing in the app will ever look up again. The same
// defect NotifyDeleteSink had, in the other place that writes to pkg/secrets.
func TestDeletingASessionRemovesItsCredentials(t *testing.T) {
	eng := newTestApp(t)

	id, err := eng.storage.CreateSession("core switches", "1.3.6.1.2.1.2.2.1.10.1",
		[]string{"10.0.0.1"}, 1000, "v2c", time.Now().UTC().Format(time.RFC3339), nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := eng.secrets.Set(secrets.SessionRef(id), `{"community":"s3cret"}`); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := eng.MonitorDeleteSession(id); err != nil {
		t.Fatalf("MonitorDeleteSession: %v", err)
	}
	if got, _ := eng.secrets.Get(secrets.SessionRef(id)); got != "" {
		t.Errorf("the session's credentials outlived it: %q", got)
	}
}

// Retention deletes finished, empty sessions in bulk. That is the same defect
// at a larger scale, and the caller cannot fix it without being told which ids
// went — which is why Cleanup returns them.
func TestRetentionRemovesTheCredentialsOfTheSessionsItDeletes(t *testing.T) {
	eng := newTestApp(t)

	id, err := eng.storage.CreateSession("finished", "1.3.6.1.2.1.1.3.0",
		[]string{"10.0.0.2"}, 1000, "v2c", time.Now().UTC().Format(time.RFC3339), nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := eng.storage.UpdateSession(id, false, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if err := eng.secrets.Set(secrets.SessionRef(id), `{"community":"s3cret"}`); err != nil {
		t.Fatal(err)
	}

	// No data points were ever written, so retention removes the session.
	if _, err := eng.MonitorCleanup(1); err != nil {
		t.Fatalf("MonitorCleanup: %v", err)
	}

	sessions, err := eng.storage.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range sessions {
		if existing.ID == id {
			t.Fatal("the session was not removed; this test proves nothing")
		}
	}
	if got, _ := eng.secrets.Get(secrets.SessionRef(id)); got != "" {
		t.Errorf("retention removed the session and left its credentials: %q", got)
	}
}

// An ACTIVE session, or one that holds data, must survive retention — and so
// must its credentials. A cleanup that removes a running session's community
// is worse than one that leaves an orphan behind.
func TestRetentionLeavesLiveSessionsAndTheirCredentialsAlone(t *testing.T) {
	eng := newTestApp(t)

	live, err := eng.storage.CreateSession("running", "1.3.6.1.2.1.1.3.0",
		[]string{"10.0.0.3"}, 1000, "v2c", time.Now().UTC().Format(time.RFC3339), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.secrets.Set(secrets.SessionRef(live), `{"community":"keep-me"}`); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.MonitorCleanup(1); err != nil {
		t.Fatal(err)
	}

	sessions, err := eng.storage.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, existing := range sessions {
		if existing.ID == live {
			found = true
		}
	}
	if !found {
		t.Error("retention removed an active session")
	}
	if got, _ := eng.secrets.Get(secrets.SessionRef(live)); got == "" {
		t.Error("retention removed an active session's credentials")
	}
}
