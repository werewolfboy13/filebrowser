package fbhttp

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/golang-jwt/jwt/v5/request"

	fbAuth "github.com/filebrowser/filebrowser/v2/auth"
	fberrors "github.com/filebrowser/filebrowser/v2/errors"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

const (
	DefaultTokenExpirationTime = time.Hour * 2

	maxAuthBodySize = 1 << 20 // 1 MiB
)

type userInfo struct {
	ID                    uint              `json:"id"`
	Locale                string            `json:"locale"`
	ViewMode              users.ViewMode    `json:"viewMode"`
	SingleClick           bool              `json:"singleClick"`
	RedirectAfterCopyMove bool              `json:"redirectAfterCopyMove"`
	Perm                  users.Permissions `json:"perm"`
	Commands              []string          `json:"commands"`
	LockPassword          bool              `json:"lockPassword"`
	HideDotfiles          bool              `json:"hideDotfiles"`
	DateFormat            bool              `json:"dateFormat"`
	Username              string            `json:"username"`
	AceEditorTheme        string            `json:"aceEditorTheme"`
}

type authToken struct {
	User userInfo `json:"user"`
	jwt.RegisteredClaims
}

type extractor []string

func (e extractor) ExtractToken(r *http.Request) (string, error) {
	token, _ := request.HeaderExtractor{"X-Auth"}.ExtractToken(r)

	// Checks if the token isn't empty and if it contains two dots.
	// The former prevents incompatibility with URLs that previously
	// used basic auth.
	if token != "" && strings.Count(token, ".") == 2 {
		return token, nil
	}

	if r.Method == http.MethodGet {
		cookie, _ := r.Cookie("auth")
		if cookie != nil && strings.Count(cookie.Value, ".") == 2 {
			return cookie.Value, nil
		}
	}

	return "", request.ErrNoTokenInRequest
}

func renewableErr(err error, r *http.Request, d *data, tk *authToken) bool {
	if d.settings.AuthMethod != fbAuth.MethodProxyAuth || err == nil {
		return false
	}

	if d.settings.LogoutPage == settings.DefaultLogoutPage {
		return false
	}

	if !errors.Is(err, jwt.ErrTokenExpired) {
		return false
	}

	// The expiration is only waived because the trusted proxy, not the token,
	// decides when the session ends. Require the proxy to still assert the same
	// identity on this request, otherwise a token that leaked before it expired
	// would authenticate on its own forever.
	return proxyAsserts(r, d, tk.User.ID)
}

// proxyAsserts reports whether the proxy-auth header on r identifies the user
// the token was issued for. The username is resolved through the user store, so
// that it is matched exactly as a regular proxy login would match it.
func proxyAsserts(r *http.Request, d *data, id uint) bool {
	auther, err := d.store.Auth.Get(fbAuth.MethodProxyAuth)
	if err != nil {
		return false
	}

	proxy, ok := auther.(*fbAuth.ProxyAuth)
	if !ok || proxy.Header == "" {
		return false
	}

	username := r.Header.Get(proxy.Header)
	if username == "" {
		return false
	}

	user, err := d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, username)
	if err != nil {
		return false
	}

	return user.ID == id
}

// renewGrace lets one session renew a token that was just revoked.
//
// A change that revokes the tokens of a user (see users.User.RevokeTokens)
// also revokes the token of the session making it. For that one session the
// handler registers its token here, and the next renewal presenting exactly
// that token is let through, once and within renewGracePeriod. The web
// interface renews as soon as it sees X-Renew-Token on the response to the
// change, so it carries on without a new login, while every other token of
// the user stays revoked.
var renewGrace = &renewGraceStore{entries: map[uint]renewGraceEntry{}}

const renewGracePeriod = time.Minute

type renewGraceEntry struct {
	token   [sha256.Size]byte
	expires time.Time
}

// renewGraceStore holds at most one entry per user, so it is bounded by the
// number of users.
type renewGraceStore struct {
	mu      sync.Mutex
	entries map[uint]renewGraceEntry
}

// allow lets token, a token of user id, be renewed once although revoked.
func (s *renewGraceStore) allow(id uint, token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	s.entries[id] = renewGraceEntry{token: sha256.Sum256([]byte(token)), expires: time.Now().Add(renewGracePeriod)}
	s.mu.Unlock()
}

// take reports whether token may be renewed although revoked, and uses the
// permission up.
func (s *renewGraceStore) take(id uint, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(entry.token[:], sum[:]) != 1 {
		return false
	}

	delete(s.entries, id)
	return time.Now().Before(entry.expires)
}

func withUser(fn handleFunc) handleFunc {
	return authenticated(fn, false)
}

// authenticated runs fn for a request that carries a valid session token.
// With renewal set, a token that was revoked is accepted when renewGrace has
// it: only the renewal endpoint does that.
func authenticated(fn handleFunc, renewal bool) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		keyFunc := func(_ *jwt.Token) (interface{}, error) {
			return d.settings.Key, nil
		}

		var tk authToken
		p := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
		token, err := request.ParseFromRequest(r, &extractor{}, keyFunc, request.WithClaims(&tk), request.WithParser(p))
		if (err != nil || !token.Valid) && !renewableErr(err, r, d, &tk) {
			return http.StatusUnauthorized, nil
		}

		expiresSoon := tk.ExpiresAt != nil && time.Until(tk.ExpiresAt.Time) < time.Hour
		updated := tk.IssuedAt != nil && tk.IssuedAt.Unix() < d.store.Users.LastUpdate(tk.User.ID)

		if expiresSoon || updated {
			w.Header().Add("X-Renew-Token", "true")
		}

		d.user, err = d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, tk.User.ID)
		if errors.Is(err, fberrors.ErrNotExist) {
			// The account was deleted after the token was issued.
			return http.StatusUnauthorized, nil
		}
		if err != nil {
			return http.StatusInternalServerError, err
		}

		// A token issued before the user's tokens were last revoked is as
		// good as expired. One without an issue time cannot be placed, and is
		// refused as soon as there was a revocation.
		if d.user.TokensNotBefore != 0 {
			if tk.IssuedAt == nil || d.user.TokenRevoked(tk.IssuedAt.Time) {
				if !renewal || !renewGrace.take(d.user.ID, token.Raw) {
					return http.StatusUnauthorized, nil
				}
			}
		}
		d.token = token.Raw

		canonicalizeRequestPath(r)
		return fn(w, r, d)
	}
}

