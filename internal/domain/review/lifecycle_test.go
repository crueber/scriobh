package review_test

import (
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/testutil"
)

var reviewTables = []string{"comments", "comment_mentions", "suggestions", "share_links", "share_link_docs", "notifications"}

func (x *h) populate() {
	x.t.Helper()
	d := x.doc("One", sugText)
	th := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: d, Quote: "quiet", Body: "hello @Edith"}).(review.ThreadItem).Item
	x.call(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: d, ParentID: th.ID, Body: "reply"})
	x.suggest(x.reviewer, d, x.get(d).Revision, rep("windows", "panes"))
	x.call(x.owner, "share.create", share.CreateIn{ProjectID: x.project, Scope: "comment", DocumentIDs: []string{d}})
}

func TestProjectPurgeLeavesNoReviewData(t *testing.T) {
	x := newH(t)
	x.populate()
	for _, tb := range reviewTables {
		if x.count("SELECT count(*) FROM "+tb) == 0 {
			t.Fatalf("the fixture left %s empty", tb)
		}
	}
	x.call(x.owner, "project.trash", projects.StateIn{ID: x.project})
	x.call(x.owner, "project.purge", projects.PurgeIn{ID: x.project, ConfirmTitle: "Review Book"})
	for _, tb := range reviewTables {
		if n := x.count("SELECT count(*) FROM " + tb); n != 0 {
			t.Errorf("%s keeps %d rows after the project was purged", tb, n)
		}
	}
}

func TestACommentOfARemovedPersonKeepsItsText(t *testing.T) {
	x := newH(t)
	d := x.doc("One", sugText)
	c := x.comment(x.reviewer, d, "quiet", "I was here.")
	if _, err := x.d.Writer().Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), x.reviewer.UserID); err != nil {
		t.Fatal(err)
	}
	got := x.thread(x.owner, c.ID)
	if got.Body != "I was here." || got.Author.Kind != "removed" || got.Author.Name != "Removed user" || got.Author.UserID != "" {
		t.Errorf("after the account was removed: %+v", got.Author)
	}
	// and the account is gone for good (the row itself): the comment stays, author unknown
	if _, err := x.d.Writer().Exec(`DELETE FROM users WHERE id = ?`, x.reviewer.UserID); err != nil {
		t.Fatal(err)
	}
	if got := x.thread(x.owner, c.ID); got.Body != "I was here." || got.Author.Kind != "removed" {
		t.Errorf("after the row was deleted: %+v", got)
	}
}

func TestBackupsHoldReviewData(t *testing.T) {
	x := newH(t)
	x.populate()
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := x.d.Backup(x.ctx, dest); err != nil {
		t.Fatal(err)
	}
	b, err := db.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, tb := range reviewTables {
		var n int
		if err := b.Read().QueryRow("SELECT count(*) FROM " + tb).Scan(&n); err != nil || n == 0 {
			t.Errorf("the backup holds %d rows of %s (%v)", n, tb, err)
		}
	}
}

