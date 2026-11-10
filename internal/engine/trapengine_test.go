package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The trap listener's engine ID is made once and kept: devices are configured
// with it. A file that does not hold one is replaced, and the replacement kept.
func TestTheTrapListenersEngineIDIsKept(t *testing.T) {
	dir := t.TempDir()
	first := loadTrapEngineID(dir)
	if len(first) != 13 || !bytes.Equal(first[:5], []byte{0x80, 0, 0, 0, 5}) {
		t.Fatalf("%x is not RFC 3411's fifth format under enterprise 0", first)
	}
	if again := loadTrapEngineID(dir); !bytes.Equal(again, first) {
		t.Errorf("a second start made a new engine ID: %x, then %x", first, again)
	}
	if err := os.WriteFile(filepath.Join(dir, trapEngineFile), []byte("not an engine ID"), 0o600); err != nil {
		t.Fatal(err)
	}
	mended := loadTrapEngineID(dir)
	if len(mended) != 13 || bytes.Equal(mended, first) {
		t.Errorf("an unreadable file gave %x", mended)
	}
	if kept := loadTrapEngineID(dir); !bytes.Equal(kept, mended) {
		t.Errorf("the replacement was not kept: %x, then %x", mended, kept)
	}
}
