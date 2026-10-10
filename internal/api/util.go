package api

import (
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// randomID returns a short random identifier for synthetic message ids.
func randomID() string {
	return uuid.NewString()
}

// nowUnix returns the current unix timestamp in seconds.
func nowUnix() int64 {
	return time.Now().Unix()
}

// atoiSafe parses an int, returning 0 on failure.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseSSE wraps the upstream SSE parser for package-local use.
func parseSSE(r io.Reader, fn func([]byte) error) error {
	return upstream.ParseSSE(r, fn)
}
