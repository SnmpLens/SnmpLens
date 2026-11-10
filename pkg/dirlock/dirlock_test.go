package dirlock

import (
	"errors"
	"testing"
)

// A directory is held by one claim at a time, and free again once released.
func TestADirectoryIsHeldByOneClaim(t *testing.T) {
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); !errors.Is(err, ErrInUse) {
		t.Fatalf("a second claim: %v", err)
	}
	release()
	again, err := Lock(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}
