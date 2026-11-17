package editiondiff_test

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editiondiff"
)

func mkText(r *rand.Rand, pool []string, paras, words int) string {
	var b strings.Builder
	for p := 0; p < paras; p++ {
		for w := 0; w < words; w++ {
			b.WriteString(pool[r.Intn(len(pool))])
			b.WriteByte(' ')
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// Every chapter of both texts is in exactly one step, whatever the edits (swap, delete, duplicate, empty, retitle,
// merge, split); the words of the steps add up to the words of the books; "unchanged" means the same text; and the
// answer is the same when asked twice.
func TestReviewAccountingOfEveryChapter(t *testing.T) {
	pool := words[:400]
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 3 + r.Intn(12)
		base := make([]editiondiff.Unit, n)
		for i := range base {
			title := fmt.Sprintf("T%d", i)
			switch r.Intn(5) {
			case 0:
				title = fmt.Sprintf("Chapter %d", i+1)
			case 1:
				title = ""
			}
			base[i] = editiondiff.Unit{Key: fmt.Sprint("e", i+1), Ord: i + 1, Title: title, Parent: -1, Text: mkText(r, pool, 1+r.Intn(5), 5+r.Intn(30))}
		}
		var head []editiondiff.Unit
		for i, u := range base {
			switch r.Intn(9) {
			case 0: // dropped
				continue
			case 1: // duplicated
				c := u
				c.Key = fmt.Sprint("d", i, "a")
				head = append(head, c)
				c.Key = fmt.Sprint("d", i, "b")
				head = append(head, c)
			case 2: // emptied
				c := u
				c.Text = ""
				c.Key = fmt.Sprint("d", i)
				head = append(head, c)
			case 3: // retitled
				c := u
				c.Title = "Other " + u.Title
				c.Key = fmt.Sprint("d", i)
				head = append(head, c)
			case 4: // split in two
				ps := strings.SplitAfter(u.Text, "\n\n")
				h := len(ps) / 2
				c1, c2 := u, u
				c1.Key, c2.Key = fmt.Sprint("d", i, "a"), fmt.Sprint("d", i, "b")
				c1.Text, c2.Text = strings.Join(ps[:h], ""), strings.Join(ps[h:], "")
				c2.Title = u.Title + " (cont.)"
				head = append(head, c1, c2)
			case 5: // reworded
				c := u
				c.Text = mkText(r, pool, 2, 10) + u.Text
				c.Key = fmt.Sprint("d", i)
				head = append(head, c)
			default:
				c := u
				c.Key = fmt.Sprint("d", i)
				head = append(head, c)
			}
		}
		r.Shuffle(len(head), func(i, j int) {
			if r.Intn(4) == 0 {
				head[i], head[j] = head[j], head[i]
			}
		})
		for i := range head {
			head[i].Ord = i + 1
			head[i].Parent = -1
		}
		in := editiondiff.Input{Base: base, Head: head, Limits: editiondiff.Defaults()}
		res, err := editiondiff.Compare(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		res2, err := editiondiff.Compare(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(res, res2) {
			t.Fatalf("seed %d: not deterministic", seed)
		}
		seenB, seenH := map[int]int{}, map[string]int{}
		wb, wh := 0, 0
		for _, s := range res.Steps {
			for _, b := range s.Base {
				seenB[b.Ord]++
			}
			for _, h := range s.Head {
				seenH[fmt.Sprint(h.Title, "|", h.Words)]++
			}
			wb += s.Words.Base
			wh += s.Words.Head
			if s.Status == editiondiff.StatusUnchanged {
				if len(s.Base) != 1 || len(s.Head) != 1 || strings.TrimSpace(base[s.Base[0].Ord-1].Text) != strings.TrimSpace(head[indexOf(head, s)].Text) {
					t.Errorf("seed %d: unchanged but texts differ: %+v", seed, s)
				}
			}
		}
		for _, u := range base {
			if seenB[u.Ord] != 1 {
				t.Fatalf("seed %d: base chapter %d is in %d steps", seed, u.Ord, seenB[u.Ord])
			}
		}
		nh := 0
		for _, s := range res.Steps {
			nh += len(s.Head)
		}
		if nh != len(head) {
			t.Fatalf("seed %d: %d head chapters in steps, want %d", seed, nh, len(head))
		}
		if wb != res.Summary.BaseWords || wh != res.Summary.HeadWords {
			t.Errorf("seed %d: words %d/%d vs %d/%d", seed, wb, wh, res.Summary.BaseWords, res.Summary.HeadWords)
		}
	}
}

func indexOf(head []editiondiff.Unit, s editiondiff.Step) int { return s.Head[0].Ord - 1 }

// A step's match is read from its first chapter of the first text only. Here chapter 1 is recorded as the document and
// chapter 2 (left out at import) is folded into the step by similarity alone, yet the step says guess=false and, as one
// document, can be taken whole.
func TestReviewMergedStepWithAGuessedMemberIsNotLabelledAGuess(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pool := words[:900]
	t1, t2 := mkText(r, pool, 6, 40), mkText(r, pool, 6, 40)
	ps := strings.SplitAfter(t2, "\n\n")
	doc := t1 + strings.Join(ps[:4], "") // the document holds chapter 1 and most of chapter 2
	base := []editiondiff.Unit{
		{Key: "e1", Ord: 1, Title: "One", Mapped: "doc_1", Parent: -1, Text: t1},
		{Key: "e2", Ord: 2, Title: "Two (left out at import)", Parent: -1, Text: t2},
	}
	head := []editiondiff.Unit{{Key: "doc_1", ID: "doc_1", Ord: 1, Title: "One", Parent: -1, Text: doc}}
	res, err := editiondiff.Compare(context.Background(), editiondiff.Input{Base: base, Head: head, ProjectMode: true, Limits: editiondiff.Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("%+v", res.Steps)
	}
	s := res.Steps[0]
	if len(s.Base) == 2 && s.Match != nil && !s.Match.Guess {
		t.Errorf("status %s: chapter 2 was joined by similarity only, but match = %+v", s.Status, *s.Match)
	}
}
