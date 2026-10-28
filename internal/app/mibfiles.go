package app

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"SnmpLens/pkg/mib"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// What the MIB editor's file list can do to a file besides opening it: the
// context menu (MibEditorPanel.svelte, mibeditor/fileMenu.js).
//
// Every name goes through resolveMibPath, as everything else the editor writes
// does: monitoring.db, service.json and the secret store sit one directory
// above mibs/, and these methods rename, copy and remove. Nothing is ever
// deleted outright. A file "deleted" here is moved to mib-backups/, the
// sibling that holds what every save replaced, and it comes back from there.

// SetMibDirectories sets the folders added in the MIB settings, whose files
// load beside the MIB folder's (pkg/mib/library.go), and answers with the
// ones taken: existing absolute directories. The renderer keeps the list and
// gives it before every load, so a load never runs against a stale one.
func (a *App) SetMibDirectories(dirs []string) []string {
	return a.mibService.SetDirectories(dirs)
}

// MibEditorRevealFile shows a MIB in the system's file manager, selected.
//
// Windows and macOS select it in its folder; Linux has no command that does
// everywhere, so the folder is opened. A file, unlike OpenFolder's directory,
// is never handed to a command that would OPEN it: explorer gets /select, and
// open gets -R.
func (a *App) MibEditorRevealFile(name string) error {
	path, err := a.resolveMibPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	argv := revealCommand(runtime.GOOS, path)
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not show %s: %w", filepath.Base(path), err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func revealCommand(goos, path string) []string {
	switch goos {
	case "windows":
		return []string{"explorer.exe", "/select," + path}
	case "darwin":
		return []string{"open", "-R", path}
	default:
		return []string{"xdg-open", filepath.Dir(path)}
	}
}

// newFileName checks a name typed for a file the editor is about to create:
// a NAME, not a path. resolveMibPath keeps the last element of whatever it is
// given, which is right for a name the list supplied and wrong for one typed:
// "../monitoring.db" would quietly become mibs/monitoring.db. A leading dot
// would make a file the directory listing hides.
func newFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("no name given")
	case strings.ContainsAny(name, `/\:`) || name == "." || name == "..":
		return "", fmt.Errorf("%q is a path; give a file name", name)
	case strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%q would be a hidden file, which the MIB list never shows", name)
	}
	return name, nil
}

// renameTarget checks a new name for a file in the MIB directory.
func (a *App) renameTarget(name, newName string) (from, to string, err error) {
	if newName, err = newFileName(newName); err != nil {
		return "", "", err
	}
	if from, err = a.resolveMibPath(name); err != nil {
		return "", "", err
	}
	if to, err = a.resolveMibPath(newName); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(from); err != nil {
		return "", "", err
	}
	// A standard MIB's file name is what it is restored by at start-up: one
	// renamed away is extracted again beside the copy, and a name taken from
	// one would be shadowed by the next restore.
	if _, bundled := a.bundledContent(name); bundled {
		return "", "", fmt.Errorf("%s ships with SnmpLens and keeps its name", name)
	}
	if _, bundled := a.bundledContent(newName); bundled {
		return "", "", fmt.Errorf("%s is the name of a MIB that ships with SnmpLens", newName)
	}
	// A rename that only changes the case is the same file on Windows and
	// macOS; anything else must not overwrite what is there.
	if !strings.EqualFold(filepath.Base(from), filepath.Base(to)) {
		if _, err := os.Stat(to); err == nil {
			return "", "", fmt.Errorf("%s already exists", filepath.Base(to))
		}
	}
	return from, to, nil
}

// MibEditorRenameFile renames a MIB in the directory, its unsaved draft with
// it.
func (a *App) MibEditorRenameFile(name, newName string) error {
	from, to, err := a.renameTarget(name, newName)
	if err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if draft, err := a.MibEditorReadDraft(name); err == nil && draft != "" {
		if a.MibEditorSaveDraft(filepath.Base(to), draft) == nil {
			_ = a.MibEditorDiscardDraft(name)
		}
	}
	return nil
}

// MibEditorDuplicateFile copies a MIB under a new name. The copy declares the
// same module, and the next load says so: one of the two is the module, the
// other a duplicate — which is the point of a copy made to try an edit on.
func (a *App) MibEditorDuplicateFile(name, newName string) error {
	from, err := a.resolveMibPath(name)
	if err != nil {
		return err
	}
	if newName, err = newFileName(newName); err != nil {
		return err
	}
	to, err := a.resolveMibPath(newName)
	if err != nil {
		return err
	}
	if _, bundled := a.bundledContent(newName); bundled {
		return fmt.Errorf("%s is the name of a MIB that ships with SnmpLens", newName)
	}
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("%s already exists", filepath.Base(to))
	}
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, raw, 0o644)
}

