package docs_test

// U8: a version before a big cut and before a split (docs/plans/UX-FIXES.md). The five-minute rule
// still writes the small edits' versions; a big cut writes one labelled version holding the text
// before it; a burst of cuts is bounded.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
)

// cutWords is a paragraph of n words, each made of the salt and letters only, so every one counts.
func cutWords(n int, salt string) string {
	ws := make([]string, n)
	for i := range ws {
		ws[i] = fmt.Sprintf("%s%c%c", salt, 'a'+i%26, 'a'+(i/26)%26)
	}
	return strings.Join(ws, " ") + "\n"
}

// countReason counts a document's versions with one reason.
func (g *fx2) countReason(docID, reason string) int {
	g.t.Helper()
	n := 0
	for _, r := range g.revs(g.ownA, docID) {
		if r.Reason == reason {
			n++
		}
	}
	return n
}

// TestBigCutWritesOneVersionHoldingTheTextBeforeIt: a save that removes more than 200 words writes one
// version, reason cut, holding the text as it was before the save.
func TestBigCutWritesOneVersionHoldingTheTextBeforeIt(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Cut", Content: cutWords(300, "alpha")})
	before := g.head(g.ownA, d.ID).Content
	g.save(g.ownA, d.ID, cutWords(50, "beta"))

	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 1 || rs[0].Reason != "cut" {
		t.Fatalf("a big cut writes exactly one version, reason cut: %s", reasons(rs))
	}
	if got := g.content(d.ID, rs[0].ID); got != before {
		t.Errorf("the version must hold the text before the cut; got %d bytes, want %d", len(got), len(before))
	}
	if rs[0].WordCount != 300 {
		t.Errorf("the version's word count is the count of the text before the cut: %d", rs[0].WordCount)
	}
}

// TestCutRuleIsMoreThanTwentyPercentOrMoreThanTwoHundredWords: the threshold, both ways, and the
// boundaries (exactly 20 percent, exactly 200 words, are not big).
func TestCutRuleIsMoreThanTwentyPercentOrMoreThanTwoHundredWords(t *testing.T) {
	cases := []struct {
		name          string
		before, after int
		big           bool
	}{
		{"201 of 1000 words is more than 200", 1000, 799, true},
		{"200 of 1000 words is not more than 200 and 20 percent is not more than 20", 1000, 800, false},
		{"150 of 1000 is 15 percent and under 200", 1000, 850, false},
		{"10 of 40 is 25 percent", 40, 30, true},
		{"20 of 100 is exactly 20 percent", 100, 80, false},
		{"21 of 100 is more than 20 percent", 100, 79, true},
		{"growth is never a cut", 100, 900, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newFx2(t)
			d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Rule", Content: cutWords(c.before, "alpha")})
			g.save(g.ownA, d.ID, cutWords(c.after, "beta"))
			got := g.countReason(d.ID, "cut")
			if c.big && got != 1 || !c.big && got != 0 {
				t.Errorf("cut versions: %d, want %v", got, map[bool]int{true: 1, false: 0}[c.big])
			}
		})
	}
}

// TestSmallEditKeepsTheFiveMinuteRule: a small edit writes no cut version. The autosave rule is
// unchanged: one version at most per five minutes, the text before the save.
func TestSmallEditKeepsTheFiveMinuteRule(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Small", Content: cutWords(1000, "alpha")})
	g.save(g.ownA, d.ID, cutWords(990, "alpha")) // 10 words gone, at t0: the creation text is kept
	if rs := g.revs(g.ownA, d.ID); reasons(rs) != "autosave" {
		t.Fatalf("first small edit: %s", reasons(rs))
	}
	g.advance(time.Minute) // one minute later, inside the window
	g.save(g.ownA, d.ID, cutWords(980, "alpha"))
	if rs := g.revs(g.ownA, d.ID); reasons(rs) != "autosave" {
		t.Fatalf("a small edit inside five minutes writes nothing: %s", reasons(rs))
	}
	g.advance(5 * time.Minute) // six minutes after the last version
	g.save(g.ownA, d.ID, cutWords(970, "alpha"))
	rs := g.revs(g.ownA, d.ID)
	if reasons(rs) != "autosave,autosave" {
		t.Fatalf("after five minutes the text before the save is kept: %s", reasons(rs))
	}
	if got := g.content(d.ID, rs[0].ID); got != cutWords(980, "alpha") {
		t.Errorf("the newest version holds the text before the save")
	}
}

