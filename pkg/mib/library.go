package mib

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The MIB folder and the folders a person added in the settings, as one set of
// files.
//
// The added folders were listed in the settings, with a switch per file, and
// loaded nothing: the renderer sent their files' NAMES, and every name was
// looked up in the MIB folder — where the file was not, or where a different
// file of that name was. gosmi is now given every folder at once, through the
// same mibFS, by name: the MIB folder first, then each added folder in order.
// A name already taken by an earlier folder is shadowed: the first folder's
// file is the one loaded under it (Shadowed lists them).
//
// An added folder is the person's, and so is whatever else it holds. What
// gosmi is shown from it passes the import gate first (ClassifyImport): a file
// that is not a MIB — a PDF, a zip, a key — is never parsed, and so never
// quoted back in a load error.

type library struct {
	// primary is the MIB folder: the editor's, the import's, the one named in
	// gosmi's messages.
	primary string
	// files maps a name to the file it is, the first folder holding it winning.
	files map[string]string
	// order lists the names, the MIB folder's first.
	order []string
	// shadowed maps a file of an added folder to the file loaded under its
	// name instead.
	shadowed map[string]string
}

// libraryGateBytes is how much of a file in an added folder is read to decide
// whether it is a MIB: the head says, and a MIB folder can be large.
const libraryGateBytes = 256 << 10

func newLibrary(primary string, extra []string) *library {
	l := &library{primary: filepath.Clean(primary), files: map[string]string{}, shadowed: map[string]string{}}
	l.addFolder(l.primary, false)
	for _, dir := range extra {
		l.addFolder(dir, true)
	}
	return l
}

func (l *library) addFolder(dir string, gated bool) {
	names, err := ListMibFiles(dir)
	if err != nil {
		return
	}
	for _, name := range names {
		path, err := SafeMibPath(dir, name)
		if err != nil {
			continue
		}
		if gated && !looksLikeMib(path) {
			continue
		}
		if first, taken := l.files[name]; taken {
			l.shadowed[path] = first
			continue
		}
		l.files[name] = path
		l.order = append(l.order, name)
	}
}

// looksLikeMib is the import gate, for a file gosmi is about to be shown.
func looksLikeMib(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(f, libraryGateBytes))
	if err != nil {
		return false
	}
	_, _, _, reject := ClassifyImport(head)
	return !reject
}

// path is the file a name stands for.
func (l *library) path(name string) (string, bool) {
	if name != filepath.Base(name) {
		return "", false
	}
	p, ok := l.files[name]
	return p, ok
}

// SetDirectories sets the folders the person added, beside the MIB folder,
// and answers with the ones taken: existing absolute directories, each once,
// never the MIB folder itself.
func (s *Service) SetDirectories(dirs []string) []string {
	taken := []string{}
	seen := map[string]bool{filepath.Clean(s.path): true}
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		clean := filepath.Clean(d)
		if !filepath.IsAbs(clean) || seen[clean] {
			continue
		}
		if info, err := os.Stat(clean); err != nil || !info.IsDir() {
			continue
		}
		seen[clean] = true
		taken = append(taken, clean)
	}
	s.mu.Lock()
	s.extra = taken
	s.mu.Unlock()
	return append([]string(nil), taken...)
}

// library is the set of files this service loads from, as the folders stand now.
func (s *Service) library() *library {
	s.mu.Lock()
	extra := append([]string(nil), s.extra...)
	s.mu.Unlock()
	return newLibrary(s.path, extra)
}

// FilePath is the file a name of the library stands for: the MIB folder's, or
// an added folder's.
func (s *Service) FilePath(name string) (string, bool) {
	return s.library().path(name)
}

// Shadowed lists the files of added folders that are not loaded because an
// earlier folder holds a file of the same name, each with that file.
func (s *Service) Shadowed() map[string]string {
	out := map[string]string{}
	for k, v := range s.library().shadowed {
		out[k] = v
	}
	return out
}
