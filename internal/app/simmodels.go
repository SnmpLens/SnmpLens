package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"SnmpLens/pkg/imagegate"
	"SnmpLens/pkg/simulator"

)

// Custom simulator models: model files somebody else wrote (pkg/simulator,
// ParseCustomModel), imported from a JSON file or from a ZIP that carries their
// icons beside them, and packages (ParseCustomPackage) — a model spread over the
// files of one folder of a ZIP, the walks a real device answered among them.
//
// They are kept in simulator-models/, a SIBLING of simulator.json, mibs/ and
// presets/ and never inside one of them, for the reason assets/ is: whatever
// lists those directories would read a model as a broken MIB or preset. A model
// is filed under its own id — `<id>.json` for a lone file, `<id>.zip` for a
// package, and its icon `<id>.png`, `.jpg` or `.gif` — never under a name the
// file or the archive chose. A package is kept as a ZIP of the files it reads,
// written here, rather than as a folder of them: one file to replace and one to
// delete, and nothing from the archive lands on disk under a name of its own.
//
// Only the dialog is bound. The renderer never names a path here: a method that
// took one would read any file the renderer asked for, and a JSON parser's
// errors quote what it read.
const simulatorModelSubdir = "simulator-models"

// The bounds on what an import reads. A ZIP's sizes are what its central
// directory SAYS, and a decompression bomb says little: every entry is read
// through a limit, and what is unpacked is counted as it is read.
const (
	maxModelArchiveBytes = 8 << 20
	maxArchiveEntries    = 64
	// maxArchiveUnpacked leaves room for a package's walks, each of them up to
	// simulator.MaxWalkBytes.
	maxArchiveUnpacked = 40 << 20
	// maxKeptPackageBytes bounds a package as it is kept: compressed again
	// here, it may come out a little larger than it arrived.
	maxKeptPackageBytes = 2 * maxModelArchiveBytes
	maxModelIconBytes   = 256 << 10
	// maxModelIconSide is far more than a picker draws; the bound is what
	// keeps the icons the model list carries small.
	maxModelIconSide = 512
	maxCustomModels  = 64
)

// The forms a model is kept in: a lone file as itself, a package as the ZIP of
// the files it reads.
const (
	keptFile    = ".json"
	keptPackage = ".zip"
)

// iconExtensions are what an icon is kept as, by the format imagegate decoded —
// never by the name it arrived with.
var iconExtensions = map[string]string{"png": ".png", "jpeg": ".jpg", "gif": ".gif"}

var utf8BOM = []byte("\xef\xbb\xbf")

// SimulatorModelImportResult is what became of one model file or package.
type SimulatorModelImportResult struct {
	// File is what was read: the file picked and, for a model in a ZIP, the
	// entry in it — a package's model.json, for a package.
	File     string `json:"file"`
	ModelID  string `json:"modelId,omitempty"`
	Name     string `json:"name,omitempty"`
	Success  bool   `json:"success"`
	Replaced bool   `json:"replaced"`
	Icon     bool   `json:"icon"`
	Error    string `json:"error,omitempty"`
	// Warnings are about what was imported anyway.
	Warnings []SimulatorModelWarning `json:"warnings"`
}

// SimulatorModelWarning is an i18n key suffix (simulator.models.warning.<key>)
// and what its sentence quotes.
type SimulatorModelWarning struct {
	Key    string `json:"key"`
	Detail string `json:"detail"`
}

// SimulatorModelIcon is a custom model's icon, as a data URI.
type SimulatorModelIcon struct {
	ID   string `json:"id"`
	Icon string `json:"icon"`
}

// modelIcon is an icon that passed the gate, and the extension it is kept as.
type modelIcon struct {
	data []byte
	ext  string
}

// iconLookup finds the icon a model names, or says why it has none.
type iconLookup func(icon string) (*modelIcon, *SimulatorModelWarning)

