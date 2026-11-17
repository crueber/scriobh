package mentions_test

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/scenes"
)

// An invented, accented vocabulary: a fifth of the words are non-ASCII, which
// is the slow path of case folding.
func prose(r *rand.Rand, words int, names []string) string {
	syll := []string{"ma", "ré", "lin", "dor", "ûn", "eth", "ar", "ól", "tis", "ve", "ná", "shi", "kor", "ba", "ï"}
	var b strings.Builder
	for i := 0; i < words; i++ {
		if i > 0 {
			if i%17 == 0 {
				b.WriteString(". ")
			} else {
				b.WriteByte(' ')
			}
		}
		if i%40 == 7 && len(names) > 0 {
			b.WriteString(names[r.Intn(len(names))])
			continue
		}
		n := 1 + r.Intn(3)
		for j := 0; j < n; j++ {
			b.WriteString(syll[r.Intn(len(syll))])
		}
		if i%23 == 0 {
			b.WriteString("’s")
		}
		if i%60 == 0 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// bigProject builds a 60,000-word project (60 scenes of 1,000 words) over a
// Compendium of n entities (every fourth has an alias, names are non-ASCII).
func bigProject(t *testing.T, x *h, n int) (pid, cid string, names []string, docIDs []string) {
	t.Helper()
	cid = x.compendium(x.owner, "Bible")
	now := db.Now()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("Eòghan%03d Ní Mhaoláin", i)
			if i%2 == 0 {
				name = fmt.Sprintf("Brónagh%03d", i)
			}
			in := entities.CreateIn{ID: cid, Kind: "character", Name: name}
			if i%4 == 0 {
				in.Aliases = []string{fmt.Sprintf("Bróna%03d the Bold", i)}
			}
			if _, err := entities.Insert(x.ctx, tx, in, now); err != nil {
				return err
			}
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pid = x.project(x.owner, "Big", cid)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 60; i++ {
		d := x.doc(x.owner, pid, fmt.Sprintf("Scene %02d", i), prose(r, 1000, names))
		docIDs = append(docIDs, d.ID)
	}
	return pid, cid, names, docIDs
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// 60,000 words, 500 entities: a save stays quick; adding an entity finds its
// mentions in seconds without holding the writer; saves are not starved.
func TestSixtyThousandWordsFiveHundredEntities(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 60,000-word project")
	}
	x := newH(t)
	pid, cid, names, docIDs := bigProject(t, x, 500)
	r := rand.New(rand.NewSource(2))

	x.settle()
	rows := x.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, pid)
	if rows < 400 {
		t.Fatalf("only %d mention rows; the fixture should mention most entities", rows)
	}
	// A cold full scan: the whole index thrown away and rebuilt from the stored text.
	x.exec(`DELETE FROM entity_mentions`)
	start := time.Now()
	st, err := mentions.Rebuild(x.ctx, x.d, pid)
	if err != nil {
		t.Fatal(err)
	}
	bound(t, "full rebuild (60 documents, 500 entities)", time.Since(start), 8*time.Second)
	bound(t, "longest writer hold of a rebuild chunk", st.Longest, 250*time.Millisecond)
	t.Logf("rebuild: %+v", st)
	if got := x.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, pid); got != rows {
		t.Fatalf("the rebuild wrote %d rows, a save-by-save index had %d", got, rows)
	}

	// A save, with the engine cold and then warm. A scene of 1,000 words saved
	// with the index on; the budget is the 400 ms save budget.
	save := func(i int) time.Duration {
		d := x.call(x.owner, "doc.get", docs.DocIn{ID: docIDs[i]}).(docs.Item).Item
		body := prose(r, 1000, names)
		s := time.Now()
		x.save(x.owner, d, body)
		return time.Since(s)
	}
	mentions.ResetCache()
	cold := save(0)
	var warm []time.Duration
	for i := 1; i < 21; i++ {
		warm = append(warm, save(i))
	}
	bound(t, "save, first after a vocabulary change", cold, 400*time.Millisecond)
	bound(t, "save, median of 20 (1,000-word scene, 500 entities)", median(warm), 400*time.Millisecond)

	// A long single document: 12,000 words in one scene.
	big := x.doc(x.owner, pid, "Long", prose(r, 12_000, names))
	s := time.Now()
	big = x.save(x.owner, big, prose(r, 12_000, names))
	bound(t, "save, one 12,000-word scene", time.Since(s), 400*time.Millisecond)

	// Adding a character: the op itself is quick, the rescan runs in seconds,
	// and the writer is held only in short chunks while saves keep landing.
	// The text names her before she exists, in ten scenes.
	for i := 0; i < 10; i++ {
		d := x.call(x.owner, "doc.get", docs.DocIn{ID: docIDs[40+i]}).(docs.Item).Item
		x.save(x.owner, d, d.Content+"\n\nThen Eòghan999 Ní Mhaoláin appeared, and Eòghan the Late left.")
	}
	x.settle()
	s = time.Now()
	hero := x.ent(x.owner, cid, "character", "Eòghan999 Ní Mhaoláin")
	bound(t, "entity.create (marks the project)", time.Since(s), 400*time.Millisecond)
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: hero, Alias: "Eòghan the Late"})
	var worst time.Duration
	var saves int
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // saves land while the rescan runs
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			d := x.call(x.owner, "doc.get", docs.DocIn{ID: docIDs[i%20]}).(docs.Item).Item
			t0 := time.Now()
			x.save(x.owner, d, prose(r, 200, names))
			// timing: not a budget: tracks the slowest save; the limit is asserted by bound
			if el := time.Since(t0); el > worst {
				worst = el
			}
			saves++
		}
	}()
	s = time.Now()
	scan := x.settle()
	took := time.Since(s)
	close(stop)
	wg.Wait()
	bound(t, "rescan after adding an entity", took, 5*time.Second)
	bound(t, "longest writer hold of one rescan chunk", scan.Longest, 250*time.Millisecond)
	bound(t, "slowest save while the rescan ran", worst, 1500*time.Millisecond)
	t.Logf("rescan: %+v, %d saves landed meanwhile", scan, saves)
	if scan.Written < 10 {
		t.Errorf("the rescan wrote %d rows; it should have found the new character in ten scenes", scan.Written)
	}
	if saves == 0 {
		t.Errorf("no save completed during the rescan: the writer was starved")
	}
	by := x.call(x.owner, "mention.by_entity", mentionsByEntity(pid, "Eòghan999 Ní Mhaoláin"))
	if got := by.(mentions.ByEntityOut).Documents; got < 10 {
		t.Errorf("the new character is in %d documents, want at least 10", got)
	}

	// The highlight read: spans for the 12,000-word scene.
	s = time.Now()
	hl := x.list(x.owner, big.ID, true)
	bound(t, "mention.list with spans, one 12,000-word scene", time.Since(s), 100*time.Millisecond)
	nspans := 0
	for _, m := range hl.Items {
		nspans += len(m.Spans)
	}
	if nspans < 100 || !hl.SpansComplete {
		t.Errorf("spans for the long scene: %d, complete %v", nspans, hl.SpansComplete)
	}
	// The reads the sidebar makes.
	s = time.Now()
	for i := 0; i < 20; i++ {
		x.list(x.owner, docIDs[i], false)
	}
	bound(t, "mention.list, mean of 20 reads", time.Since(s)/20, 20*time.Millisecond)
	s = time.Now()
	for i := 0; i < 20; i++ {
		x.call(x.owner, "scene.entities", scenes.EntitiesIn{ID: docIDs[i]})
	}
	bound(t, "scene.entities, mean of 20 reads", time.Since(s)/20, 20*time.Millisecond)
	s = time.Now()
	x.call(x.owner, "entity.first_appearance", mentions.FirstIn{ID: pid})
	bound(t, "entity.first_appearance for 500 entities", time.Since(s), 100*time.Millisecond)
	s = time.Now()
	x.call(x.owner, "mention.by_entity", mentionsByEntity(pid, names[3]))
	bound(t, "mention.by_entity", time.Since(s), 40*time.Millisecond)
}

func mentionsByEntity(pid, entity string) mentions.ByEntityIn {
	return mentions.ByEntityIn{ID: pid, Entity: entity, Limit: 100}
}
