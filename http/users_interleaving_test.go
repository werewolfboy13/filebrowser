package fbhttp

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/werewolfboy13/filebrowser/v2/rules"
	"github.com/werewolfboy13/filebrowser/v2/users"
)

// The tests in this file put two requests that change, or read, one account
// in a chosen order. Nothing in them depends on timing: the users store is
// wrapped, and a test runs the second request at the exact point of the
// first where it is to land. Every test checks that this point was reached.

// watchedUsers is the users store with places for a test to step in: after a
// record was read, before a change is handed to the store and after the store
// has taken it. The functions run in the goroutine of the request, outside
// any lock of the store.
type watchedUsers struct {
	users.Store
	afterGet    atomic.Pointer[func(id interface{})]
	beforeWrite atomic.Pointer[func(u *users.User, fields []string)]
	afterWrite  atomic.Pointer[func(u *users.User, fields []string)]
}

func (s *watchedUsers) Get(base string, follow bool, id interface{}) (*users.User, error) {
	u, err := s.Store.Get(base, follow, id)
	if f := s.afterGet.Load(); err == nil && f != nil {
		(*f)(id)
	}
	return u, err
}

func (s *watchedUsers) Update(u *users.User, fields ...string) error {
	if f := s.beforeWrite.Load(); f != nil {
		(*f)(u, fields)
	}
	err := s.Store.Update(u, fields...)
	if f := s.afterWrite.Load(); err == nil && f != nil {
		(*f)(u, fields)
	}
	return err
}

// markUpdater is the compare-and-swap of the users store.
type markUpdater interface {
	UpdateIfMark(u *users.User, mark int64, revoke bool, fields ...string) (*users.User, error)
}

func (s *watchedUsers) UpdateIfMark(u *users.User, mark int64, revoke bool, fields ...string) (*users.User, error) {
	if f := s.beforeWrite.Load(); f != nil {
		(*f)(u, fields)
	}
	stored, err := s.Store.(markUpdater).UpdateIfMark(u, mark, revoke, fields...)
	if f := s.afterWrite.Load(); err == nil && f != nil {
		(*f)(u, fields)
	}
	return stored, err
}

// watchUsers puts a watchedUsers in front of the users store of the
// instance.
func (e *fullEnv) watchUsers() *watchedUsers {
	w := &watchedUsers{Store: e.st.Users}
	e.st.Users = w
	return w
}

// onceWhen returns a function that runs fn the first time it is called with ok
// true, and reports through fired that it did.
func onceWhen(fired *atomic.Bool, fn func()) func(ok bool) {
	var armed atomic.Bool
	armed.Store(true)
	return func(ok bool) {
		if ok && armed.CompareAndSwap(true, false) {
			fn()
			fired.Store(true)
		}
	}
}

const (
	adminChosenPassword = "admin-chosen-password-9"
	bobChosenPassword   = "bob-chosen-password-1"
)

// ownChange is bob changing his own password, with his current one.
func (e *fullEnv) ownChange(token string) (int, http.Header) {
	e.t.Helper()
	change := `{"what":"user","which":["password"],"current_password":"` + fullEnvUserPassword +
		`","data":{"id":2,"password":"` + bobChosenPassword + `"}}`
	code, h, _ := e.do(http.MethodPut, "/api/users/2", token, strings.NewReader(change), nil)
	return code, h
}

// adminReset is the administrator setting bob's password.
func (e *fullEnv) adminReset(adminToken string) int {
	e.t.Helper()
	reset := `{"what":"user","which":["password"],"current_password":"` + fullEnvAdminPassword +
		`","data":{"id":2,"password":"` + adminChosenPassword + `"}}`
	code, _, _ := e.do(http.MethodPut, "/api/users/2", adminToken, strings.NewReader(reset), nil)
	return code
}

