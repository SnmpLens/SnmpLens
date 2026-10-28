package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/mib"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The MIB editor.
//
// Every method that names a file goes through resolveMibPath. The existing
// ListMibFiles takes a caller-supplied directory and hands it straight to
// os.ReadDir, which is a precedent worth not following: monitoring.db,
// service.json and the pkg/secrets store all live one directory above the MIB
// folder, and these methods WRITE.

// mibBackupDirName is a SIBLING of the MIB directory, never inside it.
//
// ListMibFiles filters only dotfiles, README and LICENSE, and the MIB path
// store enables everything it finds. A backup left beside the original would
// therefore be loaded as a second copy of the same module — and since
// os.ReadDir is alphabetical, "IF-MIB.1234.bak" would load before "IF-MIB" and
// its stale content would win, silently, with no error anywhere.
const mibBackupDirName = "mib-backups"

// mibTempDirName holds the write-then-rename staging file, a sibling of the
// MIB folder for exactly the reason above.
const mibTempDirName = "mib-temp"

// resolveMibPath maps a file name to a path inside the MIB directory, and
// refuses anything that would land outside it.
func (a *App) resolveMibPath(name string) (string, error) {
	return mib.SafeMibPath(a.persistentMibDir, name)
}

// bundledContent returns the embedded copy of a standard MIB, if there is one.
func (a *App) bundledContent(name string) (string, bool) {
	// An fs.FS always uses forward slashes, even on Windows.
	raw, err := bundledFile(a.mibs, "mibs/"+filepath.Base(name))
	if err != nil {
		return "", false
	}
	content, _ := mib.NormaliseSource(raw)
	return content, true
}

