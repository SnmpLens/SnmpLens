package app

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"SnmpLens/pkg/mib"
)

// Importing MIB files into the persistent directory.
//
// An archive of MIBs can hold one folder per release with the same file name in
// each, and file names that say what a product is called rather than which
// module is inside. The import used to copy every file to the MIB directory
// under its own name, so a dropped folder holding several releases of one file
// kept whichever was written last and reported a success for each; two
// different modules under one file name overwrote each other; and a module
// stored under a product name was invisible to every module importing it.
//
// So a file is stored under the name of the module it DECLARES, and a module is
// stored once: of several versions in one import, one is kept (mib.Preferred —
// one that parses, then the newest, then the shallower folder) and every other
// is reported as not imported, saying which was and why. A module the directory
// already holds is replaced, the previous file backed up to mib-backups.

// maxImportBytes bounds what ImportMibFiles will read into memory. The largest
// MIBs in circulation are a couple of megabytes.
const maxImportBytes = 16 << 20

// MibImportResult is one file's outcome.
type MibImportResult struct {
	// FileName is the file as it was dropped, relative to the folder dropped,
	// so two files of one name in two folders are told apart.
	FileName string `json:"fileName"`
	Success  bool   `json:"success"`
	// Skipped is a file that was not written and needed not be: an identical
	// copy, or another version of a module this import kept.
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// Module is what the file declares.
	Module string `json:"module,omitempty"`
	// StoredAs is its name in the MIB directory, which is what a load result
	// names it by.
	StoredAs string `json:"storedAs,omitempty"`
	// Replaced is the file it replaced, backed up first.
	Replaced string `json:"replaced,omitempty"`
	// Note says what happened when it was not a plain copy or a plain "already
	// there": another version kept, a module replaced.
	Note string `json:"note,omitempty"`

	// Outcome is what happened, for an interface that groups the results by
	// module and says it in the reader's language (ImportOutcome*). The
	// sentences above stay for the log and for anything that only prints.
	Outcome string `json:"outcome"`
	// Reason refines Outcome: the kind of file ignored (mib.NonMib*), why a
	// version was set aside (mib.Lost*), what went wrong (ImportError*).
	Reason string `json:"reason,omitempty"`
	// KeptFrom is, for a version set aside, the file of the version kept.
	KeptFrom string `json:"keptFrom,omitempty"`
	// Date is the version's own LAST-UPDATED or latest REVISION, YYYY-MM-DD,
	// when it states one.
	Date string `json:"date,omitempty"`
	// Source is the file's own path, as it was dropped: the import report
	// offers to import a version set aside instead of the one kept, and that
	// is the file it imports. Only a path the renderer handed over, given back.
	Source string `json:"source,omitempty"`
}

// What an import did with one file.
const (
	ImportOutcomeImported   = "imported"   // written; the module was not in the directory
	ImportOutcomeReplaced   = "replaced"   // written over another version, which was backed up
	ImportOutcomePresent    = "present"    // the directory already holds this very version
	ImportOutcomeSetAside   = "setaside"   // another version of the module in this import was kept
	ImportOutcomeIgnored    = "ignored"    // not a MIB
	ImportOutcomeError      = "error"      // a MIB that could not be imported
	ImportErrorBundled      = "bundled"    // it would replace a module SnmpLens ships
	ImportErrorTooLarge     = "toolarge"   // over maxImportBytes
	ImportErrorUnreadable   = "unreadable" // the file could not be read
	ImportErrorNameConflict = "conflict"   // its file name holds another module
	ImportErrorWrite        = "write"      // the directory could not be written
)

// importCandidate is one file that passed the gate.
type importCandidate struct {
	index   int // its place in the results
	src     string
	rel     string
	depth   int
	base    string
	raw     []byte
	content string
	module  string
}