// adminRemovesDelete is the administrator taking bob's permission to delete
// away, which ends bob's sessions.
func (e *fullEnv) adminRemovesDelete(adminToken string) int {
	e.t.Helper()
	change := `{"what":"user","which":["perm"],"current_password":"` + fullEnvAdminPassword +
		`","data":{"id":2,"perm":{"admin":false,"execute":false,"create":true,"rename":true,"modify":true,"delete":false,"share":true,"download":true}}}`
	code, _, _ := e.do(http.MethodPut, "/api/users/2", adminToken, strings.NewReader(change), nil)
	return code
}

// assertResetStands checks that the administrator's reset of bob's password
// is what is stored, and that no session of bob from before it is left:
// neither the given tokens nor a login with a password of before.
func assertResetStands(t *testing.T, e *fullEnv, tokens map[string]string) {
	t.Helper()
	stored := e.user(2)
	if !users.CheckPwd(adminChosenPassword, stored.Password) {
		t.Errorf("the administrator's reset is not what is stored (bob's own password is: %v)", users.CheckPwd(bobChosenPassword, stored.Password))
	}
	for name, token := range tokens {
		if token == "" {
			continue
		}
		if e.works(token) {
			t.Errorf("%s works after the administrator's reset", name)
		}
		if code, _, renewed := e.do(http.MethodPost, "/api/renew", token, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("renewal of %s after the administrator's reset = %d (the renewed token works: %v)", name, code, e.works(renewed))
		}
	}
	for _, password := range []string{fullEnvUserPassword, bobChosenPassword} {
		if code, _, _ := e.tryLogin("bob", password); code != http.StatusForbidden {
			t.Errorf("login with %q after the administrator's reset = %d, want 403", password, code)
		}
	}
	if code, _, _ := e.tryLogin("bob", adminChosenPassword); code != http.StatusOK {
		t.Errorf("login with the password the administrator set = %d, want 200", code)
	}
}

// Two revocations that are worked out from the same record in the same
// second came to the same mark. The administrator's reset reads bob's
// record and is held, as it is while it hashes the new password. Bob's own
// change runs from start to finish meanwhile, and is handed a token. The
// reset was then stored with the mark bob's change had stored, and bob's
// token, issued at that mark, went on working with the administrator's
// password in place.
//
// "The same second" is made certain by putting bob's mark ahead of the
// clock, as it is for the rest of every second in which a revocation was
// stored.
//
// The second of the two is now refused, with 409: it was decided on a record
// that is no longer the account. Sent again, it is stored, with a mark of
// its own.
func TestTwoRevocationsInTheSameSecondDoNotShareAMark(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) { u.TokensNotBefore = time.Now().Unix() + 120 })
	w := e.watchUsers()
	bobTok := e.login("bob", fullEnvUserPassword)
	adminTok := e.login("admin", fullEnvAdminPassword)

	held, release := make(chan struct{}), make(chan struct{})
	var fired atomic.Bool
	hold := onceWhen(&fired, func() {
		close(held)
		<-release
	})
	before := func(u *users.User, _ []string) { hold(u.ID == 2) }
	w.beforeWrite.Store(&before)

	adminDone := make(chan int, 1)
	go func() { adminDone <- e.adminReset(adminTok) }()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the administrator's reset never got to storing its change")
	}

	code, h := e.ownChange(bobTok)
	bobNew := h.Get("X-Auth-Token")
	if code != http.StatusOK || !e.works(bobNew) {
		close(release)
		t.Fatalf("bob's own change = %d, and its token works: %v", code, e.works(bobNew))
	}
	bobMark := e.user(2).TokensNotBefore

	close(release)
	adminCode := <-adminDone
	if !fired.Load() {
		t.Fatal("the administrator's reset was not held")
	}

	stored := e.user(2)
	resetStored := users.CheckPwd(adminChosenPassword, stored.Password)
	if resetStored && e.works(bobNew) {
		t.Fatalf("bob's session works with the administrator's password in place: both revocations stored the mark %d", stored.TokensNotBefore)
	}

	switch adminCode {
	case http.StatusOK:
		if !resetStored || stored.TokensNotBefore <= bobMark {
			t.Fatalf("the reset was answered with 200: stored %v, mark %d after %d", resetStored, stored.TokensNotBefore, bobMark)
		}
	case http.StatusConflict:
		if resetStored || stored.TokensNotBefore != bobMark || !e.works(bobNew) {
			t.Fatalf("the reset was answered with 409, yet something of it was stored: password %v, mark %d (was %d), bob's session works: %v",
				resetStored, stored.TokensNotBefore, bobMark, e.works(bobNew))
		}
		// Sent again, it goes through, and ends bob's session.
		if code := e.adminReset(adminTok); code != http.StatusOK {
			t.Fatalf("the reset, sent again = %d", code)
		}
		if again := e.user(2).TokensNotBefore; again <= bobMark {
			t.Errorf("the reset stored the mark %d after bob's %d", again, bobMark)
		}
	default:
		t.Fatalf("the administrator's reset = %d", adminCode)
	}
	assertResetStands(t, e, map[string]string{"the token of bob's own change": bobNew, "bob's first token": bobTok})
}

