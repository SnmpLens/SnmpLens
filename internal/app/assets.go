package app

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"SnmpLens/pkg/imagegate"

)

// Map backgrounds: the pictures a map widget draws over.
//
// A SIBLING of mibs/ and presets/, never a child of either. ListMibFiles feeds
// what it finds in the MIB directory to gosmi, and preset.List parses what it
// finds in the preset directory as JSON — a PNG in either is a broken MIB or a
// broken preset in a list nobody can use. mib-backups/, mib-drafts/ and
// mib-temp/ are siblings for the same reason and this follows them.
//
// The preset carries a NAME and the operator supplies the file. That split is
// the whole security posture: a preset that carried image bytes would be an
// arbitrary blob this application decodes, and a preset that carried a path
// would be an arbitrary-file-read primitive over the bridge. What a stranger's
// file can do here is ask for a name the operator has already accepted.

const assetSubdir = "assets"

// AssetInfo is one background in the library.
type AssetInfo struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
	// Error is why a file in the directory cannot be shown. It is listed WITH
	// its problem rather than hidden, for the reason a broken preset is: you
	// cannot fix a file you were not told about.
	Error string `json:"error,omitempty"`
}

// AssetImportResult is one file's outcome, shaped like PresetImportResult
// because it is shown in the same kind of list.
type AssetImportResult struct {
	FileName string `json:"fileName"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// assetDir returns the background directory, creating it on first use.
func (a *App) assetDir() (string, error) {
	if a.persistentMibDir == "" {
		return "", fmt.Errorf("the configuration directory is not ready")
	}
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), assetSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create the background directory: %w", err)
	}
	return dir, nil
}

// resolveAssetPath turns a name the renderer or a preset supplied into a path
// inside the background directory, or refuses.
//
// The same shape as resolvePresetPath and mib.SafeMibPath, and for the same
// reason: monitoring.db, service.json and the secret store all sit one
// directory above this one, and every method here takes a name from outside.
func (a *App) resolveAssetPath(name string) (string, error) {
	if err := imagegate.ValidName(name); err != nil {
		return "", err
	}
	dir, err := a.assetDir()
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, filepath.Base(name))
	if filepath.Dir(full) != filepath.Clean(dir) {
		return "", fmt.Errorf("refusing a background outside the background directory: %q", name)
	}
	return full, nil
}

// ListMapAssets returns the backgrounds on this machine.
//
// Named List… deliberately: tools/genbridge.mjs gives anything starting with
// List an empty ARRAY as its screenshot fixture, and a name it does not
// recognise gets null — which throws on the first .map in a generated file
// nobody reads.
func (a *App) ListMapAssets() []AssetInfo {
	dir, err := a.assetDir()
	if err != nil {
		log.Printf("ListMapAssets: %v", err)
		return []AssetInfo{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("ListMapAssets: %v", err)
		return []AssetInfo{}
	}

	out := make([]AssetInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info := AssetInfo{Name: e.Name()}
		if st, err := e.Info(); err == nil {
			info.Bytes = st.Size()
		}
		// Re-inspected on every listing rather than trusted because it is in
		// the directory: a file can be dropped in by hand, and the answer to
		// "will this draw" has to be the same one the renderer will get.
		data, err := readBounded(filepath.Join(dir, e.Name()))
		if err != nil {
			info.Error = err.Error()
		} else if meta, err := imagegate.Inspect(data); err != nil {
			info.Error = err.Error()
		} else {
			info.Width, info.Height, info.Format = meta.Width, meta.Height, meta.Format
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// ImportMapAssets copies image files into the background directory.
//
// The gate is a DECODE, not an extension: see pkg/imagegate. A file that does not
// pass is not copied at all — which is the difference from the preset gate,
// where a file with problems is kept precisely so its problem list can be read.
// There is nothing to read here: an image either draws or it does not.
func (a *App) ImportMapAssets(paths []string) []AssetImportResult {
	results := make([]AssetImportResult, 0, len(paths))
	for _, src := range paths {
		results = append(results, a.importSingleAsset(src))
	}
	return results
}

func (a *App) importSingleAsset(src string) AssetImportResult {
	name := filepath.Base(src)
	fail := func(format string, args ...any) AssetImportResult {
		return AssetImportResult{FileName: name, Success: false, Error: fmt.Sprintf(format, args...)}
	}

	dst, err := a.resolveAssetPath(name)
	if err != nil {
		return fail("%v", err)
	}
	data, err := readBounded(src)
	if err != nil {
		return fail("%v", err)
	}
	meta, err := imagegate.Inspect(data)
	if err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		log.Printf("ImportMapAssets: could not write %s: %v", dst, err)
		return fail("write error: %v", err)
	}
	return AssetImportResult{
		FileName: name, Success: true, Width: meta.Width, Height: meta.Height,
	}
}

// ReadMapAsset returns one background as a data URI, ready for an <img>.
//
// A data URI rather than a file path or a served URL: the renderer has no
// filesystem, and giving the WebView an origin that reads the configuration
// directory would be a larger hole than the one this closes. The cost is that
// the picture crosses the bridge base64-encoded, a third larger than the file,
// which is what imagegate.MaxBytes is really bounding.
//
// The media type is what the DECODER said, never what the name said: a file
// called rack.png that is a GIF is served as a GIF, so the browser is never
// asked to sniff.
func (a *App) ReadMapAsset(name string) (string, error) {
	path, err := a.resolveAssetPath(name)
	if err != nil {
		return "", err
	}
	data, err := readBounded(path)
	if err != nil {
		return "", err
	}
	meta, err := imagegate.Inspect(data)
	if err != nil {
		return "", err
	}
	return "data:" + meta.Media + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// DeleteMapAsset removes one background.
//
// Dashboards already bound are untouched, the way deleting a preset leaves
// bound sessions polling: what they draw simply loses its picture, and the
// shapes stay. A map with no background is a legitimate drawing, so there is
// nothing here to fail.
func (a *App) DeleteMapAsset(name string) error {
	path, err := a.resolveAssetPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// readBounded reads a file the renderer named, refusing one too large to hold.
//
// os.ReadFile on a renderer-supplied path is an unbounded allocation: a path to
// a large file — or to a device that never ends — is a way to take the process
// down without a single packet. The same guard importSinglePreset uses, and it
// STATS first so the refusal is about the file rather than about the memory.
func readBounded(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", filepath.Base(path))
	}
	if st.Size() > imagegate.MaxBytes {
		return nil, fmt.Errorf("this file is %d KB; a background is at most %d KB",
			st.Size()/1024, imagegate.MaxBytes/1024)
	}
	return os.ReadFile(path)
}

// ImportMapAssetDialog is the file picker for backgrounds.
//
// The filter names the three formats the gate actually accepts, which is a
// courtesy and not the gate: a dialog filter is what the operating system
// offers, and the decision is the DECODE in ImportMapAssets. Somebody who types
// a name past the filter gets the same answer as somebody who picks one.
func (a *App) ImportMapAssetDialog() ([]AssetImportResult, error) {
	paths, err := a.openFiles("Add map backgrounds", fileFilter("Images", "*.png;*.jpg;*.jpeg;*.gif"))
	if err != nil {
		return nil, err
	}
	// Cancelled. An empty ARRAY rather than nil: the caller renders a result
	// list, and null throws on .map.
	return a.ImportMapAssets(paths), nil
}
