package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	fbhttp "github.com/filebrowser/filebrowser/v2/http"
)

// containsPieceOf reports whether text holds any four characters in a row of
// secret. The URL parser quotes the part of the URL it tripped over, so a
// message can give away a piece of the password without the whole of it.
func containsPieceOf(text, secret string) (string, bool) {
	const length = 4
	for i := 0; i+length <= len(secret); i++ {
		if piece := secret[i : i+length]; strings.Contains(text, piece) {
			return piece, true
		}
	}

	return "", false
}

// A Redis password with a character that is special in a URL made the server
// print the whole URL, password included, each time it failed to start.
func TestUploadCacheErrorHidesTheRedisURL(t *testing.T) {
	// What `openssl rand -base64` gives, with each special character in turn.
	for _, special := range []string{"/", "@", "#", "?", ":", "%", " ", "[", "\\"} {
		t.Run("password with "+special, func(t *testing.T) {
			password := "Zm9vQmFy" + special + "c2VjcmV0+cXV4=="
			redisURL := "redis://default:" + password + "@redis.invalid:6379"

			var err error
			if _, parseErr := redis.ParseURL(redisURL); parseErr != nil {
				// The server's own path. It fails before it connects to anything.
				_, err = fbhttp.NewUploadCache(redisURL)
				if err == nil {
					t.Fatal("expected an error")
				}
			} else {
				// A URL that is valid is not tried here: it would be connected
				// to. This is what a refused connection looks like.
				err = fmt.Errorf("failed to connect to redis: %w",
					errors.New("dial tcp: lookup redis.invalid: no such host"))
			}

			message := uploadCacheError(err, redisURL).Error()

			if strings.Contains(message, redisURL) {
				t.Errorf("the message has the URL in it: %s", message)
			}
			if piece, found := containsPieceOf(message, password); found {
				t.Errorf("the message has %q of the password in it: %s", piece, message)
			}
			if !strings.HasPrefix(message, "failed to initialize upload cache: ") {
				t.Errorf("the message does not say what failed: %s", message)
			}
		})
	}
}

func TestUploadCacheErrorForAnInvalidURL(t *testing.T) {
	tests := map[string]string{
		"a password that reads as a port":     "redis://default:Zm9v/YmFy+cXV4==@redis.invalid:6379",
		"a password that reads as a database": "redis://default:1234/5678secret@redis.invalid:6379",
		"another scheme":                      "https://default:c2VjcmV0cGFzcw@redis.invalid:6379",
		"no scheme":                           "default:c2VjcmV0cGFzcw@redis.invalid:6379",
	}

	for name, redisURL := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := fbhttp.NewUploadCache(redisURL)
			if err == nil {
				t.Fatal("expected an error")
			}

			got := uploadCacheError(err, redisURL)
			if !errors.Is(got, errInvalidRedisURL) {
				t.Errorf("error = %v, want the one for an invalid URL", got)
			}
			// The message names the setting and how to write a password.
			for _, want := range []string{"FB_REDIS_CACHE_URL", "--redisCacheUrl", "percent-encoded"} {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("the message does not mention %s: %v", want, got)
				}
			}
		})
	}
}

func TestUploadCacheErrorKeepsTheConnectionError(t *testing.T) {
	const redisURL = "redis://default:c2VjcmV0cGFzcw@redis.invalid:6379"
	cause := errors.New("WRONGPASS invalid username-password pair or user is disabled")
	err := fmt.Errorf("failed to connect to redis: %w", cause)

	got := uploadCacheError(err, redisURL)

	if !errors.Is(got, cause) {
		t.Errorf("error = %v, want it to wrap the cause", got)
	}
	want := "failed to initialize upload cache: failed to connect to redis: " + cause.Error()
	if got.Error() != want {
		t.Errorf("message = %q, want %q", got.Error(), want)
	}
}

func TestUploadCacheErrorRedactsAConnectionErrorThatQuotesThePassword(t *testing.T) {
	const password = "c2VjcmV0cGFzcw"
	const redisURL = "redis://default:" + password + "@redis.invalid:6379"
	err := fmt.Errorf("failed to connect to redis: AUTH %s was refused by %s", password, redisURL)

	message := uploadCacheError(err, redisURL).Error()

	if strings.Contains(message, password) {
		t.Errorf("the message has the password in it: %s", message)
	}
	if !strings.Contains(message, "[redacted]") {
		t.Errorf("the message does not show that something was left out: %s", message)
	}
}
