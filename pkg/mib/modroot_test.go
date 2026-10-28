package mib

import "testing"

// The file list's "show in the MIB tree" goes to where the tree shows a module:
// its MODULE-IDENTITY when it has one, the definition the rest hang from otherwise.
func TestAModuleIsFoundInTheTreeAtItsRoot(t *testing.T) {
	loadedCatalogue(t)
	s := NewService("../../mibs")
	for module, want := range map[string]string{
		"IF-MIB":     "1.3.6.1.2.1.31", // ifMIB MODULE-IDENTITY
		"SNMPv2-SMI": "1.3",            // no identity: org, which nearly all of it is under
		"NOPE-MIB":   "",               // not loaded
	} {
		if got := s.ModuleRoot(module); got != want {
			t.Errorf("%s: %q, want %q", module, got, want)
		}
	}
}

func TestTheSubtreeRootIsWhatTheRestHangsFrom(t *testing.T) {
	oids := []string{"0.0", "1.3.6.1.4.1.99", "1.3.6.1.4.1.99.1", "1.3.6.1.4.1.99.1.1", "1.3.6.1.4.1.99.2"}
	defined := map[string]bool{}
	for _, o := range oids {
		defined[o] = true
	}
	if got := subtreeRoot(oids, defined); got != "1.3.6.1.4.1.99" {
		t.Errorf("got %s", got)
	}
	// Nothing under anything: the shallowest.
	flat := []string{"1.3.6.1.4.1.99.5", "1.3.6.1.4.1.98"}
	if got := subtreeRoot(flat, map[string]bool{flat[0]: true, flat[1]: true}); got != "1.3.6.1.4.1.98" {
		t.Errorf("flat: %s", got)
	}
}
