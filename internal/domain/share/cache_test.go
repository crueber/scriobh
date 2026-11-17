package share

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestUnlockDelayEscalates(t *testing.T) {
	want := []time.Duration{0, 0, 0, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, w := range want {
		if got := unlockDelay(i + 1); got != w {
			t.Errorf("after %d wrong tries: %v, want %v", i+1, got, w)
		}
	}
	if unlockDelay(40) != time.Hour {
		t.Errorf("the wait is capped: %v", unlockDelay(40))
	}
}

// The owner making or changing a link does not wait behind strangers trying passphrases, nor they behind it.
func TestTheOwnersHashingHasItsOwnSlots(t *testing.T) {
	kdfSlots <- struct{}{}
	kdfSlots <- struct{}{}
	defer func() { <-kdfSlots; <-kdfSlots }()
	done := make(chan error, 1)
	go func() { _, err := HashPassphrase("an owner's passphrase"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the owner's hashing waited for the strangers' slots")
	}
	// and the strangers are refused when theirs are full, not queued
	if _, ok := acquireKDF(); ok {
		t.Error("the strangers' slots were not full")
	}
}

func downloadFixture(t *testing.T) (*db.DB, Link) {
	t.Helper()
	d := testutil.DB(t)
	now := db.Now()
	for _, q := range []string{
		`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer','` + now + `','` + now + `')`,
		`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','Book','b','` + now + `','` + now + `')`,
		`INSERT INTO documents(id, project_id, kind, title, content, content_hash, created_at, updated_at) VALUES ('doc_1','prj_1','chapter','One','First text.','h1','` + now + `','` + now + `')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d, Link{ID: "shl_1", ProjectID: "prj_1", ProjectTitle: "Book", Scope: ScopeRead, Ghosts: "omit", Whole: true}
}

// A download is built once for the same link and the same text, rebuilt when the text changes or a minute has gone,
// and at most two are built at a time: a third caller is told to wait, not made to parse the book.
func TestDownloadsAreCachedAndBounded(t *testing.T) {
	d, l := downloadFixture(t)
	dl := NewDownloads()
	ctx := context.Background()
	_, body, err := dl.Get(ctx, d, l)
	if err != nil || !strings.Contains(body, "First text.") {
		t.Fatalf("%v %q", err, body)
	}
	if _, _, err := dl.Get(ctx, d, l); err != nil || dl.Builds.Load() != 1 {
		t.Fatalf("the second call built again: %d builds (%v)", dl.Builds.Load(), err)
	}
	// the text changes: a new key
	if _, err := d.Writer().Exec(`UPDATE documents SET content = 'Second text.', content_hash = 'h2' WHERE id = 'doc_1'`); err != nil {
		t.Fatal(err)
	}
	if _, body, _ = dl.Get(ctx, d, l); !strings.Contains(body, "Second text.") || dl.Builds.Load() != 2 {
		t.Fatalf("after a change: %d builds, %q", dl.Builds.Load(), body)
	}
	// a minute later it is built again
	restore := db.SetClock(func() time.Time { return time.Now().Add(2 * time.Minute) })
	_, _, _ = dl.Get(ctx, d, l)
	restore()
	if dl.Builds.Load() != 3 {
		t.Errorf("after the minute: %d builds", dl.Builds.Load())
	}
	// two builds in flight: a third, uncached, is refused as busy; a cached one is still served
	dl.sem <- struct{}{}
	dl.sem <- struct{}{}
	if _, err := d.Writer().Exec(`UPDATE documents SET content = 'Third text.', content_hash = 'h3' WHERE id = 'doc_1'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dl.Get(ctx, d, l); !errors.Is(err, ErrBusy) {
		t.Errorf("a third build: %v", err)
	}
	<-dl.sem
	<-dl.sem
	if _, body, err := dl.Get(ctx, d, l); err != nil || !strings.Contains(body, "Third text.") {
		t.Errorf("after the slots were free: %v", err)
	}
	// a link of another project's documents gets another key: nothing is shared across links
	other := l
	other.ID = "shl_2"
	before := dl.Builds.Load()
	_, _, _ = dl.Get(ctx, d, other)
	if dl.Builds.Load() != before+1 {
		t.Error("a link was served another's cached download")
	}
}

// A link that names documents and has lost them all shows nothing: never the whole manuscript.
func TestALinkWhoseRootsAreGoneServesNothing(t *testing.T) {
	d, _ := downloadFixture(t)
	id, tok, hash := NewToken()
	now := db.Now()
	for _, q := range []string{
		`INSERT INTO share_links(id, project_id, label, scope, whole, token_hash, created_at, updated_at) VALUES ('` + id + `','prj_1','x','read',0,'` + hash + `','` + now + `','` + now + `')`,
		`INSERT INTO share_link_docs(share_id, document_id) VALUES ('` + id + `','doc_1')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Authenticate(context.Background(), d, nil, tok); err != nil {
		t.Fatalf("a link with a live root: %v", err)
	}
	if _, err := d.Writer().Exec(`DELETE FROM documents WHERE id = 'doc_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO documents(id, project_id, kind, title, content, content_hash, created_at, updated_at) VALUES ('doc_2','prj_1','chapter','Other','Other text.','h','` + now + `','` + now + `')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(context.Background(), d, nil, tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("a link whose roots were purged: %v (it must not widen to the other chapter)", err)
	}
}
