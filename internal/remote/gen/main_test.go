package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The client and the switch must follow the interfaces they implement. A method
// added to Host without regenerating fails to compile anyway; this catches a
// signature changed in a way that still compiles, and says what to run.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "backend.go"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := generate(src)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
			t.Errorf("%s is stale: run go generate ./internal/remote", name)
		}
	}
}
