package backup

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

type testSecrets struct {
	Sinks map[string]string `json:"sinks"`
}

func write(t *testing.T, secrets any, passphrase string) []byte {
	t.Helper()
	var buf bytes.Buffer
	err := Write(&buf, Manifest{AppVersion: "v1.2.3"},
		map[string]any{"notify": map[string]any{"sinks": []string{"a"}}},
		map[string]map[string][]byte{"presets": {"switch.json": []byte(`{"formatVersion":1}`)}},
		secrets, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, raw []byte) *Archive {
	t.Helper()
	a, err := Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestABackupReadsBackAsWritten(t *testing.T) {
	a := read(t, write(t, testSecrets{Sinks: map[string]string{"s1": "TOKEN"}}, "correct horse battery"))
	if a.Manifest.Kind != Kind || a.Manifest.AppVersion != "v1.2.3" || a.Manifest.Secrets != "included" {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	var notify struct{ Sinks []string }
	if ok, err := a.Section("notify", &notify); !ok || err != nil || len(notify.Sinks) != 1 {
		t.Fatalf("section: ok=%v err=%v %+v", ok, err, notify)
	}
	if string(a.Files["presets"]["switch.json"]) != `{"formatVersion":1}` {
		t.Fatalf("files = %v", a.Files)
	}
	var s testSecrets
	if err := a.Open("correct horse battery", &s); err != nil || s.Sinks["s1"] != "TOKEN" {
		t.Fatalf("open: %v %+v", err, s)
	}
}

func TestTheSecretsAreNeverInTheFileInTheClear(t *testing.T) {
	raw := write(t, testSecrets{Sinks: map[string]string{"s1": "TOKEN-IN-THE-CLEAR"}}, "correct horse battery")
	z, _ := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	for _, f := range z.File {
		data, _ := readEntry(f, maxEntrySize)
		if bytes.Contains(data, []byte("TOKEN-IN-THE-CLEAR")) {
			t.Fatalf("%s holds the secret in the clear", f.Name)
		}
	}
}

func TestAWrongPassphraseOpensNothing(t *testing.T) {
	a := read(t, write(t, testSecrets{Sinks: map[string]string{"s1": "TOKEN"}}, "correct horse battery"))
	var s testSecrets
	if err := a.Open("wrong horse battery", &s); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v", err)
	}
}

// The secrets are authenticated with the manifest, so they cannot be moved into
// another backup and opened there.
func TestSecretsSplicedIntoAnotherBackupDoNotOpen(t *testing.T) {
	one := read(t, write(t, testSecrets{Sinks: map[string]string{"s1": "TOKEN"}}, "correct horse battery"))
	var buf bytes.Buffer
	if err := Write(&buf, Manifest{AppVersion: "v9"}, nil, nil, testSecrets{}, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	other := read(t, buf.Bytes())
	other.Sealed = one.Sealed
	var s testSecrets
	if err := other.Open("correct horse battery", &s); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("spliced secrets opened: err = %v", err)
	}
}

func TestAShortPassphraseIsRefused(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, Manifest{}, nil, nil, testSecrets{}, "short"); err == nil {
		t.Fatal("a five-character passphrase was accepted")
	}
}

func TestABackupWithoutSecretsSaysSo(t *testing.T) {
	a := read(t, write(t, nil, ""))
	if a.Manifest.Secrets != "omitted" || a.HasSecrets() {
		t.Fatalf("manifest = %+v, sealed = %d bytes", a.Manifest, len(a.Sealed))
	}
}

// A backup may have been written by anything: names are plain file names in a
// folder this package knows, and nothing else gets in.
func TestWhatAForeignArchiveHoldsIsRefused(t *testing.T) {
	for _, name := range []string{
		"presets/../../monitoring.db", "../evil", "mibs/sub/X-MIB", "secrets.key",
		"presets/.hidden", `assets/a\b.png`, "sections/../x.json",
	} {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		w, _ := z.Create(manifestName)
		w.Write([]byte(`{"kind":"snmplens-backup","formatVersion":1,"secrets":"omitted"}`))
		w, _ = z.Create(name)
		w.Write([]byte("x"))
		z.Close()
		if _, err := Read(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestOnlyThisFormatIsRead(t *testing.T) {
	for manifest, want := range map[string]string{
		`{"kind":"something-else","formatVersion":1}`:                       "not a SnmpLens backup",
		`{"kind":"snmplens-backup","formatVersion":2}`:                      "format 2",
		`{"kind":"snmplens-backup","formatVersion":1,"secrets":"included"}`: "disagree",
	} {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		w, _ := z.Create(manifestName)
		w.Write([]byte(manifest))
		z.Close()
		_, err := Read(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", manifest, err, want)
		}
	}
	if _, err := Read(bytes.NewReader([]byte("PK not really")), 13); err == nil {
		t.Error("a file that is not a ZIP was read")
	}
}

// The KDF parameters come from the file, so they are bounded.
func TestSealingParametersFromTheFileAreBounded(t *testing.T) {
	raw := []byte(`{"kdf":"argon2id","time":3,"memory":16777216,"threads":4,"salt":"AAAAAAAAAAAAAAAAAAAAAA==","nonce":"AAAAAAAAAAAAAAAA","data":""}`)
	if _, err := unseal(raw, "correct horse battery", nil); err == nil || errors.Is(err, ErrPassphrase) {
		t.Fatalf("sixteen gigabytes per guess was attempted: %v", err)
	}
}
