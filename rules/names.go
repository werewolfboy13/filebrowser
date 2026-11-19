package rules

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CanonicalName returns a form of name, a file name or a path, in which two
// names are equal when a filesystem could take them for the same name. It is
// for comparing names where a name is refused (a deny rule, the name of a
// temporary file, a file of the server's own), never for opening a file, and
// never for deciding that a name is allowed.
//
// How a name is composed is always ignored: "é" is one code point or an "e"
// followed by a combining accent, and filesystems that normalise names (APFS,
// HFS+, ZFS with normalization set) open the same file under both. So are the
// compatibility forms of a character (a ligature, a fullwidth letter), which
// some of them fold as well.
//
// With foldCase, case is ignored too, in full: "STRASSE" for "straße" and
// "files" for "ﬁles", as a filesystem that ignores case does it, not only "A"
// for "a". So are the code points HFS+ skips when it compares names.
//
// The form of a form is the form itself: taking it again changes nothing.
//
// The form errs on the side of taking two names for one. A filesystem keeps
// more names apart than this does: which names it folds depends on the
// filesystem and on the Unicode version it was built with, and comparing more
// broadly than any of them is what makes a refusal hold on all of them. The
// result can have separators and dots that name does not have (a fullwidth
// solidus becomes "/"), which is one more reason not to use it as a path.
func CanonicalName(name string, foldCase bool) string {
	if isASCII(name) {
		if foldCase {
			return strings.ToLower(name)
		}
		return name
	}

	if !foldCase {
		return norm.NFKD.String(name)
	}

	// Caseless matching of compatibility-equivalent strings, as Unicode
	// defines it (D147): fold what is decomposed, twice, because folding can
	// produce characters that decompose further, and decomposing ones that
	// fold. A Caser has state, so it is not shared.
	fold := cases.Fold()
	name = fold.String(norm.NFD.String(skipIgnorable(name)))
	name = fold.String(norm.NFKD.String(name))

	// One more pass, letter by letter, that the folds above cannot undo.
	//
	// Folding is not lower-casing: the Cherokee letters fold to their
	// capitals, and cases.Fold gives each of them in the other case,
	// whichever it is given. Folding twice gave them back as they were, so
	// that the two cases of a Cherokee name stayed apart. Lower-casing
	// brings them together, and changes no other letter that was folded.
	//
	// The dotless i has no folding of its own. A filesystem that compares
	// names in upper case (NTFS, and what is shared from it) takes it for
	// an "i": the capital of both is "I".
	name = strings.Map(func(r rune) rune {
		if r == '\u0131' {
			return 'i'
		}
		return unicode.ToLower(r)
	}, name)
	return norm.NFKD.String(name)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// skipIgnorable removes the code points that HFS+ ignores when it compares
// names without regard to case: zero-width joiners and marks, directional
// formatting, the byte order mark. A name with one of them in it opens the
// file of the name without.
func skipIgnorable(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200C && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x206A && r <= 0x206F, r == 0xFEFF:
			return -1
		}
		return r
	}, name)
}