// loadModels reads the custom models from their directory and makes them the
// simulator's. A file that no longer reads — edited by hand, or written by a
// later version — is logged and left where it is; a device of its model says
// which model it lacks when it is started.
func (s *simulatorService) loadModels() {
	entries, err := os.ReadDir(s.modelDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("simulator: cannot list %s: %v", s.modelDir, err)
	}
	type kept struct {
		m        simulator.CustomModel
		modified time.Time
	}
	bySlug := map[string]kept{}
	for _, e := range entries {
		name := e.Name()
		form := filepath.Ext(name)
		if e.IsDir() || (form != keptFile && form != keptPackage) {
			continue
		}
		m, err := readKept(filepath.Join(s.modelDir, name), form)
		if err != nil {
			log.Printf("simulator: model %s no longer reads: %v", name, err)
			continue
		}
		if m.Slug()+form != name {
			log.Printf("simulator: %s says it is %q; a model is kept under its own id", name, m.Slug())
			continue
		}
		var modified time.Time
		if info, err := e.Info(); err == nil {
			modified = info.ModTime()
		}
		// Both forms at once are an import that stopped between writing one
		// and removing the other: the one written last is the model.
		if other, ok := bySlug[m.Slug()]; ok && !modified.After(other.modified) {
			continue
		}
		bySlug[m.Slug()] = kept{m, modified}
	}
	list := make([]simulator.CustomModel, 0, len(bySlug))
	for _, k := range bySlug {
		list = append(list, k.m)
	}
	slices.SortFunc(list, func(a, b simulator.CustomModel) int { return strings.Compare(a.Slug(), b.Slug()) })
	if err := simulator.SetCustomModels(list); err != nil {
		log.Printf("simulator: the custom models: %v", err)
		return
	}
	s.models = list
}

// readKept reads a model the directory keeps, in the form it is kept in.
func readKept(name, form string) (simulator.CustomModel, error) {
	if form == keptFile {
		raw, err := readAtMost(name, simulator.MaxCustomModelBytes)
		if err != nil {
			return simulator.CustomModel{}, err
		}
		return simulator.ParseCustomModel(bytes.TrimPrefix(raw, utf8BOM))
	}
	files, err := keptPackageFiles(name)
	if err != nil {
		return simulator.CustomModel{}, err
	}
	m, _, err := simulator.ParseCustomPackage(files)
	return m, err
}

// keptPackageFiles reads back the files of a package the directory keeps, by
// their names in the package.
func keptPackageFiles(name string) ([]simulator.PackageFile, error) {
	data, err := readAtMost(name, maxKeptPackageBytes)
	if err != nil {
		return nil, err
	}
	ar, err := openArchive(data)
	if err != nil {
		return nil, err
	}
	var files []simulator.PackageFile
	for _, f := range ar.files {
		if !simulator.PackageReads(f.Name) {
			continue
		}
		b, err := ar.read(f, packageFileLimit(f.Name))
		if err != nil {
			return nil, err
		}
		files = append(files, simulator.PackageFile{Name: f.Name, Data: b})
	}
	return files, nil
}

// emitSimulatorModelsChanged tells the renderer to list the models again.
func (a *App) emitSimulatorModelsChanged() {
	a.emit("simulator:models")
}

// ListSimulatorModelIcons returns the icon of each custom model that has one.
//
// Decoded again on every listing, as a map background is: a file in the
// directory can be replaced by hand, and whether it draws is decided by the
// same gate either way.
func (a *App) ListSimulatorModelIcons() []SimulatorModelIcon {
	out := []SimulatorModelIcon{}
	s := a.sim
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.models {
		if uri, ok := s.iconOf(m.Slug()); ok {
			out = append(out, SimulatorModelIcon{ID: m.ID(), Icon: uri})
		}
	}
	return out
}

// iconOf is the icon kept for a model, as a data URI whose media type is what
// the DECODER said.
func (s *simulatorService) iconOf(slug string) (string, bool) {
	for _, ext := range []string{".png", ".jpg", ".gif"} {
		data, err := readAtMost(filepath.Join(s.modelDir, slug+ext), maxModelIconBytes)
		if err != nil {
			continue
		}
		meta, err := checkIcon(data)
		if err != nil {
			log.Printf("simulator: the icon of model %s: %v", slug, err)
			continue
		}
		return "data:" + meta.Media + ";base64," + base64.StdEncoding.EncodeToString(data), true
	}
	return "", false
}

