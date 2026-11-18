package rules

import "testing"

func TestCanonicalName(t *testing.T) {
	t.Parallel()

	same := []struct {
		name string
		a, b string
		fold bool
	}{
		{"composition", "caf\u00e9", "cafe\u0301", false},
		{"composition, folded", "caf\u00e9", "cafe\u0301", true},
		{"two marks in either order", "a\u0323\u0302", "a\u0302\u0323", false},
		{"angstrom sign", "\u212b", "\u00c5", false},
		{"ligature", "\ufb01les", "files", false},
		{"fullwidth", "\uff46\uff49\uff4c\uff45", "file", false},
		{"ascii case", "Report.TXT", "report.txt", true},
		{"sharp s", "stra\u00dfe", "STRASSE", true},
		{"capital sharp s", "stra\u00dfe", "STRA\u1e9eE", true},
		{"ligature and case", "\ufb01les", "FILES", true},
		{"final sigma", "\u03bf\u03b4\u03cc\u03c2", "\u039f\u0394\u038c\u03a3", true},
		{"decomposed capital", "caf\u00e9", "CAFE\u0301", true},
		{"dotted capital i", "\u0130", "i\u0307", true},
		{"fullwidth capital", "\uff21", "a", true},
		{"zero width non-joiner", ".g\u200cit", ".GIT", true},
		{"byte order mark", "\ufeffname", "name", true},
		// A string that is all ASCII takes a shortcut. It has to end up
		// where the same name written otherwise does.
		{"ascii against its fullwidth form", "ABC-def_1.txt", "\uff21\uff22\uff23-def_1.txt", true},
		{"ascii against its fullwidth form, case kept", "ABC", "\uff21\uff22\uff23", false},
	}
	for _, tc := range same {
		if a, b := CanonicalName(tc.a, tc.fold), CanonicalName(tc.b, tc.fold); a != b {
			t.Errorf("%s: CanonicalName(%+q, %v) = %+q, CanonicalName(%+q, %v) = %+q; want the same", tc.name, tc.a, tc.fold, a, tc.b, tc.fold, b)
		}
	}

	different := []struct {
		name string
		a, b string
		fold bool
	}{
		{"case, not folded", "Report", "report", false},
		{"sharp s, not folded", "stra\u00dfe", "strasse", false},
		{"other letters", "caf\u00e9", "cafe", true},
		{"other letters, not folded", "caf\u00e9", "cafe", false},
		{"longer name", "report", "reports", true},
		{"zero width non-joiner, not folded", "a\u200cb", "ab", false},
	}
	for _, tc := range different {
		if a, b := CanonicalName(tc.a, tc.fold), CanonicalName(tc.b, tc.fold); a == b {
			t.Errorf("%s: CanonicalName(%+q, %v) and CanonicalName(%+q, %v) are both %+q", tc.name, tc.a, tc.fold, tc.b, tc.fold, a)
		}
	}

	// Separators stay where they are, so a path can be compared element by
	// element as a string: no letter composes with, or folds into, a "/".
	if got := CanonicalName("/Caf\u00e9/\u00c9t\u00e9", true); got != "/cafe\u0301/e\u0301te\u0301" {
		t.Errorf("CanonicalName of a path = %+q", got)
	}
}
