// Package backup is the file a configuration is saved to and restored from.
//
// It knows the format and nothing of what the sections mean: a backup is a ZIP
// holding a manifest, named JSON sections, files under a few fixed folders, and,
// when the person chose to include them, the secrets sealed under a passphrase.
// internal/app decides what goes in each section and what restoring one does.
//
// Two rules carry the safety, and both come from the file being something a
// person keeps and moves between machines.
//
// The secrets are never in the file in the clear. On the machine they are sealed
// with a key the operating system holds (pkg/secrets), and that key does not
// leave it; in the file they are sealed with a key derived from a passphrase,
// with Argon2id, and authenticated together with the manifest, so the secrets of
// one backup cannot be spliced into another. Without the passphrase everything
// else still restores.
//
// A backup is read the way the other files people hand to SnmpLens are read: it
// may have been written by anything. The archive's own directory is not trusted:
// entries are counted, each is read through a limit and what is unpacked is added
// up as it is read; a name is a folder this package knows and a plain file name,
// never a path.
package backup

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// Kind is what a backup says it is, first.
	Kind = "snmplens-backup"
	// FormatVersion is the version this package writes and the only one it reads.
	FormatVersion = 1
	// Extension is the file name extension dialogs offer.
	Extension = ".snmplens"

	// MinPassphrase is the shortest passphrase secrets are sealed under. The file
	// is meant to be copied around, so the passphrase is all that stands between
	// it and every credential SnmpLens holds.
	MinPassphrase = 10

	manifestName = "manifest.json"
	sealedName   = "secrets.sealed"
	sectionDir   = "sections/"
)

// The folders a backup may carry files in. Anything else in the archive is
// refused, not ignored: a backup is written by SnmpLens, and something else in
// it means it was not.
var folders = map[string]bool{
	"presets":          true,
	"assets":           true,
	"simulator-models": true,
	"mibs":             true,
}

// Bounds for a file somebody else may have written.
const (
	maxEntries    = 20000
	maxEntrySize  = 64 << 20
	maxTotalSize  = 512 << 20
	maxSealedSize = 16 << 20
)

// Manifest says what the file is, before anything in it is believed.
type Manifest struct {
	Kind          string `json:"kind"`
	FormatVersion int    `json:"formatVersion"`
	CreatedAt     string `json:"createdAt"`
	AppVersion    string `json:"appVersion"`
	// Secrets is "included" or "omitted", so nobody passes a file on believing it
	// holds none: the simulator's bench files say the same of themselves.
	Secrets string `json:"secrets"`
}

// Archive is a backup's content, with its secrets still sealed.
type Archive struct {
	Manifest Manifest
	// Sections are JSON documents by name: "renderer", "notify", and so on.
	Sections map[string]json.RawMessage
	// Files are by folder, then by file name.
	Files map[string]map[string][]byte
	// Sealed is the secrets as written, opened with Open and a passphrase.
	Sealed []byte

	manifestRaw []byte
}

// HasSecrets says whether the backup carries sealed secrets.
func (a *Archive) HasSecrets() bool { return len(a.Sealed) > 0 }

// Write writes a backup. secrets, when not nil, is sealed under passphrase; the
// manifest's Secrets field is set from that, whatever the caller put there.
func Write(w io.Writer, m Manifest, sections map[string]any, files map[string]map[string][]byte, secrets any, passphrase string) error {
	m.Kind, m.FormatVersion = Kind, FormatVersion
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	m.Secrets = "omitted"
	if secrets != nil {
		if len([]rune(passphrase)) < MinPassphrase {
			return fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
		}
		m.Secrets = "included"
	}
	manifestRaw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	z := zip.NewWriter(w)
	put := func(name string, data []byte) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := put(manifestName, manifestRaw); err != nil {
		return err
	}
	for _, name := range sortedKeys(sections) {
		if !validName(name) {
			return fmt.Errorf("section name %q", name)
		}
		raw, err := json.MarshalIndent(sections[name], "", "  ")
		if err != nil {
			return fmt.Errorf("section %s: %w", name, err)
		}
		if err := put(sectionDir+name+".json", raw); err != nil {
			return err
		}
	}
	for _, folder := range sortedKeys(files) {
		if !folders[folder] {
			return fmt.Errorf("folder %q", folder)
		}
		for _, name := range sortedKeys(files[folder]) {
			if !validName(name) {
				return fmt.Errorf("file name %q in %s", name, folder)
			}
			if err := put(folder+"/"+name, files[folder][name]); err != nil {
				return err
			}
		}
	}
	if secrets != nil {
		plain, err := json.Marshal(secrets)
		if err != nil {
			return err
		}
		sealed, err := seal(plain, passphrase, manifestRaw)
		if err != nil {
			return err
		}
		if err := put(sealedName, sealed); err != nil {
			return err
		}
	}
	return z.Close()
}

