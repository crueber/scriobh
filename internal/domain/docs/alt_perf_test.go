package docs_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/testutil"
)

func p95(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)*95/100]
}

// A 20,000-word chapter with 500 alternatives: saving (the worst case is typing
// at the top, which moves every anchor), cycling one and listing them all must
// stay inside the document budgets (ARCHITECTURE: a save under 120 ms). The
// numbers are logged. The limits are scaled when the race detector is on.
func TestAlternativePerformance(t *testing.T) {
	f := newFx(t)
	f.reg.Env().SetService(altstore.ServiceLimits, alts.Limits{PerDoc: 1000})
	rng := rand.New(rand.NewSource(1))
	filler := strings.Fields("the quiet house was empty and cold then she left a bird sang over river stones dark light harbour lamp rope sail")
	var b strings.Builder
	var items []string
	for p := 0; p < 500; p++ { // 500 paragraphs of 40 words: 20,000 words
		fmt.Fprintf(&b, "w%04d", p)
		for w := 1; w < 40; w++ {
			b.WriteByte(' ')
			b.WriteString(filler[rng.Intn(len(filler))])
		}
		b.WriteString(".\n\n")
		items = append(items, fmt.Sprintf(`{"scope":"word","quote":"w%04d","variants":["x%04d","y%04d"]}`, p, p, p))
	}
	content := b.String() + alts.Marker + `{"version":1,"items":[` + strings.Join(items, ",") + `]} -->` + "\n"

	start := time.Now()
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Big", Content: content})
	create := time.Since(start)
	if l := f.alts(d.ID); len(l.Items) != 500 || l.Active != 500 {
		t.Fatalf("%d alternatives, %d active", len(l.Items), l.Active)
	}
	f.invariant(d.ID)
	t.Logf("create from a 500-item comment: %v (content %d KB)", create, len(f.get(d.ID).Content)>>10)

	var saves, chooses, lists, nearSaves []time.Duration
	cur := f.get(d.ID)
	for i := 0; i < 20; i++ { // typing at the top: every anchor moves
		next := "x" + cur.Content
		t0 := time.Now()
		got, err := f.save(cur, next)
		saves = append(saves, time.Since(t0))
		if err != nil {
			t.Fatal(err)
		}
		cur = got
		f.invariant(cur.ID) // checked after the clock stops: the budget is the save's
	}
	for i := 0; i < 20; i++ { // typing in the middle of a word nobody anchored
		next := strings.Replace(cur.Content, " house was", " house, was", 1)
		if next == cur.Content {
			next = strings.Replace(cur.Content, " house, was", " house was", 1)
		}
		t0 := time.Now()
		got, err := f.save(cur, next)
		nearSaves = append(nearSaves, time.Since(t0))
		if err != nil {
			t.Fatal(err)
		}
		cur = got
		f.invariant(cur.ID)
	}
	all := f.alts(d.ID)
	for i := 0; i < 20; i++ {
		a := all.Items[rng.Intn(len(all.Items))]
		t0 := time.Now()
		if _, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, AlternativeID: a.ID, Step: "next"}); err != nil {
			t.Fatal(err)
		}
		chooses = append(chooses, time.Since(t0))
	}
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		l := f.alts(d.ID)
		lists = append(lists, time.Since(t0))
		if len(l.Items) != 500 {
			t.Fatal("list")
		}
	}
	f.invariant(d.ID)
	// Every sample is logged: a save that lands on a WAL checkpoint is the slow one, and the p95 of 20 is the slowest of them.
	t.Logf("samples, save at the top: %v", saves)
	t.Logf("samples, ordinary save: %v", nearSaves)
	t.Logf("p95 over 20: save at the top (500 anchors move) %v, ordinary save %v, choose %v, list %v", p95(saves), p95(nearSaves), p95(chooses), p95(lists))
	budget := 250 * time.Millisecond * timingFactor // the machine is shared: twice the 120 ms save budget, and loosened more under the race detector
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		for name, ds := range map[string][]time.Duration{"save at the top": saves, "ordinary save": nearSaves, "choose": chooses, "list": lists} {
			if got := p95(ds); got > budget {
				t.Errorf("%s: p95 %v over the %v budget", name, got, budget)
			}
		}
	})
}
