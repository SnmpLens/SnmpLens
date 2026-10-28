package mib

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sleepinggenius2/gosmi"
)

// gosmi's view of the MIB directory.
//
// gosmi finds an imported module by FILE NAME: asked for FOO-MIB, it looks for
// a file called FOO-MIB, FOO-MIB.mib, .my, .mi2 or .txt, and nothing else. A
// vendor file called foo_v2.mib that declares FOO-MIB loads when it is named
// explicitly, and is invisible to every module that imports it — and whether
// that import then resolved depended on whether the file happened to sort
// before its importer. Vendor files are named after products, releases and
// customers far more often than after the module inside.
//
// So gosmi is not given the directory, it is given this: the same files, plus
// one entry per module under its DECLARED name for every file gosmi would not
// find by its own rule. And what gosmi reads through it is the lenient reading
// (repairSource) of the text decoded as DecodeSource decodes it, so the loader,
// the diagnosis and the editor all see the same file.
//
// When several files declare one module, ONE of them is the module (Preferred),
// and its entry comes first. Every file is still readable under its own name.

// gosmiExtensions are the suffixes gosmi accepts after a module name
// (smi/internal/module.go, GetModuleFile).
var gosmiExtensions = map[string]bool{"": true, "mib": true, "my": true, "mi2": true, "txt": true}

// gosmiFinds reports whether gosmi, asked for module, would open file.
func gosmiFinds(file, module string) bool {
	parts := strings.SplitN(file, ".", 2)
	if parts[0] != module {
		return false
	}
	ext := ""
	if len(parts) > 1 {
		ext = parts[1]
	}
	return gosmiExtensions[ext]
}

type mibFS struct {
	lib *library
	// declared is the module each file declares, for every file that declares one.
	declared map[string]string
	// chosen is the file that IS the module, for every module some file declares.
	chosen map[string]string
	// reasons say why a file declaring a module several files declare is not
	// the one kept.
	reasons map[string]string
	// aliases are the entries added under a module's name.
	aliases map[string]string
	// repairs are what the lenient reading changed, by file, the last time gosmi
	// read it.
	repairs map[string][]Diagnostic
	// real is every file of the library (library.go): the MIB folder's and the
	// added folders'.
	real map[string]bool
}

// mountLocked points gosmi at the library through a freshly indexed mibFS.
// The caller MUST hold gosmiMu.
//
// Whatever other directories gosmi searches — its built-in defaults outside
// Windows, SMIPATH — are kept, ahead of this one as before.
func mountLocked(lib *library) *mibFS {
	dir := lib.primary
	fsys := newMibFS(lib)
	var others []string
	for _, p := range filepath.SplitList(gosmi.GetPath()) {
		if p == "" || strings.HasPrefix(p, "[") || filepath.Clean(p) == dir {
			continue
		}
		others = append(others, p)
	}
	gosmi.SetFS(gosmi.NamedFS(dir, fsys))
	if len(others) > 0 {
		gosmi.PrependPath(strings.Join(others, string(os.PathListSeparator)))
	}
	return fsys
}

func newMibFS(lib *library) *mibFS {
	m := &mibFS{
		lib:      lib,
		declared: map[string]string{},
		chosen:   map[string]string{},
		reasons:  map[string]string{},
		aliases:  map[string]string{},
		repairs:  map[string][]Diagnostic{},
		real:     map[string]bool{},
	}
	byModule := map[string][]string{}
	for _, f := range lib.order {
		m.real[f] = true
		path := lib.files[f]
		if module := declaredModuleName(path); module != "" {
			m.declared[f] = module
			byModule[module] = append(byModule[module], f)
		}
	}
	for module, list := range byModule {
		m.choose(module, list)
	}
	for module, f := range m.chosen {
		m.alias(module, f)
	}
	return m
}

