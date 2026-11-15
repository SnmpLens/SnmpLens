package app

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"SnmpLens/pkg/flatpak"
	"SnmpLens/pkg/mib"
)

// SystemMibDirectory is a folder other software installed MIBs into.
type SystemMibDirectory struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

// systemMibDirectories are where net-snmp keeps its MIBs and looks for more:
// the package's own folder, the iana and ietf ones Debian's
// snmp-mibs-downloader fills, a /usr/local build's, and the per-user folder.
// macOS ships net-snmp, and its MIBs, at the first.
var systemMibDirectories = []string{
	"/usr/share/snmp/mibs",
	"/usr/share/snmp/mibs/iana",
	"/usr/share/snmp/mibs/ietf",
	"/usr/local/share/snmp/mibs",
	"~/.snmp/mibs",
}

// ListSystemMibDirectories lists the folders of systemMibDirectories that
// exist and hold files, as the MIB settings offer them. Added, a folder loads
// from where it is, like any other (pkg/mib/library.go): nothing is copied, so
// what the package manager updates is what is loaded.
//
// List…, so the screenshot bridge answers it with an empty list rather than
// null (tools/genbridge.mjs).
func (a *App) ListSystemMibDirectories() []SystemMibDirectory {
	if goruntime.GOOS == "windows" {
		return []SystemMibDirectory{}
	}
	home, _ := os.UserHomeDir()
	return findMibDirectories(systemMibDirectories, home, flatpak.Sandboxed())
}

// findMibDirectories resolves each candidate and keeps those with files. In a
// Flatpak the host's /usr is under /run/host (--filesystem=host-os), and the
// path given back is the one that loads there.
func findMibDirectories(candidates []string, home string, sandboxed bool) []SystemMibDirectory {
	found := []SystemMibDirectory{}
	for _, dir := range candidates {
		switch {
		case strings.HasPrefix(dir, "~/"):
			if home == "" {
				continue
			}
			dir = filepath.Join(home, dir[2:])
		case sandboxed && strings.HasPrefix(dir, "/usr/"):
			dir = "/run/host" + dir
		}
		files, err := mib.ListMibFiles(dir)
		if err != nil || len(files) == 0 {
			continue
		}
		found = append(found, SystemMibDirectory{Path: dir, Files: len(files)})
	}
	return found
}
