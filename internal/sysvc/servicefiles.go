//go:build windows || linux

package sysvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// What only a running service keeps in its directories: the accounts allowed to
// drive it, and the status it publishes. Built where there is a service.

// operatorsFile lists the accounts allowed to drive the service from a window,
// in the service's own directory.
const operatorsFile = "operators.json"

func loadOperators(dir string) ([]Operator, error) {
	data, err := os.ReadFile(filepath.Join(dir, operatorsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ops []Operator
	if err := json.Unmarshal(data, &ops); err != nil {
		return nil, fmt.Errorf("read %s: %w", operatorsFile, err)
	}
	return ops, nil
}

func saveOperators(dir string, ops []Operator) error {
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, operatorsFile), data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", operatorsFile, err)
	}
	return nil
}

// writeStatus publishes the service's status for any local user to read.
func writeStatus(dir string, s Status) {
	if dir == "" {
		return
	}
	data, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(dir, statusFile), data, 0o644)
}