// The administrator's reset is stored while bob's own change is under way:
// bob's request has read the account, has checked his current password
// against it, and is hashing the new one. Bob's change used to be stored
// last, and whole: his password replaced the administrator's, and the token
// he was handed carried the newest mark. The reset was undone, by whoever
// knew the password it was meant to replace.
func TestAdminResetDuringAUsersOwnChangeIsNotUndone(t *testing.T) {
	e := newFullEnv(t)
	w := e.watchUsers()
	bobTok := e.login("bob", fullEnvUserPassword)
	adminTok := e.login("admin", fullEnvAdminPassword)

	var fired atomic.Bool
	reset := onceWhen(&fired, func() {
		if code := e.adminReset(adminTok); code != http.StatusOK {
			t.Errorf("the administrator's reset = %d", code)
		}
	})
	before := func(u *users.User, _ []string) { reset(u.ID == 2) }
	w.beforeWrite.Store(&before)

	code, h := e.ownChange(bobTok)
	if !fired.Load() {
		t.Fatal("the reset was not stored during bob's change")
	}
	if code != http.StatusConflict {
		t.Errorf("bob's own change, with an administrator's reset stored under it = %d, want 409", code)
	}
	if h.Get("X-Auth-Token") != "" {
		t.Error("the refused change handed out a token")
	}
	assertResetStands(t, e, map[string]string{"the token handed out with bob's change": h.Get("X-Auth-Token"), "bob's token": bobTok})
}

// The same, a moment earlier: the reset is stored after bob's request was
// let in (his token was good, for the account as it was) and before it
// reads the account to change it. His current password is checked against
// the record his request came in with, which has the old hash, so the check
// passes; the change must not be made on the strength of that.
func TestOwnChangeLetInBeforeAnAdminResetIsRefused(t *testing.T) {
	e := newFullEnv(t)
	w := e.watchUsers()
	bobTok := e.login("bob", fullEnvUserPassword)
	adminTok := e.login("admin", fullEnvAdminPassword)

	var fired atomic.Bool
	reset := onceWhen(&fired, func() {
		if code := e.adminReset(adminTok); code != http.StatusOK {
			t.Errorf("the administrator's reset = %d", code)
		}
	})
	// The first time bob's record is read from here on is when his request
	// is let in.
	after := func(id interface{}) { reset(id == interface{}(uint(2))) }
	w.afterGet.Store(&after)

	code, h := e.ownChange(bobTok)
	if !fired.Load() {
		t.Fatal("the reset was not stored during bob's change")
	}
	if code != http.StatusConflict && code != http.StatusUnauthorized {
		t.Errorf("bob's own change, let in before an administrator's reset = %d, want 409", code)
	}
	assertResetStands(t, e, map[string]string{"the token handed out with bob's change": h.Get("X-Auth-Token"), "bob's token": bobTok})
}

