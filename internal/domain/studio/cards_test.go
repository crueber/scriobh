package studio_test

import (
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/studio"
)

func cardWorld(t *testing.T) (*world, person, string, string) {
	t.Helper()
	w := newWorld(t)
	u := w.session(w.person("u", "UTC"), "ses_u")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	mkGoal(t, w, u, "day", "words", 10)
	w.save(u, d, lorem(20, "a"))
	card := w.call(u, "studio.card.create", studio.CardIn{ExpiresDays: 30}).(studio.CardItem).Item
	return w, u, card.Token, d
}

// A card's numbers are worked out at most once a minute, and what a request must always know (the token, revoked, expired)
// is read live: a warm cache never keeps a link alive.
func TestACardIsWorkedOutOnceAMinuteButTheTokenIsCheckedEveryTime(t *testing.T) {
	w, u, token, d := cardWorld(t)
	first, err := w.cards.Open(t.Context(), token, true)
	if err != nil || first.WritingDays30 != 1 || first.Streak != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	// Written to inside the minute: the card does not change, and nothing is worked out again.
	w.at("2026-03-02T09:00:30Z")
	w.save(u, d, w.text(u, d)+lorem(5, "b"))
	for i := 0; i < 10; i++ {
		if _, err := w.cards.Open(t.Context(), token, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.cards.Computed(); n != 1 {
		t.Errorf("worked out %d times inside a minute", n)
	}
	// A minute on, it is worked out again.
	w.at("2026-03-02T09:01:31Z")
	if _, err := w.cards.Open(t.Context(), token, false); err != nil || w.cards.Computed() != 2 {
		t.Errorf("after a minute: %d %v", w.cards.Computed(), err)
	}
	// Warm, and revoked: the very next request is the one 404.
	w.call(u, "studio.card.revoke", studio.CardRevokeIn{ID: token[:30]})
	if _, err := w.cards.Open(t.Context(), token, true); err != studio.ErrNoCard {
		t.Errorf("a revoked card opened from a warm cache: %v", err)
	}
	// Warm, and expired.
	w2, _, tok2, _ := cardWorld(t)
	if _, err := w2.cards.Open(t.Context(), tok2, false); err != nil {
		t.Fatal(err)
	}
	w2.now = w2.now.Add(31 * 24 * time.Hour)
	if _, err := w2.cards.Open(t.Context(), tok2, false); err != studio.ErrNoCard {
		t.Errorf("an expired card opened from a warm cache: %v", err)
	}
}

// Requests that arrive together share one computation, and a page never writes: views are counted in memory, written once
// when flushed, and the owner's list adds the ones not yet written.
func TestCardViewsAreCountedInMemoryAndWrittenOnce(t *testing.T) {
	w, u, token, _ := cardWorld(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.cards.Open(t.Context(), token, true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := w.cards.Computed(); n != 1 {
		t.Errorf("40 simultaneous requests worked out %d cards", n)
	}
	var stored int
	_ = w.d.Read().QueryRow(`SELECT views FROM studio_cards`).Scan(&stored)
	if stored != 0 {
		t.Errorf("a page view wrote to the database: %d", stored)
	}
	if got := w.call(u, "studio.card.list", studio.CardListIn{}).(studio.CardListOut).Items[0]; got.Views != 40 || got.LastViewedAt == "" {
		t.Errorf("the list: %+v", got)
	}
	if err := w.cards.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = w.d.Read().QueryRow(`SELECT views FROM studio_cards`).Scan(&stored)
	if n, _ := w.cards.Pending(token[:30]); stored != 40 || n != 0 {
		t.Errorf("after the flush: %d stored, %d pending", stored, n)
	}
	// A failed write puts the counts back.
	w.cards.Open(t.Context(), token, true)
	if _, err := w.d.Writer().Exec(`ALTER TABLE studio_cards RENAME TO studio_cards_x`); err != nil {
		t.Fatal(err)
	}
	if err := w.cards.Flush(t.Context()); err == nil {
		t.Error("the flush did not fail")
	}
	if n, _ := w.cards.Pending(token[:30]); n != 1 {
		t.Errorf("a failed flush lost the count: %d", n)
	}
}

// Cards revoked or expired more than 90 days ago are deleted, a bounded number a pass, with what was held for them.
func TestOldRevokedAndExpiredCardsAreSwept(t *testing.T) {
	w, u, token, _ := cardWorld(t)
	old := w.call(u, "studio.card.create", studio.CardIn{ExpiresDays: 1}).(studio.CardItem).Item
	young := w.call(u, "studio.card.create", studio.CardIn{ExpiresDays: 1}).(studio.CardItem).Item
	w.call(u, "studio.card.revoke", studio.CardRevokeIn{ID: old.ID})
	w.now = w.now.Add(60 * 24 * time.Hour)
	w.call(u, "studio.card.revoke", studio.CardRevokeIn{ID: young.ID})
	w.now = w.now.Add(40 * 24 * time.Hour) // 100 days after the first revoke, 40 after the second; the first card's own expiry is long past too
	if n, err := w.cards.Sweep(t.Context()); err != nil || n != 2 {
		t.Fatalf("swept %d, %v (the long-revoked, and the first card of the world, expired 70 days ago... )", n, err)
	}
	var left int
	_ = w.d.Read().QueryRow(`SELECT count(*) FROM studio_cards`).Scan(&left)
	if left != 1 {
		t.Errorf("%d cards left", left)
	}
	_ = token
	if n, err := w.cards.Sweep(t.Context()); err != nil || n != 0 {
		t.Errorf("a second pass swept %d, %v", n, err)
	}
}
