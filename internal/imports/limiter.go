package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// Reading a foreign source costs memory and CPU out of proportion to the size of
// the request (a Scrivener zip of 40 MB is read into 100 MB of text, parsed and
// converted), and a preview and an apply each do it. So the reading is done a few
// at a time for the whole process: SCRIOBH_IMPORT_CONCURRENCY (default 2, at most
// 16) conversions at once; another waits a few seconds and then is told to try
// again (429). Writing to the database is not part of it: that is the single
// writer's own queue.

// conversions counts the sources read into an intake (a cache hit is not one).
var conversions atomic.Int64

const (
	defaultImportConcurrency = 2
	maxImportConcurrency     = 16
	importWait               = 3 * time.Second
)

var (
	slotMu   sync.Mutex
	slotCh   chan struct{}
	slotWait = importWait
)

// slots is the semaphore, made on first use from the environment.
func slots() chan struct{} {
	slotMu.Lock()
	defer slotMu.Unlock()
	if slotCh == nil {
		n := defaultImportConcurrency
		if v, err := strconv.Atoi(os.Getenv("SCRIOBH_IMPORT_CONCURRENCY")); err == nil && v >= 1 {
			n = min(v, maxImportConcurrency)
		}
		slotCh = make(chan struct{}, n)
	}
	return slotCh
}

// busy is the calm answer of a full house.
func busy() error {
	return &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy reading other imports. Try again in a moment.", RetryAfterSeconds: 5}
}

// holders is the people who hold a place now. One person holds at most one at a time (the places are for everyone, and
// two large previews started together by one writer, or one key, must not fill them and turn every other import away).
// A single import or compile that reads several sources does so one after another, taking and giving back a place for
// each, so it is never refused by this rule; a second request of the same person made while the first is reading is.
var (
	holdersMu sync.Mutex
	holders   = map[string]bool{}
)

// acquire takes a place for one conversion. It returns the function that gives it
// back. If there is no place within a few seconds, if the same person already holds
// one, or the caller goes away, it says so.
func acquire(ctx context.Context) (func(), error) {
	ch := slots()
	wait := func() time.Duration { slotMu.Lock(); defer slotMu.Unlock(); return slotWait }()
	if ctx == nil {
		ctx = context.Background()
	}
	who := ""
	if p, ok := ops.PrincipalFrom(ctx); ok {
		who = p.UserID
	}
	if who != "" {
		holdersMu.Lock()
		if holders[who] {
			holdersMu.Unlock()
			return nil, busy()
		}
		holders[who] = true
		holdersMu.Unlock()
	}
	forget := func() {
		if who != "" {
			holdersMu.Lock()
			delete(holders, who)
			holdersMu.Unlock()
		}
	}
	release := func() { <-ch; forget() }
	select {
	case ch <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return release, nil
	case <-t.C:
		forget()
		return nil, busy()
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// ---- a short memory of what a source was read into --------------------------------------------------

// A preview reads the source, and the apply that follows sends the same bytes and
// reads them again. What a converter makes is a function of the bytes, the layout
// and the options and of nothing else (no clock, no network, no state), so the
// intake made for one is kept for a short while under a hash of all of those, and
// handed to the next that asks for the same. It is only ever read from; an apply
// still checks its token against it, and a source that differs by a byte has
// another key.

const (
	cacheTTL     = 3 * time.Minute
	cacheEntries = 2
	cacheMaxSize = 64 << 20 // text held by one entry; a larger source is not kept
)

type cacheEntry struct {
	key string
	sum string
	in  *intake
	at  time.Time
}

var (
	cacheMu sync.Mutex
	cached  []cacheEntry
)

func cacheGet(key string) *intake {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	now := time.Now()
	live := cached[:0]
	var hit *intake
	for _, e := range cached {
		if now.Sub(e.at) > cacheTTL {
			continue
		}
		live = append(live, e)
		if e.key == key {
			hit = e.in
		}
	}
	cached = live
	return hit
}

func cachePut(key, sum string, in *intake) {
	size := in.total
	if in.extras != nil {
		for _, n := range in.extras.research {
			size += len(n.body)
		}
		for _, p := range in.extras.pictures {
			size += len(p.pic.Data)
		}
	}
	if in.epub != nil {
		for _, c := range in.epub.chapters {
			size += len(c.baseline)
		}
	}
	if size > cacheMaxSize {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached = append(cached, cacheEntry{key: key, sum: sum, in: in, at: time.Now()})
	if len(cached) > cacheEntries {
		cached = cached[len(cached)-cacheEntries:]
	}
}

func cacheDropSum(sum string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	live := cached[:0]
	for _, e := range cached {
		if e.sum != sum {
			live = append(live, e)
		}
	}
	cached = live
}

func cacheSweep(now time.Time) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	live := cached[:0]
	for _, e := range cached {
		if now.Sub(e.at) <= cacheTTL {
			live = append(live, e)
		}
	}
	cached = live
}

// cacheKey hashes everything a conversion is a function of.
func (s source) cacheKey(layout string) string {
	h := sha256.New()
	w := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	o := s.conv
	w(layout, s.name, o.scrivResearch, o.scrivNotes, o.wpStatus, o.wpTypes, o.ghostStatus, o.ghostTypes, o.pictures, o.scrivSheets, o.epubSections, o.epubPages)
	// How a PDF, a Pandoc document or a text file is cut is a function of the rule too (a Word file's has its own line below).
	w("cut", s.headings, o.docxSplit, o.docxStyle, strconv.Itoa(o.docxWords), o.docxPattern, o.docxMatch, o.docxTry, o.docxTryMatch)
	for _, b := range o.docxBreaks {
		w(strconv.Itoa(b))
	}
	// A staged source carries the hash it was staged with: the bytes are not hashed again for every preview of a review.
	bytesOf := func(kind string, b []byte) {
		if s.sum != "" {
			w(kind, s.sum)
			return
		}
		w(kind, strconv.Itoa(len(b)))
		h.Write(b)
	}
	if len(s.epub) > 0 {
		bytesOf("epub", s.epub)
	}
	if len(s.pdf) > 0 {
		bytesOf("pdf", s.pdf)
	}
	if len(s.text) > 0 {
		bytesOf("text", s.text)
	}
	if len(s.zip) > 0 {
		bytesOf("zip", s.zip)
	}
	if len(s.docx) > 0 && s.sum != "" { // a staged Word file: the cut is a function of its hash and the reading rules, not of the edits
		w("docx", s.sum, s.headings, s.changes, o.docxSplit, o.docxStyle, strconv.Itoa(o.docxWords), o.docxPattern, o.docxMatch, o.docxTry, o.docxTryMatch)
		for _, b := range o.docxBreaks {
			w(strconv.Itoa(b))
		}
	}
	if len(s.pandoc) > 0 {
		// What Pandoc makes of a file is a function of its version too.
		w("pandoc", s.pandocFmt, s.headings, s.pandocVersion())
		bytesOf("pandoc-bytes", s.pandoc)
	}
	for _, f := range s.files {
		b := f.bytesOf()
		w(f.Path, strconv.Itoa(len(b)), strconv.FormatInt(f.Size, 10))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}