// Bob's change is stored first, and the administrator's reset right after
// it, before bob's request has made the token it answers with. That token
// was issued from the account as it was read again after the change: with
// the reset in it. It carried the mark of the reset, and outlived it.
//
// The token is now issued from the record bob's change left, as the store
// read it back under its lock. The answer still carries one, as every
// answer to such a change does; it is over as soon as it is made.
func TestTokenOfAnOwnChangeDoesNotOutliveARevocationThatFollows(t *testing.T) {
	e := newFullEnv(t)
	w := e.watchUsers()
	bobTok := e.login("bob", fullEnvUserPassword)
	adminTok := e.login("admin", fullEnvAdminPassword)

	var fired atomic.Bool
	reset := onceWhen(&fired, func() {
		if code := e.adminReset(adminTok); code != http.StatusOK {
			t.Errorf("the administrator's reset = %d", code)
		}
	})
	after := func(u *users.User, _ []string) { reset(u.ID == 2) }
	w.afterWrite.Store(&after)

	code, h := e.ownChange(bobTok)
	if !fired.Load() {
		t.Fatal("the reset was not stored during bob's change")
	}
	// Bob's change was stored, before the reset: that is a success.
	if code != http.StatusOK {
		t.Errorf("bob's own change = %d, want 200", code)
	}
	replacement := h.Get("X-Auth-Token")
	if strings.Count(replacement, ".") != 2 {
		t.Errorf("the answer to a change of the own password carries no token (X-Auth-Token %q)", replacement)
	}
	assertResetStands(t, e, map[string]string{"the token handed out with bob's change": replacement, "bob's token": bobTok})
}

// hookAcceptingBob is a hook that accepts bob with one password, and says
// that his locale is French: something to store at every login.
func hookAcceptingBob(password string) string {
	return `if [ "$USERNAME" = bob ] && [ "$PASSWORD" = '` + password + `' ]; then
  echo hook.action=auth
  echo user.locale=fr
else
  echo hook.action=block
fi
`
}

// A login through the hook reads the account, compares the password with
// it, and stores what the hook said. It stored the whole record it had
// read, with the hook's fields changed. What an administrator stored in the
// meantime was overwritten with what the login had read before: a
// permission that was taken away was back, a rule that was added was gone.
func TestHookLoginDoesNotUndoAnAdminChangeStoredMeanwhile(t *testing.T) {
	changes := map[string]struct {
		body  string
		check func(t *testing.T, e *fullEnv, stored *users.User, token string)
	}{
		// Ends bob's sessions: the login's change is refused, and the login
		// starts over from the account as it is now.
		"a permission is taken away": {
			body: `{"what":"user","which":["perm"],"data":{"id":2,"perm":{"admin":false,"execute":false,"create":true,"rename":true,"modify":true,"delete":false,"share":true,"download":true}}}`,
			check: func(t *testing.T, e *fullEnv, stored *users.User, token string) {
				if stored.Perm.Delete {
					t.Error("the permission the administrator took away is back after the login")
				}
				if code, _, _ := e.do(http.MethodDelete, "/api/resources/keep.txt", token, nil, nil); code != http.StatusForbidden {
					t.Errorf("DELETE with the token of the login = %d, want 403", code)
				}
			},
		},
		// Ends nothing, so nothing tells the login: it must not have
		// written the field in the first place.
		"a rule is added": {
			body: `{"what":"user","which":["rules"],"data":{"id":2,"rules":[{"allow":false,"path":"/private"}]}}`,
			check: func(t *testing.T, e *fullEnv, stored *users.User, token string) {
				if len(stored.Rules) != 1 || stored.Rules[0].Path != "/private" {
					t.Errorf("the rule the administrator added is gone after the login: %+v", stored.Rules)
				}
				if code, _, body := e.do(http.MethodGet, "/api/raw/private/secret.txt", token, nil, nil); code != http.StatusForbidden {
					t.Errorf("GET of the denied file with the token of the login = %d %q, want 403", code, body)
				}
			},
		},
	}

	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			e := newFullEnv(t)
			e.writeBobFile("keep.txt", "x")
			e.writeBobFile("private/secret.txt", "secret")
			adminTok := e.login("admin", fullEnvAdminPassword)
			w := e.watchUsers()
			e.useHookAuth(hookAcceptingBob(fullEnvUserPassword))

			var fired atomic.Bool
			admin := onceWhen(&fired, func() {
				if code, _, body := e.do(http.MethodPut, "/api/users/2", adminTok, strings.NewReader(change.body), nil); code != http.StatusOK {
					t.Errorf("the administrator's change = %d %q", code, body)
				}
			})
			before := func(u *users.User, _ []string) { admin(u.ID == 2) }
			w.beforeWrite.Store(&before)

			code, _, token := e.tryLogin("bob", fullEnvUserPassword)
			if !fired.Load() {
				t.Fatal("the administrator's change was not stored during the login")
			}
			if code != http.StatusOK || !e.works(token) {
				t.Fatalf("the login = %d, and its token works: %v", code, e.works(token))
			}

			stored := e.user(2)
			if stored.Locale != "fr" {
				t.Errorf("what the hook said was not stored: locale %q", stored.Locale)
			}
			change.check(t, e, stored, token)
		})
	}
}

