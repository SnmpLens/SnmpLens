//go:build windows

package updater

import (
	"os"
	"path/filepath"
	"strings"
)

// pathUnder reports whether dir is base or a directory under it, on cleaned,
// lowercased paths with a separator boundary: a raw prefix match took
// "…\SnmpLens Portable" for "…\SnmpLens", and failed when InstallLocation
// carried a trailing separator that filepath.Dir does not.
func pathUnder(dir, base string) bool {
	if base == "" {
		return false
	}
	dir = strings.ToLower(filepath.Clean(dir))
	base = strings.ToLower(filepath.Clean(base))
	return dir == base || strings.HasPrefix(dir, base+string(os.PathSeparator))
}
