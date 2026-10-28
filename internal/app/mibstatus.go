package app

import (
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"SnmpLens/pkg/mib"
)

// Every MIB file's verdict (pkg/mib/verdict.go), for the editor's file list,
// the MIB settings and the import report alike.
//
// The verdict joins what the last load did with a file and what the editor's
// analysis finds in it. Both halves used to reach the renderer separately and
// be joined there, by each screen for itself: the editor's list marked a file
// in error while the MIB settings counted it loaded, and the file open in the
// editor was judged by a quick syntax check until its full analysis answered.
// They are joined here now, once, and every screen reads the same answer.
//
// The analyses are cached per file by size and modification time, and dropped
// whole when the loaded set of modules changes, since an import that was
// missing may now be satisfied. A directory of a hundred vendor MIBs costs a
// second or two the first time and nothing after.

// MibFileStatus is one file's verdict.
type MibFileStatus struct {
	Name string `json:"name"`
	// Loaded is whether the last load included the file. The MIB settings list
	// those; the editor's list shows every file in the directory.
	Loaded  bool        `json:"loaded"`
	Verdict mib.Verdict `json:"verdict"`
}

type mibAnalysisEntry struct {
	size    int64
	modTime time.Time
	diags   []mib.Diagnostic
}

type mibStatusCache struct {
	mu        sync.Mutex
	catalogue string
	entries   map[string]mibAnalysisEntry
	// loads is what the last load did with each file, by file name.
	loads map[string]mib.MibLoadResult
}

// rememberLoads keeps what a load did, for the verdicts that follow it.
func (c *mibStatusCache) rememberLoads(results []mib.MibLoadResult) {
	loads := make(map[string]mib.MibLoadResult, len(results))
	for _, r := range results {
		loads[r.FileName] = r
	}
	c.mu.Lock()
	c.loads = loads
	c.mu.Unlock()
}

func (c *mibStatusCache) load(name string) (mib.MibLoadResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.loads[name]
	return r, ok
}

// MibFileStatuses gives the verdict on every file in the MIB directory, and on
// every file the last load read from anywhere else.
func (a *App) MibFileStatuses() []MibFileStatus {
	names, err := mib.ListMibFiles(a.persistentMibDir)
	if err != nil {
		names = nil
	}
	cat := mib.Symbols()
	key := strings.Join(cat.Modules, "\x00")

	// The analyses run outside the lock: the first pass over a large directory
	// takes seconds, and the editor judges the buffer being typed through the
	// same cache on every pause.
	c := &a.mibStatuses
	c.mu.Lock()
	if c.entries == nil || c.catalogue != key {
		c.entries = map[string]mibAnalysisEntry{}
		c.catalogue = key
	}
	known := make(map[string]mibAnalysisEntry, len(c.entries))
	for name, e := range c.entries {
		known[name] = e
	}
	loads := c.loads
	c.mu.Unlock()

	out := make([]MibFileStatus, 0, len(names)+len(loads))
	fresh := make(map[string]mibAnalysisEntry, len(names))
	for _, name := range names {
		path, err := mib.SafeMibPath(a.persistentMibDir, name)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		e, ok := known[name]
		if !ok || e.size != info.Size() || !e.modTime.Equal(info.ModTime()) {
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			content, _, _ := mib.DecodeSource(raw)
			e = mibAnalysisEntry{size: info.Size(), modTime: info.ModTime(),
				diags: mib.AnalyseAll(content, cat).Diagnostics}
		}
		fresh[name] = e
		status := MibFileStatus{Name: name, Verdict: mib.Judge(nil, e.diags)}
		if load, ok := loads[name]; ok {
			status.Loaded = true
			status.Verdict = mib.Judge(&load, e.diags)
		}
		out = append(out, status)
	}
	// A file loaded from a directory of the user's own is judged by its load.
	var elsewhere []string
	for name := range loads {
		if _, ok := fresh[name]; !ok {
			elsewhere = append(elsewhere, name)
		}
	}
	sort.Strings(elsewhere)
	for _, name := range elsewhere {
		load := loads[name]
		out = append(out, MibFileStatus{Name: name, Loaded: true, Verdict: mib.Judge(&load, nil)})
	}

	c.mu.Lock()
	// Files that are gone take their entries with them; a catalogue that moved
	// on meanwhile makes these analyses stale, and they are not kept.
	if c.catalogue == key {
		c.entries = fresh
	}
	c.mu.Unlock()
	return out
}

// judgeBuffer is the verdict on text being edited as the directory's file
// name, from its analysis.
//
// The last load speaks for the file as it is ON DISK. While the buffer is that
// text, the load counts in full: a module gosmi refused is failed whatever the
// analysis thinks of it. Once the buffer differs, the load describes another
// text and only the analysis does — except a refusal because another file holds
// the module, which no edit of this text undoes.
func (a *App) judgeBuffer(name, content string, an mib.Analysis) mib.Verdict {
	if name == "" {
		return an.Verdict
	}
	load, ok := a.mibStatuses.load(name)
	if !ok {
		return an.Verdict
	}
	if load.IsDuplicate() || a.isOnDisk(name, content) {
		return mib.Judge(&load, an.Diagnostics)
	}
	return an.Verdict
}

// isOnDisk is whether content is the directory's file name as it reads now.
func (a *App) isOnDisk(name, content string) bool {
	path, err := a.resolveMibPath(name)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	disk, _, _ := mib.DecodeSource(raw)
	return disk == content
}

// analyseAs is the editor's analysis of content, judged as the directory's
// file name ("" for a file from elsewhere).
func (a *App) analyseAs(name, content string) mib.Analysis {
	an := mib.AnalyseAll(content, mib.Symbols())
	an.Verdict = a.judgeBuffer(name, content, an)
	return an
}