// The hook variant of two revocations in one second: the administrator
// takes a permission away, which ends bob's sessions, and is held before
// the change is stored. Bob logs in through the hook with a password that
// is not the stored one, which ends his sessions too, and gets a token. The
// administrator's change was then stored with the same mark, and bob's
// token survived the revocation that came with it.
func TestHookPasswordChangeAndAdminRevocationDoNotShareAMark(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) { u.TokensNotBefore = time.Now().Unix() + 120 })
	adminTok := e.login("admin", fullEnvAdminPassword)
	w := e.watchUsers()
	e.useHookAuth(hookAcceptingBob("new-source-password-5"))

	held, release := make(chan struct{}), make(chan struct{})
	var fired atomic.Bool
	hold := onceWhen(&fired, func() {
		close(held)
		<-release
	})
	before := func(u *users.User, _ []string) { hold(u.ID == 2) }
	w.beforeWrite.Store(&before)

	adminDone := make(chan int, 1)
	go func() { adminDone <- e.adminRemovesDelete(adminTok) }()
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the administrator's change never got to being stored")
	}

	code, _, hookTok := e.tryLogin("bob", "new-source-password-5")
	if code != http.StatusOK || !e.works(hookTok) {
		close(release)
		t.Fatalf("the hook login = %d, and its token works: %v", code, e.works(hookTok))
	}
	loginMark := e.user(2).TokensNotBefore

	close(release)
	adminCode := <-adminDone

	stored := e.user(2)
	if !stored.Perm.Delete && e.works(hookTok) {
		t.Fatalf("bob's session works after the administrator's revoking change was stored: both stored the mark %d", stored.TokensNotBefore)
	}

	switch adminCode {
	case http.StatusOK:
		if stored.Perm.Delete || stored.TokensNotBefore <= loginMark {
			t.Fatalf("the change was answered with 200: permission still there %v, mark %d after %d", stored.Perm.Delete, stored.TokensNotBefore, loginMark)
		}
	case http.StatusConflict:
		if !stored.Perm.Delete || stored.TokensNotBefore != loginMark {
			t.Fatalf("the change was answered with 409, yet something of it was stored: %+v, mark %d (was %d)", stored.Perm, stored.TokensNotBefore, loginMark)
		}
		if code := e.adminRemovesDelete(adminTok); code != http.StatusOK {
			t.Fatalf("the change, sent again = %d", code)
		}
	default:
		t.Fatalf("the administrator's change = %d", adminCode)
	}

	stored = e.user(2)
	if stored.Perm.Delete || stored.TokensNotBefore <= loginMark {
		t.Errorf("after the administrator's change: permission still there %v, mark %d after %d", stored.Perm.Delete, stored.TokensNotBefore, loginMark)
	}
	if e.works(hookTok) {
		t.Error("bob's session from before the administrator's change works after it")
	}
}

