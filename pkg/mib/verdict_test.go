package mib

import "testing"

func TestJudge(t *testing.T) {
	syntax := Diagnostic{Severity: SevError, Code: CodeSyntax, Message: "1:1: unexpected"}
	twice := Diagnostic{Severity: SevError, Code: CodeDuplicateName, Message: "defined twice"}
	tabs := Diagnostic{Severity: SevWarning, Code: CodeTabs, Message: "tabs"}
	info := Diagnostic{Severity: SevInfo, Code: CodeNoDescription, Message: "no description"}
	loaded := &MibLoadResult{Success: true}
	repaired := &MibLoadResult{Success: true, Warnings: []Diagnostic{
		{Severity: SevWarning, Code: CodeEscapedQuote, Message: "escaped quote"},
		{Severity: SevWarning, Code: CodeBadDate, Message: "bad date"},
	}}
	refused := &MibLoadResult{Success: false, Error: "refused"}

	for _, tc := range []struct {
		name  string
		load  *MibLoadResult
		diags []Diagnostic
		want  Verdict
	}{
		{"refused by gosmi, whatever the text", refused, nil, Verdict{VerdictFailed, 1, "refused"}},
		{"refused beats an analysis with errors", refused, []Diagnostic{twice}, Verdict{VerdictFailed, 1, "refused"}},
		{"a syntax error cannot load", nil, []Diagnostic{tabs, syntax}, Verdict{VerdictFailed, 1, syntax.Message}},
		{"loads, and wrong", loaded, []Diagnostic{tabs, twice, twice}, Verdict{VerdictError, 2, twice.Message}},
		{"loads, worth fixing", loaded, []Diagnostic{info, tabs}, Verdict{VerdictWarning, 1, tabs.Message}},
		{"the load's warnings, not added to the analysis's", repaired, []Diagnostic{tabs}, Verdict{VerdictWarning, 2, tabs.Message}},
		{"the load's warnings alone", repaired, nil, Verdict{VerdictWarning, 2, "escaped quote"}},
		{"an info is nothing to mark", loaded, []Diagnostic{info}, Verdict{Level: VerdictOK}},
		{"not loaded, clean", nil, nil, Verdict{Level: VerdictOK}},
	} {
		if got := Judge(tc.load, tc.diags); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
