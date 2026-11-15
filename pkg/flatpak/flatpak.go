// Package flatpak says whether SnmpLens runs inside a Flatpak sandbox.
//
// Three things the application does on Linux assume the host, and each is
// wrong in the sandbox without saying so: the updater replaces the running
// binary, which is read-only under /app; autostart writes an XDG entry under
// $XDG_CONFIG_HOME, which the sandbox points at ~/.var/app where no desktop
// looks; the system service installs a unit through systemctl and pkexec, which
// the sandbox cannot reach. Each asks here and takes the sandbox's way instead.
package flatpak

import (
	"os"
	"strings"
)

// AppID is the application ID the release bundle is built under
// (build/linux/flatpak). FLATPAK_ID wins when it is set: it is what the
// sandbox was actually started as.
const AppID = "com.snmplens.SnmpLens"

// infoFile is what flatpak puts at the root of every sandbox, and nothing else
// does: an environment variable can be inherited or set by hand, a file at / of
// a mount namespace the sandbox built cannot.
var infoFile = "/.flatpak-info"

// Sandboxed reports whether this process runs inside a Flatpak sandbox.
func Sandboxed() bool {
	_, err := os.Stat(infoFile)
	return err == nil
}

// ID is the application ID this sandbox was started as.
func ID() string {
	if id := strings.TrimSpace(os.Getenv("FLATPAK_ID")); id != "" {
		return id
	}
	return AppID
}
