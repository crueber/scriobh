package semantic_test

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/testutil"
)

const (
	perfChunks = 10_000
	perfDim    = 768
)

func TestSemanticSearchBudget(t *testing.T) {
	e := newEnv(t, semantic.Config{QueryRatePerMinute: 1_000_000}, nil)
	f := newFakeEmbed(t, "ollama")
	f.dim.Store(perfDim)
	pid := e.setup(f)
	var ids []string
	for i := 0; i < 120; i++ {
		var ps []string
		for p := 0; p < 10; p++ {
			ps = append(ps, para(fmt.Sprintf("topic%d word%d", i, p), 70))
		}
		ids = append(ids, e.doc(fmt.Sprintf("Scene %03d", i), "scene", "", strings.Join(ps, "\n\n")))
	}
	e.turnOn(pid)
	e.settle() // the real worker builds a real index, to know the real chunks
	st := e.status(e.owner)
	real := st.Chunks
	if real < 500 || st.Dim != perfDim {
		t.Fatalf("%+v", st)
	}
	// Pad it with random vectors over real positions up to the budget's 10,000.
	r := rand.New(rand.NewSource(11))
	rows, err := e.d.Read().Query(`SELECT document_id, ord, start_pos, end_pos, words, text_hash FROM embed_chunks ORDER BY document_id, ord`)
	if err != nil {
		t.Fatal(err)
	}
	type c struct {
		doc          string
		ord, a, b, w int
		h            string
	}
	var cs []c
	for rows.Next() {
		var x c
		rows.Scan(&x.doc, &x.ord, &x.a, &x.b, &x.w, &x.h)
		cs = append(cs, x)
	}
	rows.Close()
	tx, _ := e.d.Writer().Begin()
	n := len(cs)
	for i := 0; real+i < perfChunks; i++ {
		x := cs[i%n]
		blob := make([]byte, perfDim)
		var n2 float64
		for k := range blob {
			v := int8(r.Intn(255) - 127)
			blob[k] = byte(v)
			n2 += float64(v) * float64(v)
		}
		if _, err := tx.Exec(`INSERT INTO embed_chunks(document_id, project_id, ord, start_pos, end_pos, words, text_hash, model, dim, vec, mult) VALUES (?,?,?,?,?,?,?,'embed-a',?,?,?)`,
			x.doc, e.project, 100000+i, x.a, x.b, x.w, x.h, perfDim, blob, 1/math.Sqrt(n2)); err != nil {
			t.Fatal(err)
		}
	}
	tx.Exec(`UPDATE embed_projects SET gen = gen + 1`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := e.count(`SELECT count(*) FROM embed_chunks`); got != perfChunks {
		t.Fatalf("%d chunks", got)
	}

	q := func() []float32 {
		v := make([]float32, perfDim)
		var n2 float64
		for i := range v {
			v[i] = float32(r.NormFloat64())
			n2 += float64(v[i] * v[i])
		}
		for i := range v {
			v[i] /= float32(math.Sqrt(n2))
		}
		return v
	}
	start := time.Now()
	items, got, err := e.sem.SearchVector(e.ctx, e.project, q(), 20, 2)
	cold := time.Since(start)
	if err != nil || got != perfChunks || len(items) == 0 {
		t.Fatalf("%v %d %d", err, got, len(items))
	}
	// 200 warm searches and 40 whole ops give the percentiles; each is 60 ms under -race on 10,000 chunks, so the
	// race pass (-short) takes 30 and 12, which still make a p95 (the 28th of 30) and keep the 10,000-chunk budget
	// subtest asserting. The plain pass takes the full counts.
	warmN, opN := 200, 40
	if testing.Short() {
		warmN, opN = 30, 12
	}
	var ts []float64
	for i := 0; i < warmN; i++ {
		v := q()
		s := time.Now()
		if _, _, err := e.sem.SearchVector(e.ctx, e.project, v, 20, 2); err != nil {
			t.Fatal(err)
		}
		ts = append(ts, float64(time.Since(s).Microseconds())/1000)
	}
	sort.Float64s(ts)
	p50, p95, max := ts[len(ts)/2], ts[len(ts)*95/100], ts[len(ts)-1]
	t.Logf("%d chunks of %d dimensions: cold (the vectors read from the database) %.1f ms; warm p50 %.2f ms, p95 %.2f ms, max %.2f ms", perfChunks, perfDim, float64(cold.Microseconds())/1000, p50, p95, max)
	// The whole op, through the registry and the fake endpoint (the question's embedding is one loopback request
	// of 768 numbers each way, which the budget leaves out): reported, with a loose sanity limit.
	var opts []float64
	for i := 0; i < opN; i++ {
		s0 := time.Now()
		out, err := e.try(e.editor, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: fmt.Sprintf("topic%d word%d detail", i%50, i%10)})
		if err != nil || len(out.(semantic.SearchOut).Items) == 0 {
			t.Fatalf("%v %+v", err, out)
		}
		opts = append(opts, float64(time.Since(s0).Microseconds())/1000)
	}
	sort.Float64s(opts)
	t.Logf("the whole search.semantic op with the embedding request to the fake: p50 %.2f ms, p95 %.2f ms", opts[len(opts)/2], opts[len(opts)*95/100])
	opP95 := opts[len(opts)*95/100]
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if opP95 > 200*float64(timingFactor) {
			t.Errorf("the whole op took %.1f ms at p95", opP95)
		}
		if p95 > 80*float64(timingFactor) {
			t.Errorf("p95 %.1f ms over the 80 ms budget", p95)
		}
		if cold > 2*time.Second*time.Duration(timingFactor) {
			t.Errorf("the cold load took %v", cold)
		}
	})
	_ = ids
}
