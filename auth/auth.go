package auth

import (
	"fmt"
	"net/http"
	"os"

	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// Auther is the authentication interface.
type Auther interface {
	// Auth is called to authenticate a request.
	Auth(r *http.Request, usr users.Store, stg *settings.Settings, srv *settings.Server) (*users.User, error)
	// LoginPage indicates if this auther needs a login page.
	LoginPage() bool
}

// ErrNoAttempt is returned by an auther that checks a password for a login
// it refuses before it has looked at one: a request without a body, a body
// it cannot read, a failed captcha. Such a request says nothing about any
// password, so it is not a guess at one.
//
// It is os.ErrPermission as well: to a caller that only asks whether the
// login was refused, it is.
var ErrNoAttempt = fmt.Errorf("%w: no credentials to check", os.ErrPermission)