// ImportMibFiles copies the given files (or directories, recursively) into the
// persistent MIB directory, one result per file found.
func (a *App) ImportMibFiles(filePaths []string) []MibImportResult {
	var results []MibImportResult
	var cands []*importCandidate

	add := func(src, rel string) {
		res, c := a.readImportCandidate(src, rel)
		res.FileName = filepath.ToSlash(rel)
		res.Source = src
		results = append(results, res)
		if c != nil {
			c.index = len(results) - 1
			cands = append(cands, c)
		}
	}

	for _, src := range filePaths {
		info, err := os.Stat(src)
		if err != nil {
			results = append(results, MibImportResult{
				FileName: filepath.Base(src),
				Error:    fmt.Sprintf("stat error: %v", err),
				Outcome:  ImportOutcomeError, Reason: ImportErrorUnreadable,
			})
			continue
		}
		if !info.IsDir() {
			add(src, filepath.Base(src))
			continue
		}
		// Relative to the folder's PARENT, so its own name leads every path.
		parent := filepath.Dir(src)
		_ = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(parent, path)
			if relErr != nil {
				rel = filepath.Base(path)
			}
			add(path, rel)
			return nil
		})
	}

	// One module, one file: group by what the files declare.
	var order []string
	groups := map[string][]*importCandidate{}
	for _, c := range cands {
		key := c.module
		if key == "" {
			// Nothing declared that the head could read: kept by file name, as
			// every file used to be.
			key = "\x00" + c.base
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}

	held := a.modulesInDirectory()
	for _, key := range order {
		group := groups[key]
		// The last tie-break Preferred applies is this order. The shallower
		// folder first: a file at the top of an archive is the current one more
		// often than one three folders down. Then the later path, numbers read
		// as numbers: LAST-UPDATED is often left unchanged between releases,
		// and a folder "1.10" is newer than "1.9".
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].depth != group[j].depth {
				return group[i].depth < group[j].depth
			}
			return mib.NaturalLess(group[j].rel, group[i].rel)
		})
		// Identical copies are one version: comparing a file with its own copy
		// decides nothing.
		var distinct []*importCandidate
		for _, c := range group {
			dup := false
			for _, d := range distinct {
				dup = dup || d.content == c.content
			}
			if !dup {
				distinct = append(distinct, c)
			}
		}
		kept := distinct[0]
		why := map[*importCandidate]mib.Lost{}
		if len(distinct) > 1 {
			mc := make([]mib.Candidate, len(distinct))
			for i, c := range distinct {
				mc[i] = mib.Candidate{Name: c.rel, Content: c.content}
			}
			best, reasons := mib.Preferred(mc)
			kept = distinct[best]
			for i, c := range distinct {
				why[c] = reasons[i]
			}
		}
		results[kept.index] = a.storeImport(kept, held)
		results[kept.index].Source = kept.src
		for _, c := range group {
			if c == kept {
				continue
			}
			lost := why[c]
			for _, d := range distinct {
				if d.content == c.content && why[d].Code != "" {
					lost = why[d] // a copy of a version that lost, for the same reason
				}
			}
			note := fmt.Sprintf("another version of %s in this import was kept, %s, because %s",
				c.module, filepath.ToSlash(kept.rel), lost.Why)
			if c.content == kept.content {
				note = fmt.Sprintf("an identical copy of %s", filepath.ToSlash(kept.rel))
				lost.Code = mib.LostIdentical
			}
			results[c.index] = MibImportResult{
				FileName: filepath.ToSlash(c.rel), Success: true, Skipped: true,
				Module: c.module, StoredAs: results[kept.index].StoredAs, Note: note,
				Outcome: ImportOutcomeSetAside, Reason: lost.Code,
				KeptFrom: filepath.ToSlash(kept.rel), Date: dateOf(c.content), Source: c.src,
			}
		}
	}
	return results
}

// dateOf is the date a module states about itself, or "" when it states none.
func dateOf(content string) string {
	if d := mib.RevisionDate(content); !d.IsZero() {
		return d.Format("2006-01-02")
	}
	return ""
}

