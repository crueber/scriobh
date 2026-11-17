package semantic

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func randVec(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

func cosine(a, b []float32) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return d / math.Sqrt(na*nb)
}

// The int8 form scores a unit query as the cosine of the vector, to within the rounding.
func TestQuantisedScoreIsTheCosine(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for _, dim := range []int{8, 96, 384, 768, 1024, 3072} {
		var worst float64
		for n := 0; n < 200; n++ {
			v, q := randVec(r, dim), randVec(r, dim)
			// A scale that is nothing like unit: the length must not matter.
			scale := float32(math.Pow(10, float64(r.Intn(7)-3)))
			for i := range v {
				v[i] *= scale
			}
			qi, mult, ok := quantise(v)
			if !ok {
				t.Fatal("no direction")
			}
			u, _ := unit(q)
			got := float64(dot(qi, u) * mult)
			if e := math.Abs(got - cosine(v, q)); e > worst {
				worst = e
			}
		}
		limit := 0.02
		if dim < 100 {
			limit = 0.04
		}
		t.Logf("dimension %d: the largest cosine error over 200 random pairs is %.4f", dim, worst)
		if worst > limit {
			t.Errorf("dimension %d: the cosine is off by %.4f", dim, worst)
		}
	}
}

// Ranking is what is used: the same neighbours come out of the int8 form as out of the exact one.
func TestQuantisedRankingAgreesWithExact(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	const dim, n = 768, 2000
	// Clustered vectors, as embeddings are: a few centres and noise.
	centres := make([][]float32, 20)
	for i := range centres {
		centres[i] = randVec(r, dim)
	}
	vecs := make([][]float32, n)
	qs := make([][]int8, n)
	ms := make([]float32, n)
	for i := range vecs {
		c := centres[r.Intn(len(centres))]
		v := make([]float32, dim)
		for k := range v {
			v[k] = c[k] + 0.7*float32(r.NormFloat64())
		}
		vecs[i] = v
		qs[i], ms[i], _ = quantise(v)
	}
	agree, total := 0, 0
	for trial := 0; trial < 50; trial++ {
		q := vecs[r.Intn(n)]
		qq := make([]float32, dim)
		for k := range qq {
			qq[k] = q[k] + 0.3*float32(r.NormFloat64())
		}
		u, _ := unit(qq)
		type sc struct {
			i int
			s float64
		}
		exact := make([]sc, n)
		tk := newTopK(10)
		for i := range vecs {
			exact[i] = sc{i, cosine(vecs[i], qq)}
			tk.offer(hit{i, dot(qs[i], u) * ms[i]})
		}
		sort.Slice(exact, func(a, b int) bool { return exact[a].s > exact[b].s })
		want := map[int]bool{}
		for _, e := range exact[:10] {
			want[e.i] = true
		}
		for _, h := range tk.sorted() {
			total++
			if want[h.idx] {
				agree++
			}
		}
	}
	t.Logf("%d of %d of the top ten agree with exact float cosine", agree, total)
	if float64(agree)/float64(total) < 0.97 {
		t.Errorf("only %d of %d agree", agree, total)
	}
}

func TestQuantiseRefusesWhatHasNoDirection(t *testing.T) {
	if _, _, ok := quantise(make([]float32, 8)); ok {
		t.Error("zeros")
	}
	if _, _, ok := quantise([]float32{float32(math.Inf(1)), 1, 1, 1, 1, 1, 1, 1}); ok {
		t.Error("an infinity")
	}
	if _, ok := unit(make([]float32, 8)); ok {
		t.Error("unit of zeros")
	}
	// A vector whose every number rounds to zero but one still has a direction.
	v := make([]float32, 8)
	v[3] = 1e-30
	if q, m, ok := quantise(v); !ok || q[3] != 127 || m <= 0 {
		t.Errorf("%v %v %v", q, m, ok)
	}
}

func TestTopKOrderIsStable(t *testing.T) {
	tk := newTopK(3)
	for i, s := range []float32{0.5, 0.9, 0.9, 0.1, 0.7, 0.9, 0.2} {
		tk.offer(hit{i, s})
	}
	got := tk.sorted()
	if len(got) != 3 || got[0].idx != 1 || got[1].idx != 2 || got[2].idx != 5 {
		t.Fatalf("%v: equal scores come out in index order", got)
	}
}

// BenchmarkScan10k768 is the scan alone over the budget's 10,000 chunks of 768 dimensions: the float query against
// int8 vectors, the document filter and the top 70 (before snippets).
func BenchmarkScan10k768(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	const n, dim = 10_000, 768
	sn := &snapshot{dim: dim, vec: make([]int8, n*dim), mult: make([]float32, n), docIdx: make([]int32, n)}
	for i := range sn.vec {
		sn.vec[i] = int8(r.Intn(255) - 127)
	}
	for i := range sn.mult {
		sn.mult[i] = 0.001
		sn.docIdx[i] = int32(i % 100)
	}
	q, _ := unit(randVec(r, dim))
	ok := make([]bool, 100)
	for i := range ok {
		ok[i] = true
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sn.scan(q, ok, 70)
	}
}
