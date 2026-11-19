package rules

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Names a filesystem that ignores case opens as one file, in both spellings.
// They are the pairs a review found APFS to take for the same name, and the
// one NTFS adds to them.
var foldedPairs = []struct {
	name string
	a, b string
}{
	{"ascii case", "private", "PRIVATE"},
	{"dotted capital i and i with a dot above", "prİvate", "pri̇vate"},
	{"sharp s", "strasse", "straße"},
	{"final sigma", "σοσ", "σος"},
	{"ligature ff", "ffoo", "ﬀoo"},
	{"ligature fi", "files", "ﬁles"},
	{"kelvin sign", "kelvin", "Kelvin"},
	{"composition", "café", "café"},
	{"micro sign", "μx", "µx"},
	{"long s with a dot", "ẛx", "ṡx"},
	{"long s", "sx", "ſx"},
	{"titlecase digraph", "ǆx", "ǅx"},
	{"ohm sign", "Ωx", "ωx"},
	{"angstrom sign", "Åx", "åx"},
	{"hangul", "가", "가"},
	{"ypogegrammeni and iota", "xͅ", "xι"},
	{"georgian", "Ⴀx", "ⴀx"},
	{"georgian mtavruli", "Აx", "აx"},
	{"n preceded by an apostrophe", "ŉx", "ʼnx"},
	{"capital sharp s and ss", "ẞx", "ssx"},
	{"capital sharp s and sharp s", "ẞx", "ßx"},
	{"greek with ypogegrammeni", "ᾀ", "ἀι"},
	{"iota with dialytika and tonos", "ΐ", "ΐ"},
	{"armenian ech yiwn", "և", "եւ"},
	{"armenian men now", "ﬓ", "մն"},
	{"greek capital with prosgegrammeni", "ᾈ", "ᾀ"},
	{"ypogegrammeni and capital iota", "ͅ", "Ι"},

	// cases.Fold gives a Cherokee letter in the other case, whichever it is
	// given: folding twice gave it back as it was.
	{"cherokee", "Ꭰx", "ꭰx"},
	{"cherokee, with a composition to undo as well", "Ꭰé", "ꭰé"},
	{"cherokee, small letters of the first block", "ᏸ", "Ᏸ"},

	// NTFS compares names in upper case, and the capital of a dotless i is
	// "I", the capital of "i".
	{"dotless i", "private", "prıvate"},
	{"dotless i and capital i", "PRIVATE", "prıvate"},
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

	// Every code point, alone and next to letters it could combine with.
	var cased []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		check(string(r))
		check("a" + string(r) + "B")
		if unicode.ToUpper(r) != r || unicode.ToLower(r) != r || unicode.SimpleFold(r) != r {
			cased = append(cased, r)
		}
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
	rng := rand.New(rand.NewSource(20261118))
	for i := 0; i < 200000; i++ {
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
