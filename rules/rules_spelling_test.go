package rules

import "testing"

// Spellings of one name. Strings that look alike in an editor are written
// with escapes, so that the test says which one it means.
const (
	cafeComposed   = "/caf\u00e9"  // é as one code point (NFC)
	cafeDecomposed = "/cafe\u0301" // e followed by a combining acute (NFD)
	strasse        = "/stra\u00dfe"
	ligatureFiles  = "/\ufb01les" // "ﬁles", with the fi ligature
)

// A deny rule compared names byte by byte, or with ASCII-style lower-casing
// on a filesystem that ignores case. A filesystem that ignores case, or the
// way a name is composed (APFS does both), opens the same file under other
// spellings, and every one of them got past the rule.
func TestDenyRuleCoversOtherSpellingsOfItsPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rulePath string
		testPath string
		fold     bool
		want     bool
	}{
		// How a name is composed is not something any filesystem's users can
		// see, so it never tells a path from a denied one.
		{"decomposed for composed", cafeComposed, cafeDecomposed + "/secret.txt", false, true},
		{"composed for decomposed", cafeDecomposed, cafeComposed + "/secret.txt", false, true},
		{"decomposed for composed, folded", cafeComposed, cafeDecomposed + "/secret.txt", true, true},
		{"decomposed itself", cafeDecomposed, cafeDecomposed, false, true},
		{"decomposed sibling", cafeComposed, cafeDecomposed + "-menu/x", false, false},

		// Case folding in full, where the filesystem ignores case.
		{"SS for sharp s", strasse, "/STRASSE/x.txt", true, true},
		{"ss for sharp s", strasse, "/strasse/x.txt", true, true},
		{"sharp s for ss", "/strasse", strasse + "/x.txt", true, true},
		{"capital sharp s", strasse, "/STRA\u1e9eE/x.txt", true, true},
		{"letters for a ligature", ligatureFiles, "/files/x.txt", true, true},
		{"capitals for a ligature", ligatureFiles, "/FILES/x.txt", true, true},
		{"ligature for letters", "/files", ligatureFiles + "/x.txt", true, true},
		{"final sigma", "/\u03bf\u03b4\u03cc\u03c2", "/\u039f\u0394\u038c\u03a3/x", true, true},
		{"kelvin sign", "/k", "/\u212a/x", true, true},
		{"composed capital for decomposed small", cafeDecomposed, "/CAF\u00c9/x", true, true},
		{"folded sibling", strasse, "/STRASSEN/x.txt", true, false},

		// Where it does not, case still tells two files apart, and so do
		// letters that only fold to the same ones.
		{"case variant on a case-sensitive filesystem", strasse, "/STRA\u1e9eE/x.txt", false, false},
		{"SS for sharp s on a case-sensitive filesystem", strasse, "/strasse/x.txt", false, false},
		{"capitals on a case-sensitive filesystem", cafeComposed, "/CAF\u00c9/x", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Rule{Path: tc.rulePath}
			if got := r.Matches(tc.testPath, tc.fold); got != tc.want {
				t.Errorf("deny Rule{Path: %+q}.Matches(%+q, fold=%v) = %v; want %v", tc.rulePath, tc.testPath, tc.fold, got, tc.want)
			}
		})
	}
}

// An allow rule gives access, so it must not cover more than it did: where a
// broader comparison takes two names for one that the filesystem keeps apart,
// a deny rule denies too much, and an allow rule would open a file nobody
// allowed. It matches its path as written, and with the case ignored the way
// it always was on a filesystem that ignores case.
func TestAllowRuleIsNotWidened(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rulePath string
		testPath string
		fold     bool
		want     bool
	}{
		{"as written", cafeComposed, cafeComposed + "/x", false, true},
		{"as written, folded", cafeComposed, cafeComposed + "/x", true, true},
		{"case variant, folded", "/Public", "/public/x", true, true},
		{"case variant, not folded", "/Public", "/public/x", false, false},

		{"decomposed for composed", cafeComposed, cafeDecomposed + "/x", false, false},
		{"decomposed for composed, folded", cafeComposed, cafeDecomposed + "/x", true, false},
		{"ss for sharp s, folded", strasse, "/strasse/x", true, false},
		{"letters for a ligature, folded", ligatureFiles, "/files/x", true, false},
		{"letters for a ligature, not folded", ligatureFiles, "/files/x", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Rule{Allow: true, Path: tc.rulePath}
			if got := r.Matches(tc.testPath, tc.fold); got != tc.want {
				t.Errorf("allow Rule{Path: %+q}.Matches(%+q, fold=%v) = %v; want %v", tc.rulePath, tc.testPath, tc.fold, got, tc.want)
			}
		})
	}
}

// Whatever a deny rule matched before, it still matches: the broader
// comparison is tried in addition to the old one, never instead of it. The
// two differ for a dotted capital I, which lower-cases to "i" and folds to
// "i" followed by a combining dot.
func TestDenyRuleStillMatchesWhatLowerCasingMatched(t *testing.T) {
	t.Parallel()

	r := &Rule{Path: "/\u0130stanbul"}
	if !r.Matches("/istanbul/x", true) {
		t.Error("a deny rule no longer matches a path that only lower-casing takes for its own")
	}
}
