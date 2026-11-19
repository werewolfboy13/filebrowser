package fbhttp

import (
	"testing"

	"github.com/werewolfboy13/filebrowser/v2/rules"
	"github.com/werewolfboy13/filebrowser/v2/settings"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// The rules of a request are made ready once and used for every path it
// checks. They are those of the user and the settings the request has at
// the time of the check: when a handler puts another user in place, or
// other rules, the checks that follow go by those.
func TestRulesOfARequestFollowItsUserAndSettings(t *testing.T) {
	alice := &users.User{Rules: []rules.Rule{{Path: "/alice-may-not"}}}
	bob := &users.User{Rules: []rules.Rule{{Path: "/bob-may-not"}}}
	d := &data{
		settings: &settings.Settings{Rules: []rules.Rule{{Path: "/nobody-may"}}},
		server:   &settings.Server{},
		user:     alice,
	}

	expect := func(when string, allowed map[string]bool) {
		t.Helper()
		for path, want := range allowed {
			if got := d.rulesAllow(path); got != want {
				t.Errorf("%s: rulesAllow(%q) = %v, want %v", when, path, got, want)
			}
		}
	}

	expect("alice", map[string]bool{"/alice-may-not/x": false, "/bob-may-not/x": true, "/nobody-may/x": false, "/open": true})
	set := d.ruleSet
	expect("alice, again", map[string]bool{"/alice-may-not": false, "/open": true})
	if d.ruleSet != set {
		t.Error("the rules were made ready again for a request that did not change")
	}

	d.user = bob
	expect("bob", map[string]bool{"/alice-may-not/x": true, "/bob-may-not/x": false, "/nobody-may/x": false})

	d.settings.Rules = []rules.Rule{{Path: "/closed-now"}}
	expect("other global rules", map[string]bool{"/nobody-may/x": true, "/closed-now/x": false, "/bob-may-not/x": false})

	bob.Rules = []rules.Rule{{Path: "/bob-may-not"}, {Allow: true, Path: "/bob-may-not/after-all"}}
	expect("other rules of the user", map[string]bool{"/bob-may-not/x": false, "/bob-may-not/after-all/x": true})

	d.settings.Rules = nil
	bob.Rules = nil
	expect("no rules", map[string]bool{"/closed-now/x": true, "/bob-may-not/x": true})

	// What the filesystem does with case is part of it.
	bob.Rules = []rules.Rule{{Path: "/Bob-May-Not"}}
	expect("case told apart", map[string]bool{"/bob-may-not/x": true})
	d.server.CaseInsensitiveFs = true
	expect("case ignored", map[string]bool{"/bob-may-not/x": false})
}
