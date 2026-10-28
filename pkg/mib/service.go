package mib

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/sleepinggenius2/gosmi"
	"github.com/sleepinggenius2/gosmi/types"
)

// Node represents a node in the MIB tree.
type Node struct {
	Name        string           `json:"name"`
	Oid         string           `json:"oid"`
	Description string           `json:"description"`
	Children    []*Node          `json:"children"`
	MibType     string           `json:"mibType"`
	Syntax      string           `json:"syntax"`
	Access      string           `json:"access"`
	EnumValues  map[string]int64 `json:"enumValues,omitempty"`

	// Status, Units and Parent are what NodeDetails.svelte renders under
	// "Status", "Units" and "Parent". It had been rendering them since it was
	// written and this struct never carried them, so the three rows were
	// guarded by `{#if}` and never appeared — dead markup nobody could see was
	// dead. Status is the one that matters: SMI marks an object `deprecated`
	// or `obsolete`, and a MIB browser that does not say so lets you build
	// monitoring on something the vendor has withdrawn.
	Status string `json:"status,omitempty"`
	Units  string `json:"units,omitempty"`
	Parent string `json:"parent,omitempty"`
}

// Service handles MIB loading and parsing.
type Service struct {
	path string
}

// NewService creates a new MIB service that operates on the given path.
func NewService(mibPath string) *Service {
	return &Service{path: mibPath}
}

// LoadAll loads all MIBs from the service's path and returns the full tree.
func (s *Service) LoadAll() ([]*Node, error) {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()

	log.Println("Loading all MIBs from:", s.path)

	// The same list the UI offers, rather than a second opinion about what a
	// MIB file looks like. This used to require a .mib or .txt suffix, which
	// the bundled standard MIBs do not have — they are extension-less files
	// named after their module — so "load everything" loaded none of them and
	// the tree came back empty with only a line in the log.
	files, err := ListMibFiles(s.path)
	if err != nil {
		return nil, fmt.Errorf("could not read MIB directory %s: %v", s.path, err)
	}

	// The loaded set is about to change, whatever the outcome: a partial load
	// changes it too.
	invalidateCatalogue()
	mountLocked(s.path)

	loadedModuleNames := []string{}
	for _, fileName := range files {
		moduleName, err := gosmi.LoadModule(fileName)
		if err != nil {
			log.Printf("Warning: could not load MIB module '%s': %v", fileName, err)
			continue
		}
		log.Printf("Successfully loaded MIB module '%s' from file '%s'", moduleName, fileName)
		loadedModuleNames = append(loadedModuleNames, moduleName)
	}

	if len(loadedModuleNames) == 0 {
		log.Println("No MIB modules were loaded. The tree will be empty.")
	}

	return s.buildTree(loadedModuleNames)
}

// InitPath initialises gosmi and points it at the persistent MIB directory.
//
// Here rather than in main, because gosmi's state is global and this package
// owns the mutex that guards it. A caller reaching for gosmi directly writes to
// that state without the lock — and, now that the symbol catalogue is cached,
// without dropping what describes it.
func InitPath(path string) {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	gosmi.Init()
	mountLocked(path)
	invalidateCatalogue()
}

// LoadCore loads modules BY NAME and reports which ones failed.
//
// The startup path used to call gosmi.LoadModule itself, outside this package
// and outside the lock. Nothing else runs at that point, so it raced with
// nothing — but it also meant the one load that always happens was the one load
// that did not go through here, which is the shape a stale cache is made of.
func LoadCore(names ...string) map[string]error {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	invalidateCatalogue()

	failed := map[string]error{}
	for _, name := range names {
		if _, err := gosmi.LoadModule(name); err != nil {
			failed[name] = err
		}
	}
	return failed
}

