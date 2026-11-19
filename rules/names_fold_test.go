package rules

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// reducedRun reports whether the tests that go through every code point, or
// through many thousands of names drawn at random, do a part of that work
// only: under the race detector and with -short.
//
// Those tests compare what one function returns for a great many inputs, in
// one goroutine, and the race detector has nothing to find in them. It makes
// them some twenty times slower all the same, in a package that is tested at
// the same time as the ones the detector is there for, on the same
// processors. A run without it does all of the work.
func reducedRun() bool {
	return raceDetector || testing.Short()
}

// inReducedRun reports whether a reduced run looks at r, a code point
// without a case: the ones the canonical form does something with (those
// that decompose, and the marks that are put in order), every one up to the
// CJK scripts, and one in 61 of the rest.
func inReducedRun(r rune) bool {
	if r < 0x3000 || r%61 == 0 {
		return true
	}
	p := norm.NFKD.PropertiesString(string(r))
	return p.Decomposition() != nil || p.CCC() != 0
}

// Names a filesystem that ignores case opens as one file, in both spellings.
// They are the pairs a review found APFS to take for the same name, and the
// one NTFS adds to them.
var foldedPairs = []struct {
	name string
	a, b string
}{
	{"ascii case", "private", "PRIVATE"},
	{"dotted capital i and i with a dot above", "pr\u0130vate", "pri\u0307vate"},
	{"sharp s", "strasse", "stra\u00dfe"},
	{"final sigma", "\u03c3\u03bf\u03c3", "\u03c3\u03bf\u03c2"},
	{"ligature ff", "ffoo", "\ufb00oo"},
	{"ligature fi", "files", "\ufb01les"},
	{"kelvin sign", "kelvin", "\u212aelvin"},
	{"composition", "caf\u00e9", "cafe\u0301"},
	{"micro sign", "\u03bcx", "\u00b5x"},
	{"long s with a dot", "\u1e9bx", "\u1e61x"},
	{"long s", "sx", "\u017fx"},
	{"titlecase digraph", "\u01c6x", "\u01c5x"},
	{"ohm sign", "\u2126x", "\u03c9x"},
	{"angstrom sign", "\u212bx", "\u00e5x"},
	{"hangul", "\u1100\u1161", "\uac00"},
	{"ypogegrammeni and iota", "x\u0345", "x\u03b9"},
	{"georgian", "\u10a0x", "\u2d00x"},
	{"georgian mtavruli", "\u1c90x", "\u10d0x"},
	{"n preceded by an apostrophe", "\u0149x", "\u02bcnx"},
	{"capital sharp s and ss", "\u1e9ex", "ssx"},
	{"capital sharp s and sharp s", "\u1e9ex", "\u00dfx"},
	{"greek with ypogegrammeni", "\u1f80", "\u1f00\u03b9"},
	{"iota with dialytika and tonos", "\u0390", "\u03b9\u0308\u0301"},
	{"armenian ech yiwn", "\u0587", "\u0565\u0582"},
	{"armenian men now", "\ufb13", "\u0574\u0576"},
	{"greek capital with prosgegrammeni", "\u1f88", "\u1f80"},
	{"ypogegrammeni and capital iota", "\u0345", "\u0399"},

	// cases.Fold gives a Cherokee letter in the other case, whichever it is
	// given: folding twice gave it back as it was.
	{"cherokee", "\u13a0x", "\uab70x"},
	{"cherokee, with a composition to undo as well", "\u13a0\u00e9", "\uab70e\u0301"},
	{"cherokee, small letters of the first block", "\u13f8", "\u13f0"},

	// NTFS compares names in upper case, and the capital of a dotless i is
	// "I", the capital of "i".
	{"dotless i", "private", "pr\u0131vate"},
	{"dotless i and capital i", "PRIVATE", "pr\u0131vate"},
}

func TestCanonicalNameFoldsWhatFilesystemsFold(t *testing.T) {
	t.Parallel()

	for _, tc := range foldedPairs {
		if a, b := CanonicalName(tc.a, true), CanonicalName(tc.b, true); a != b {
			t.Errorf("%s: CanonicalName(%+q) = %+q, CanonicalName(%+q) = %+q; want the same", tc.name, tc.a, a, tc.b, b)
		}

		// What it is for: a deny rule written in one spelling covers the
		// other, and what lies below it.
		for _, p := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
			rule := &Rule{Path: "/" + p[0]}
			if !rule.Matches("/"+p[1], true) || !rule.Matches("/"+p[1]+"/secret.txt", true) {
				t.Errorf("%s: a deny rule for %+q does not cover %+q", tc.name, "/"+p[0], "/"+p[1])
			}
		}
	}
}

