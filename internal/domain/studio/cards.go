package studio

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The public side of the private progress card.
//
// A card is opened by anyone who holds the link, with no session, so what a request costs is bounded here and not left to
// the person's own limits:
//
//   - A request never writes. Views are counted in memory and written at most once a minute (and when the server stops), in
//     one transaction for all cards; the owner's list adds what is not yet written.
//   - What a card shows is worked out at most once a minute for each card, and at most four cards at a time: a page, its
//     picture and its data share one computation, and many addresses cannot make the server read all-time statistics in
//     parallel. The token itself is checked on every request with one indexed read (revoked, expired, the owner gone), so a
//     revoked or expired link stops at once however warm the cache is, and the cache is dropped at once when the
//     passphrase of any project is set, changed or cleared, or the person's projects change (a trash, a restore, a
//     membership), by a fingerprint read on every request.
//   - Cards that were revoked or expired long ago are deleted (Sweep).

// CardsService is the name the cards' service is registered under in ops.Env.
const CardsService = "studio.cards"

const (
	cardCacheFor    = time.Minute
	cardFlushEvery  = time.Minute
	cardMaxCompute  = 4
	cardMaxCached   = 512
	cardKeepDays    = 90
	cardSweepPerRun = 500
)

type cachedCard struct {
	view CardView
	fp   string
	show bool
	at   time.Time
}

// Cards serves the public cards.
type Cards struct {
	d   *db.DB
	sem chan struct{}

	computed atomic.Int64 // how many cards were worked out (not served from the cache)

	mu      sync.Mutex
	cache   map[string]cachedCard
	flights map[string]chan struct{}
	views   map[string]int
	viewAt  map[string]string
	lastOut time.Time
}

// NewCards makes the service. Without one registered (a schema-only build) the owner's list shows the written counts.
func NewCards(d *db.DB) *Cards {
	return &Cards{d: d, sem: make(chan struct{}, cardMaxCompute), cache: map[string]cachedCard{}, flights: map[string]chan struct{}{}, views: map[string]int{}, viewAt: map[string]string{}, lastOut: db.NowTime()}
}

func cardsOf(env *ops.Env) *Cards {
	c, _ := ops.Service[*Cards](env, CardsService)
	return c
}

// Computed is how many times a card has been worked out rather than served from the cache.
func (c *Cards) Computed() int64 { return c.computed.Load() }

