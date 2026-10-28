package mib

import "sort"

// Rules a person may mute.
//
// A vendor MIB indented with tabs reports every line of it; one that never
// writes a DESCRIPTION reports every object. That advice is right and, for a
// file nobody here will rewrite, it is noise: it buried the one finding that
// mattered, and it marked the file "warning" in every list for something no
// one intends to change. Muting a rule takes its findings out of the verdict
// and out of the editor's list until it is unmuted.
//
// Only advice. An error is never muted, nor a finding that something resolves
// to nothing (no-type, no-oid, import-lost): those describe the tree the rest
// of SnmpLens reads, not the style of a file.
var mutable = map[string]bool{
	CodeTabs:          true,
	CodeLongLine:      true,
	CodeNoDescription: true,
	CodeUnusedImport:  true,
	CodeRowAccess:     true,
	CodeImportAlias:   true,
	CodeEscapedQuote:  true,
	CodeBadDate:       true,
}

// MutableRules lists the codes a person may mute.
func MutableRules() []string {
	out := make([]string, 0, len(mutable))
	for c := range mutable {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Mutable says whether a code may be muted.
func Mutable(code string) bool { return mutable[code] }

// JudgeMuted is Judge with the findings of muted rules left out, the load's
// as well as the analysis's. An error is judged whatever is muted.
func JudgeMuted(load *MibLoadResult, diags []Diagnostic, muted map[string]bool) Verdict {
	if len(muted) == 0 {
		return Judge(load, diags)
	}
	keep := func(d Diagnostic) bool { return d.Severity == SevError || !muted[d.Code] || !mutable[d.Code] }
	shown := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		if keep(d) {
			shown = append(shown, d)
		}
	}
	if load != nil && len(load.Warnings) > 0 {
		l := *load
		l.Warnings = nil
		for _, d := range load.Warnings {
			if keep(d) {
				l.Warnings = append(l.Warnings, d)
			}
		}
		load = &l
	}
	return Judge(load, shown)
}
