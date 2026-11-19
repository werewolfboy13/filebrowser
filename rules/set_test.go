package rules

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// matchesAsBefore is Rule.Matches as it was before it and Set learned to
// leave out work: every form worked out for every comparison. It is what
// both are compared with.
func matchesAsBefore(r *Rule, path string, fold bool) bool {
	if r.Regex {
		re, err := r.Regexp.compile()
		if err != nil {
			return !r.Allow
		}
		return re.MatchString(path)
	}

	if fold {
		if isAtOrBelow(strings.ToLower(path), strings.ToLower(r.Path)) {
			return true
		}
	} else if isAtOrBelow(path, r.Path) {
		return true
	}
	if r.Allow {
		return false
	}
	return isAtOrBelow(CanonicalName(path, fold), CanonicalName(r.Path, fold))
}

// allowsAsBefore decides on a path as the request handler did: every rule
// in turn, the last that matches wins.
func allowsAsBefore(path string, fold bool, lists ...[]Rule) bool {
	allow := true
	for _, list := range lists {
		for i := range list {
			if matchesAsBefore(&list[i], path, fold) {
				allow = list[i].Allow
			}
		}
	}
	return allow
}

// nameParts are the pieces random names are put together from: the same
// name in several spellings, so that rules and paths meet often, in all
// combinations of ASCII and not.
var nameParts = []string{
	"docs", "Docs", "DOCS", "private", "Private", "pr\u0131vate", "caf\u00e9", "cafe\u0301", "CAF\u00c9",
	"stra\u00dfe", "STRASSE", "strasse", "\ufb01les", "files", "FILES", "\u13a0\u00e9", "\uab70e\u0301",
	"\u5831\u544a\u66f8", "\uff46\uff49\uff4c\uff45\uff53", "a", "A", "b", ".git", ".g\u200cit", "x.txt", "X.TXT",
	"\u03c3\u03bf\u03c2", "\u03a3\u039f\u03a3", "\u212aelvin", "kelvin",
}

func randomPath(rng *rand.Rand) string {
	n := 1 + rng.Intn(4)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = nameParts[rng.Intn(len(nameParts))]
	}
	p := "/" + strings.Join(parts, "/")
	if rng.Intn(10) == 0 {
		p += "/"
	}
	return p
}

func randomRules(rng *rand.Rand, n int) []Rule {
	list := make([]Rule, n)
	for i := range list {
		switch rng.Intn(10) {
		case 0:
			list[i] = Rule{Regex: true, Allow: rng.Intn(2) == 0, Regexp: &Regexp{Raw: `\.txt$`}}
		case 1:
			// One that cannot be evaluated: it fails closed.
			list[i] = Rule{Regex: true, Allow: rng.Intn(2) == 0, Regexp: &Regexp{Raw: `(`}}
		case 2:
			list[i] = Rule{Regex: true, Allow: rng.Intn(2) == 0}
		case 3:
			list[i] = Rule{Allow: rng.Intn(2) == 0, Path: "/"}
		default:
			list[i] = Rule{Allow: rng.Intn(3) == 0, Path: randomPath(rng)}
		}
	}
	return list
}

// A set decides as its rules do one by one, and Rule.Matches as it did
// before it left out the canonical forms of names that are all ASCII: for
// random rules and paths in every mix of spellings, with and without case
// folded.
func TestSetDecidesAsTheRulesDoOneByOne(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(20261118))
	failures := 0
	for round := 0; round < 3000 && failures < 10; round++ {
		global, byUser := randomRules(rng, rng.Intn(4)), randomRules(rng, rng.Intn(8))
		for _, fold := range []bool{false, true} {
			set := NewSet(fold, global, byUser)
			for i := 0; i < 30; i++ {
				path := randomPath(rng)

				want := allowsAsBefore(path, fold, global, byUser)
				if got := set.Allows(path); got != want {
					failures++
					t.Errorf("Set.Allows(%+q) = %v with fold %v, the rules one by one say %v\nglobal %s\nuser   %s", path, got, fold, want, describe(global), describe(byUser))
				}
				for _, list := range [][]Rule{global, byUser} {
					for j := range list {
						if got, want := list[j].Matches(path, fold), matchesAsBefore(&list[j], path, fold); got != want {
							failures++
							t.Errorf("Matches(%+q, %v) of %s = %v, was %v", path, fold, describe(list[j:j+1]), got, want)
						}
					}
				}
			}
		}
	}
}

func describe(list []Rule) string {
	var b strings.Builder
	for _, r := range list {
		switch {
		case r.Regex && r.Regexp == nil:
			fmt.Fprintf(&b, "[allow=%v regex without a pattern] ", r.Allow)
		case r.Regex:
			fmt.Fprintf(&b, "[allow=%v regex %q] ", r.Allow, r.Regexp.Raw)
		default:
			fmt.Fprintf(&b, "[allow=%v %+q] ", r.Allow, r.Path)
		}
	}
	return b.String()
}

// The cases the shortcut for ASCII names must not take: one of the two
// names is not ASCII, and their canonical forms are the same.
func TestSetComparesCanonicalFormsWhenOneNameIsNotASCII(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		rule, path string
		fold       bool
	}{
		{"/\ufb01les", "/files/a.txt", false},                // the rule is not ASCII, the path is
		{"/files", "/\ufb01les/a.txt", false},                // the path is not ASCII, the rule is
		{"/files", "/\uff46\uff49\uff4c\uff45\uff53", false}, // fullwidth
		{"/STRASSE", "/stra\u00dfe", true},
		{"/stra\u00dfe", "/strasse/x", true},
		{"/private", "/PR\u0131VATE", true},
	} {
		set := NewSet(tc.fold, []Rule{{Path: tc.rule}})
		if set.Allows(tc.path) {
			t.Errorf("a set with a deny rule for %+q allows %+q (fold %v)", tc.rule, tc.path, tc.fold)
		}
		if !(&Rule{Path: tc.rule}).Matches(tc.path, tc.fold) {
			t.Errorf("a deny rule for %+q does not match %+q (fold %v)", tc.rule, tc.path, tc.fold)
		}
	}

	// No rules, nothing to decide.
	if !NewSet(true).Allows("/anything") {
		t.Error("a set without rules allows nothing")
	}
}

// The scenario a review measured: 30 deny rules, half of them not ASCII,
// and the 3000 names of a directory.
func BenchmarkRulesOverAListing(b *testing.B) {
	var list []Rule
	for i := 0; i < 30; i++ {
		p := fmt.Sprintf("/private-%02d", i)
		if i%2 == 0 {
			p = fmt.Sprintf("/\u6a5f\u5bc6-%02d-Stra\u00dfe", i)
		}
		list = append(list, Rule{Path: p})
	}

	for _, names := range []struct{ name, format string }{
		{"ascii", "/big/report-%05d-final-version.txt"},
		{"not ascii", "/big/\u5831\u544a\u66f8-%05d-\u6700\u7d42\u7248-Gr\u00f6\u00dfe-\u00e9.txt"},
	} {
		paths := make([]string, 3000)
		for i := range paths {
			paths[i] = fmt.Sprintf(names.format, i)
		}
		for _, fold := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s, fold %v, one by one", names.name, fold), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					for _, p := range paths {
						allowsAsBefore(p, fold, list)
					}
				}
			})
			b.Run(fmt.Sprintf("%s, fold %v, set", names.name, fold), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					set := NewSet(fold, list)
					for _, p := range paths {
						set.Allows(p)
					}
				}
			})
		}
	}
}