// checkIcon is pkg/imagegate's decode, and a picker's bounds on top of it.
func checkIcon(data []byte) (imagegate.Info, error) {
	if len(data) > maxModelIconBytes {
		return imagegate.Info{}, fmt.Errorf("an icon is at most %d KB", maxModelIconBytes>>10)
	}
	meta, err := imagegate.Inspect(data)
	if err != nil {
		return meta, err
	}
	if meta.Width > maxModelIconSide || meta.Height > maxModelIconSide {
		return meta, fmt.Errorf("an icon is at most %d×%d pixels, and this one is %d×%d",
			maxModelIconSide, maxModelIconSide, meta.Width, meta.Height)
	}
	if _, ok := iconExtensions[meta.Format]; !ok {
		return meta, fmt.Errorf("%s is not a format an icon is kept in", meta.Format)
	}
	return meta, nil
}

// ImportSimulatorModelsDialog asks for model files — JSON, or ZIP archives with
// icons and packages — and imports them.
func (a *App) ImportSimulatorModelsDialog() ([]SimulatorModelImportResult, error) {
	paths, err := a.openFiles("Import simulator models", fileFilter("Simulator models (JSON, ZIP)", "*.json;*.zip"))
	if err != nil {
		return nil, err
	}
	// Cancelled is an empty list, never null: the caller reports each result.
	return a.importSimulatorModels(paths), nil
}

// importSimulatorModels imports each file, makes what the directory now holds
// the simulator's models, and restarts the running devices of a model that
// changed — as an edited device restarts, so that it answers as it now is.
func (a *App) importSimulatorModels(paths []string) []SimulatorModelImportResult {
	results := []SimulatorModelImportResult{}
	s := a.sim
	if s == nil {
		for _, p := range paths {
			results = append(results, SimulatorModelImportResult{File: filepath.Base(p), Error: errNoSimulator.Error(),
				Warnings: []SimulatorModelWarning{}})
		}
		return results
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range paths {
		results = append(results, s.importModelFile(p)...)
	}
	s.loadModels()
	for r := range results {
		a.restartModelDevicesLocked(s, &results[r])
	}
	a.emitSimulatorModelsChanged()
	return results
}

// restartModelDevicesLocked restarts the running devices of the model a result
// replaced, so that they answer as it now is — as an edited device restarts.
// s.mu is held.
func (a *App) restartModelDevicesLocked(s *simulatorService, res *SimulatorModelImportResult) {
	if !res.Success || !res.Replaced {
		return
	}
	for i, d := range s.devices {
		if d.Model != res.ModelID || !s.fleet.Stop(d.ID) {
			continue
		}
		if err := a.startSimulatedLocked(s, i); err != nil {
			res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "restartFailed", Detail: fmt.Sprintf("%s: %v", d.Name, err)})
		}
	}
}

// importModelFile imports what one picked file holds: a model, or an archive of
// them. What it IS is read from its first bytes, not from its name.
func (s *simulatorService) importModelFile(src string) []SimulatorModelImportResult {
	name := filepath.Base(src)
	fail := func(format string, args ...any) []SimulatorModelImportResult {
		return []SimulatorModelImportResult{{File: name, Error: fmt.Sprintf(format, args...), Warnings: []SimulatorModelWarning{}}}
	}
	data, err := readAtMost(src, maxModelArchiveBytes)
	if err != nil {
		return fail("%v", err)
	}
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return s.importModelArchive(name, data)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return fail("this is neither a model file (JSON) nor a ZIP archive")
	}
	// A lone file cannot bring an icon: the one it names would have to be read
	// from wherever the file came from, and a path a stranger's file chooses is
	// exactly what is never followed.
	skipped := func(icon string) (*modelIcon, *SimulatorModelWarning) {
		return nil, &SimulatorModelWarning{Key: "iconSkipped", Detail: icon}
	}
	return []SimulatorModelImportResult{s.importModel(name, data, skipped)}
}

// archive is a ZIP read within bounds: every entry through a limit, and what
// is unpacked counted as it is read.
type archive struct {
	files    []*zip.File
	unpacked int64
}

// openArchive lists what a ZIP holds, less its folders, dotfiles and the
// resource forks macOS adds.
func openArchive(data []byte) (*archive, error) {
	// An entry named ../x is harmless here, since no entry is written by its
	// name; Go reports one as ErrInsecurePath beside a reader that works.
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("this ZIP archive cannot be read: %v", err)
	}
	ar := &archive{}
	for _, f := range zr.File {
		base := path.Base(f.Name)
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX/") || strings.HasPrefix(base, ".") {
			continue
		}
		ar.files = append(ar.files, f)
	}
	if len(ar.files) > maxArchiveEntries {
		return nil, fmt.Errorf("the archive holds %d files, and an import reads at most %d", len(ar.files), maxArchiveEntries)
	}
	return ar, nil
}