func withAdmin(fn handleFunc) handleFunc {
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Admin {
			return http.StatusForbidden, nil
		}

		return fn(w, r, d)
	})
}

func loginHandler(tokenExpireTime time.Duration) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		// The credentials are read here, within a time limit, and handed to
		// the auther from memory: a login that never sends its body must not
		// hold a connection, and the user name is needed before the password
		// is looked at.
		var body []byte
		if r.Body != nil {
			var (
				status int
				err    error
			)
			body, status, err = readSmallBody(w, r)
			if err != nil {
				return status, err
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		auther, err := d.store.Auth.Get(d.settings.AuthMethod)
		if err != nil {
			return http.StatusInternalServerError, err
		}

		// Guessing a password is slowed down per user name and client
		// address. Only methods that check a password are concerned: a proxy
		// vouches for its users itself.
		done := func(attemptOutcome) {}
		if d.limits != nil && checksPassword(d.settings.AuthMethod) {
			var cred struct {
				Username string `json:"username"`
			}
			_ = json.Unmarshal(body, &cred)

			attempt, retryAfter := d.limits.login.begin(limiterKey("login", clientAddr(r), cred.Username))
			if attempt == nil {
				return tooManyAttempts(w, retryAfter)
			}
			done = attempt
		}

		user, err := auther.Auth(r, d.store.Users, d.settings, d.server)
		switch {
		case errors.Is(err, os.ErrPermission):
			done(attemptFailed)
			return http.StatusForbidden, nil
		case err != nil:
			done(attemptAborted)
			return http.StatusInternalServerError, err
		}
		done(attemptSucceeded)

		return printToken(w, r, d, user, tokenExpireTime)
	}
}

// checksPassword reports whether logging in with the method means presenting
// a password that could be guessed.
func checksPassword(method settings.AuthMethod) bool {
	return method == fbAuth.MethodJSONAuth || method == fbAuth.MethodHookAuth
}

type signupBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var signupHandler = func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
	if !d.settings.Signup {
		return http.StatusMethodNotAllowed, nil
	}

	if r.Body == nil {
		return http.StatusBadRequest, nil
	}

	info := &signupBody{}
	if status, err := decodeJSONBody(w, r, info); err != nil {
		return status, err
	}

	if info.Password == "" || info.Username == "" {
		return http.StatusBadRequest, nil
	}

	user := &users.User{
		Username: info.Username,
	}

	if err := user.ValidateLengths("Username"); err != nil {
		return http.StatusBadRequest, err
	}

	d.settings.Defaults.Apply(user)

	// Users signed up via the signup handler should never become admins, even
	// if that is the default permission.
	user.Perm.Admin = false

	// Self-registered users should not inherit execution capabilities from
	// default settings, regardless of what the administrator has configured
	// as the default. Execution rights must be explicitly granted by an admin.
	user.Perm.Execute = false
	user.Commands = []string{}

	pwd, err := users.ValidateAndHashPwd(info.Password, d.settings.MinimumPasswordLength)
	if err != nil {
		return http.StatusBadRequest, err
	}

	user.Password = pwd

	derivedScope, err := d.settings.CreateUserHome(user, d.server.Root, false)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	log.Printf("new user: %s, home dir: [%s].", user.Username, user.Scope)

	err = d.store.Users.SaveProvisioned(user, derivedScope)
	if errors.Is(err, fberrors.ErrExist) {
		return http.StatusConflict, err
	} else if err != nil {
		return http.StatusInternalServerError, err
	}

	return http.StatusOK, nil
}

func renewHandler(tokenExpireTime time.Duration) handleFunc {
	return authenticated(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("X-Renew-Token", "false")
		return printToken(w, r, d, d.user, tokenExpireTime)
	}, true)
}

func printToken(w http.ResponseWriter, _ *http.Request, d *data, user *users.User, tokenExpirationTime time.Duration) (int, error) {
	claims := &authToken{
		User: userInfo{
			ID:                    user.ID,
			Locale:                user.Locale,
			ViewMode:              user.ViewMode,
			SingleClick:           user.SingleClick,
			RedirectAfterCopyMove: user.RedirectAfterCopyMove,
			Perm:                  user.Perm,
			LockPassword:          user.LockPassword,
			Commands:              user.Commands,
			HideDotfiles:          user.HideDotfiles,
			DateFormat:            user.DateFormat,
			Username:              user.Username,
			AceEditorTheme:        user.AceEditorTheme,
		},
		RegisteredClaims: jwt.RegisteredClaims{
			// Dated so that it is not mistaken for a token revoked a moment
			// ago: see users.User.IssueTime.
			IssuedAt:  jwt.NewNumericDate(user.IssueTime(time.Now())),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenExpirationTime)),
			Issuer:    "File Browser",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(d.settings.Key)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	w.Header().Set("Content-Type", "text/plain")
	if _, err := w.Write([]byte(signed)); err != nil {
		return http.StatusInternalServerError, err
	}
	return 0, nil
}