// MibEditorDeleteFiles moves MIBs out of the directory into mib-backups/,
// where MibEditorDeletedFiles finds them and MibEditorRestoreDeleted brings
// them back. A standard MIB is refused: it would be extracted again at the
// next start, which is not what deleting it means.
func (a *App) MibEditorDeleteFiles(names []string) ([]string, error) {
	moved := []string{}
	var problems []string
	for _, name := range names {
		if _, bundled := a.bundledContent(name); bundled {
			problems = append(problems, fmt.Sprintf("%s ships with SnmpLens and is restored at every start", name))
			continue
		}
		path, err := a.resolveMibPath(name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if _, err := a.keepCopy(name, raw, "deleted"); err != nil {
			problems = append(problems, fmt.Sprintf("%s was not deleted: %v", name, err))
			continue
		}
		if err := os.Remove(path); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		_ = a.MibEditorDiscardDraft(name)
		moved = append(moved, filepath.Base(path))
	}
	if len(problems) > 0 {
		return moved, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return moved, nil
}

// keepCopy writes content to mib-backups/ as <name>.<nanoseconds>.<kind>.
func (a *App) keepCopy(name string, content []byte, kind string) (string, error) {
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), mibBackupDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, filepath.Base(name))
	path := fmt.Sprintf("%s.%d.%s", base, time.Now().UnixNano(), kind)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = fmt.Sprintf("%s.%d-%d.%s", base, time.Now().UnixNano(), i, kind)
		if i > 100 {
			return "", fmt.Errorf("could not find a free name in %s", dir)
		}
	}
	return path, os.WriteFile(path, content, 0o644)
}

// MibBackup is a copy kept in mib-backups/: what a save replaced, or a file
// that was deleted.
type MibBackup struct {
	// ID is the copy's own file name, which is how it is asked for again.
	ID      string `json:"id"`
	Name    string `json:"name"`
	SavedAt string `json:"savedAt"`
	Size    int64  `json:"size"`
	Deleted bool   `json:"deleted"`
}

// backupNameRe reads <name>.<nanoseconds>[-n].<bak|deleted>.
var backupNameRe = regexp.MustCompile(`^(.+)\.(\d{10,})(?:-\d+)?\.(bak|deleted)$`)

