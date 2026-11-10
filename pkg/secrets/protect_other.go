//go:build !windows && !darwin

package secrets

import (
	"os"
	"path/filepath"
)

// On Linux and the rest, the key is a 0600 file next to the database.
//
// The honest limits: this keeps the key away from other user accounts and out
// of a copied monitoring.db, and that is all. It does not protect against
// another process running as the same user. Reaching the Secret Service would
// need a D-Bus dependency and still fails on a headless box with no keyring
// daemon — the common case for something left running as a collector — so the
// file is the predictable choice rather than the impressive one.
type fileProtector struct{ path string }

func newProtector(dir string) keyProtector {
	return &fileProtector{path: filepath.Join(dir, "sinks.key")}
}

// newMachineProtector is the same file: what keeps the service's key from
// other accounts is its directory, which only the service may enter.
func newMachineProtector(dir string) keyProtector { return newProtector(dir) }

func (p *fileProtector) name() string { return "encrypted-file" }

func (p *fileProtector) loadKey() ([]byte, error) {
	return os.ReadFile(p.path)
}

func (p *fileProtector) saveKey(key []byte) error {
	return writeKeyOnce(p.path, key)
}