// LoadSpecific loads only the specified MIB files from the service's path.
func (s *Service) LoadSpecific(fileNames []string) ([]*Node, error) {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()

	log.Printf("Loading %d specific MIBs from: %s", len(fileNames), s.path)

	if len(fileNames) == 0 {
		log.Println("No MIB files specified. The tree will be empty.")
		return []*Node{}, nil
	}

	invalidateCatalogue()
	mountLocked(s.path)

	loadedModuleNames := []string{}
	for _, fileName := range fileNames {
		moduleName, err := gosmi.LoadModule(fileName)
		if err != nil {
			log.Printf("Warning: could not load MIB module '%s': %v", fileName, err)
		} else {
			log.Printf("Successfully loaded MIB module '%s' from file '%s'", moduleName, fileName)
			loadedModuleNames = append(loadedModuleNames, moduleName)
		}
	}

	if len(loadedModuleNames) == 0 {
		log.Println("No MIB modules were loaded. The tree will be empty.")
	}

	return s.buildTree(loadedModuleNames)
}

func (s *Service) buildTree(loadedModuleNames []string) ([]*Node, error) {
	nodeMap := make(map[string]*Node)
	isChild := make(map[string]bool)

	for _, moduleName := range loadedModuleNames {
		module, err := gosmi.GetModule(moduleName)
		if err != nil {
			log.Printf("Warning: could not retrieve module '%s' after loading: %v", moduleName, err)
			continue
		}
		for _, node := range module.GetNodes() {
			if node.Oid == nil {
				continue
			}
			oidStr := node.Oid.String()
			if oidStr == "" {
				continue
			}
			if _, exists := nodeMap[oidStr]; !exists {
				newNode := &Node{
					Name:        node.Name,
					Oid:         oidStr,
					Description: node.Description,
					Children:    []*Node{},
					MibType:     node.Kind.String(),
					Access:      node.Access.String(),
				}
				// "Unknown" is gosmi's zero value, not a status a MIB declares.
				// Sending it would put the word in the panel for every node.
				if st := node.Status.String(); st != "" && st != "Unknown" {
					newNode.Status = st
				}
				if node.Type != nil {
					newNode.Syntax = node.Type.Name
					newNode.Units = node.Type.Units
					if node.Type.Enum != nil && len(node.Type.Enum.Values) > 0 {
						newNode.EnumValues = make(map[string]int64)
						for _, val := range node.Type.Enum.Values {
							newNode.EnumValues[val.Name] = val.Value
						}
					}
				}
				nodeMap[oidStr] = newNode
			}
		}
	}

	log.Printf("Created %d unique nodes with OIDs.", len(nodeMap))

	for _, mibNode := range nodeMap {
		oidStr := mibNode.Oid
		lastDot := strings.LastIndex(oidStr, ".")
		if lastDot != -1 {
			parentOidStr := oidStr[:lastDot]
			if parentNode, ok := nodeMap[parentOidStr]; ok {
				parentNode.Children = append(parentNode.Children, mibNode)
				// The NAME, not the node: the tree is nested, so a pointer back
				// up would be a cycle and json.Marshal would not return.
				mibNode.Parent = parentNode.Name
				isChild[oidStr] = true
			}
		}
	}

	var rootNodes []*Node
	for oid, mibNode := range nodeMap {
		if !isChild[oid] {
			rootNodes = append(rootNodes, mibNode)
		}
	}
	log.Printf("Identified %d root nodes.", len(rootNodes))

	for _, root := range rootNodes {
		sortNodes(root)
	}
	sort.Slice(rootNodes, func(i, j int) bool {
		return isOidLess(rootNodes[i].Oid, rootNodes[j].Oid)
	})

	log.Println("MIB tree processing complete.")
	return rootNodes, nil
}

