//go:build windows

package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the data-encryption key is sealed with DPAPI, bound to the current
// user account.
//
// This is mandatory, not belt-and-braces: os.WriteFile(path, data, 0o600)
// produces mode 0666 on Windows, because Go does not translate Unix permission
// bits into Windows ACLs. "The key file is 0600" is therefore not a protection
// story on this platform — DPAPI is.
type dpapiProtector struct{ path string }

func newProtector(dir string) keyProtector {
	return &dpapiProtector{path: filepath.Join(dir, "sinks.key")}
}

func (p *dpapiProtector) name() string { return "windows-dpapi" }

func (p *dpapiProtector) loadKey() ([]byte, error) {
	sealed, err := os.ReadFile(p.path)
	if err != nil {
		return nil, err
	}
	return unprotect(sealed)
}

func (p *dpapiProtector) saveKey(key []byte) error {
	sealed, err := protect(key)
	if err != nil {
		return err
	}
	return writeKeyOnce(p.path, sealed)
}

// entropy ties the sealed blob to this application, so another program running
// as the same user cannot unseal it by merely passing the bytes to DPAPI.
var entropy = []byte("SnmpLens/sink-secrets/v1")

func blobFrom(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func blobBytes(b windows.DataBlob) []byte {
	out := make([]byte, b.Size)
	copy(out, unsafe.Slice(b.Data, b.Size))
	return out
}

func protect(plain []byte) ([]byte, error) {
	in := blobFrom(plain)
	ent := blobFrom(entropy)
	var out windows.DataBlob
	// CRYPTPROTECT_UI_FORBIDDEN (0x1): never show a prompt; this may run with
	// no window at all.
	if err := windows.CryptProtectData(&in, nil, &ent, 0, nil, 0x1, &out); err != nil {
		return nil, fmt.Errorf("dpapi protect: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobBytes(out), nil
}

func unprotect(sealed []byte) ([]byte, error) {
	in := blobFrom(sealed)
	ent := blobFrom(entropy)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &ent, 0, nil, 0x1, &out); err != nil {
		return nil, fmt.Errorf("dpapi unprotect: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobBytes(out), nil
}