func (a *App) backups() ([]MibBackup, error) {
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), mibBackupDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []MibBackup{}, nil
		}
		return nil, err
	}
	out := []MibBackup{}
	for _, e := range entries {
		m := backupNameRe.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		nanos, _ := strconv.ParseInt(m[2], 10, 64)
		out = append(out, MibBackup{
			ID: e.Name(), Name: m[1], Size: info.Size(), Deleted: m[3] == "deleted",
			SavedAt: time.Unix(0, nanos).UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// MibEditorBackups lists the copies kept of one MIB, newest first.
func (a *App) MibEditorBackups(name string) ([]MibBackup, error) {
	all, err := a.backups()
	if err != nil {
		return []MibBackup{}, err
	}
	out := []MibBackup{}
	for _, b := range all {
		if b.Name == filepath.Base(name) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt > out[j].SavedAt })
	return out, nil
}

// MibEditorDeletedFiles lists the MIBs deleted from the directory and not
// brought back, newest first: one per name, the last copy of each.
func (a *App) MibEditorDeletedFiles() ([]MibBackup, error) {
	all, err := a.backups()
	if err != nil {
		return []MibBackup{}, err
	}
	latest := map[string]MibBackup{}
	for _, b := range all {
		if !b.Deleted {
			continue
		}
		if path, err := a.resolveMibPath(b.Name); err == nil {
			if _, err := os.Stat(path); err == nil {
				continue // back in the directory, by a restore or a new file
			}
		}
		if cur, ok := latest[b.Name]; !ok || b.SavedAt > cur.SavedAt {
			latest[b.Name] = b
		}
	}
	out := make([]MibBackup, 0, len(latest))
	for _, b := range latest {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt > out[j].SavedAt })
	return out, nil
}

// backupPath maps a copy's ID to its path, refusing anything that is not one.
func (a *App) backupPath(id string) (string, string, error) {
	base := filepath.Base(strings.TrimSpace(id))
	m := backupNameRe.FindStringSubmatch(base)
	if base != id || m == nil {
		return "", "", fmt.Errorf("%q is not a kept copy", id)
	}
	return filepath.Join(filepath.Dir(a.persistentMibDir), mibBackupDirName, base), m[1], nil
}

// MibEditorReadBackup returns a kept copy, decoded as the editor reads a MIB.
func (a *App) MibEditorReadBackup(id string) (string, error) {
	path, _, err := a.backupPath(id)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content, _, _ := mib.DecodeSource(raw)
	return content, nil
}

// MibEditorRestoreDeleted puts a deleted MIB back under its name, if that name
// is free, and answers with it.
func (a *App) MibEditorRestoreDeleted(id string) (string, error) {
	path, name, err := a.backupPath(id)
	if err != nil {
		return "", err
	}
	to, err := a.resolveMibPath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(to); err == nil {
		return "", fmt.Errorf("%s exists again; rename it first", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(to, raw, 0o644); err != nil {
		return "", err
	}
	// The copy has done its job; a later delete keeps a new one.
	_ = os.Remove(path)
	return filepath.Base(to), nil
}

// MibEditorBundledContent is the copy of a standard MIB that ships in the
// binary, for comparing a modified one against it.
func (a *App) MibEditorBundledContent(name string) (string, error) {
	content, ok := a.bundledContent(name)
	if !ok {
		return "", fmt.Errorf("%s does not ship with SnmpLens", name)
	}
	return content, nil
}

// MibEditorModuleRoot is the OID the MIB tree shows a loaded module at: its
// MODULE-IDENTITY, or its shallowest definition. Empty when it is not loaded.
func (a *App) MibEditorModuleRoot(module string) string {
	return a.mibService.ModuleRoot(module)
}

// MibExportResult says what an export wrote.
type MibExportResult struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
	// Missing is what the exported modules import and the directory does not
	// hold: whoever receives the archive needs those too.
	Missing []string `json:"missing"`
}

// MibEditorExportWithDependencies writes MIBs, and every module they import
// that the directory holds, into a zip chosen in a save dialog.
//
// A vendor MIB on its own is the commonest thing to hand someone and the
// least use to them: it loads only beside what it imports. Empty Path is a
// cancelled dialog.
func (a *App) MibEditorExportWithDependencies(names []string) (MibExportResult, error) {
	files, missing, err := a.exportClosure(names)
	if err != nil {
		return MibExportResult{}, err
	}
	if a.ctx == nil {
		return MibExportResult{}, fmt.Errorf("no window")
	}
	suggested := "mibs.zip"
	if len(names) == 1 {
		suggested = strings.TrimSuffix(filepath.Base(names[0]), filepath.Ext(names[0])) + ".zip"
	}
	dest, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Export MIBs with their dependencies",
		DefaultFilename: suggested,
		Filters:         []wruntime.FileFilter{{DisplayName: "ZIP archive", Pattern: "*.zip"}},
	})
	if err != nil || dest == "" {
		return MibExportResult{Missing: missing, Files: files}, err
	}
	if !strings.EqualFold(filepath.Ext(dest), ".zip") {
		dest += ".zip"
	}
	f, err := os.Create(dest)
	if err != nil {
		return MibExportResult{}, err
	}
	if err := a.writeMibZip(f, files); err != nil {
		f.Close()
		os.Remove(dest)
		return MibExportResult{}, err
	}
	if err := f.Close(); err != nil {
		return MibExportResult{}, err
	}
	return MibExportResult{Path: dest, Files: files, Missing: missing}, nil
}

// exportClosure is the files to export for names: each one, and the file of
// every module they import, transitively, as the directory holds them.
func (a *App) exportClosure(names []string) (files, missing []string, err error) {
	graph, err := a.mibService.ModuleGraph()
	if err != nil {
		return nil, nil, err
	}
	byModule := map[string]mib.GraphNode{}
	byFile := map[string]mib.GraphNode{}
	for _, n := range graph {
		byModule[n.Module] = n
		if n.Present {
			byFile[n.File] = n
		}
	}
	seenFile := map[string]bool{}
	seenModule := map[string]bool{}
	var queue []string
	for _, name := range names {
		if _, err := a.resolveMibPath(name); err != nil {
			return nil, nil, err
		}
		n, ok := byFile[filepath.Base(name)]
		if !ok {
			return nil, nil, fmt.Errorf("%s is not a MIB in the directory", name)
		}
		if !seenFile[n.File] {
			seenFile[n.File] = true
			files = append(files, n.File)
		}
		queue = append(queue, n.Module)
	}
	for len(queue) > 0 {
		module := queue[0]
		queue = queue[1:]
		if seenModule[module] {
			continue
		}
		seenModule[module] = true
		n := byModule[module]
		if !n.Present {
			missing = append(missing, module)
			continue
		}
		if !seenFile[n.File] {
			seenFile[n.File] = true
			files = append(files, n.File)
		}
		queue = append(queue, n.Imports...)
	}
	sort.Strings(missing)
	if missing == nil {
		missing = []string{}
	}
	return files, missing, nil
}

// writeMibZip writes the files, as they are on disk, under their own names.
func (a *App) writeMibZip(w io.Writer, files []string) error {
	zw := zip.NewWriter(w)
	for _, name := range files {
		path, err := a.resolveMibPath(name)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entry, err := zw.Create(filepath.Base(path))
		if err != nil {
			return err
		}
		if _, err := entry.Write(raw); err != nil {
			return err
		}
	}
	return zw.Close()
}
