package fbhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/golang-jwt/jwt/v5/request"

	fbAuth "github.com/werewolfboy13/filebrowser/v3/auth"
	fberrors "github.com/werewolfboy13/filebrowser/v3/errors"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
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

	// PasswordLogin marks a token that was issued in answer to a login with
	// the account's password. A token that comes from a renewal does not
	// carry it, so it says that whoever got this token knew the password at
	// the time, which holding a session alone does not. See confirmPassword
	// for what it is used for.
	PasswordLogin bool `json:"pwl,omitempty"`

	// Record is the revocation mark (users.User.TokensNotBefore) of the
	// account record the token was issued from: which state of the account
	// the request that got the token had read. The token is good for as long
	// as the account still carries that mark.
	//
	// The date of a token cannot say this. A request reads the account,
	// works for a while (a login hashes the password) and signs; a change of
	// password that is stored in between ends every session, and the token
	// signed a moment later, from the record of before, is dated after it.
	//
	// It is a pointer so that a mark of zero (an account that was never
	// revoked) is written down as well: a token without the claim is one
	// from before the claim existed, and is placed by its date alone.
	Record *int64 `json:"rec,omitempty"`

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

// withUser runs fn for a request that carries a valid session token.
func withUser(fn handleFunc) handleFunc {
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

		d.user, err = d.store.Users.Get(d.server.Root, d.server.FollowExternalSymlinks, tk.User.ID)
		if errors.Is(err, fberrors.ErrNotExist) {
			// The account was deleted after the token was issued.
			return http.StatusUnauthorized, nil
		}
		if err != nil {
			return http.StatusInternalServerError, err
		}

		// A token issued before the user's tokens were last revoked is as
		// good as expired, for every request and for a renewal alike. One
		// without an issue time cannot be placed, and is refused as soon as
		// there was a revocation.
		if d.user.TokensNotBefore != 0 {
			if tk.IssuedAt == nil || d.user.TokenRevoked(tk.IssuedAt.Time) {
				return http.StatusUnauthorized, nil
			}
		}
		// So is a token that was issued from the account as it was before
		// the last revocation, whatever its date: see authToken.Record. This
		// only ever refuses a token the check above let through.
		if tk.Record != nil && *tk.Record != d.user.TokensNotBefore {
			return http.StatusUnauthorized, nil
		}
		d.token = token.Raw
		d.loginToken = tk.PasswordLogin

		// Only a session that is still good is told to renew its token. Set
		// before the checks above, the header went out with the 401 that
		// ends a session as well, and a client that renews when it sees the
		// header asked for a renewal that could only be refused.
		expiresSoon := tk.ExpiresAt != nil && time.Until(tk.ExpiresAt.Time) < time.Hour
		updated := tk.IssuedAt != nil && tk.IssuedAt.Unix() < d.store.Users.LastUpdate(tk.User.ID)

		if expiresSoon || updated {
			w.Header().Add("X-Renew-Token", "true")
		}

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
		leave := func() {}
		if d.limits != nil && checksPassword(d.settings.AuthMethod) {
			// Read the way the authers read the body: the first JSON value
			// in it, with a user name and a password that are strings.
			var cred struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(bytes.NewReader(body)).Decode(&cred); err != nil || cred.Username == "" {
				// The authers refuse such a body before they look at a
				// password, or have no account to look one up for. It is
				// not a guess, so it is not counted, and it leaves nothing
				// behind in the limiter: requests that cost the server
				// nothing must not be able to fill it.
				return http.StatusForbidden, nil
			}

			addr := clientAddr(r)
			attempt, retryAfter := d.limits.login.begin(addr, limiterKey("login", addr, cred.Username))
			if attempt == nil {
				return tooManyAttempts(w, retryAfter)
			}
			done = attempt

			// Checking a password is work: a hash to compute, or a hook to
			// run. Only so much of it is done at a time for one address,
			// and the rest waits its turn: see workGate. A login whose turn
			// does not come is turned away without having been looked at,
			// which says nothing about its password.
			//
			// A turn is held for as long as the auther takes. The authers
			// bound that themselves: a hook, and a captcha service, are
			// given a time to answer, and are given up when the request
			// ends (see auth.HookAuth.RunCommand, auth.ReCaptcha.Ok).
			if leave = d.limits.loginWork.enter(r.Context(), addr); leave == nil {
				done(attemptAborted)
				return tooManyAttempts(w, time.Second)
			}
		}

		user, err := func() (*users.User, error) {
			defer leave()
			return auther.Auth(r, d.store.Users, d.settings, d.server)
		}()
		switch {
		case errors.Is(err, fbAuth.ErrNoAttempt):
			// Refused before a password was looked at, for a reason the
			// check above does not know (a failed captcha, a field of the
			// auther's own): not a guess either.
			done(attemptAborted)
			return http.StatusForbidden, nil
		case errors.Is(err, os.ErrPermission):
			done(attemptFailed)
			return http.StatusForbidden, nil
		case errors.Is(err, fbAuth.ErrHookRefused):
			// The hook was given the password and did not accept it, without
			// saying that it is wrong; or it was stopped before it answered
			// because the client gave up on it. For the client that is still
			// an error of the server; for the limiter it is a guess that
			// failed, or guessing through a hook that answers this way, or
			// that a client gives up on when it does not accept at once,
			// would never be slowed down.
			done(attemptFailed)
			return http.StatusInternalServerError, err
		case errors.Is(err, fberrors.ErrUserChanged):
			// The account was changed, more than once, while the login was
			// storing what the hook said about it, and the login gave up:
			// see HookAuth.SaveUser. Nothing was stored. That says nothing
			// about the password, and trying again is all there is to do.
			done(attemptAborted)
			return http.StatusConflict, err
		case err != nil:
			done(attemptAborted)
			return http.StatusInternalServerError, err
		}
		done(attemptSucceeded)

		signed, err := signSessionToken(d, user, tokenExpireTime, d.settings.AuthMethod == fbAuth.MethodJSONAuth)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		return writeToken(w, signed)
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
	return withUser(func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("X-Renew-Token", "false")
		return printToken(w, r, d, d.user, tokenExpireTime)
	})
}

