// Package secrets stores the credentials outbound sinks need (SMTP passwords,
// webhook tokens) somewhere a windowless dispatcher can still read them.
//
// It exists because the two obvious places are both wrong here. The renderer's
// localStorage encryption keeps its AES key as an extractable plaintext JWK
// beside the ciphertext it protects, and nothing in the webview is reachable
// from a background goroutine anyway. Putting the password in the sink config
// row means plaintext in monitoring.db.
//
// What this defends against: another user on the machine reading the file, and
// a copied or backed-up monitoring.db leaking credentials. What it does NOT
// defend against: another process running as the same user — on a desktop OS
// that process can ask the same keychain or call the same DPAPI unseal. No
// desktop credential store solves that, and pretending otherwise would be worse
// than saying it plainly.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// MaxSecretLen is a portable ceiling enforced in Go, not only in the UI.
const MaxSecretLen = 2048

var (
	// ErrNotFound means no secret is stored under that reference.
	ErrNotFound = errors.New("secrets: not found")

	// errNoKeyYet is what a protector returns when it has never held a key, as
	// distinct from holding one it cannot read right now. Only the first case
	// may mint a replacement.
	errNoKeyYet = errors.New("secrets: no key yet")
	// ErrTooLong means the secret exceeds MaxSecretLen.
	ErrTooLong = errors.New("secrets: secret is too long")
)

// Store holds credentials by opaque reference. Only references ever cross the
// Wails bridge or land in monitoring.db.
type Store interface {
	Get(ref string) (string, error)
	Set(ref, secret string) error
	Delete(ref string) error
	// Backend names the protection actually in use, so the settings UI can say
	// what is true on THIS machine rather than what was hoped for.
	Backend() string
}

// SinkRef is the reference shape for a notification sink's credential.
func SinkRef(sinkID string) string { return "sink/" + sinkID + "/secret" }

// SessionRef is the reference shape for a monitoring session's SNMP
// credentials. They are stored as one JSON blob rather than three entries
// because they are always read together, and because a partial read would
// produce a confusing "auth works, privacy does not" failure.
func SessionRef(sessionID string) string { return "session/" + sessionID + "/snmp" }

// TrapUsersRef holds the SNMPv3 users the trap listener accepts, as last
// applied from the renderer.
//
// It exists for background mode. The listener can be started at login, before
// any window and so before anything that could hand it the users, and without
// them it hears v1 and v2c and drops every v3 notification. Held here rather
// than in service.json because the passphrases are in it.
func TrapUsersRef() string { return "traps/local/usm" }

// SettingsKeyRef is the key the renderer's settings blob is sealed with.
//
// One ref with no id: there is exactly one renderer profile. The KEY lives
// here rather than the credentials themselves — the credentials stay in
// localStorage, sealed, and what changes is that nothing in that folder can
// open them any more.
func SettingsKeyRef() string { return "settings/local/key" }

// SimulatorDeviceRef is the reference shape for a simulated device's community
// and passphrases. The device's configuration file holds everything else and
// none of these, because a configuration file is what gets copied and shared.
func SimulatorDeviceRef(deviceID string) string { return "simulator/" + deviceID + "/secrets" }

// BackupRef is where the passphrase of a backup SnmpLens made on its own is kept,
// so that this machine can open it and nothing else needs to know it.
func BackupRef(name string) string { return "backup/" + name + "/passphrase" }

// keyProtector guards the data-encryption key. Each OS gets the strongest
// mechanism available without a new dependency.
type keyProtector interface {
	loadKey() ([]byte, error)
	saveKey(key []byte) error
	name() string
}

// fileStore keeps an AES-256-GCM sealed map on disk. The key itself is
// protected by the platform's keyProtector.
type fileStore struct {
	mu        sync.Mutex
	path      string
	protector keyProtector
	key       []byte
}

