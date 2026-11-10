package storage

import (
	"errors"
	"fmt"
	"os"
)

// SnapshotTo writes a consistent copy of the database to path, which must not
// exist yet: what the system service is handed of the window's history when it
// is installed, and what the window takes back when it is removed. The samples
// waiting in the batch are written first, and VACUUM INTO is one read
// transaction, so writes go on meanwhile and the copy is whole.
func (s *Storage) SnapshotTo(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists already", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.flushBatch()
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("copy the monitoring database: %w", err)
	}
	return nil
}