// MibLoadResult represents the load result for a single MIB file.
type MibLoadResult struct {
	FileName string `json:"fileName"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	// ModuleName is what the file DECLARES, which is what every IMPORTS clause
	// elsewhere looks for and is not necessarily what the file is called.
	//
	// gosmi.LoadModule returns it and it was being discarded on success. It
	// costs nothing to keep — no extra read, no extra time under the exclusive
	// gosmi mutex — and without it a caller can list which files loaded and
	// still cannot say which MODULES are resident, which is the question every
	// unsatisfied import is really asking.
	ModuleName string `json:"moduleName,omitempty"`
	// Diagnosis explains a failure: the stage it stopped at, the located
	// problems, the imports it could not satisfy. Present for failures, and for
	// a module that loaded with objects it could not resolve — it re-reads and
	// re-parses the file, which is worth doing once something is already wrong
	// and not on every file of a working directory.
	Diagnosis *LoadDiagnosis `json:"diagnosis,omitempty"`
	// Warnings are what loaded and is not right: a repair the lenient reading
	// made (CodeEscapedQuote, CodeBadDate, CodeImportAlias), or objects that
	// resolved to no type or no OID (CodeNoType, CodeNoOid). "Loaded" used to be
	// green for a module half of whose objects had no type. One per line and
	// kind: two escaped quotes on one line are one thing to fix.
	Warnings []Diagnostic `json:"warnings,omitempty"`
}

// MibLoadResponse wraps the tree and diagnostics for a load operation.
type MibLoadResponse struct {
	Tree        []*Node         `json:"tree"`
	Diagnostics []MibLoadResult `json:"diagnostics"`
}

// LoadWithDiagnostics loads MIBs and returns both the tree and per-file diagnostics.
func (s *Service) LoadWithDiagnostics(fileNames []string) MibLoadResponse {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	return s.loadWithDiagnosticsLocked(fileNames)
}

// loadWithDiagnosticsLocked is LoadWithDiagnostics for a caller already holding
// gosmiMu — Rebuild, which must not release it between tearing gosmi down and
// building it back up.
func (s *Service) loadWithDiagnosticsLocked(fileNames []string) MibLoadResponse {

	log.Printf("Loading %d MIBs with diagnostics from: %s", len(fileNames), s.path)

	var diagnostics []MibLoadResult
	var loadedModuleNames []string

	// An empty list means empty. It used to mean "everything in the
	// directory", which is the opposite of what the editor reload needs: it
	// passes the ENABLED MIBs, and re-enabling every file the user switched
	// off is not a fallback, it is a bug. The tab that does want all of them
	// asks for all of them by name — see App.LoadMibsWithDiagnostics.
	//
	// This only surfaced when the filter here was fixed: it required a .mib
	// or .txt suffix, and the bundled MIBs are extension-less, so the
	// fallback quietly loaded nothing and looked correct.

	invalidateCatalogue()
	fsys := mountLocked(s.path)

	// Shared across every diagnosis this loop triggers: the directory listing
	// is the same for all of them.
	ctx := newDiagContext(s)

	// Cycles first, because gosmi cannot survive one: it recurses until the
	// stack ends, and it does so holding this lock, so the whole application
	// stops. Refusing the files in the loop keeps everything else loadable.
	cycles := s.findImportCycles(fileNames)

	// Then one file per module. Two files declaring one module both loaded,
	// and the second replaced the first in silence.
	superseded := settleDuplicates(fsys, fileNames)

	for _, fileName := range fileNames {
		if cycle, inCycle := cycles[fileName]; inCycle {
			log.Printf("Diagnostic: refusing '%s': import cycle %s", fileName, cycle)
			diagnostics = append(diagnostics, cycleDiagnostic(fileName, cycle))
			continue
		}
		if dup, ok := superseded[fileName]; ok {
			log.Printf("Diagnostic: not loading '%s': %s", fileName, dup.Error)
			diagnostics = append(diagnostics, dup)
			continue
		}

		// Around the load, because this is the only moment gosmi's real error
		// exists: smi.LoadModule prints it and returns an empty string. The
		// diagnosis afterwards is READ-only and cannot recover it by retrying
		// — retrying would also load the file's dependencies as a side effect,
		// re-enabling MIBs the user switched off.
		var moduleName string
		var err error
		printed := captureStdout(func() {
			moduleName, err = gosmi.LoadModule(fileName)
		})

		if err != nil {
			// gosmi says "Could not load module at X" for a missing file, a
			// PDF, a syntax error on line 412 and an unsatisfiable IMPORTS
			// clause alike. Work out which, while we still hold the lock.
			diag := s.diagnose(fileName, ctx, printed)
			log.Printf("Diagnostic: failed to load '%s' at stage %s: %s", fileName, diag.Stage, diag.Summary)
			diagnostics = append(diagnostics, MibLoadResult{
				FileName:  fileName,
				Success:   false,
				Error:     diag.Summary,
				Diagnosis: &diag,
			})
			continue
		}

		log.Printf("Diagnostic: loaded '%s' as module '%s'", fileName, moduleName)
		result := MibLoadResult{FileName: fileName, Success: true, ModuleName: moduleName}
		seen := map[string]bool{}
		for _, fix := range fsys.repairs[fileName] {
			key := fmt.Sprintf("%d %s", fix.Line, fix.Code)
			if !seen[key] {
				seen[key] = true
				result.Warnings = append(result.Warnings, fix)
			}
		}
		if quality := moduleQuality(moduleName); len(quality) > 0 {
			result.Warnings = append(result.Warnings, quality...)
			// The diagnosis is what says WHY: the import that is missing, the
			// type nothing defines.
			diag := s.diagnose(fileName, ctx, "")
			result.Diagnosis = &diag
		}
		diagnostics = append(diagnostics, result)
		loadedModuleNames = append(loadedModuleNames, moduleName)
	}

	tree, err := s.buildTree(loadedModuleNames)
	if err != nil {
		log.Printf("Error building tree: %v", err)
		return MibLoadResponse{Tree: []*Node{}, Diagnostics: diagnostics}
	}

	return MibLoadResponse{Tree: tree, Diagnostics: diagnostics}
}

// OidDetails contains the translated information for a given OID.
type OidDetails struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// OidInfo contains detailed MIB information for an OID, including enum values.
type OidInfo struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Syntax      string           `json:"syntax,omitempty"`
	EnumValues  map[string]int64 `json:"enumValues,omitempty"`
}

// GetOidDetails takes a raw OID string and returns its translated details if found.
func (s *Service) Translate(oid string) OidDetails {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()

	smiOid, err := types.OidFromString(oid)
	if err != nil {
		return OidDetails{Name: oid, Description: "Invalid OID format"}
	}

	node, err := gosmi.GetNodeByOID(smiOid)
	if err != nil {
		return OidDetails{Name: oid, Description: "OID not found in loaded MIBs"}
	}
	return OidDetails{Name: node.Name, Description: node.Description}
}

// ResolveOid returns detailed MIB info for a single OID, including enum values.
func (s *Service) ResolveOid(oid string) OidInfo {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	return resolveOidLocked(oid)
}

// resolveOidLocked is ResolveOid for callers already holding gosmiMu.
//
// sync.RWMutex is not recursive, and a read lock taken while another is held
// blocks as soon as a writer is queued — Go parks new readers behind a pending
// writer to keep it from starving. ResolveOids held the outer lock and called
// ResolveOid for every OID of a walk, so one MIB load arriving mid-batch hung
// both goroutines and the window with them.
func resolveOidLocked(oid string) OidInfo {
	smiOid, err := types.OidFromString(oid)
	if err != nil {
		return OidInfo{Name: oid}
	}
	node, err := gosmi.GetNodeByOID(smiOid)
	if err != nil {
		return OidInfo{Name: oid}
	}
	info := OidInfo{Name: node.Name, Description: node.Description}
	if node.Type != nil {
		info.Syntax = node.Type.Name
		if node.Type.Enum != nil && len(node.Type.Enum.Values) > 0 {
			info.EnumValues = make(map[string]int64)
			for _, val := range node.Type.Enum.Values {
				info.EnumValues[val.Name] = val.Value
			}
		}
	}
	return info
}

// ResolveOids returns detailed MIB info for a batch of OIDs.
func (s *Service) ResolveOids(oids []string) map[string]OidInfo {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()

	result := make(map[string]OidInfo, len(oids))
	for _, oid := range oids {
		result[oid] = resolveOidLocked(oid)
	}
	return result
}

func sortNodes(node *Node) {
	if len(node.Children) > 0 {
		sort.Slice(node.Children, func(i, j int) bool {
			return isOidLess(node.Children[i].Oid, node.Children[j].Oid)
		})
		for _, child := range node.Children {
			sortNodes(child)
		}
	}
}

// ListMibFiles returns a list of MIB file names in the specified directory.
func ListMibFiles(dirPath string) ([]string, error) {
	var mibFiles []string

	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		return mibFiles, err
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return mibFiles, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			name := entry.Name()
			if name[0] != '.' && name != "README" && name != "LICENSE" && !isTransient(name) {
				mibFiles = append(mibFiles, name)
			}
		}
	}

	return mibFiles, nil
}

// isTransient reports whether a name is an editor or editor-crash artefact
// rather than a MIB.
//
// gosmi's module map is last-wins and os.ReadDir is alphabetical, so
// IF-MIB.bak beside IF-MIB loads second and replaces the real one for every
// IMPORTS in the directory. The staging file now lives outside mibs/; this is
// the second line of defence, and it covers the ones an editor or a backup
// tool leaves behind too.
func isTransient(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "~") {
		return true
	}
	for _, suffix := range []string{".tmp", ".bak", ".old", ".orig", ".swp", ".save"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func isOidLess(oid1, oid2 string) bool {
	parts1 := strings.Split(oid1, ".")
	parts2 := strings.Split(oid2, ".")

	for i := 0; i < len(parts1) && i < len(parts2); i++ {
		num1, err1 := strconv.Atoi(parts1[i])
		num2, err2 := strconv.Atoi(parts2[i])

		if err1 != nil || err2 != nil {
			return parts1[i] < parts2[i]
		}
		if num1 != num2 {
			return num1 < num2
		}
	}
	return len(parts1) < len(parts2)
}

// settleDuplicates keeps one file per module among the ones being loaded, and
// reports each of the others as not loaded, saying which file was and why.
//
// Among the files ASKED FOR: one the operator switched off is not a candidate,
// so enabling the older of two copies and disabling the newer is how a choice
// is overridden.
func settleDuplicates(fsys *mibFS, fileNames []string) map[string]MibLoadResult {
	byModule := map[string][]string{}
	for _, f := range fileNames {
		if module := fsys.declared[f]; module != "" {
			byModule[module] = append(byModule[module], f)
		}
	}
	out := map[string]MibLoadResult{}
	for module, files := range byModule {
		kept := fsys.prefer(module, files)
		for _, f := range files {
			if f == kept {
				continue
			}
			summary := fmt.Sprintf("%s is also declared by %s, which was loaded instead because %s; disable or remove one of the two",
				module, kept, fsys.reasons[f])
			out[f] = MibLoadResult{
				FileName: f, ModuleName: module, Error: summary,
				Diagnosis: &LoadDiagnosis{FileName: f, ModuleName: module, Stage: StageDuplicate, Summary: summary},
			}
		}
	}
	return out
}

// Codes for what a loaded module left unresolved.
const (
	CodeNoType = "no-type"
	CodeNoOid  = "no-oid"
)

// moduleQuality says what of a loaded module resolved to nothing.
//
// gosmi loads a module whose types or parents it cannot resolve, and gives the
// objects concerned no type or no OID. A walk then shows their values raw and
// unnamed, and nothing had said why.
func moduleQuality(moduleName string) []Diagnostic {
	module, err := gosmi.GetModule(moduleName)
	if err != nil {
		return nil
	}
	var untyped, unplaced []string
	for _, n := range module.GetNodes() {
		if n.Oid == nil || n.OidLen == 0 || n.Oid.String() == "" {
			unplaced = append(unplaced, n.Name)
			continue
		}
		if (n.Kind == types.NodeScalar || n.Kind == types.NodeColumn) && n.Type == nil {
			untyped = append(untyped, n.Name)
		}
	}
	var out []Diagnostic
	if len(untyped) > 0 {
		out = append(out, Diagnostic{
			Severity: SevWarning, Code: CodeNoType, Symbol: fmt.Sprint(len(untyped)),
			Message: fmt.Sprintf("%s no type (%s): their SYNTAX names a type no loaded module defines, so their values are shown raw",
				plural(len(untyped), "object has", "objects have"), listSome(untyped)),
		})
	}
	if len(unplaced) > 0 {
		out = append(out, Diagnostic{
			Severity: SevWarning, Code: CodeNoOid, Symbol: fmt.Sprint(len(unplaced)),
			Message: fmt.Sprintf("%s no OID (%s): they hang from a node no loaded module defines, so they are not in the tree",
				plural(len(unplaced), "definition has", "definitions have"), listSome(unplaced)),
		})
	}
	return out
}

// listSome names the first few of a list and counts the rest.
func listSome(names []string) string {
	const shown = 4
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:shown], ", "), len(names)-shown)
}
