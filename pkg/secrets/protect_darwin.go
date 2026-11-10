//go:build darwin

package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// On macOS the data-encryption key lives in the login Keychain, reached through
// the bundled `security` tool. Using the CLI rather than the Security framework
// keeps the build CGO-free, which matters because the release workflow builds
// darwin/universal.
type keychainProtector struct{ service, account string }

// securityTool is the ABSOLUTE path, deliberately.
//
// exec.Command with a bare name searches $PATH, and this is the one exec site
// in the application that puts a credential on a command line: whatever ran
// instead would be handed the data-encryption key as argv. `security` has
// shipped at this path since Mac OS X 10.0, and a machine where it is missing
// is one where the Keychain backend should fail rather than fall back to
// whatever else answers to the name.
//
// The exec sites in pkg/network resolve `traceroute` and friends through PATH
// too, and are a weaker version of the same thing: they pass a hostname, not a
// secret. This one is fixed because the consequence is different in kind.
const securityTool = "/usr/bin/security"

func newProtector(dir string) keyProtector {
	_ = dir
	return &keychainProtector{service: "SnmpLens", account: "sink-secrets"}
}

func (p *keychainProtector) name() string { return "macos-keychain" }

// keychainItemNotFound is what `security` exits with when the item has never
// been created. Every other non-zero status — a locked keychain, a denied
// prompt, no access to the login keychain — means the key exists and cannot be
// read now, which must NOT be answered by minting a new one over it.
const keychainItemNotFound = 44

func (p *keychainProtector) loadKey() ([]byte, error) {
	out, err := exec.Command(securityTool, "find-generic-password",
		"-s", p.service, "-a", p.account, "-w").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == keychainItemNotFound {
			return nil, errNoKeyYet
		}
		return nil, fmt.Errorf("keychain lookup: %w", err)
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func (p *keychainProtector) saveKey(key []byte) error {
	encoded := base64.StdEncoding.EncodeToString(key)
	// No -U: an item that exists is another process's key, minted at the same
	// moment, and updating it would leave that process sealing with a key
	// nobody keeps. The add fails instead (errSecDuplicateItem) and resolveKey
	// takes the key that was kept. The secret is passed with -w; `security` is
	// exec'd directly, never through a shell, so it never reaches a history.
	cmd := exec.Command(securityTool, "add-generic-password",
		"-s", p.service, "-a", p.account, "-w", encoded)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain store: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
