package rules

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrInvalidRule is returned by Validate for a rule that cannot be evaluated.
var ErrInvalidRule = errors.New("invalid rule")

// Checker is a Rules checker.
type Checker interface {
	Check(path string) bool
}

// Rule is a allow/disallow rule.
type Rule struct {
	Regex  bool    `json:"regex"`
	Allow  bool    `json:"allow"`
	Path   string  `json:"path"`
	Regexp *Regexp `json:"regexp"`
}

// MatchHidden matches paths with a basename
// that begins with a dot.
func MatchHidden(path string) bool {
	return path != "" && strings.HasPrefix(filepath.Base(path), ".")
}

// Matches matches a path against a rule. When fold is true the comparison is
// case-insensitive: on a case-insensitive filesystem two paths differing only
// in case name the same file, so a rule written for one spelling has to cover
// the others or it can be trivially evaded.
//
// A rule that denies is compared more broadly than one that allows. The
// filesystem decides which spellings name the same file, and it is not known
// here exactly which it takes for one: so a deny rule covers every spelling
// that any filesystem could (see CanonicalName), and an allow rule
// only the ones it always covered. Both err on the side of denying.
//
//   - A deny rule ignores how a name is composed ("é" as one code point or as
//     "e" and a combining accent), whatever fold says: there are filesystems
//     that ignore it and still tell upper from lower case. With fold it
//     ignores case in full, as such a filesystem does: "STRASSE" for
//     "straße", "files" for "ﬁles".
//   - An allow rule matches its path as written, and with fold the lower-cased
//     path, as every rule did before deny rules were widened. An allow rule
//     that matched a path the filesystem keeps apart from its own would open
//     a file nobody allowed.
//
// Regex rules are never folded. An admin-authored pattern means what it says,
// and lowering its input would silently change it; use (?i) for those.
//
// A regex rule that cannot be evaluated (no pattern, or one that does not
// compile) fails closed: it matches every path when it is a deny rule and none
// when it is an allow rule, so it can only ever take access away.
func (r *Rule) Matches(path string, fold bool) bool {
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

	// Many paths against many rules: see Set, which does the same and
	// works out each form once.

	// An allow rule matches exactly, by design, and stops here. The price is
	// that an exception carved out of a denied directory ("/Café/public"
	// under a denied "/Café") only opens the spelling it was written in:
	// where the names on disk are composed otherwise, the deny rule covers
	// them and the allow rule does not, so write it as the names are.
	if r.Allow {
		return false
	}

	// In addition to the comparison above, never instead of it: the two do
	// not fold every letter alike, and a deny rule must not match less than
	// it did.
	//
	// Not for two names that are all ASCII: their canonical forms are what
	// was just compared (the names themselves, or lower-cased with fold).
	if isASCII(path) && isASCII(r.Path) {
		return false
	}
	return isAtOrBelow(CanonicalName(path, fold), CanonicalName(r.Path, fold))
}

// Set is a list of rules made ready for deciding on many paths: what
// Rule.Matches works out from a rule for every path it is given (its path
// in lower case, its canonical form) is worked out once, when the set is
// made, and what it works out from the path for every rule is worked out
// once for each path.
//
// Listing a directory decides on every entry in it, and each decision
// compares the path with every rule. Done rule by rule, a directory of a
// few thousand names that are not ASCII under a few dozen deny rules took
// several times as long to list as before deny rules were compared on
// canonical forms.
//
// A set is made from rules as they are at that moment, and keeps pointing
// at them: it is for rules that do not change while it is used, and is
// meant to live for one request, which reads its rules once.
type Set struct {
	fold  bool
	rules []preparedRule
}

// preparedRule is a rule with the forms of its path that paths are compared
// with.
type preparedRule struct {
	rule *Rule

	// path is the path of the rule as Matches compares it first: in lower
	// case with fold, as written without.
	path string

	// canonical is the canonical form of the path, for a rule that denies.
	canonical string

	// ascii tells that the path is all ASCII.
	ascii bool
}