// newTokenHeader is the response header a session is handed its new token
// in, when what it just did ended the token it came with: see
// replaceSessionToken.
const newTokenHeader = "X-Auth-Token"

// replaceSessionToken hands the session that made the request a token to go
// on with, in the newTokenHeader header of the response.
//
// It is for a request that revokes the tokens of its own user (a change of
// the own password, permissions or scope). The token the request came with
// is over from that moment, like every other one of the user, and there is no
// way of renewing it: a renewal can be asked for by anyone who holds the
// token, and whoever asks first would be the one to keep a session. The new
// token goes out with the answer to the change instead, which only the
// client that made the change receives.
//
// The token is a credential, so the response must not be stored. Every
// response of the API already says so (see globalHeaders); it is said here
// as well in case that is ever relaxed. The client is also told not to renew:
// the only token it could renew with is the one that was just revoked.
func replaceSessionToken(w http.ResponseWriter, d *data, user *users.User, tokenExpirationTime time.Duration) error {
	signed, err := signSessionToken(d, user, tokenExpirationTime, false)
	if err != nil {
		return err
	}

	w.Header().Set(newTokenHeader, signed)
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("X-Renew-Token", "false")
	return nil
}

// printToken answers with a token for user that does not say how the session
// began: what a renewal hands out.
func printToken(w http.ResponseWriter, _ *http.Request, d *data, user *users.User, tokenExpirationTime time.Duration) (int, error) {
	signed, err := signSessionToken(d, user, tokenExpirationTime, false)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	return writeToken(w, signed)
}

func writeToken(w http.ResponseWriter, signed string) (int, error) {
	w.Header().Set("Content-Type", "text/plain")
	if _, err := w.Write([]byte(signed)); err != nil {
		return http.StatusInternalServerError, err
	}
	return 0, nil
}

// signSessionToken issues a session token for user. passwordLogin marks it as
// the answer to a login with the account's password.
//
// user must be the record the request read, as it read it: the token is tied
// to the revocation mark of that record (see authToken.Record), and is over
// as soon as the stored account carries another one.
func signSessionToken(d *data, user *users.User, tokenExpirationTime time.Duration, passwordLogin bool) (string, error) {
	record := user.TokensNotBefore
	claims := &authToken{
		PasswordLogin: passwordLogin,
		Record:        &record,
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

	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(d.settings.Key)
}