// read unpacks one entry, refusing one that unpacks to more than limit.
func (ar *archive) read(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s unpacks to more than %d KB", path.Base(f.Name), limit>>10)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%s cannot be unpacked: %w", path.Base(f.Name), err)
	}
	defer rc.Close()
	// archive/zip stops at the size an entry declares, and the limit is the
	// bound on what it may declare.
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s cannot be unpacked: %w", path.Base(f.Name), err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s unpacks to more than %d KB", path.Base(f.Name), limit>>10)
	}
	if ar.unpacked += int64(len(b)); ar.unpacked > maxArchiveUnpacked {
		return nil, fmt.Errorf("the archive unpacks to more than %d MB", maxArchiveUnpacked>>20)
	}
	return b, nil
}

// exhausted reports whether the archive has unpacked all an import may.
func (ar *archive) exhausted() bool { return ar.unpacked > maxArchiveUnpacked }

// icon reads the icon a model names from the archive, through the gate; found
// is nil when the archive holds none of that name.
func (ar *archive) icon(found *zip.File, name string) (*modelIcon, *SimulatorModelWarning) {
	if found == nil {
		return nil, &SimulatorModelWarning{Key: "iconMissing", Detail: name}
	}
	data, err := ar.read(found, maxModelIconBytes)
	if err != nil {
		return nil, &SimulatorModelWarning{Key: "iconRefused", Detail: err.Error()}
	}
	meta, err := checkIcon(data)
	if err != nil {
		return nil, &SimulatorModelWarning{Key: "iconRefused", Detail: err.Error()}
	}
	return &modelIcon{data: data, ext: iconExtensions[meta.Format]}, nil
}

// packageFileLimit is how much one file of a package may unpack to: a walk is
// the recording of a whole agent, and the rest is JSON.
func packageFileLimit(name string) int64 {
	if strings.HasPrefix(strings.ToLower(name), "walks/") {
		return simulator.MaxWalkBytes
	}
	return simulator.MaxCustomModelBytes
}