// Pending is the views counted and not yet written, and the time of the last.
func (c *Cards) Pending(id string) (int, string) {
	if c == nil {
		return 0, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.views[id], c.viewAt[id]
}

// Forget drops what is held in memory for cards that are gone or revoked.
func (c *Cards) Forget(ids ...string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for _, id := range ids {
		delete(c.cache, id)
	}
	c.mu.Unlock()
}

// fingerprint is what a computed card depends on besides its own numbers: the passphrases that exist (their number and the
// newest change) and the person's projects (those that count and all of them). One short read, made on every request.
func (c *Cards) fingerprint(ctx context.Context, user string) (string, error) {
	var fp string
	err := c.d.Read().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM project_gates) || ':' || (SELECT COALESCE(max(updated_at), '') FROM project_gates) || ':' ||
		(SELECT count(*) FROM project_members m JOIN projects p ON p.id = m.project_id WHERE m.user_id = ?1 AND p.deleted_at IS NULL AND p.trashed_at IS NULL) || ':' ||
		(SELECT count(*) FROM project_members WHERE user_id = ?1)`, user).Scan(&fp)
	return fp, err
}

func (c *Cards) cached(id, fp string, show bool, now time.Time) (CardView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[id]
	if !ok || e.fp != fp || e.show != show || now.Sub(e.at) >= cardCacheFor || now.Before(e.at) {
		return CardView{}, false
	}
	return e.view, true
}

func (c *Cards) store(id, fp string, show bool, v CardView, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= cardMaxCached {
		for k, e := range c.cache {
			if now.Sub(e.at) >= cardCacheFor || len(c.cache) >= cardMaxCached {
				delete(c.cache, k)
			}
		}
	}
	c.cache[id] = cachedCard{view: v, fp: fp, show: show, at: now}
}

// Open finds the card a token opens and says what it shows. Every failure is ErrNoCard. A successful open counts as a view
// when view is true (the page; the picture and the data are not views).
func (c *Cards) Open(ctx context.Context, token string, view bool) (CardView, error) {
	m := cardTokenRe.FindStringSubmatch(token)
	if m == nil {
		return CardView{}, ErrNoCard
	}
	id := m[1]
	var user, stored, expires, revoked string
	var show int
	err := c.d.Read().QueryRowContext(ctx, `SELECT c.user_id, c.token_hash, c.expires_at, c.revoked_at, c.show_projects FROM studio_cards c
		JOIN users u ON u.id = c.user_id AND u.deleted_at IS NULL AND u.disabled_at IS NULL WHERE c.id = ?`, id).Scan(&user, &stored, &expires, &revoked, &show)
	if err != nil {
		stored = cardDummy
	}
	want, _ := hex.DecodeString(stored)
	got, _ := hex.DecodeString(cardHash(m[2]))
	now := db.NowTime()
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 || revoked != "" || expires <= db.Format(now) {
		return CardView{}, ErrNoCard
	}
	fp, err := c.fingerprint(ctx, user)
	if err != nil {
		return CardView{}, err
	}
	out, ok := c.cached(id, fp, show == 1, now)
	for tries := 0; !ok; tries++ {
		c.mu.Lock()
		f, busy := c.flights[id]
		if !busy || tries >= 3 {
			f = make(chan struct{})
			c.flights[id] = f
		}
		c.mu.Unlock()
		if busy && tries < 3 {
			// Someone is already working this card out: wait for that and use it.
			select {
			case <-f:
			case <-ctx.Done():
				return CardView{}, ctx.Err()
			}
			out, ok = c.cached(id, fp, show == 1, now)
			continue
		}
		out, err = c.compute(ctx, id, user, fp, expires, show == 1, now)
		c.mu.Lock()
		if c.flights[id] == f {
			delete(c.flights, id)
		}
		c.mu.Unlock()
		close(f)
		if err != nil {
			return CardView{}, err
		}
		break
	}
	out.ExpiresAt = expires
	if view {
		c.mu.Lock()
		c.views[id]++
		c.viewAt[id] = db.Format(now)
		c.mu.Unlock()
		c.flushSoon(now)
	}
	return out, nil
}

// compute works a card out, at most cardMaxCompute at a time.
func (c *Cards) compute(ctx context.Context, id, user, fp, expires string, show bool, now time.Time) (CardView, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return CardView{}, ctx.Err()
	}
	c.computed.Add(1)
	v, err := buildCardView(ctx, c.d, user, expires, show)
	if err != nil {
		return CardView{}, err
	}
	c.store(id, fp, show, v, now)
	return v, nil
}

// flushSoon starts a write in the background when the last one is a minute old.
func (c *Cards) flushSoon(now time.Time) {
	c.mu.Lock()
	due := now.Sub(c.lastOut) >= cardFlushEvery && len(c.views) > 0
	if due {
		c.lastOut = now // one writer at a time
	}
	c.mu.Unlock()
	if due {
		go func() { _ = c.Flush(context.Background()) }()
	}
}

// Flush writes the counted views, all cards in one transaction. A failed write puts the counts back for the next one.
func (c *Cards) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	views, at := c.views, c.viewAt
	c.views, c.viewAt = map[string]int{}, map[string]string{}
	c.lastOut = db.NowTime()
	c.mu.Unlock()
	if len(views) == 0 {
		return nil
	}
	err := c.d.WithTx(ctx, func(tx *sql.Tx) error {
		for id, n := range views {
			if _, err := tx.ExecContext(ctx, `UPDATE studio_cards SET views = views + ?, last_viewed_at = ? WHERE id = ?`, n, at[id], id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.mu.Lock()
		for id, n := range views {
			c.views[id] += n
			if c.viewAt[id] < at[id] {
				c.viewAt[id] = at[id]
			}
		}
		c.mu.Unlock()
	}
	return err
}

// Sweep deletes cards that were revoked, or expired, more than 90 days ago, at most 500 a pass, and forgets what was held
// for them. It returns how many it deleted.
func (c *Cards) Sweep(ctx context.Context) (int, error) {
	cutoff := db.Format(db.NowTime().AddDate(0, 0, -cardKeepDays))
	var ids []string
	err := c.d.WithTx(ctx, func(tx *sql.Tx) error {
		ids = ids[:0]
		rows, err := tx.QueryContext(ctx, `SELECT id FROM studio_cards WHERE (revoked_at <> '' AND revoked_at < ?1) OR expires_at < ?1 LIMIT ?2`, cutoff, cardSweepPerRun)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM studio_cards WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	c.Forget(ids...)
	if len(ids) > 0 {
		c.mu.Lock()
		for _, id := range ids {
			delete(c.views, id)
			delete(c.viewAt, id)
		}
		c.mu.Unlock()
	}
	return len(ids), nil
}

// cleanText makes a string plain text that is safe in an XML 1.0 document and short: bytes that are not UTF-8, the control
// characters XML forbids (and the C1 controls), U+FFFE and U+FFFF, bidirectional and invisible formatting characters are
// dropped, line breaks and tabs become spaces, and at most max characters are kept (with an ellipsis when cut). It cuts
// before anything is escaped, so no entity is split.
func cleanText(s string, max int) string {
	var b strings.Builder
	n, cut := 0, false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size <= 1 {
			continue
		}
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			r = ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0xFFFE || r == 0xFFFF:
			continue
		case r == 0x061C || r == 0x180E || (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2060 && r <= 0x2064) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF:
			continue
		}
		if n == max {
			cut = true
			break
		}
		b.WriteRune(r)
		n++
	}
	out := strings.TrimSpace(b.String())
	if cut {
		out += "…"
	}
	return out
}
