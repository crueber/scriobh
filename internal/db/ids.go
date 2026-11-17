package db

import (
	"crypto/rand"
	"regexp"
	"sync"
	"time"
)

// IDs are a short prefix plus a 26-character ULID: sortable by creation
// time, URL-safe, generated in Go, never by SQLite.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	idMu       sync.Mutex
	idLastMS   int64
	idLastRand [10]byte
)

// clock is replaceable in tests.
var clock = time.Now

// SetClock replaces the time source and returns a restore function.
func SetClock(f func() time.Time) (restore func()) {
	old := clock
	clock = f
	return func() { clock = old }
}

// NewID returns prefix + "_" + a monotonic ULID, e.g. "prj_01J8Z...".
func NewID(prefix string) string {
	idMu.Lock()
	defer idMu.Unlock()
	ms := clock().UnixMilli()
	if ms <= idLastMS {
		// Same millisecond (or the clock stepped back): increment the random part
		// so IDs from one process are strictly increasing.
		ms = idLastMS
		for i := len(idLastRand) - 1; i >= 0; i-- {
			idLastRand[i]++
			if idLastRand[i] != 0 {
				break
			}
		}
	} else {
		idLastMS = ms
		if _, err := rand.Read(idLastRand[:]); err != nil {
			panic("db: entropy source failed: " + err.Error())
		}
		idLastRand[0] &= 0x7f // leave headroom so increments cannot overflow the 80 bits
	}
	var out [26]byte
	t := uint64(ms)
	for i := 9; i >= 0; i-- {
		out[i] = crockford[t&31]
		t >>= 5
	}
	// 80 random bits -> 16 base32 characters.
	var acc uint64
	var bits uint
	j := 10
	for _, b := range idLastRand {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = crockford[(acc>>bits)&31]
			j++
		}
	}
	return prefix + "_" + string(out[:])
}

// timeLayout is RFC 3339 in UTC with millisecond precision. It sorts
// lexicographically, which is what the indexes rely on.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Now returns the current time as a stored string.
func Now() string { return clock().UTC().Format(timeLayout) }

// Format renders a time the way it is stored.
func Format(t time.Time) string { return t.UTC().Format(timeLayout) }

// Parse reads a stored time.
func Parse(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// NowTime returns the current time from the injectable clock.
func NowTime() time.Time { return clock().UTC() }

var idShape = regexp.MustCompile(`^[a-z]{2,8}_[0-9A-HJKMNP-TV-Z]{26}$`)

// ValidID reports whether id has the shape NewID makes: a short lower-case
// prefix, an underscore and 26 characters of Crockford base32. Code that turns
// an id into a path (the purge hooks) checks it first: a Base check lets "." and
// ".." through, and ".." is the data directory.
func ValidID(id string) bool { return idShape.MatchString(id) }
