package mib

import (
	"log"
	"sync"

	"github.com/sleepinggenius2/gosmi"
)

// gosmi keeps its whole world in package-level state: one search path, one
// module table, one node index. Every exported call here reaches into it.
//
// Wails dispatches each bound method on its own goroutine, so a batch
// ResolveOids from the operations panel can already overlap a LoadAll from the
// MIB settings tab — a read racing a mutation of that shared table. The editor
// makes it far more likely, because saving a MIB tears the world down and
// rebuilds it while the rest of the app carries on resolving OIDs.
//
// One EXCLUSIVE mutex for the package, not an RWMutex.
//
// gosmi has no read-only operations. internal.(*Object).GetSmiNode is a
// getter that memoises: on first call it writes x.Oid and x.OidLen. Two
// goroutines holding a read lock and resolving the same OID therefore write
// the same fields at the same time — reproduced under -race with four
// concurrent Translate/ResolveOids/Table callers, which is exactly what the
// app does while a walk resolves names in several tabs.
//
// A shared lock would be correct only if reading were reading. Serialising is
// the price of a dependency whose accessors mutate; the lock is held for a
// batch rather than per OID, so a 300-varbind walk takes it once.
var gosmiMu sync.Mutex

// Reloaded reports what a rebuild produced.
type Reloaded struct {
	Tree        []*Node         `json:"tree"`
	Diagnostics []MibLoadResult `json:"diagnostics"`
	// Health is the answer to the only question that matters after editing a
	// MIB the rest of the app depends on: does the tree still resolve.
	Health Health `json:"health"`
}

// Health is a smoke test of the rebuilt tree.
type Health struct {
	Ok bool `json:"ok"`
	// Failures name the probes that did not resolve, in a form a person can
	// act on.
	Failures []string `json:"failures"`
	// Modules is how many are loaded, so "0" is visible rather than implied.
	Modules int `json:"modules"`
}

// healthProbes are OIDs that must resolve in any working installation. They
// come from the core modules every other MIB imports from, so if these fail
// the tree is broken regardless of which file was edited.
var healthProbes = []struct{ oid, want string }{
	{"1.3.6.1.2.1.1.1", "sysDescr"},
	{"1.3.6.1.2.1.1.3", "sysUpTime"},
	{"1.3.6.1.2.1.2.2.1.10", "ifInOctets"},
}

// Rebuild tears gosmi down and loads the given files again.
//
// A full Exit/Init is the only way to forget a module: gosmi has no unload, so
// after editing a MIB the previously parsed version would otherwise stay in
// the table and the editor would appear to do nothing. The search path is
// reapplied inside the lock because Init resets it.
func (s *Service) Rebuild(fileNames []string) Reloaded {
	// ONE lock, held from the teardown to the health probe.
	//
	// It used to be released right after the core modules, so readers were let
	// into a world holding only SNMPv2-SMI and SNMPv2-TC: measured, 12 reads
	// of sysDescr came back with a different name during three rebuilds — not
	// an error, a WRONG ANSWER, which is worse. And two rebuilds at once
	// interleaved, each Exit/Init destroying what the other had just loaded,
	// so the health probe reported a failure that was not real — which
	// app_mibeditor.go records as a "major" system event and routes to every
	// configured syslog, webhook and mail sink.
	gosmiMu.Lock()
	defer gosmiMu.Unlock()

	// The whole world is about to be replaced, so nothing cached about it
	// survives. Dropped BEFORE the teardown rather than after the reload,
	// because the lock is held across both and a panic in between must not
	// leave a catalogue describing modules that no longer exist.
	invalidateCatalogue()

	gosmi.Exit()
	gosmi.Init()
	mountLocked(s.path)

	// The core modules first: everything else imports from them, and loading
	// them explicitly makes a broken one show up as itself rather than as a
	// hundred confusing failures in unrelated files.
	var diagnostics []MibLoadResult
	for _, core := range []string{"SNMPv2-SMI", "SNMPv2-TC"} {
		if _, err := gosmi.LoadModule(core); err != nil {
			diagnostics = append(diagnostics, MibLoadResult{
				FileName: core, Success: false,
				Error: "core module failed to load: " + err.Error(),
			})
			log.Printf("mib: core module %s failed to reload: %v", core, err)
		}
	}
	resp := s.loadWithDiagnosticsLocked(fileNames)
	resp.Diagnostics = append(diagnostics, resp.Diagnostics...)

	return Reloaded{
		Tree:        resp.Tree,
		Diagnostics: resp.Diagnostics,
		Health:      checkHealthLocked(),
	}
}

// checkHealth resolves a few OIDs that must work.
func checkHealth() Health {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	return checkHealthLocked()
}

// checkHealthLocked is checkHealth for a caller already holding gosmiMu.
func checkHealthLocked() Health {

	h := Health{Ok: true, Failures: []string{}, Modules: len(gosmi.GetLoadedModules())}
	for _, probe := range healthProbes {
		node, err := gosmi.GetNode(probe.want)
		switch {
		case err != nil || node.Name != probe.want:
			h.Ok = false
			h.Failures = append(h.Failures, probe.want+" ("+probe.oid+") no longer resolves")

		case node.Oid.String() != probe.oid:
			// The OID, which the probe has always carried and never compared.
			//
			// gosmi resolves imports lazily and returns nil rather than
			// failing, so gutting SNMPv2-SMI leaves every dependent module
			// "loaded" with its objects still present BY NAME — and an empty
			// OID, because the parent chain resolves to nothing. Measured: the
			// tree came back with 0 roots, Translate(1.3.6.1.2.1.1.1.0)
			// answered "iso", and this function reported ok=true with 15
			// modules. The editor then showed a green "reloaded" toast over a
			// destroyed tree.
			h.Ok = false
			got := node.Oid.String()
			if got == "" {
				got = "nothing"
			}
			h.Failures = append(h.Failures,
				probe.want+" resolves to "+got+" instead of "+probe.oid+
					" — a module it depends on is missing or empty")
		}
	}
	return h
}
