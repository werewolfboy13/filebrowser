package fileutils

import (
	"strings"
	"testing"
)

// The temporary file of an upload is refused by name. On a filesystem that
// ignores case and composition, it answers to more names than its own and
// the ASCII spellings of it: any of the letters can be one that only folds
// to it. "ff" is the pair a name of hexadecimal digits can hold.
func TestIsTempNameInEverySpelling(t *testing.T) {
	const own = ".fbtmp-tus-ab1bf3866b359ff2c90acc2fa6674cc5"
	if !ownTempName.MatchString(own) {
		t.Fatal("the test's name is not one this package gives")
	}

	for name, spelling := range map[string]string{
		"as created":                   own,
		"upper case":                   strings.ToUpper(own),
		"ff ligature":                  strings.Replace(own, "ff", "\ufb00", 1),
		"long s":                       strings.Replace(own, "tus", "tu\u017f", 1),
		"fullwidth letters":            strings.Replace(own, "fbtmp", "\uff46\uff42\uff54\uff4d\uff50", 1), // fullwidth "fbtmp"
		"fullwidth hyphen":             strings.Replace(own, "-tus-", "\uff0dtus\uff0d", 1),
		"mathematical digits":          strings.Replace(own, "3866", "\U0001d7d1\U0001d7d6\U0001d7d4\U0001d7d4", 1),
		"zero width joiner inside":     strings.Replace(own, "tus", "t\u200dus", 1),
		"zero width non-joiner inside": strings.Replace(own, "fbtmp", "fb\u200ctmp", 1),
		"byte order mark in front":     "\ufeff" + own,
		"without the tus part":         ".FBTMP-" + strings.Repeat("\uff21", 32), // fullwidth "A"
	} {
		if !IsTempName(spelling) {
			t.Errorf("%s: %+q is not taken for a temporary file", name, spelling)
		}
	}

	for _, other := range []string{
		"", ".", "report.txt", ".fbtmp", ".fbtmp-", ".fbtmp-tus-", "fbtmp-tus-ab1bf3866b359ff2c90acc2fa6674cc5",
		".fbtmp-tus-ab1bf3866b359ff2c90acc2fa6674cc", ".fbtmp-tus-ab1bf3866b359ff2c90acc2fa6674cc5.txt",
		".fbtmp-tus-gb1bf3866b359ff2c90acc2fa6674cc5", "caf\u00e9", "\u00dcbersicht.pdf",
	} {
		if IsTempName(other) {
			t.Errorf("%+q is taken for a temporary file", other)
		}
	}
}

// What is removed as a leftover is only what this package wrote: a name in
// another spelling is somebody's file.
func TestOnlyTheExactNameIsOurs(t *testing.T) {
	const own = ".fbtmp-tus-ab1bf3866b359ff2c90acc2fa6674cc5"
	if !IsOwnTempName(own) || !IsOwnTempName(".fbtmp-"+strings.Repeat("0", 32)) {
		t.Error("the names this package gives are not recognised")
	}
	for _, other := range []string{strings.ToUpper(own), strings.Replace(own, "ff", "\ufb00", 1), own + "x", "report.txt"} {
		if IsOwnTempName(other) {
			t.Errorf("%+q is taken for a name this package gave", other)
		}
	}
}