// Open selects the best available backend for dir (the app's config directory).
func Open(dir string) (Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: create %s: %w", dir, err)
	}
	// MkdirAll never TIGHTENS a directory that already exists, and this one
	// usually does: app.startup creates <config>/SnmpLens/mibs at 0755 first,
	// which creates the parent at 0755. So the 0700 above was decorative, and
	// the protection the file backend documents — and the settings banner
	// implies — was not in place on Linux. Chmod is a no-op on Windows, where
	// ACLs govern instead.
	if err := os.Chmod(dir, 0o700); err != nil {
		log.Printf("secrets: could not tighten %s to 0700: %v", dir, err)
	}
	s := &fileStore{
		path:      filepath.Join(dir, "sinks.secrets"),
		protector: newProtector(dir),
	}
	key, err := resolveKey(s.protector)
	if err != nil {
		return nil, err
	}
	s.key = key
	return s, nil
}

// resolveKey returns the protector's key, creating one ONLY when the
// protector has never held one.
//
// Anything else — a permissions problem, a locked keychain, a profile restored
// under a different Windows user — must not mint a replacement: it would
// overwrite the real key, and every stored secret would then fail to decrypt
// with "cannot decrypt the store". Unrecoverable, from a failure that may have
// lasted one second.
func resolveKey(p keyProtector) ([]byte, error) {
	key, err := p.loadKey()
	switch {
	case err == nil:
		return key, nil
	case isMissingKey(err):
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("secrets: generate key: %w", err)
		}
		if err := p.saveKey(key); err != nil {
			// Saving never replaces a key: another process — the window and a
			// headless supervision starting together on a first run, two test
			// binaries — can mint one at the same moment, and writing over it
			// would leave that process sealing with a key nobody keeps. The
			// loser's save fails instead, and it takes the key that was kept.
			if kept, lerr := p.loadKey(); lerr == nil {
				return kept, nil
			}
			return nil, fmt.Errorf("secrets: persist key: %w", err)
		}
		return key, nil
	}
	return nil, fmt.Errorf("secrets: read key: %w", err)
}

// isMissingKey reports whether the protector has no key yet, as opposed to
// having one it could not read.
//
// The distinction is the whole point: "not there" means first run, and
// anything else means do not touch what is there.
func isMissingKey(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNotFound) {
		return true
	}
	// The macOS protector shells out to `security`, which reports a missing
	// item as an exit status rather than as os.ErrNotExist.
	return errors.Is(err, errNoKeyYet)
}

// Backend reports the key protection in effect.
func (s *fileStore) Backend() string { return s.protector.name() }

func (s *fileStore) load() (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]string{}, nil
	}

	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("secrets: store is truncated")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// A wrong key or a tampered file. Refuse rather than silently starting
		// empty, which would look like "my credentials vanished".
		return nil, fmt.Errorf("secrets: cannot decrypt the store: %w", err)
	}

	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *fileStore) save(values map[string]string) error {
	plain, err := json.Marshal(values)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)

	// Write to a temporary file and rename: a crash mid-write must not leave a
	// half-written store that no longer decrypts.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the secret stored under ref.
func (s *fileStore) Get(ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := values[ref]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set stores a secret. An empty secret deletes it, so clearing a password in
// the UI actually removes it rather than storing "".
func (s *fileStore) Set(ref, secret string) error {
	if len(secret) > MaxSecretLen {
		return ErrTooLong
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	if secret == "" {
		delete(values, ref)
	} else {
		values[ref] = secret
	}
	return s.save(values)
}

// Delete removes a secret.
func (s *fileStore) Delete(ref string) error {
	return s.Set(ref, "")
}

// writeKeyOnce writes data to path unless something is there already: through
// a temporary file of its own and a hard link, which fails when path exists
// rather than replacing it. Where hard links are not to be had — a FAT volume —
// it renames, as it always did.
func writeKeyOnce(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		return os.Rename(name, path)
	}
	return nil
}
