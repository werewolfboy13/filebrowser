package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// errInvalidRedisURL is returned for a Redis URL that cannot be used. It says
// nothing about the URL itself: see uploadCacheError.
var errInvalidRedisURL = errors.New("failed to initialize upload cache: the Redis URL " +
	"(--redisCacheUrl, FB_REDIS_CACHE_URL) is not valid. It is not shown here, because it holds " +
	"the password. Its form is redis://user:password@host:port, or rediss:// for TLS, and a " +
	"password with characters other than letters, digits, '-', '.', '_' and '~' has to be " +
	"percent-encoded in it")

// uploadCacheError returns the error to report when the upload cache could
// not be created, without the Redis URL or its password in it.
//
// The error ends up in the log, on every restart of a container that cannot
// start. For a URL that cannot be parsed, the parser's message quotes the URL,
// or the part of it that it tripped over, which is usually a piece of the
// password: a password with a "/" in it is read as a host and a port. So none
// of that message is kept. For a URL that can be parsed, the error is about
// the connection, and names at most the host.
func uploadCacheError(err error, redisURL string) error {
	options, parseErr := redis.ParseURL(redisURL)
	if parseErr != nil {
		return errInvalidRedisURL
	}

	message := err.Error()
	redacted := message
	for _, secret := range []string{redisURL, options.Password} {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[redacted]")
		}
	}
	if redacted != message {
		return fmt.Errorf("failed to initialize upload cache: %s", redacted)
	}

	return fmt.Errorf("failed to initialize upload cache: %w", err)
}