// An account that is revoked again every time a hook login is about to
// store its change does not keep the login going for ever: it gives up, is
// answered with 409, and has stored nothing. The next login, left alone,
// works.
func TestHookLoginGivesUpWhenTheAccountKeepsBeingRevoked(t *testing.T) {
	e := newFullEnv(t)
	w := e.watchUsers()
	inner := w.Store
	e.useHookAuth(hookAcceptingBob(fullEnvUserPassword))

	var revocations atomic.Int32
	var armed atomic.Bool
	armed.Store(true)
	before := func(u *users.User, _ []string) {
		if u.ID != 2 || !armed.Load() {
			return
		}
		revocations.Add(1)
		a, err := inner.Get(e.root, false, uint(2))
		if err != nil {
			t.Error(err)
			return
		}
		a.RevokeTokens(time.Now())
		if err := inner.Update(a, "TokensNotBefore"); err != nil {
			t.Error(err)
		}
	}
	w.beforeWrite.Store(&before)

	code, _, body := e.tryLogin("bob", fullEnvUserPassword)
	if code != http.StatusConflict {
		t.Errorf("the login = %d %q, want 409", code, body)
	}
	if n := revocations.Load(); n < 2 || n > 5 {
		t.Errorf("the login tried to store its change %d times", n)
	}
	if locale := e.user(2).Locale; locale == "fr" {
		t.Error("the login that gave up stored its change")
	}

	armed.Store(false)
	token := e.login("bob", fullEnvUserPassword)
	if !e.works(token) || e.user(2).Locale != "fr" {
		t.Errorf("the next login: token works %v, locale %q", e.works(token), e.user(2).Locale)
	}
}

// A login, or a renewal, reads the account and signs a token a moment
// later. Whatever ends the sessions of the account in between ends that
// token as well: it was issued from the account of before.
func TestLoginAndRenewalOverlappingARevocationDoNotSurvive(t *testing.T) {
	// Each revocation is carried out by a complete request, at the moment
	// the login or renewal under test has read bob's record.
	type setup struct {
		e        *fullEnv
		adminTok string
		otherTok string // another session of bob
	}
	revocations := map[string]struct {
		hook   bool
		revoke func(t *testing.T, s *setup)
	}{
		"an administrator's reset": {revoke: func(t *testing.T, s *setup) {
			if code := s.e.adminReset(s.adminTok); code != http.StatusOK {
				t.Errorf("the reset = %d", code)
			}
		}},
		"the user's own change, from another session": {revoke: func(t *testing.T, s *setup) {
			if code, _ := s.e.ownChange(s.otherTok); code != http.StatusOK {
				t.Errorf("the change = %d", code)
			}
		}},
		"a permission taken away": {revoke: func(t *testing.T, s *setup) {
			if code := s.e.adminRemovesDelete(s.adminTok); code != http.StatusOK {
				t.Errorf("the change = %d", code)
			}
		}},
		"a hook login with a password that changed": {hook: true, revoke: func(t *testing.T, s *setup) {
			if code, _, _ := s.e.tryLogin("bob", "new-source-password-5"); code != http.StatusOK {
				t.Errorf("the hook login = %d", code)
			}
		}},
	}

	for name, rv := range revocations {
		for _, request := range []string{"login", "renewal"} {
			if rv.hook && request == "login" {
				// Two hook logins of one user: covered where the hook is.
				continue
			}
			t.Run(request+" overlapping "+name, func(t *testing.T) {
				// Each has an instance of its own, and waits for a second
				// to pass: side by side.
				t.Parallel()

				e := newFullEnv(t)
				s := &setup{e: e}
				s.adminTok = e.login("admin", fullEnvAdminPassword)
				s.otherTok = e.login("bob", fullEnvUserPassword)
				tok := e.login("bob", fullEnvUserPassword)
				w := e.watchUsers()
				if rv.hook {
					e.useHookAuth(`if [ "$USERNAME" = bob ] && [ "$PASSWORD" = 'new-source-password-5' ]; then
  echo hook.action=auth
else
  echo hook.action=block
fi
`)
				}

				var fired atomic.Bool
				revoke := onceWhen(&fired, func() {
					rv.revoke(t, s)

					// The request under test goes on, and signs, once the
					// second of the revocation has passed. Its token is then
					// dated after the mark: the date alone would take it for
					// one that was issued after the revocation.
					u, err := w.Store.Get(e.root, false, uint(2))
					if err != nil {
						t.Error(err)
						return
					}
					time.Sleep(time.Until(time.Unix(u.TokensNotBefore, 0).Add(20 * time.Millisecond)))
				})
				after := func(id interface{}) {
					revoke(id == interface{}(uint(2)) || id == interface{}("bob"))
				}
				w.afterGet.Store(&after)

				var (
					code  int
					token string
				)
				if request == "login" {
					code, _, token = e.tryLogin("bob", fullEnvUserPassword)
				} else {
					code, _, token = e.do(http.MethodPost, "/api/renew", tok, nil, nil)
				}
				if !fired.Load() {
					t.Fatalf("nothing was revoked during the %s", request)
				}

				// It read the account of before, so it may well have been
				// answered. What it was answered with must be worth nothing.
				if code == http.StatusOK {
					if e.works(token) {
						t.Errorf("the token of a %s that read the account before %s works after it", request, name)
					}
					if code, _, _ := e.do(http.MethodPost, "/api/renew", token, nil, nil); code != http.StatusUnauthorized {
						t.Errorf("renewal of that token = %d, want 401", code)
					}
				}
				for name, token := range map[string]string{"bob's token": tok, "bob's other token": s.otherTok} {
					if e.works(token) {
						t.Errorf("%s works after the revocation", name)
					}
				}
			})
		}
	}
}

