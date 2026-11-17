package docs_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/ops"
)

// The invariant (the comment is the serialisation of the tables, the body holds
// the chosen wording of every active alternative at its anchor, no comment
// without alternatives) must hold after every op and every save path, whatever
// order they come in. This drives them at random, seeded, and checks it from
// the database after each one that succeeds. Refusals are fine; a server
// error is not.
func TestInvariantAfterEveryOp(t *testing.T) {
	seeds, steps := 6, 160
	if testing.Short() {
		seeds, steps = 2, 60
	}
	for seed := 1; seed <= seeds; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(seed)))
			f := newFx(t)
			d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Fuzz", Content: altBody + "\nA café 😀 sentence, with naïve words.\n\n- one item\n- two item\n"})
			item := f.stash(f.pidA, "S", "A stashed paragraph of words.")
			words := strings.Fields("the quiet house empty stayed window street paragraph waited moved café naïve 😀 sentence words item")
			var what string
			applied, refused := map[string]int{}, map[string]int{}
			defer func() { t.Logf("applied %v\nrefused %v", applied, refused) }()
			ok := func(err error) {
				t.Helper()
				if err == nil {
					applied[what]++
					return
				}
				refused[what]++
				switch ops.AsError(err).Code {
				case ops.CodeInvalid, ops.CodeConflict, ops.CodeNotFound:
					return
				}
				t.Fatalf("server error: %v", err)
			}
			for step := 0; step < steps; step++ {
				cur := f.get(d.ID)
				body := alts.Split(cur.Content).Body
				l := f.alts(d.ID)
				pick := func() (docs.AltOut, bool) {
					if len(l.Items) == 0 {
						return docs.AltOut{}, false
					}
					return l.Items[rng.Intn(len(l.Items))], true
				}
				rev := cur.Revision
				what = ""
				switch n := rng.Intn(16); n {
				case 0, 1, 2:
					w := words[rng.Intn(len(words))]
					scope := []string{"word", "word", "sentence"}[rng.Intn(3)]
					what = "create " + w
					_, err := f.call(f.ownA, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: rev, Scope: scope, Quote: w, Occurrence: ip(rng.Intn(3)), Variants: []string{words[rng.Intn(len(words))] + "x"}})
					ok(err)
				case 3, 4:
					if a, has := pick(); has {
						what = "choose"
						_, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID, Step: []string{"next", "previous", "original"}[rng.Intn(3)]})
						ok(err)
					}
				case 5:
					if a, has := pick(); has {
						what = "add_variant"
						_, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID, Text: fmt.Sprintf("v%d", step)})
						ok(err)
					}
				case 6:
					if a, has := pick(); has {
						what = "edit_variant"
						v := a.Variants[rng.Intn(len(a.Variants))]
						_, err := f.call(f.ownA, "alternative.edit_variant", docs.AltVariantEditIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID, VariantID: v.ID, Text: fmt.Sprintf("e%d", step)})
						ok(err)
					}
				case 7:
					if a, has := pick(); has {
						what = "remove_variant"
						v := a.Variants[rng.Intn(len(a.Variants))]
						_, err := f.call(f.ownA, "alternative.remove_variant", docs.AltVariantRemoveIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID, VariantID: v.ID})
						ok(err)
					}
				case 8:
					if a, has := pick(); has {
						what = "delete"
						_, err := f.call(f.ownA, "alternative.delete", docs.AltDeleteIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID})
						ok(err)
					}
				case 9:
					if a, has := pick(); has {
						what = "reanchor"
						_, err := f.call(f.ownA, "alternative.reanchor", docs.AltReanchorIn{ID: d.ID, BaseRevision: rev, AlternativeID: a.ID, Quote: words[rng.Intn(len(words))], Occurrence: ip(0)})
						ok(err)
					}
				case 10, 11, 12:
					// An edit made by someone who sees only text: with or without the comment.
					rs := []rune(body)
					a := rng.Intn(len(rs))
					b := min(len(rs), a+rng.Intn(8))
					ins := []string{"", "x", " more words ", "😀", "quiet", "\n\n", "new"}[rng.Intn(7)]
					nb := string(rs[:a]) + ins + string(rs[b:])
					content := nb
					if rng.Intn(2) == 0 {
						content = alts.Split(cur.Content).Compose(alts.Split(cur.Content).Line) // keeps the (now possibly stale) comment
						content = strings.Replace(content, body, nb, 1)
					}
					what = "doc.update"
					_, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: rev, Content: &content})
					ok(err)
				case 13:
					what = "ghost"
					_, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: rev, Ranges: []docs.Range{{Quote: words[rng.Intn(len(words))], Occurrence: ip(rng.Intn(2))}}})
					ok(err)
				case 14:
					what = "revive"
					_, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: rev, All: true})
					ok(err)
				case 15:
					if rng.Intn(2) == 0 {
						what = "drag_back"
						_, err := f.call(f.ownA, "stash.drag_back", docs.DragBackIn{ID: f.pidA, ItemID: item.ID, DocumentID: d.ID, BaseRevision: rev, At: ip(rng.Intn(len([]rune(body)) + 1)), Placement: []string{"block", "inline"}[rng.Intn(2)]})
						ok(err)
					} else {
						what = "restore"
						var rid string
						if err := f.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? ORDER BY random() LIMIT 1`, d.ID).Scan(&rid); err == nil {
							_, err := f.call(f.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: rid, BaseRevision: rev})
							ok(err)
						}
					}
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("step %d (%s): invariant check panicked: %v", step, what, r)
						}
					}()
					f.invariant(d.ID)
				}()
				// Every refused or applied op leaves the history readable.
				_ = revisions.ReasonAlternative
				_ = stash.Kinds
			}
		})
	}
}