// importModelArchive imports what a ZIP holds: every package — a folder that
// holds model.json, and everything under it — and every other model file,
// each with the icon it names from the same archive. Nothing in the archive is
// ever written under the name it has there.
func (s *simulatorService) importModelArchive(name string, data []byte) []SimulatorModelImportResult {
	fail := func(format string, args ...any) []SimulatorModelImportResult {
		return []SimulatorModelImportResult{{File: name, Error: fmt.Sprintf(format, args...), Warnings: []SimulatorModelWarning{}}}
	}
	ar, err := openArchive(data)
	if err != nil {
		return fail("%v", err)
	}
	// A file belongs to the deepest package folder above it, if one is.
	var roots []string
	for _, f := range ar.files {
		if root := path.Dir(f.Name); strings.EqualFold(path.Base(f.Name), simulator.PackageModelFile) && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	packageOf := func(file string) (string, bool) {
		best, found := "", false
		for _, r := range roots {
			if (r == "." || strings.HasPrefix(file, r+"/")) && (!found || len(r) > len(best)) {
				best, found = r, true
			}
		}
		return best, found
	}

	results := []SimulatorModelImportResult{}
	imported := map[string]bool{}
	for _, f := range ar.files {
		if ar.exhausted() {
			break
		}
		root, inPackage := packageOf(f.Name)
		switch {
		case inPackage && !imported[root]:
			imported[root] = true
			var members []*zip.File
			for _, g := range ar.files {
				if r, ok := packageOf(g.Name); ok && r == root {
					members = append(members, g)
				}
			}
			results = append(results, s.importPackage(name, ar, root, members))
		case !inPackage && strings.EqualFold(path.Ext(f.Name), ".json"):
			results = append(results, s.importArchivedModel(name, ar, f))
		}
	}
	if len(results) == 0 {
		return fail("the archive holds no model: a model is a .json file in it, or a folder holding model.json")
	}
	return results
}

// importArchivedModel imports a lone model file of an archive, with the icon
// it names — looked for beside it first, since two models of one archive may
// each call theirs icon.png.
func (s *simulatorService) importArchivedModel(archiveName string, ar *archive, f *zip.File) SimulatorModelImportResult {
	entry := archiveName + " › " + clipText(f.Name, 120)
	raw, err := ar.read(f, simulator.MaxCustomModelBytes)
	if err != nil {
		return SimulatorModelImportResult{File: entry, Error: err.Error(), Warnings: []SimulatorModelWarning{}}
	}
	dir := path.Dir(f.Name)
	lookup := func(icon string) (*modelIcon, *SimulatorModelWarning) {
		var found *zip.File
		for _, g := range ar.files {
			if path.Base(g.Name) == icon && (found == nil || path.Dir(g.Name) == dir) {
				found = g
			}
		}
		return ar.icon(found, icon)
	}
	return s.importModel(entry, bytes.TrimPrefix(raw, utf8BOM), lookup)
}

// importPackage imports the package in one folder of an archive: the files it
// reads, and the icon its model.json names, looked for beside model.json first
// and then anywhere in the folder. A JSON file the package does not read is
// named rather than passed over: traps.json misspelt would otherwise leave a
// device with no notifications, and nothing saying why.
func (s *simulatorService) importPackage(archiveName string, ar *archive, root string, members []*zip.File) SimulatorModelImportResult {
	res := SimulatorModelImportResult{File: archiveName + " › " + clipText(path.Join(root, simulator.PackageModelFile), 120),
		Warnings: []SimulatorModelWarning{}}
	inPackage := func(f *zip.File) string {
		if root == "." {
			return f.Name
		}
		return strings.TrimPrefix(f.Name, root+"/")
	}
	var files []simulator.PackageFile
	for _, f := range members {
		rel := inPackage(f)
		if !simulator.PackageReads(rel) {
			if strings.EqualFold(path.Ext(rel), ".json") {
				res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: "packageIgnored", Detail: clipText(rel, 120)})
			}
			continue
		}
		data, err := ar.read(f, packageFileLimit(rel))
		if err != nil {
			res.Error = err.Error()
			return res
		}
		files = append(files, simulator.PackageFile{Name: rel, Data: data})
	}
	m, warnings, err := simulator.ParseCustomPackage(files)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	for _, w := range warnings {
		res.Warnings = append(res.Warnings, SimulatorModelWarning{Key: w.Key, Detail: w.Detail})
	}
	packed, err := repack(files)
	if err != nil {
		res.Error = fmt.Sprintf("could not pack the package: %v", err)
		return res
	}
	lookup := func(icon string) (*modelIcon, *SimulatorModelWarning) {
		var found *zip.File
		for _, f := range members {
			if path.Base(f.Name) == icon && (found == nil || inPackage(f) == icon) {
				found = f
			}
		}
		return ar.icon(found, icon)
	}
	return s.keepModel(res, m, lookup, keptPackage, packed)
}

// repack is the ZIP a package is kept as: the files it reads and nothing else,
// by their names in the package.
func repack(files []simulator.PackageFile) ([]byte, error) { return repackUnder("", files) }