// What has not changed: updates that do not end sessions, by the user and
// by an administrator, are stored whatever happens around them, and end
// nothing.
func TestUpdatesThatEndNoSessionAreNotRefused(t *testing.T) {
	e := newFullEnv(t)
	bobTok := e.login("bob", fullEnvUserPassword)
	adminTok := e.login("admin", fullEnvAdminPassword)

	if code, body := e.putUser(bobTok, 2, `{"what":"user","which":["locale"],"data":{"id":2,"locale":"de"}}`); code != http.StatusOK {
		t.Fatalf("bob's change of locale = %d %q", code, body)
	}
	// The whole record, as the administrator's form sends it, with nothing
	// in it that ends a session: the same permissions and scope, no
	// password.
	stored := e.user(2)
	whole := `{"what":"user","which":["all"],"current_password":"` + fullEnvAdminPassword + `","data":{"id":2,"username":"bob","scope":"` + stored.Scope +
		`","locale":"it","viewMode":"list","perm":{"admin":false,"execute":false,"create":true,"rename":true,"modify":true,"delete":true,"share":true,"download":true},"commands":[],"rules":[{"allow":false,"path":"/private"}]}}`
	if code, body := e.putUser(adminTok, 2, whole); code != http.StatusOK {
		t.Fatalf("the administrator's update of the whole record = %d %q", code, body)
	}

	after := e.user(2)
	if after.Locale != "it" || len(after.Rules) != 1 || after.Rules[0] != (rules.Rule{Path: "/private"}) {
		t.Errorf("stored: locale %q, rules %+v", after.Locale, after.Rules)
	}
	if after.TokensNotBefore != stored.TokensNotBefore || !users.CheckPwd(fullEnvUserPassword, after.Password) {
		t.Errorf("an update that ends no session moved the mark (%d to %d) or the password", stored.TokensNotBefore, after.TokensNotBefore)
	}
	if !e.works(bobTok) {
		t.Error("bob's session ended")
	}
}
