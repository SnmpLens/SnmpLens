package mib

// One verdict per MIB file, for every place that shows one.
//
// Two questions decide it, and they used to be answered on two screens and in
// the renderer: does gosmi LOAD the file (the MIB settings, from the load
// results), and is what it loads RIGHT (the editor's list, from the analysis).
// A module that loaded with a definition given twice was "loaded" in one and
// "error" in the other, and nothing said both were true. The verdict puts them
// on one scale, worst first, and it is decided here, once:
//
//	failed   nothing from the file is in the tree: gosmi refused it, another
//	         file declaring the same module was loaded instead, or it does not
//	         parse as it is;
//	error    it loads, and some of its definitions are wrong or missing from
//	         the tree — a type nothing defines, a name given twice;
//	warning  everything resolves; something is still worth fixing — a mistake
//	         SnmpLens tolerates and other tools refuse, a clause RFC 2578
//	         requires;
//	ok       nothing to say.

// The levels of a Verdict, worst first.
const (
	VerdictFailed  = "failed"
	VerdictError   = "error"
	VerdictWarning = "warning"
	VerdictOK      = "ok"
)

// Verdict is how a MIB file stands.
type Verdict struct {
	Level string `json:"level"`
	// Count is how many problems of that level: the errors of an "error", the
	// warnings of a "warning".
	Count int `json:"count"`
	// Detail is the first of them, in a sentence: what a tooltip says.
	Detail string `json:"detail,omitempty"`
}

// Judge gives the verdict on a file from what the last load did with it (nil
// when it was not loaded, or when the text judged is not the text loaded) and
// what the analysis found in it.
func Judge(load *MibLoadResult, diags []Diagnostic) Verdict {
	if load != nil && !load.Success {
		return Verdict{Level: VerdictFailed, Count: 1, Detail: load.Error}
	}
	var errors, warnings int
	var firstError, firstWarning string
	for _, d := range diags {
		if d.Code == CodeSyntax {
			return Verdict{Level: VerdictFailed, Count: 1, Detail: d.Message}
		}
		switch d.Severity {
		case SevError:
			if errors == 0 {
				firstError = d.Message
			}
			errors++
		case SevWarning:
			if warnings == 0 {
				firstWarning = d.Message
			}
			warnings++
		}
	}
	if errors > 0 {
		return Verdict{Level: VerdictError, Count: errors, Detail: firstError}
	}
	// The load's warnings and the analysis's overlap — a repair is reported by
	// both — so the larger of the two counts, never their sum.
	if load != nil && len(load.Warnings) > warnings {
		if warnings == 0 {
			firstWarning = load.Warnings[0].Message
		}
		warnings = len(load.Warnings)
	}
	if warnings > 0 {
		return Verdict{Level: VerdictWarning, Count: warnings, Detail: firstWarning}
	}
	return Verdict{Level: VerdictOK}
}

// IsDuplicate is a load refused because another file declaring the same
// module was loaded instead. It stands whatever the file's text says, since
// the text is not what was wrong.
func (r *MibLoadResult) IsDuplicate() bool {
	return r != nil && !r.Success && r.Diagnosis != nil && r.Diagnosis.Stage == StageDuplicate
}
