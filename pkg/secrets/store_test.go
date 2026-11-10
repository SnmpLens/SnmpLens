package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStore(t *testing.T) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, dir
}

func TestRoundTrip(t *testing.T) {
	s, _ := newStore(t)

	if err := s.Set(SinkRef("abc"), "hunter2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(SinkRef("abc"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestMissingSecretIsDistinguishable(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Get(SinkRef("nope"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Clearing a password in the UI must remove it, not store an empty string that
// would later be sent as a credential.
func TestEmptySecretDeletes(t *testing.T) {
	s, _ := newStore(t)
	s.Set(SinkRef("abc"), "hunter2")
	if err := s.Set(SinkRef("abc"), ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if _, err := s.Get(SinkRef("abc")); !errors.Is(err, ErrNotFound) {
		t.Errorf("secret survived being cleared: %v", err)
	}
}

func TestOversizedSecretRejected(t *testing.T) {
	s, _ := newStore(t)
	err := s.Set(SinkRef("abc"), strings.Repeat("x", MaxSecretLen+1))
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
}

// The whole point: the secret must not be readable from the file on disk.
func TestSecretIsNotStoredInClear(t *testing.T) {
	s, dir := newStore(t)
	const secret = "s3cr3t-passphrase-not-in-clear"
	if err := s.Set(SinkRef("abc"), secret); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		found = true
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s contains the secret in clear", e.Name())
		}
	}
	if !found {
		t.Fatal("no file was written; the test proved nothing")
	}
}

// A second Open on the same directory must find the same key, or every restart
// would look like "my credentials vanished".
func TestSecretsSurviveReopen(t *testing.T) {
	dir := t.TempDir()

	first, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.Set(SinkRef("abc"), "keepme"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, err := second.Get(SinkRef("abc"))
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got != "keepme" {
		t.Errorf("got %q after reopen, want keepme", got)
	}
}

// A tampered or foreign store must fail loudly rather than silently look empty,
// which would read as "my credentials vanished" while they are merely
// unreadable.
func TestCorruptStoreIsReported(t *testing.T) {
	s, dir := newStore(t)
	s.Set(SinkRef("abc"), "hunter2")

	path := filepath.Join(dir, "sinks.secrets")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(SinkRef("abc")); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a corrupt store must be reported, got %v", err)
	}
}

func TestBackendIsNamed(t *testing.T) {
	s, _ := newStore(t)
	switch s.Backend() {
	case "windows-dpapi", "macos-keychain", "encrypted-file":
	default:
		t.Errorf("unexpected backend name %q", s.Backend())
	}
}

func TestSinkRefShape(t *testing.T) {
	if got := SinkRef("uuid-1"); got != "sink/uuid-1/secret" {
		t.Errorf("SinkRef = %q", got)
	}
}

// keyProtector stub whose failures we choose.
type flakyProtector struct {
	key     []byte
	loadErr error
	saves   int
	saveErr error
}

func (p *flakyProtector) name() string { return "flaky" }
func (p *flakyProtector) loadKey() ([]byte, error) {
	if p.loadErr != nil {
		return nil, p.loadErr
	}
	return p.key, nil
}
func (p *flakyProtector) saveKey(key []byte) error {
	p.saves++
	if p.saveErr != nil {
		return p.saveErr
	}
	p.key = key
	return nil
}

// A key that cannot be READ is not a key that is ABSENT.
//
// Open used to answer any loadKey error by generating a replacement and
// writing it over the old one. A locked keychain, a permissions blip, a
// profile restored under a different Windows user — one second of failure and
// every stored secret decrypts to "cannot decrypt the store", permanently.
func TestOpenDoesNotMintAKeyOverAnUnreadableOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"permission denied", os.ErrPermission},
		{"locked keychain", errors.New("keychain lookup: exit status 51")},
		{"dpapi refused", errors.New("unprotect: access denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &flakyProtector{key: []byte("the-real-key-nobody-may-replace!!"), loadErr: tc.err}

			if _, err := resolveKey(p); err == nil {
				t.Fatalf("%v was answered with a new key", tc.err)
			}
			if p.saves != 0 {
				t.Errorf("the key was rewritten %d time(s) despite being unreadable", p.saves)
			}
		})
	}
}