// After every kind of write to a document, every comment that says it is anchored is anchored to exactly
// the text it quotes; the rest are orphaned, which is a promise, not a guess. Driven at random, seeded.
func TestAnchorInvariantAfterEveryWritePath(t *testing.T) {
	x := newH(t)
	rng := rand.New(rand.NewSource(7))
	text := ""
	for i := 0; i < 12; i++ {
		text += fmt.Sprintf("Paragraph %d starts here. The quick brown fox number %d jumps over the lazy dog %d times. Zebra%d ends it.\n\n", i, i, i*3, i)
	}
	doc := x.doc("Fuzz", text)
	// comments over assorted places, some with the same words
	for i := 0; i < 12; i++ {
		for _, q := range []string{fmt.Sprintf("brown fox number %d", i), "lazy dog", fmt.Sprintf("Zebra%d", i)} {
			_, err := x.reg.Call(x.ctx, x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: q, Before: fmt.Sprintf("Paragraph %d", i), Body: "c"})
			_ = err // a repeated phrase that cannot be told apart is refused, which is fine
		}
	}
	check := func(step string) {
		t.Helper()
		d := x.get(doc)
		body := comanchor.Body(d.Content)
		ix := alts.NewIndex(body)
		rows, err := x.d.Read().Query(`SELECT anchor_state, anchor_start, anchor_end, anchor_quote FROM comments WHERE document_id = ? AND parent_id IS NULL`, doc)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		active := 0
		for rows.Next() {
			var st, q string
			var s, e int
			if err := rows.Scan(&st, &s, &e, &q); err != nil {
				t.Fatal(err)
			}
			if st != "active" {
				continue
			}
			active++
			a, ok1 := ix.Byte(s)
			b, ok2 := ix.Byte(e)
			if !ok1 || !ok2 || a > b || b > len(body) || body[a:b] != q {
				t.Fatalf("%s: an active comment says %q and the text there is %q", step, q, func() string {
					if ok1 && ok2 && a <= b && b <= len(body) {
						return body[a:b]
					}
					return "(out of range)"
				}())
			}
		}
	}
	check("start")
	var versions []string
	for i := 0; i < 160; i++ {
		d := x.get(doc)
		body := comanchor.Body(d.Content)
		step := ""
		switch rng.Intn(9) {
		case 0, 1: // type a word somewhere
			at := rng.Intn(len(body))
			for at < len(body) && body[at] != ' ' {
				at++
			}
			step = "insert"
			x.set(doc, body[:at]+" extra"+body[at:]+strings.TrimPrefix(d.Content, body))
		case 2: // delete a stretch
			a := rng.Intn(len(body) - 10)
			b := a + 1 + rng.Intn(9)
			for a > 0 && body[a]&0xC0 == 0x80 {
				a--
			}
			for b < len(body) && body[b]&0xC0 == 0x80 {
				b++
			}
			step = "delete"
			x.set(doc, body[:a]+body[b:]+strings.TrimPrefix(d.Content, body))
		case 3: // rewrite a word in place
			step = "rewrite"
			x.set(doc, strings.Replace(d.Content, "lazy", "sleepy", 1))
		case 4: // put it back
			step = "unrewrite"
			x.set(doc, strings.Replace(d.Content, "sleepy", "lazy", 1))
		case 5: // save a version, restore an older one
			if len(versions) > 0 && rng.Intn(2) == 0 {
				step = "restore"
				_, _ = x.reg.Call(x.ctx, x.owner, "revision.restore", docs.RestoreIn{ID: doc, RevisionID: versions[rng.Intn(len(versions))], BaseRevision: d.Revision})
			} else {
				step = "snapshot"
				out := x.call(x.owner, "revision.snapshot", revisions.SnapshotIn{ID: doc, Label: fmt.Sprintf("v%d", i)})
				versions = append(versions, out.(revisions.Item).Item.ID)
			}
		case 6: // an alternative, and a choice
			step = "alternative"
			_, _ = x.reg.Call(x.ctx, x.owner, "alternative.create", docs.AltCreateIn{ID: doc, BaseRevision: d.Revision, Scope: "word", Quote: "jumps", Variants: []string{"leaps"}})
		case 7:
			step = "choose"
			if l, err := x.reg.Call(x.ctx, x.owner, "alternative.list", docs.AltListIn{ID: doc}); err == nil && len(l.(docs.AltListOut).Items) > 0 {
				_, _ = x.reg.Call(x.ctx, x.owner, "alternative.choose", docs.AltChooseIn{ID: doc, BaseRevision: d.Revision, AlternativeID: l.(docs.AltListOut).Items[0].ID, Step: "next"})
			}
		case 8: // an accepted suggestion
			step = "suggestion"
			if s, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: d.Revision,
				Changes: []review.ChangeIn{{Quote: "starts here", Text: "begins here"}}}); err == nil {
				_, _ = x.reg.Call(x.ctx, x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: s.(review.SuggestionItem).Item.ID})
			}
		}
		check(fmt.Sprintf("step %d (%s)", i, step))
	}
	// a split moves the tail: whatever is left in the head is still anchored to what it says
	d := x.get(doc)
	cut := len(comanchor.Body(d.Content)) / 2
	for cut < len(d.Content) && d.Content[cut] != '\n' {
		cut++
	}
	_, _ = x.reg.Call(x.ctx, x.owner, "doc.split", docs.SplitIn{ID: doc, BaseRevision: d.Revision, Offset: alts.Len16(d.Content[:cut]), Title: "Tail"})
	check("after the split")
	// the walk was not empty: anchors were moved, kept and lost along the way
	active := x.count(`SELECT count(*) FROM comments WHERE anchor_state = 'active'`)
	orphan := x.count(`SELECT count(*) FROM comments WHERE anchor_state = 'orphaned'`)
	t.Logf("after 160 writes: %d anchored, %d orphaned", active, orphan)
	if active == 0 || orphan == 0 {
		t.Errorf("the walk should leave some comments anchored and some lost: %d and %d", active, orphan)
	}
}

