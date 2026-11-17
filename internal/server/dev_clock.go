//go:build !prod

package server

import (
	"os"
	"strconv"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// A test hook of the development build only (production builds, -tags prod, do not contain it).
// SCRIOBH_DEV_CLOCK_OFFSET_SECONDS shifts the clock the database layer stamps rows with, so an
// end-to-end test can write "yesterday's" versions through the ordinary ops, restart the server on
// the same data directory without the offset, and then restore them.
func init() {
	v := os.Getenv("SCRIOBH_DEV_CLOCK_OFFSET_SECONDS")
	if v == "" {
		return
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return
	}
	d := time.Duration(n) * time.Second
	db.SetClock(func() time.Time { return time.Now().Add(d) })
}