// readImportCandidate reads one file and says whether it may be imported.
func (a *App) readImportCandidate(src, rel string) (MibImportResult, *importCandidate) {
	name := filepath.Base(src)

	// Bounded before it is read. os.ReadFile on a renderer-named path is an
	// unbounded allocation, so a path to a large file — or to a device that
	// never ends — is a way to take the process down without sending a single
	// packet. The largest MIB anyone ships is a couple of megabytes; the
	// benchmark in pkg/mib uses 185 KB.
	if info, err := os.Stat(src); err == nil && info.Size() > maxImportBytes {
		return MibImportResult{Error: fmt.Sprintf("this file is %d MB; a MIB is text and nothing legitimate is over %d MB",
			info.Size()/(1<<20), maxImportBytes/(1<<20)), Outcome: ImportOutcomeError, Reason: ImportErrorTooLarge}, nil
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		log.Printf("ImportMibFiles: failed to read %s: %v", src, err)
		return MibImportResult{Error: fmt.Sprintf("read error: %v", err), Outcome: ImportOutcomeError, Reason: ImportErrorUnreadable}, nil
	}

	// Only a MIB gets into the MIB directory.
	//
	// This is a security boundary as much as a usability one. This method reads
	// any absolute path the renderer names, and MibEditorRead hands the content
	// of anything in that directory back — so the two calls together are an
	// arbitrary-file-read primitive over the bridge. What decides how much that
	// is worth is what is allowed to land here. A private key, a password file,
	// a browser profile: not a MIB, not admitted, never readable.
	//
	// It is NOT a validity check: a MIB with a syntax error is precisely what
	// the diagnosis feature exists for. The refusal reuses pkg/mib's own
	// description of the files people download by mistake, so an HTML page or a
	// PDF is still named as what it is — now without leaving a copy behind.
	if kind, summary, hint, reject := mib.ClassifyImport(raw); reject {
		msg := summary
		if hint != "" {
			msg += " — " + hint
		}
		log.Printf("ImportMibFiles: refused %s (%s)", name, summary)
		return MibImportResult{Error: msg, Outcome: ImportOutcomeIgnored, Reason: kind}, nil
	}

	content, _, _ := mib.DecodeSource(raw)
	module := mib.ModuleName(content)

	// A standard MIB that ships in the binary is never overwritten from here —
	// by its MODULE, whatever the file is called.
	//
	// This is not an exotic case: vendor archives routinely include their own
	// SNMPv2-SMI, SNMPv2-TC and SNMPv2-CONF, and nearly every other MIB imports
	// from those three. Replacing one with a copy that differs or is truncated
	// breaks the tree for everything, and it STAYS broken across restarts,
	// because ensureStandardMibs restores only files that are ABSENT — a
	// replaced one is never repaired. It used to be refused by FILE name only,
	// so the same SNMPv2-SMI arriving as snmpv2-smi.txt went in beside the
	// bundled one, and the module map being last-wins, replaced it anyway.
	//
	// Refused rather than backed up: the MIB editor already owns deliberate
	// change to a bundled file, with its own backup directory and
	// MibEditorRestoreBundled to undo it. An import is a bulk operation nobody
	// reads the results of unless something goes wrong, which is exactly when
	// this needs to be loud.
	for _, bundled := range []string{name, module} {
		if bundled == "" {
			continue
		}
		if _, ok := a.bundledContent(bundled); ok {
			log.Printf("ImportMibFiles: refused to replace the bundled %s", bundled)
			return MibImportResult{Module: module, Error: fmt.Sprintf(
				"%s ships with SnmpLens and nearly every MIB imports from it; "+
					"replacing it would break MIB resolution. Use the MIB editor if you really mean to change it.", bundled),
				Outcome: ImportOutcomeError, Reason: ImportErrorBundled}, nil
		}
	}

	return MibImportResult{}, &importCandidate{
		src: src, rel: rel, depth: strings.Count(filepath.ToSlash(rel), "/"),
		base: name, raw: raw, content: content, module: module,
	}
}

