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

	if r.Allow {
		return false
	}

	// In addition to the comparison above, never instead of it: the two do
	// not fold every letter alike, and a deny rule must not match less than
	// it did.
	return isAtOrBelow(CanonicalName(path, fold), CanonicalName(r.Path, fold))
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
