package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// updaterPublicKeys are the base64-encoded Ed25519 public keys a release
// manifest may be signed with — newest first. Their private counterparts sign
// SnmpLens-checksums.txt in the release workflow; generate a pair with
// `go run ./tools/updatersign keygen` and store the private half in the GitHub
// secret UPDATER_PRIVATE_KEY.
//
// A LIST, and not one key, because of the order a rotation has to happen in. A
// copy already installed trusts exactly the keys embedded in the binary it is
// running, so a release signed with a key it has never heard of is refused —
// including the release that would have introduced that key. The way through is
// therefore:
//
//  1. add the new key here, and publish that release SIGNED WITH THE OLD ONE,
//     so every copy can install it;
//  2. wait for it to be adopted — this is the whole safety of the operation,
//     and nothing but time provides it;
//  3. point UPDATER_PRIVATE_KEY at the new key. Copies that took step 1 accept
//     it; copies that skipped it cannot update again and must be reinstalled by
//     hand;
//  4. later, drop the retired key from this list.
//
// Every entry is a key that may install software on someone's machine, so the
// list holds what a transition needs and nothing more: a key stays only while
// there are copies that trust nothing else.
//
// While the list is empty, downloads are still integrity-checked with SHA-256
// but NOT authenticated. (A var rather than a const so the empty-default branch
// is not flagged as dead code before a key is configured.)
var updaterPublicKeys = []string{
	// From the rotation of 2026-09, and IN SERVICE since v1.6.1: the key CI
	// signs with. Step 3 happened when v1.6.0 had had its week of adoption.
	"TPh8H92qyRkMFpc1CXCJKp/G5jf4OTqlHd6Mx0BHljs=",
	// The original key. It signed every release up to and including v1.6.0, the
	// transition one, and nothing after it. It is kept as the way BACK: while it
	// is here, a release can still be signed with it and every copy accepts it,
	// which is what makes the switch above reversible. Step 4 — dropping this
	// entry — closes that door, and is due once v1.6.1 has been adopted in its
	// turn.
	"svOWbkIuFQTiebt+DKzaohFFdeANV8NjcdX3cQiybPw=",
}

// signatureEnforced reports whether any public key is configured, in which case
// a valid signature on the checksums manifest is mandatory.
func signatureEnforced() bool {
	for _, key := range updaterPublicKeys {
		if strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}

// VerifySignature is exported because the release workflow verifies with THIS
// function rather than a second implementation: a pipeline that checks
// differently from the application can publish a release the application then
// refuses, which is the one failure the signature exists to prevent.
//
// VerifySignature checks that sigBase64 is a valid Ed25519 signature of
// manifest under ONE of the embedded public keys.
//
// A malformed entry is passed over rather than fatal: it must not be able to
// refuse a manifest the next key would have accepted, and a list where no entry
// is usable at all is the one case that reads as a broken build.
func VerifySignature(manifest, sigBase64 []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigBase64)))
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}

	usable := 0
	for _, encoded := range updaterPublicKeys {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" {
			continue
		}
		pub, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		usable++
		if ed25519.Verify(ed25519.PublicKey(pub), manifest, sig) {
			return nil
		}
	}
	if usable == 0 {
		return errors.New("invalid embedded updater public key")
	}
	return errors.New("checksums signature verification failed")
}

// manifestVersionLine is the first line the release workflow writes into the
// checksums manifest: `version v1.2.3`.
const manifestVersionPrefix = "version "

// checkManifestVersion refuses a manifest that is not the one for `want`.
//
// The signature answers "did we sign this?" and nothing else. It cannot answer
// "is this the manifest for the release being installed?", because an OLD
// manifest is signed just as validly as a new one — so anyone able to serve
// what the app fetches can hand back a previous release's manifest and its
// binaries, and the app would verify both perfectly and install an older,
// possibly vulnerable, version. Binding the manifest to its tag is what closes
// that; nothing else in the chain does.
//
// A manifest without the line is REFUSED rather than tolerated, because
// tolerating it is the replay: an attacker replaying a manifest from before
// this existed would simply be waved through. Only releases published by this
// workflow are ever installed, and the updater refuses to move backwards, so
// there is no case where a legitimate update carries a manifest that predates
// the line.
func checkManifestVersion(manifest []byte, want string) error {
	first, _, _ := strings.Cut(strings.TrimSpace(string(manifest)), "\n")
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, manifestVersionPrefix) {
		return fmt.Errorf("checksums manifest is not bound to a release; refusing it")
	}
	got := strings.TrimSpace(strings.TrimPrefix(first, manifestVersionPrefix))
	if got != want {
		return fmt.Errorf("checksums manifest is for %s, not %s; refusing a replayed manifest", got, want)
	}
	return nil
}