// 40 anchored comments in a chapter and 2,000 in a project list quickly, and keeping them anchored costs a save little.
func TestReviewPerformance(t *testing.T) {
	x := newH(t)
	para := func(i int) string {
		return fmt.Sprintf("Paragraph %d opens with a sentence of some length. The marker%d phrase sits in the middle of it, and the end of the paragraph says goodbye. %s\n\n", i, i, words(60))
	}
	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString(para(i))
	}
	chapter := x.doc("Long chapter", b.String())
	for i := 0; i < 40; i++ {
		x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: chapter, Quote: fmt.Sprintf("marker%d phrase", i*2), Body: "A remark about this place."})
	}
	// the rest of the 2,000, written straight to the table: the list is what is being timed
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for i := 0; i < 1960; i++ {
			// 960 anchored (with the 40 made above, the 1,000 open anchored threads a document may hold),
			// the rest on the document as a whole
			state, start, end, quote := "none", 0, 0, ""
			if i < 960 {
				state, start, end, quote = "active", 0, 5, "Parag"
			}
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, kind, author_id, body, status, anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_revision, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, db.NewID("cmt"), x.project, chapter, "comment", x.reviewer.UserID, "bulk comment number "+fmt.Sprint(i), "open", state, start, end, quote, quote, 1, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	measure := func(name string, budget time.Duration, f func()) time.Duration {
		best := time.Hour
		for i := 0; i < 5; i++ {
			s := time.Now()
			f()
			// timing: not a budget: keeps the fastest run; the limit is asserted in the subtest
			if d := time.Since(s); d < best {
				best = d
			}
		}
		t.Logf("%s: %v (budget %v)", name, best, budget*timingFactor)
		t.Run(name, func(t *testing.T) {
			testutil.Timing(t)
			if best > budget*timingFactor {
				t.Errorf("%s took %v, over %v", name, best, budget*timingFactor)
			}
		})
		return best
	}
	measure("list 200 threads of a 2,000-comment project", 80*time.Millisecond, func() {
		x.call(x.owner, "comment.list", review.ListIn{ProjectID: x.project, Limit: 200})
	})
	measure("list the 40 threads of one chapter", 60*time.Millisecond, func() {
		x.call(x.owner, "comment.list", review.ListIn{ProjectID: x.project, DocumentID: chapter, State: "active", Limit: 200, Plain: true})
	})
	measure("the queue over 2,000 threads", 150*time.Millisecond, func() {
		x.call(x.owner, "review.queue", review.QueueIn{ProjectID: x.project, Limit: 50})
	})
	// a save with 40 comments to keep anchored, typing at the top (every anchor moves), against the same save with none
	save := func(label string) time.Duration {
		var best = time.Hour
		for i := 0; i < 6; i++ {
			d := x.get(chapter)
			next := "x" + d.Content
			s := time.Now()
			x.call(x.owner, "doc.update", docs.UpdateIn{ID: chapter, BaseRevision: d.Revision, Content: &next})
			// timing: not a budget: keeps the fastest run; the limit is asserted in the "anchoring cost" subtest
			if e := time.Since(s); e < best {
				best = e
			}
		}
		t.Logf("%s: %v", label, best)
		return best
	}
	with := save("a save of the 80-paragraph chapter with 2,000 comments on it")
	bare := x.doc("Bare chapter", b.String())
	var without = time.Hour
	for i := 0; i < 6; i++ {
		d := x.get(bare)
		next := "x" + d.Content
		s := time.Now()
		x.call(x.owner, "doc.update", docs.UpdateIn{ID: bare, BaseRevision: d.Revision, Content: &next})
		// timing: not a budget: keeps the fastest run; the limit is asserted in the "anchoring cost" subtest
		if e := time.Since(s); e < without {
			without = e
		}
	}
	t.Logf("the same save with no comments: %v", without)
	t.Run("anchoring cost of a save", func(t *testing.T) {
		testutil.Timing(t)
		if with > without+40*time.Millisecond*timingFactor {
			t.Errorf("anchoring cost a save %v (%v against %v)", with-without, with, without)
		}
	})
}