// MibEditorList returns the MIBs in the persistent directory.
func (a *App) MibEditorList() ([]mib.FileInfo, error) {
	names, err := mib.ListMibFiles(a.persistentMibDir)
	if err != nil {
		return []mib.FileInfo{}, err
	}

	out := make([]mib.FileInfo, 0, len(names))
	for _, name := range names {
		info := mib.FileInfo{Name: name}
		if path, err := a.resolveMibPath(name); err == nil {
			if st, err := os.Stat(path); err == nil {
				info.Size = st.Size()
			}
			info.Module = mib.DeclaredModule(path)
			if embedded, ok := a.bundledContent(name); ok {
				info.Bundled = true
				if raw, err := os.ReadFile(path); err == nil {
					onDisk, _ := mib.NormaliseSource(raw)
					info.Modified = onDisk != embedded
				}
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// MibEditorRead opens a MIB from the persistent directory.
func (a *App) MibEditorRead(name string) (mib.Source, error) {
	path, err := a.resolveMibPath(name)
	if err != nil {
		return mib.Source{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return mib.Source{}, err
	}
	// Decoded before it crosses the bridge.
	//
	// Wails marshals every bound result with encoding/json, which replaces
	// each invalid UTF-8 byte with U+FFFD. So a Latin-1 vendor MIB used to
	// reach the editor already mangled, and saving it — even with NO edits —
	// wrote the replacement characters back over the original bytes. That was
	// answered by refusing to open any file that is not UTF-8, which refused
	// the vendor MIBs that most needed an editor: they LOAD, since gosmi
	// accepts any byte inside a string. They are now read as Windows-1252,
	// Encoding says so, and the editor tells the user that saving writes UTF-8
	// before anything is written.
	content, eol, encoding := mib.DecodeSource(raw)
	return a.directorySource(name, path, raw, content, eol, encoding), nil
}

// directorySource is a file of the MIB directory as the editor opens it.
func (a *App) directorySource(name, path string, raw []byte, content, eol, encoding string) mib.Source {
	_, bundled := a.bundledContent(name)
	src := mib.Source{
		Name: filepath.Base(name), Path: path, Content: content, Eol: eol, Encoding: encoding,
		Bundled: bundled, Sha256: mib.ChecksumBytes(raw),
	}
	src.SetAnalysis(a.analyseAs(src.Name, content))
	return src
}

// MibEditorEncodings lists the encodings a MIB can be read as (pkg/mib/encodings.go).
func (a *App) MibEditorEncodings() []string {
	return mib.Encodings()
}

// MibEditorReadAs opens a MIB of the directory again, read in the encoding the
// person chose: the guess (UTF-8, else Windows-1252) is wrong for a file
// written in Shift_JIS or GBK, and its prose comes out as accented Latin.
func (a *App) MibEditorReadAs(name, encoding string) (mib.Source, error) {
	path, err := a.resolveMibPath(name)
	if err != nil {
		return mib.Source{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return mib.Source{}, err
	}
	content, eol, err := mib.DecodeAs(raw, encoding)
	if err != nil {
		return mib.Source{}, err
	}
	return a.directorySource(name, path, raw, content, eol, strings.ToLower(encoding)), nil
}

// MibEditorReopenExternalAs reads the file last opened from outside the
// directory again, in another encoding. From the bytes kept when it was
// opened, never from a path: a method reading a path the renderer names would
// read any file and hand it back.
func (a *App) MibEditorReopenExternalAs(encoding string) (mib.Source, error) {
	a.externalMu.Lock()
	ext := a.lastExternal
	a.externalMu.Unlock()
	if ext.raw == nil {
		return mib.Source{}, fmt.Errorf("no file was opened from outside the MIB directory")
	}
	content, eol, err := mib.DecodeAs(ext.raw, encoding)
	if err != nil {
		return mib.Source{}, err
	}
	return a.externalSource(ext, content, eol, strings.ToLower(encoding)), nil
}

// externalFile is the file last opened from outside the MIB directory.
type externalFile struct {
	path string
	raw  []byte
}

func (a *App) externalSource(ext externalFile, content, eol, encoding string) mib.Source {
	// Offer the module's own name rather than the file name, as the import
	// stores it: gosmi resolves a module by name, and a file named after the
	// module it holds is found by every tool, not only through mibFS.
	suggested := mib.ModuleName(content)
	if suggested == "" {
		suggested = filepath.Base(ext.path)
	}
	src := mib.Source{
		Name: suggested, Path: ext.path, Content: content, Eol: eol, Encoding: encoding,
		External: true, Sha256: mib.ChecksumBytes(ext.raw),
	}
	src.SetAnalysis(a.analyseAs("", content))
	return src
}

// MibEditorOpenExternal opens a MIB from anywhere on disk, read-only.
//
// External means external: the file is loaded into the editor but its path is
// not kept, so a save has to choose a name inside the MIB directory. Writing
// back to an arbitrary path is how an editor turns into a way to overwrite any
// file the user can reach.
func (a *App) MibEditorOpenExternal() (mib.Source, error) {
	if a.ctx == nil {
		return mib.Source{}, fmt.Errorf("no window")
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Open a MIB file",
		Filters: []runtime.FileFilter{
			{DisplayName: "MIB files", Pattern: "*.mib;*.txt;*.my;*"},
		},
	})
	if err != nil {
		return mib.Source{}, err
	}
	if path == "" {
		return mib.Source{}, nil // cancelled
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return mib.Source{}, err
	}
	content, eol, encoding := mib.DecodeSource(raw)
	ext := externalFile{path: path, raw: raw}
	a.externalMu.Lock()
	a.lastExternal = ext
	a.externalMu.Unlock()
	return a.externalSource(ext, content, eol, encoding), nil
}

// MibEditorValidate checks buffer text without touching disk or gosmi.
func (a *App) MibEditorValidate(content string) []mib.Diagnostic {
	return mib.Validate(content)
}

// MibEditorAnalyse runs every check from one parse.
//
// It used to call three functions that each parsed the file: 130 ms of work on
// a 185 KB MIB for 45 ms of answers, on every pause in typing. The bridge call
// was unified before the parse was, which is why the editor still felt slow.
//
// name is the directory file the buffer is being edited as, "" for one opened
// from elsewhere: the verdict takes the last load into account (judgeBuffer).
func (a *App) MibEditorAnalyse(name, content string) mib.Analysis {
	return a.analyseAs(name, content)
}

// MibEditorSave writes a MIB, backing up whatever was there first.
//
// force skips the "changed on disk since you opened it" check. Nothing here
// refuses to save a MIB that fails validation: the file may be a work in
// progress, and an editor that will not let you save is not an editor. The
// consequences are made visible instead — the reload reports what broke, and
// a bundled MIB can be restored.
func (a *App) MibEditorSave(name, content, baseSha256 string, force bool) (mib.SaveResult, error) {
	path, err := a.resolveMibPath(name)
	if err != nil {
		return mib.SaveResult{}, err
	}

	result := mib.SaveResult{Diagnostics: mib.Validate(content)}

	existing, statErr := os.ReadFile(path)

	// Refuse a save that would silently discard someone else's edit. The
	// baseline is the BYTES the editor read (mib.ChecksumBytes); if the disk no
	// longer holds them, the file changed underneath — another instance, an
	// import, a text editor. Compared as bytes, whatever encoding it was read in.
	if statErr == nil && !force && baseSha256 != "" && mib.ChecksumBytes(existing) != baseSha256 {
		result.Conflict = true
		return result, nil
	}

	if statErr == nil {
		backup, err := a.backupMib(name, existing)
		if err != nil {
			return result, fmt.Errorf("could not back up %s before saving: %w", name, err)
		}
		result.BackupPath = backup
	}

	eol := "lf"
	if statErr == nil {
		_, eol, _ = mib.DecodeSource(existing)
	}
	payload := []byte(mib.RestoreEol(content, eol))

	// Write to a temporary file on the SAME volume, then rename: a crash
	// halfway through must not leave a truncated MIB that breaks every module
	// importing from it.
	// In a SIBLING directory, for the same reason backups and drafts are:
	// ListMibFiles returns everything in mibs/ that is not a dotfile, so a
	// crash between the write and the rename left IF-MIB.tmp beside IF-MIB —
	// and os.ReadDir being alphabetical, the half-written copy loaded second
	// and won, gosmi's module map being last-wins.
	tmpDir := filepath.Join(filepath.Dir(a.persistentMibDir), mibTempDirName)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return result, err
	}
	tmp := filepath.Join(tmpDir, filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return result, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return result, err
	}

	result.Saved = true
	result.Sha256 = mib.ChecksumBytes(payload)
	return result, nil
}

// backupMib copies the previous content into a sibling directory.
func (a *App) backupMib(name string, content []byte) (string, error) {
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), mibBackupDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Nanoseconds, and a collision loop.
	//
	// A second-resolution stamp made two saves inside one wall-clock second
	// write the same backup name, and the second silently destroyed the first
	// — the copy of the version the user actually wanted back.
	base := filepath.Join(dir, filepath.Base(name))
	path := fmt.Sprintf("%s.%d.bak", base, time.Now().UnixNano())
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = fmt.Sprintf("%s.%d-%d.bak", base, time.Now().UnixNano(), i)
		if i > 100 {
			return "", fmt.Errorf("could not find a free backup name in %s", dir)
		}
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// MibEditorRestoreBundled puts back the copy that ships in the binary.
//
// This is the way out of having broken a standard MIB. Nearly every other MIB
// IMPORTS from SNMPv2-SMI and SNMPv2-TC, so an editing mistake in one of those
// takes the whole tree with it, and the file on disk is the only copy the app
// reads at runtime.
func (a *App) MibEditorRestoreBundled(name string) (mib.Source, error) {
	embedded, ok := a.bundledContent(name)
	if !ok {
		return mib.Source{}, fmt.Errorf("%s does not ship with SnmpLens; there is nothing to restore", name)
	}
	if _, err := a.MibEditorSave(name, embedded, "", true); err != nil {
		return mib.Source{}, err
	}
	return a.MibEditorRead(name)
}

// MibEditorReload rebuilds the tree from the files the user actually enabled.
//
// It used to fall back to "everything on disk" when handed an empty list, which
// silently re-enabled every MIB somebody had deliberately switched off in
// Settings. An empty list now means an empty list.
func (a *App) MibEditorReload(enabledFiles []string) mib.Reloaded {
	result := a.mibService.Rebuild(enabledFiles)
	a.mibStatuses.rememberLoads(result.Diagnostics)
	if !result.Health.Ok {
		a.recordSystemEvent(events.KindSystemMibLoadFailed, "major",
			"The MIB tree no longer resolves after a reload: "+strings.Join(result.Health.Failures, "; "))
	}
	return result
}

// --- symbols and import assistance ---

// MibEditorSymbols returns every name the loaded tree knows.
func (a *App) MibEditorSymbols() mib.Catalogue {
	return mib.Symbols()
}

// MibEditorCheckImports reports symbols the buffer uses but never imports.
func (a *App) MibEditorCheckImports(content string) []mib.MissingImport {
	missing := mib.CheckImports(content, mib.Symbols())
	if missing == nil {
		return []mib.MissingImport{}
	}
	return missing
}

// MibEditorFixImports returns the buffer with its IMPORTS clause repaired.
func (a *App) MibEditorFixImports(content string) mib.ImportFix {
	return mib.FixImports(content, mib.Symbols())
}

// MibEditorQuickFix writes the fix for the problems of kind code whose symbol
// is one of symbols, or for every one of that kind when symbols is empty
// (pkg/mib/quickfix.go). Nothing is saved: the result goes into the buffer as
// one edit, which the editor can undo.
func (a *App) MibEditorQuickFix(content, code string, symbols []string) mib.QuickFixResult {
	return mib.QuickFix(content, code, symbols, mib.Symbols())
}

// MibEditorRepair returns the buffer with what SnmpLens reads leniently
// written in — an escaped quote, a malformed date, a misspelt module name — so
// the file loads in other MIB tools as it does here.
func (a *App) MibEditorRepair(content string) mib.RepairResult {
	return mib.Repair(content)
}

// --- drafts ---

// mibDraftDirName is where unsaved buffers live, a sibling of the MIB folder
// for the same reason backups are: anything inside mibs/ gets loaded as a MIB.
const mibDraftDirName = "mib-drafts"

// DraftInfo describes an unsaved buffer recovered from a previous session.
type DraftInfo struct {
	Name    string `json:"name"`
	SavedAt string `json:"savedAt"`
	Size    int64  `json:"size"`
}

func (a *App) draftPath(name string) (string, error) {
	clean := filepath.Base(strings.TrimSpace(name))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("invalid draft name %q", name)
	}
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), mibDraftDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, clean+".draft"), nil
}

// MibEditorSaveDraft keeps an unsaved buffer outside the MIB directory.
//
// It exists because the editor is one tab among seven: the buffer has to
// survive switching away, closing the window and the machine going down, and
// none of those are moments where the user chose to write a possibly broken
// MIB into the folder the whole app loads from.
func (a *App) MibEditorSaveDraft(name, content string) error {
	path, err := a.draftPath(name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// MibEditorReadDraft returns a stored buffer, or empty if there is none.
func (a *App) MibEditorReadDraft(name string) (string, error) {
	path, err := a.draftPath(name)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(raw), nil
}

// MibEditorListDrafts reports what can be recovered.
func (a *App) MibEditorListDrafts() ([]DraftInfo, error) {
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), mibDraftDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []DraftInfo{}, nil
		}
		return []DraftInfo{}, err
	}
	out := []DraftInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".draft") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, DraftInfo{
			Name:    strings.TrimSuffix(e.Name(), ".draft"),
			SavedAt: info.ModTime().UTC().Format(time.RFC3339),
			Size:    info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt > out[j].SavedAt })
	return out, nil
}

// MibEditorDiscardDraft removes a stored buffer.
func (a *App) MibEditorDiscardDraft(name string) error {
	path, err := a.draftPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MibDependencyGraph reports what every MIB in the directory imports.
//
// Cheap enough to offer freely: readImports reads the head of each file as text
// and stops at the semicolon ending the IMPORTS clause — no parse, no gosmi,
// and therefore no exclusive lock held across the scan.
func (a *App) MibDependencyGraph() ([]mib.GraphNode, error) {
	if a.mibService == nil {
		return []mib.GraphNode{}, fmt.Errorf("MIB service not initialized")
	}
	nodes, err := a.mibService.ModuleGraph()
	if err != nil {
		return []mib.GraphNode{}, err
	}
	if nodes == nil {
		// Never nil: it crosses the bridge as null and throws on the first .map.
		return []mib.GraphNode{}, nil
	}
	return nodes, nil
}
