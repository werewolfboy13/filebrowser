package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// MethodJSONAuth is used to identify json auth.
const MethodJSONAuth settings.AuthMethod = "json"

// dummyHash is used to prevent user enumeration timing attacks.
// It MUST be a valid bcrypt hash.
const dummyHash = "$2a$10$O4mEMeOL/nit6zqe.WQXauLRbRlzb3IgLHsa26Pf0N/GiU9b.wK1m"

type jsonCred struct {
	Password  string `json:"password"`
	Username  string `json:"username"`
	ReCaptcha string `json:"recaptcha"`
}

// JSONAuth is a json implementation of an Auther.
type JSONAuth struct {
	ReCaptcha *ReCaptcha `json:"recaptcha" yaml:"recaptcha"`
}

// Auth authenticates the user via a json in content body.
func (a JSONAuth) Auth(r *http.Request, usr users.Store, _ *settings.Settings, srv *settings.Server) (*users.User, error) {
	var cred jsonCred

	if r.Body == nil {
		return nil, ErrNoAttempt
	}

	err := json.NewDecoder(r.Body).Decode(&cred)
	if err != nil {
		return nil, ErrNoAttempt
	}

	// No account has an empty name: there is no password to compare with.
	if cred.Username == "" {
		return nil, ErrNoAttempt
	}

	// If ReCaptcha is enabled, check the code.
	if a.ReCaptcha != nil && a.ReCaptcha.Secret != "" {
		ok, err := a.ReCaptcha.Ok(r.Context(), cred.ReCaptcha)

		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, ErrNoAttempt
		}
	}

	u, err := usr.Get(srv.Root, srv.FollowExternalSymlinks, cred.Username)

	hash := dummyHash
	if err == nil {
		hash = u.Password
	}

	if !users.CheckPwd(cred.Password, hash) {
		return nil, os.ErrPermission
	}

	if err != nil {
		return nil, os.ErrPermission
	}

	return u, nil
}

// LoginPage tells that json auth doesn't require a login page.
func (a JSONAuth) LoginPage() bool {
	return true
}

const reCaptchaAPI = "/recaptcha/api/siteverify"

// ReCaptcha identifies a recaptcha connection.
type ReCaptcha struct {
	Host   string `json:"host"`
	Key    string `json:"key"`
	Secret string `json:"secret"`
}

// reCaptchaTimeout is how long the captcha service gets to answer. A login
// waits for it, and holds one of the turns of its client address for as
// long: see hookTimeout. It is a variable so that tests can shorten it.
var reCaptchaTimeout = 10 * time.Second

// Ok checks if a reCaptcha responde is correct. The service is asked for as
// long as ctx lasts, and no longer than reCaptchaTimeout.
func (r *ReCaptcha) Ok(ctx context.Context, response string) (bool, error) {
	body := url.Values{}
	body.Set("secret", r.Secret)
	body.Add("response", response)

	client := &http.Client{Timeout: reCaptchaTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Host+reCaptchaAPI, strings.NewReader(body.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, nil
	}

	var data struct {
		Success bool `json:"success"`
	}

	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return false, err
	}

	return data.Success, nil
}
