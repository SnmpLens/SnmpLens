package mib

import (
	"strings"

	"github.com/sleepinggenius2/gosmi"
)

// ModuleRoot is the OID the MIB tree shows a loaded module at: its
// MODULE-IDENTITY, or else the definition most of its others descend from.
// Empty when the module is not loaded or defines nothing with an OID.
//
// Not merely the shallowest definition: SNMPv2-SMI's is zeroDotZero, { 0 0 },
// a leaf beside the tree the module actually builds, and an SMIv1 vendor MIB
// declares its enterprise arc and hangs everything under it.
func (s *Service) ModuleRoot(module string) string {
	gosmiMu.Lock()
	defer gosmiMu.Unlock()
	if !gosmi.IsLoaded(module) {
		return ""
	}
	m, err := gosmi.GetModule(module)
	if err != nil {
		return ""
	}
	if n, ok := m.GetIdentityNode(); ok && n.Oid != nil && n.Oid.String() != "" {
		return n.Oid.String()
	}
	var oids []string
	defined := map[string]bool{}
	for _, n := range m.GetNodes() {
		if n.Oid == nil || n.Oid.String() == "" {
			continue
		}
		oid := n.Oid.String()
		if !defined[oid] {
			defined[oid] = true
			oids = append(oids, oid)
		}
	}
	return subtreeRoot(oids, defined)
}

// subtreeRoot is the OID among oids with the most of the others under it; the
// shallower, then the first, on a tie.
func subtreeRoot(oids []string, defined map[string]bool) string {
	under := map[string]int{}
	for _, oid := range oids {
		for p := oid; ; {
			i := strings.LastIndexByte(p, '.')
			if i < 0 {
				break
			}
			p = p[:i]
			if defined[p] {
				under[p]++
			}
		}
	}
	best := ""
	for _, oid := range oids {
		switch {
		case best == "",
			under[oid] > under[best],
			under[oid] == under[best] && strings.Count(oid, ".") < strings.Count(best, "."):
			best = oid
		}
	}
	return best
}
