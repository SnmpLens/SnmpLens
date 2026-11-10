// Package dirlock claims a data directory for one process.
//
// One supervision engine per directory is not a convention, it is what keeps
// it whole: two would both poll every session into one monitoring.db, both
// route every event to the same destinations — every alert sent twice — and
// fight over the trap port. A window and a headless run, or a window and the
// system service, started on the same directory are exactly that case.
package dirlock

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrInUse means another process has the directory.
var ErrInUse = errors.New("another SnmpLens process is already using this data directory")

// FileName is the lock file in the directory. The lock is the operating
// system's, held by an open handle, so a process that dies releases it: the file
// left behind means nothing.
const FileName = "engine.lock"

// Lock claims dir for this process until release is called.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(dir, FileName))
}