// choose settles which of the files declaring module is the module.
func (m *mibFS) choose(module string, files []string) string {
	if len(files) == 1 {
		m.chosen[module] = files[0]
		return files[0]
	}
	// The caller's order is the last tie-break: the file named after the
	// module first, then the later name ("example 1.10" before "example 1.9").
	sort.SliceStable(files, func(i, j int) bool {
		fi, fj := gosmiFinds(files[i], module), gosmiFinds(files[j], module)
		if fi != fj {
			return fi
		}
		return NaturalLess(files[j], files[i])
	})
	cands := make([]Candidate, 0, len(files))
	for _, f := range files {
		content := ""
		if path, ok := m.lib.path(f); ok {
			if raw, err := os.ReadFile(path); err == nil {
				content, _, _ = DecodeSource(raw)
			}
		}
		cands = append(cands, Candidate{Name: f, Content: content})
	}
	best, reasons := Preferred(cands)
	m.chosen[module] = files[best]
	for i, f := range files {
		m.reasons[f] = reasons[i].Why
	}
	return files[best]
}

// alias adds module's entry when gosmi would not find the chosen file under
// it, or would find another file first. Never over a real file of exactly that
// name: asked for that file, gosmi must get that file.
func (m *mibFS) alias(module, file string) {
	if m.real[module] && file != module {
		return
	}
	needed := !gosmiFinds(file, module)
	if !needed {
		for f := range m.real {
			if f != file && gosmiFinds(f, module) {
				needed = true
				break
			}
		}
	}
	if needed {
		m.aliases[module] = file
	} else {
		delete(m.aliases, module)
	}
}

// prefer makes file the module it declares among the ones being loaded,
// whatever the directory as a whole would have chosen: a file the operator
// switched off is not a candidate.
func (m *mibFS) prefer(module string, files []string) string {
	f := m.choose(module, files)
	m.alias(module, f)
	return f
}

// Open serves a file, or a module under its declared name, as the lenient
// reading of its text.
func (m *mibFS) Open(name string) (fs.File, error) {
	if name != filepath.Base(name) || name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	file := name
	if target, ok := m.aliases[name]; ok {
		file = target
	}
	path, ok := m.lib.path(file)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content, _, _ := DecodeSource(raw)
	r := repairSource(content)
	if len(r.Fixes) > 0 {
		m.repairs[file] = r.Fixes
	} else {
		delete(m.repairs, file)
	}
	return &memFile{Reader: bytes.NewReader([]byte(r.Text)), info: memInfo{name: name, size: int64(len(r.Text))}}, nil
}

// ReadDir lists the module entries first, so the chosen file wins over any
// other that gosmi would match by name, then the files themselves.
func (m *mibFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	modules := make([]string, 0, len(m.aliases))
	for module := range m.aliases {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	out := make([]fs.DirEntry, 0, len(modules)+len(m.lib.order))
	for _, module := range modules {
		out = append(out, fs.FileInfoToDirEntry(memInfo{name: module}))
	}
	// Every folder's files, as one: a file of an added folder is found by its
	// name like one of the MIB folder.
	for _, name := range m.lib.order {
		out = append(out, fs.FileInfoToDirEntry(memInfo{name: name}))
	}
	return out, nil
}

type memFile struct {
	*bytes.Reader
	info memInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Close() error               { return nil }

type memInfo struct {
	name string
	size int64
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0o444 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }

// NamedAfter reports whether gosmi, asked for module, finds the file by its
// name alone.
func NamedAfter(file, module string) bool { return gosmiFinds(file, module) }

// DeclaredModule is the module a MIB file declares, read from its head.
func DeclaredModule(path string) string { return declaredModuleName(path) }

// FileNameFor is the name a file declaring module is stored under: its own if
// gosmi finds it by it, the module's otherwise, keeping an extension gosmi
// accepts.
func FileNameFor(module, current string) string {
	if module == "" || gosmiFinds(current, module) {
		return current
	}
	parts := strings.SplitN(current, ".", 2)
	if len(parts) == 1 {
		return module
	}
	if ext := strings.ToLower(parts[1]); gosmiExtensions[ext] {
		return module + "." + ext
	}
	return module + ".mib"
}