// First run must still work: absent means absent.
func TestOpenMintsAKeyOnlyWhenThereIsNone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.Set(SinkRef("a"), "hunter2"); err != nil {
		t.Fatal(err)
	}

	// Reopening finds the key and the secret.
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	got, err := again.Get(SinkRef("a"))
	if err != nil || got != "hunter2" {
		t.Fatalf("secret did not survive a reopen: %q %v", got, err)
	}
}

// The end-to-end consequence, on the real store: make the key unreadable and
// Open must refuse rather than start a new one and orphan the secrets.
func TestOpenRefusesWhenTheKeyIsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny the owner on Windows")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(SinkRef("a"), "hunter2"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var keyFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".key") {
			keyFile = filepath.Join(dir, e.Name())
		}
	}
	if keyFile == "" {
		t.Skip("this platform keeps its key outside the directory")
	}
	if err := os.Chmod(keyFile, 0o000); err != nil {
		t.Skip("cannot make the key unreadable here")
	}
	defer os.Chmod(keyFile, 0o600)

	if _, err := Open(dir); err == nil {
		t.Error("Open succeeded with an unreadable key; it has minted a new one over it")
	}
}

// Absent means absent: first run must still mint one, exactly once.
func TestResolveKeyMintsOnceWhenAbsent(t *testing.T) {
	p := &flakyProtector{loadErr: os.ErrNotExist}
	key, err := resolveKey(p)
	if err != nil {
		t.Fatalf("first run refused: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key is %d bytes", len(key))
	}
	if p.saves != 1 {
		t.Errorf("saved %d times, want 1", p.saves)
	}

	// Now it exists, and must be returned rather than replaced.
	p.loadErr = nil
	again, err := resolveKey(p)
	if err != nil || string(again) != string(key) {
		t.Errorf("the stored key was not reused: %v", err)
	}
	if p.saves != 1 {
		t.Errorf("saved again on a key that was present (%d)", p.saves)
	}
}

// macOS reports "no such item" as an exit status, which used to be
// indistinguishable from a locked keychain.
func TestKeychainNotFoundIsAMissingKeyAndNothingElseIs(t *testing.T) {
	if !isMissingKey(errNoKeyYet) {
		t.Error("errNoKeyYet is not recognised as a missing key")
	}
	for _, err := range []error{
		os.ErrPermission,
		errors.New("keychain lookup: exit status 51"),
		errors.New("secrets: cannot decrypt the store"),
	} {
		if isMissingKey(err) {
			t.Errorf("%v was treated as a missing key", err)
		}
	}
}

// raceProtector is a store another process minted a key in at the same moment:
// it had none when asked, and refuses this process's save because the other's
// is kept.
type raceProtector struct {
	asked  bool
	winner []byte
}

func (p *raceProtector) name() string { return "race" }
func (p *raceProtector) loadKey() ([]byte, error) {
	if !p.asked {
		p.asked = true
		return nil, errNoKeyYet
	}
	return p.winner, nil
}
func (p *raceProtector) saveKey([]byte) error { return fs.ErrExist }

// Two processes minting a key at once — the window and a headless supervision
// on a first run — end with ONE key: the loser takes the key that was kept
// rather than failing to open, or writing over a key the other already seals
// with.
func TestTheLoserOfAMintingRaceTakesTheKeptKey(t *testing.T) {
	p := &raceProtector{winner: []byte("the-key-the-other-process-kept!!")}
	key, err := resolveKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(p.winner) {
		t.Errorf("this process seals with %q while the store keeps %q", key, p.winner)
	}
}

// A key written once is not written over.
func TestAKeyFileIsWrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sinks.key")
	if err := writeKeyOnce(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeKeyOnce(path, []byte("second")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("a second write: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "first" {
		t.Errorf("the key file holds %q", got)
	}
	if left, _ := filepath.Glob(path + ".*.tmp"); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

// The service's store keeps what it is given across opens, as the user's does:
// the installer writes it, the service reads it.
func TestTheMachineStoreKeepsWhatItIsGiven(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMachine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(SessionRef("s1"), "community"); err != nil {
		t.Fatal(err)
	}
	again, err := OpenMachine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Get(SessionRef("s1")); err != nil || got != "community" {
		t.Fatalf("read back %q, %v", got, err)
	}
}