// TestBigCutInsideTheWindowWritesItsOwnVersion: a big cut five minutes after an autosave still writes
// its version; the autosave rule does not swallow it.
func TestBigCutInsideTheWindowWritesItsOwnVersion(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Window", Content: cutWords(1000, "alpha")})
	g.save(g.ownA, d.ID, cutWords(995, "alpha")) // autosave of the creation text
	g.advance(time.Minute)
	beforeCut := g.head(g.ownA, d.ID).Content
	g.save(g.ownA, d.ID, cutWords(600, "beta"))

	rs := g.revs(g.ownA, d.ID)
	if n := g.countReason(d.ID, "cut"); n != 1 {
		t.Fatalf("one cut version inside the window: %d (%s)", n, reasons(rs))
	}
	for _, r := range rs {
		if r.Reason == "cut" && g.content(d.ID, r.ID) != beforeCut {
			t.Errorf("the cut version holds the text before the cut")
		}
	}
}

// TestBurstOfBigCutsIsBounded: twenty-five big cuts in a row keep exactly the cap of unlabelled cut
// versions (MaxCutVersions, 10), the newest among them.
func TestBurstOfBigCutsIsBounded(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Burst", Content: cutWords(300, "a")})
	var lastBefore string
	for i := 0; i < 25; i++ {
		salt := string(rune('b' + i%20))
		g.save(g.ownA, d.ID, cutWords(300, salt)) // grow back to 300 (not a cut, a different text)
		g.advance(time.Minute)
		lastBefore = g.head(g.ownA, d.ID).Content
		g.save(g.ownA, d.ID, cutWords(20, "z")) // a big cut
		g.advance(time.Minute)
	}
	if n := g.countReason(d.ID, "cut"); n != 10 {
		t.Fatalf("cut versions after a burst: %d, want the cap of 10", n)
	}
	rs := g.revs(g.ownA, d.ID)
	for _, r := range rs {
		if r.Reason == "cut" {
			if got := g.content(d.ID, r.ID); got != lastBefore {
				t.Errorf("the newest cut version holds the text before the last cut")
			}
			break
		}
	}
}

// TestPruneBoundsUnlabelledCutVersions: pruning keeps the cap of unlabelled cut versions however they
// were written, and never drops a labelled one.
func TestPruneBoundsUnlabelledCutVersions(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Prune", Content: cutWords(300, "alpha")})
	err := g.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < 12; i++ {
			at := db.Format(g.now.AddDate(0, 0, -i-40))
			if _, err := revisions.Record(tx, revisions.RecordIn{DocumentID: d.ID, Revision: 1, Content: cutWords(300, fmt.Sprintf("old%02d", i)),
				Words: 300, Reason: "cut", At: at, Now: at}); err != nil {
				return err
			}
		}
		_, err := revisions.Record(tx, revisions.RecordIn{DocumentID: d.ID, Revision: 1, Content: cutWords(300, "named"), Words: 300,
			Reason: "cut", Label: "Keep this", At: db.Format(g.now.AddDate(0, 0, -90)), Now: db.Format(g.now)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revisions.Prune(context.Background(), g.d, revisions.DefaultPolicy(), g.now, revisions.Scope{}, false); err != nil {
		t.Fatal(err)
	}
	unlabelled, labelled := 0, 0
	for _, r := range g.revs(g.ownA, d.ID) {
		switch {
		case r.Reason == "cut" && r.Label == "":
			unlabelled++
		case r.Label == "Keep this":
			labelled++
		}
	}
	if unlabelled != 10 || labelled != 1 {
		t.Errorf("after prune: %d unlabelled cut versions (cap 10) and %d labelled", unlabelled, labelled)
	}
}