// modulesInDirectory maps every module the MIB directory holds to the files
// declaring it.
func (a *App) modulesInDirectory() map[string][]string {
	out := map[string][]string{}
	names, err := mib.ListMibFiles(a.persistentMibDir)
	if err != nil {
		return out
	}
	for _, n := range names {
		path, err := mib.SafeMibPath(a.persistentMibDir, n)
		if err != nil {
			continue
		}
		if module := mib.DeclaredModule(path); module != "" {
			out[module] = append(out[module], n)
		}
	}
	return out
}

// storeImport writes the kept version of a module, replacing whatever file the
// directory held for it.
func (a *App) storeImport(c *importCandidate, held map[string][]string) MibImportResult {
	res := MibImportResult{FileName: filepath.ToSlash(c.rel), Module: c.module, Date: dateOf(c.content)}
	fail := func(reason, format string, args ...any) MibImportResult {
		res.Success, res.Skipped = false, false
		res.Outcome, res.Reason = ImportOutcomeError, reason
		res.Error = fmt.Sprintf(format, args...)
		return res
	}
	dest := mib.FileNameFor(c.module, c.base)
	dst, err := mib.SafeMibPath(a.persistentMibDir, dest)
	if err != nil {
		return fail(ImportErrorNameConflict, "%v", err)
	}
	res.StoredAs = filepath.Base(dst)

	// A file of that name holding ANOTHER module is not ours to overwrite.
	if info, err := os.Stat(dst); err == nil && !info.IsDir() {
		if other := mib.DeclaredModule(dst); other != c.module {
			if other != "" {
				res.StoredAs = ""
				return fail(ImportErrorNameConflict, "the MIB directory already has a file named %s, holding %s; rename one of them", dest, other)
			}
		} else if c.module == "" {
			// Nothing declared to compare by: the bytes, as it always was.
			if raw, err := os.ReadFile(dst); err == nil && string(raw) == string(c.raw) {
				res.Success, res.Skipped, res.Outcome = true, true, ImportOutcomePresent
				return res
			}
		}
	}

	var replaced []string
	for _, f := range held[c.module] {
		path, err := mib.SafeMibPath(a.persistentMibDir, f)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		before, _, _ := mib.DecodeSource(raw)
		if before == c.content && f == res.StoredAs {
			log.Printf("ImportMibFiles: skipped %s (already present as %s)", c.rel, f)
			res.Success, res.Skipped, res.Outcome = true, true, ImportOutcomePresent
			return res
		}
		if _, err := a.backupMib(f, raw); err != nil {
			return fail(ImportErrorWrite, "could not back up %s before replacing it: %v", f, err)
		}
		if f != res.StoredAs {
			if err := os.Remove(path); err != nil {
				return fail(ImportErrorWrite, "could not remove %s, which also holds %s: %v", f, c.module, err)
			}
		}
		replaced = append(replaced, f)
		if before == c.content {
			res.Note = fmt.Sprintf("already in the MIB directory as %s; now stored under the name of the module it declares", f)
			continue
		}
		if previous := mib.RevisionDate(before); previous.After(mib.RevisionDate(c.content)) {
			res.Note = fmt.Sprintf("replaced a NEWER version (last updated %s); it is in mib-backups", previous.Format("2006-01-02"))
		}
	}

	if err := os.WriteFile(dst, c.raw, 0o644); err != nil {
		log.Printf("ImportMibFiles: failed to write %s: %v", dst, err)
		return fail(ImportErrorWrite, "write error: %v", err)
	}
	held[c.module] = []string{res.StoredAs}

	res.Success, res.Outcome = true, ImportOutcomeImported
	if len(replaced) > 0 {
		res.Outcome = ImportOutcomeReplaced
		res.Replaced = strings.Join(replaced, ", ")
		if res.Note == "" {
			res.Note = "replaced the version already in the MIB directory; it is in mib-backups"
		}
	}
	log.Printf("ImportMibFiles: imported %s as %s", c.rel, res.StoredAs)
	return res
}