// Every letter is taken for its other cases: the capital a filesystem that
// compares in upper case (NTFS) makes of it, and the letters simple case
// folding puts it with (what APFS, HFS+ and the others that fold go by, at
// the least). Checked for every code point.
func TestCanonicalNameFoldsEveryCasePair(t *testing.T) {
	t.Parallel()

	failures := 0
	check := func(r, other rune) {
		if r == other || failures > 20 {
			return
		}
		if a, b := CanonicalName(string(r), true), CanonicalName(string(other), true); a != b {
			failures++
			t.Errorf("%U %+q and %U %+q are one letter in two cases, but CanonicalName gives %+q and %+q", r, string(r), other, string(other), a, b)
		}
	}

	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		check(r, unicode.ToUpper(r))
		for other := unicode.SimpleFold(r); other != r; other = unicode.SimpleFold(other) {
			check(r, other)
		}
	}
}

// The canonical form of a canonical form is itself. A form that changed when
// it was taken again would be one in which two spellings of a name can end
// up apart: that is how folding case twice came to undo itself.
func TestCanonicalNameIsIdempotent(t *testing.T) {
	t.Parallel()

	failures := 0
	check := func(s string) {
		for _, fold := range []bool{false, true} {
			if failures > 20 {
				return
			}
			once := CanonicalName(s, fold)
			if twice := CanonicalName(once, fold); twice != once {
				failures++
				t.Errorf("CanonicalName(%+q, %v) = %+q, and of that %+q", s, fold, once, twice)
			}
		}
	}

	// Every code point, alone and next to letters it could combine with. A
	// reduced run leaves out most of those the form has nothing to do with:
	// see inReducedRun.
	reduced := reducedRun()
	var cased []rune
	codePoints := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		hasCase := unicode.ToUpper(r) != r || unicode.ToLower(r) != r || unicode.SimpleFold(r) != r
		if hasCase {
			cased = append(cased, r)
		}
		if reduced && !hasCase && !inReducedRun(r) {
			continue
		}
		codePoints++
		check(string(r))
		check("a" + string(r) + "B")
	}

	// Random names, drawn mostly from where the trouble is: letters that
	// have a case, marks that combine, compatibility forms, the code points
	// that are skipped, and bytes that are not UTF-8 at all.
	special := []rune{
		'/', '.', '-', 'i', 'I', 's', 'S', 'k', 0x130, 0x131, 0xdf, 0x1e9e, 0x17f, 0x212a, 0x2126, 0x212b,
		0x300, 0x301, 0x307, 0x308, 0x323, 0x345, 0x3c2, 0x3c3, 0x3a3, 0x390, 0x1f80, 0x1f88, 0x149, 0x1c5,
		0x13a0, 0xab70, 0x13f0, 0x13f8, 0x10a0, 0x2d00, 0x1c90, 0x10d0, 0xfb00, 0xfb01, 0xfb13, 0xff21, 0xff41,
		0xff0f, 0x1100, 0x1161, 0xac00, 0x200c, 0x200d, 0x200e, 0x202a, 0x206a, 0xfeff, 0xfdfa, 0x2160, 0x24b6,
		0x1d400, 0x1e900, 0x1e922, 0x10400, 0x10428,
	}
	for _, r := range special {
		check(string(r))
		check("a" + string(r) + "B")
	}

	names := 200000
	if reduced {
		names = 10000
	}
	t.Logf("%d code points and %d random names are checked (reduced run: %v)", codePoints, names, reduced)

	rng := rand.New(rand.NewSource(20261118))
	for i := 0; i < names; i++ {
		var b strings.Builder
		for n := 1 + rng.Intn(12); n > 0; n-- {
			switch rng.Intn(10) {
			case 0, 1, 2:
				b.WriteRune(special[rng.Intn(len(special))])
			case 3, 4, 5:
				b.WriteRune(cased[rng.Intn(len(cased))])
			case 6:
				b.WriteRune(rune(rng.Intn(0x80)))
			case 7:
				b.WriteRune(rune(rng.Intn(0x3000)))
			case 8:
				b.WriteRune(rune(rng.Intn(unicode.MaxRune + 1)))
			default:
				b.WriteByte(byte(0x80 + rng.Intn(0x80)))
			}
		}
		check(b.String())
	}
}