// NewSet makes a set of the rules of the lists, in their order: the rules
// of a later list come after those of an earlier one. fold is as for
// Rule.Matches.
func NewSet(fold bool, lists ...[]Rule) *Set {
	n := 0
	for _, list := range lists {
		n += len(list)
	}

	s := &Set{fold: fold, rules: make([]preparedRule, 0, n)}
	for _, list := range lists {
		for i := range list {
			rule := &list[i]
			p := preparedRule{rule: rule}
			if !rule.Regex {
				p.path = rule.Path
				if fold {
					p.path = strings.ToLower(rule.Path)
				}
				p.ascii = isASCII(rule.Path)
				if !rule.Allow {
					p.canonical = CanonicalName(rule.Path, fold)
				}
			}
			s.rules = append(s.rules, p)
		}
	}
	return s
}

// Allows reports what the rules decide for path: what the last rule that
// matches it says, and that it is allowed when none does. It is the same as
// asking Rule.Matches of every rule in turn.
func (s *Set) Allows(path string) bool {
	if len(s.rules) == 0 {
		return true
	}

	var (
		// The forms of the path, each worked out when the first rule needs
		// it.
		lower, canonical       string
		hasLower, hasCanonical bool
		ascii                  = isASCII(path)
	)

	// From the last rule to the first: the first that matches decides.
	for i := len(s.rules) - 1; i >= 0; i-- {
		p := &s.rules[i]
		rule := p.rule

		if rule.Regex {
			re, err := rule.Regexp.compile()
			if err != nil {
				// Fails closed: see Rule.Matches.
				if !rule.Allow {
					return false
				}
				continue
			}
			if re.MatchString(path) {
				return rule.Allow
			}
			continue
		}

		compared := path
		if s.fold {
			if !hasLower {
				lower, hasLower = strings.ToLower(path), true
			}
			compared = lower
		}
		if isAtOrBelow(compared, p.path) {
			return rule.Allow
		}

		// The canonical forms, for a rule that denies, unless both names
		// are ASCII: see Rule.Matches.
		if rule.Allow || (ascii && p.ascii) {
			continue
		}
		if !hasCanonical {
			canonical, hasCanonical = CanonicalName(path, s.fold), true
		}
		if isAtOrBelow(canonical, p.canonical) {
			return false
		}
	}

	return true
}

// isAtOrBelow reports whether path is rulePath or lies below it.
func isAtOrBelow(path, rulePath string) bool {
	if path == rulePath {
		return true
	}

	prefix := rulePath
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	return strings.HasPrefix(path, prefix)
}

// Regexp is a wrapper to the native regexp type where we
// save the raw expression.
type Regexp struct {
	Raw    string `json:"raw"`
	regexp *regexp.Regexp
}

// compile returns the compiled expression, compiling it on first use.
func (r *Regexp) compile() (*regexp.Regexp, error) {
	if r == nil {
		return nil, errors.New("missing regular expression")
	}

	if r.regexp == nil {
		re, err := regexp.Compile(r.Raw)
		if err != nil {
			return nil, err
		}
		r.regexp = re
	}

	return r.regexp, nil
}

// MatchString checks if a string matches the regexp. An expression that does
// not compile matches nothing.
func (r *Regexp) MatchString(s string) bool {
	re, err := r.compile()
	if err != nil {
		return false
	}

	return re.MatchString(s)
}

// Validate reports whether the rule can be evaluated. It is meant to be called
// before a rule is stored.
func (r *Rule) Validate() error {
	if !r.Regex {
		return nil
	}

	if _, err := r.Regexp.compile(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}

	return nil
}

// Validate validates every rule of a list, as Rule.Validate does.
func Validate(list []Rule) error {
	for i := range list {
		if err := list[i].Validate(); err != nil {
			return err
		}
	}

	return nil
}