// repackUnder is a ZIP of a package's files, in a folder of that name when one
// is given: the shape a package is exported in.
func repackUnder(folder string, files []simulator.PackageFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: path.Join(folder, f.Name), Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// importModel checks one lone model file and keeps it.
func (s *simulatorService) importModel(file string, raw []byte, iconFor iconLookup) SimulatorModelImportResult {
	res := SimulatorModelImportResult{File: file, Warnings: []SimulatorModelWarning{}}
	m, err := simulator.ParseCustomModel(raw)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	return s.keepModel(res, m, iconFor, keptFile, raw)
}

// keepModel keeps a model that passed its checks, in the form it came in,
// under the model's own id, with the icon it names when iconFor finds one. A
// model imported without an icon keeps the one it had: someone editing the
// JSON alone should not lose what the archive brought. A model kept in the
// other form is replaced by this one.
func (s *simulatorService) keepModel(res SimulatorModelImportResult, m simulator.CustomModel, iconFor iconLookup,
	form string, data []byte) SimulatorModelImportResult {
	res.ModelID, res.Name = m.ID(), m.Name()
	keptAs := filepath.Join(s.modelDir, m.Slug()+form)
	other := filepath.Join(s.modelDir, m.Slug()+otherForm(form))
	if isFile(keptAs) || isFile(other) {
		res.Replaced = true
	} else if s.countModelFiles() >= maxCustomModels {
		res.Error = fmt.Sprintf("SnmpLens keeps at most %d custom models", maxCustomModels)
		return res
	}
	if err := os.MkdirAll(s.modelDir, 0o755); err != nil {
		res.Error = fmt.Sprintf("could not create the model directory: %v", err)
		return res
	}

	var icon *modelIcon
	var warning *SimulatorModelWarning
	if m.Icon() != "" {
		icon, warning = iconFor(m.Icon())
	}
	// The icon before the model: a model kept is one the picker shows, and it
	// should not show without the icon it came with.
	if icon != nil {
		for _, ext := range iconExtensions {
			if ext != icon.ext {
				_ = os.Remove(filepath.Join(s.modelDir, m.Slug()+ext))
			}
		}
		if err := writeFileAtomic(filepath.Join(s.modelDir, m.Slug()+icon.ext), icon.data); err != nil {
			res.Error = fmt.Sprintf("could not keep the icon: %v", err)
			return res
		}
		res.Icon = true
	} else if _, kept := s.iconOf(m.Slug()); kept {
		res.Icon = true
		if warning != nil && warning.Key == "iconSkipped" {
			warning = nil
		}
	}
	if warning != nil {
		res.Warnings = append(res.Warnings, *warning)
	}
	if err := writeFileAtomic(keptAs, data); err != nil {
		res.Error = fmt.Sprintf("could not keep the model: %v", err)
		return res
	}
	// Removed once the new form is written: stopped in between, the directory
	// holds both, and loadModels takes the newer.
	if err := os.Remove(other); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("simulator: model %s: the %s it replaces stays: %v", m.Slug(), filepath.Base(other), err)
	}
	res.Success = true
	return res
}

// otherForm is the form a model is not kept in when it is kept in form.
func otherForm(form string) string {
	if form == keptFile {
		return keptPackage
	}
	return keptFile
}

func isFile(name string) bool {
	st, err := os.Stat(name)
	return err == nil && !st.IsDir()
}

// countModelFiles is how many models the directory holds now, imports of this
// round included.
func (s *simulatorService) countModelFiles() int {
	entries, err := os.ReadDir(s.modelDir)
	if err != nil {
		return 0
	}
	slugs := map[string]bool{}
	for _, e := range entries {
		if form := filepath.Ext(e.Name()); !e.IsDir() && (form == keptFile || form == keptPackage) {
			slugs[strings.TrimSuffix(e.Name(), form)] = true
		}
	}
	return len(slugs)
}

// SimulatorDeleteModel deletes a custom model and its icon.
//
// A model a device is made from is not deleted: the device would not start
// again, and nothing would say why until somebody tried.
func (a *App) SimulatorDeleteModel(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	slug, ok := simulator.CustomModelSlug(id)
	if !ok {
		return fmt.Errorf("%q is not a custom model", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var users []string
	for _, d := range s.devices {
		if d.Model == id {
			users = append(users, d.Name)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("%d simulated device(s) are made from this model (%s): delete them first",
			len(users), strings.Join(users, ", "))
	}
	for _, ext := range []string{keptFile, keptPackage, ".png", ".jpg", ".gif"} {
		if err := os.Remove(filepath.Join(s.modelDir, slug+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("deleting the model: %w", err)
		}
	}
	s.loadModels()
	a.emitSimulatorModelsChanged()
	return nil
}

// readAtMost reads a file, refusing one larger than limit: checked on the open
// file, and again while reading, since it can grow in between.
func readAtMost(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", filepath.Base(name))
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is %d KB, and at most %d KB is read", filepath.Base(name), st.Size()>>10, limit>>10)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d KB", filepath.Base(name), limit>>10)
	}
	return data, nil
}

// writeFileAtomic replaces a file through a temporary one, so that a crash
// mid-write never leaves half of one.
func writeFileAtomic(name string, data []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// clipText shortens what an archive calls an entry to what a result line can
// show.
func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