// Read reads a backup without opening its secrets.
func Read(r io.ReaderAt, size int64) (*Archive, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, errors.New("this is not a SnmpLens backup: it is not a ZIP archive")
	}
	if len(z.File) > maxEntries {
		return nil, fmt.Errorf("this backup holds %d entries, more than %d", len(z.File), maxEntries)
	}
	a := &Archive{Sections: map[string]json.RawMessage{}, Files: map[string]map[string][]byte{}}
	var total int64
	seen := map[string]bool{}
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "/") {
			continue // a directory entry carries nothing
		}
		if seen[f.Name] {
			return nil, fmt.Errorf("%s appears twice in this backup", f.Name)
		}
		seen[f.Name] = true
		limit := int64(maxEntrySize)
		if f.Name == sealedName {
			limit = maxSealedSize
		}
		data, err := readEntry(f, limit)
		if err != nil {
			return nil, err
		}
		if total += int64(len(data)); total > maxTotalSize {
			return nil, fmt.Errorf("this backup unpacks to more than %d MB", maxTotalSize>>20)
		}
		switch {
		case f.Name == manifestName:
			a.manifestRaw = data
		case f.Name == sealedName:
			a.Sealed = data
		case strings.HasPrefix(f.Name, sectionDir) && strings.HasSuffix(f.Name, ".json"):
			name := strings.TrimSuffix(strings.TrimPrefix(f.Name, sectionDir), ".json")
			if !validName(name) || !json.Valid(data) {
				return nil, fmt.Errorf("%s is not a section this backup can hold", f.Name)
			}
			a.Sections[name] = data
		default:
			folder, name, ok := strings.Cut(f.Name, "/")
			if !ok || !folders[folder] || !validName(name) {
				return nil, fmt.Errorf("%s is not something a SnmpLens backup holds", f.Name)
			}
			if a.Files[folder] == nil {
				a.Files[folder] = map[string][]byte{}
			}
			a.Files[folder][name] = data
		}
	}
	if a.manifestRaw == nil {
		return nil, errors.New("this is not a SnmpLens backup: it has no manifest")
	}
	if err := json.Unmarshal(a.manifestRaw, &a.Manifest); err != nil || a.Manifest.Kind != Kind {
		return nil, errors.New("this is not a SnmpLens backup")
	}
	if a.Manifest.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("this backup is in format %d, and this version of SnmpLens reads format %d",
			a.Manifest.FormatVersion, FormatVersion)
	}
	if (a.Manifest.Secrets == "included") != a.HasSecrets() {
		return nil, errors.New("this backup's manifest and its content disagree about its secrets")
	}
	return a, nil
}

// ErrPassphrase is a passphrase that does not open the secrets, or secrets that
// were altered: the two cannot be told apart, by design.
var ErrPassphrase = errors.New("the passphrase does not open this backup's secrets")

// Open unseals the backup's secrets into v.
func (a *Archive) Open(passphrase string, v any) error {
	if !a.HasSecrets() {
		return errors.New("this backup holds no secrets")
	}
	plain, err := unseal(a.Sealed, passphrase, a.manifestRaw)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// Section decodes a section into v, and reports whether the backup has it.
func (a *Archive) Section(name string, v any) (bool, error) {
	raw, ok := a.Sections[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return true, fmt.Errorf("section %s: %w", name, err)
	}
	return true, nil
}

// A name is one plain file name: no folder, no way up, nothing hidden.
func validName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.ContainsAny(name, `/\:`) &&
		name != "." && name != ".." && !strings.HasPrefix(name, ".") && path.Base(name) == name
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", f.Name, err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", f.Name, err)
	}
	if n > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", f.Name, limit>>20)
	}
	return buf.Bytes(), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- sealing ---

// Argon2id, at the second of the parameter sets RFC 9106 recommends (64 MiB,
// three passes): a few hundred milliseconds once, when a backup is written or
// restored, against every guess an attacker holding the file has to make.
const (
	kdfTime    = 3
	kdfMemory  = 64 * 1024
	kdfThreads = 4
)

type sealedSecrets struct {
	KDF     string `json:"kdf"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

func seal(plain []byte, passphrase string, manifest []byte) ([]byte, error) {
	s := sealedSecrets{KDF: "argon2id", Time: kdfTime, Memory: kdfMemory, Threads: kdfThreads,
		Salt: make([]byte, 16), Nonce: make([]byte, 12)}
	rand.Read(s.Salt)  // never fails since Go 1.24
	rand.Read(s.Nonce) // likewise
	gcm, err := cipherFor(passphrase, s)
	if err != nil {
		return nil, err
	}
	s.Data = gcm.Seal(nil, s.Nonce, plain, manifest)
	return json.Marshal(s)
}

func unseal(raw []byte, passphrase string, manifest []byte) ([]byte, error) {
	var s sealedSecrets
	if err := json.Unmarshal(raw, &s); err != nil || s.KDF != "argon2id" {
		return nil, errors.New("this backup's secrets are in a form this version cannot open")
	}
	// The parameters come from the file, so they are bounded: a file asking for
	// sixteen gigabytes of memory per guess is a way to stop the application.
	if s.Time < 1 || s.Time > 10 || s.Memory < 8*1024 || s.Memory > 1024*1024 || s.Threads < 1 ||
		len(s.Salt) < 16 || len(s.Nonce) != 12 {
		return nil, errors.New("this backup's secrets are sealed with parameters this version refuses")
	}
	gcm, err := cipherFor(passphrase, s)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Data, manifest)
	if err != nil {
		return nil, ErrPassphrase
	}
	return plain, nil
}

func cipherFor(passphrase string, s sealedSecrets) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), s.Salt, s.Time, s.Memory, s.Threads, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
