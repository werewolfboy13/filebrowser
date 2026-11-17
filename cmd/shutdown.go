package cmd

import (
	"context"
	"log"
	"net/http"
	"time"
)

// shutdownTimeout is how long the requests in progress get to finish once
// the server is told to stop.
//
// It has to be shorter than the time the server is given to stop, or the
// server is killed while it waits and the database is never closed. Docker
// gives a container 10 seconds unless it is told otherwise, and compose.yaml
// sets a stop_grace_period above this value.
const shutdownTimeout = 8 * time.Second

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
