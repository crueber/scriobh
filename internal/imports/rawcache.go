package imports

import (
	"strings"
	"sync"
	"time"
)

// What a converter read from a staged file, kept for a few minutes: the output of the PDF child process, and what Pandoc made of a file.
// A change of how the book is cut then cuts what was read again, and neither the child nor Pandoc runs a second time (within the five
// minutes: a later change reads the file again). The cache is small (two entries) and short (five minutes, by a timer, not only at the next
// use), it is keyed by the staged file's hash, and it is emptied of a book the moment its draft ends (applied, discarded or expired), so a
// book's text does not wait in memory for anything.

const (
	rawEntries = 2
	rawTTL     = 5 * time.Minute
)

type rawEntry struct {
	v    any
	used time.Time
}

var (
	rawMu sync.Mutex
	raw   = map[string]*rawEntry{}
)

func rawGet(key string) (any, bool) {
	rawMu.Lock()
	defer rawMu.Unlock()
	now := time.Now()
	for k, e := range raw {
		if now.Sub(e.used) > rawTTL {
			delete(raw, k)
		}
	}
	e := raw[key]
	if e == nil {
		return nil, false
	}
	e.used = now
	return e.v, true
}

func rawPut(key string, v any) {
	rawMu.Lock()
	defer rawMu.Unlock()
	if len(raw) >= rawEntries {
		oldest := ""
		for k, e := range raw {
			if oldest == "" || e.used.Before(raw[oldest].used) {
				oldest = k
			}
		}
		delete(raw, oldest)
	}
	raw[key] = &rawEntry{v: v, used: time.Now()}
	time.AfterFunc(rawTTL+time.Second, func() { rawSweep(time.Now()) }) // a book is let go on time whether or not anything asks again
}

// rawSweep lets go of what has not been used for the whole of the time it is kept.
func rawSweep(now time.Time) {
	rawMu.Lock()
	for k, e := range raw {
		if now.Sub(e.used) > rawTTL {
			delete(raw, k)
		}
	}
	rawMu.Unlock()
	lookMu.Lock()
	for k, b := range lookCache {
		if now.Sub(b.used) > lookBookTTL {
			delete(lookCache, k)
		}
	}
	lookMu.Unlock()
}

// dropCaches lets go of everything held for a staged file, by its hash: what the PDF child or Pandoc read of it, and the blocks of a Word
// file held for looking inside its chapters. It is called when the draft ends.
func dropCaches(sum string) {
	if sum == "" {
		return
	}
	forgetWordCounts()
	rawMu.Lock()
	for k := range raw {
		if strings.Contains(k, "|"+sum) {
			delete(raw, k)
		}
	}
	rawMu.Unlock()
	lookMu.Lock()
	for k := range lookCache {
		if strings.HasPrefix(k, sum+"|") {
			delete(lookCache, k)
		}
	}
	lookMu.Unlock()
}
