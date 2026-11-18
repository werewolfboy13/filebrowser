package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/spf13/viper"
)

// shutdownTimeout is how long the requests in progress get to finish once
// the server is told to stop, unless --shutdownTimeout says otherwise.
//
// It has to be shorter than the time the server is given to stop, or the
// server is killed while it waits and the database is never closed. How long
// Docker gives a container is not the same everywhere: 10 seconds according
// to its documentation, and about 3 were measured with Docker Desktop. So
// compose.yaml sets a stop_grace_period above this value, and the documented
// `docker run` commands a --stop-timeout.
const shutdownTimeout = 8 * time.Second

// shutdownTimeoutFrom returns the time that --shutdownTimeout, or
// FB_SHUTDOWN_TIMEOUT, gives the requests in progress.
//
// It is read as a duration with its unit. A bare number is refused rather
// than taken for seconds, or for the nanoseconds it would otherwise mean.
func shutdownTimeoutFrom(v *viper.Viper) (time.Duration, error) {
	value := v.GetString("shutdownTimeout")
	if value == "" {
		return shutdownTimeout, nil
	}

	timeout, err := time.ParseDuration(value)
	if err != nil || timeout < 0 {
		return 0, fmt.Errorf("invalid shutdownTimeout %q: it takes a duration with a unit, such as 8s or 1m", value)
	}

	return timeout, nil
}

// shutdownServer stops srv. It takes no new connections, waits for the
// requests in progress for up to timeout, and then closes the connections
// that are still open. It reports whether every request finished in time.
//
// A download or an upload can take far longer than any timeout, so running
// out of time is not an error: the caller goes on to close the database and
// to exit normally.
func shutdownServer(srv *http.Server, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err == nil {
		log.Println("Graceful shutdown complete.")
		return true
	}

	log.Printf("Requests were still in progress after %s (%v): closing their connections.", timeout, err)
	if err := srv.Close(); err != nil {
		log.Printf("HTTP close error: %v", err)
	}
	log.Println("Shutdown complete.")

	return false
}
