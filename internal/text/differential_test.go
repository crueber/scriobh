package text

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A differential test: seeded random documents built from the constructs the
// editor treats specially, counted by the editor's parser in Node and by the
// Go counter. Both must agree on every one. The seed is fixed so the test is
// reproducible; DIFF_SEED and DIFF_CASES explore further.

var inlineBits = []string{
	"alpha", "beta", "gamma-delta", "x1", "Máire", "42", "—", "...", "ß", "word", "two words", "a", "naïve", "日本語", "x\u00a0y", "\ufeff",
	"*em*", "**strong**", "~~gone~~", "*", "_", "**", "~~", "`code`", "` a b `", "``x ` y``", "`", "a*b*c", "snake_case_name",
	"[link text](http://a.b/c)", "[l](<http://a.b/c d>)", "[a][ref]", "[ref]", "[ref][]", "![img alt](pic.png)", "![x](a b.png)",
	"<http://x.y/z>", "<a@b.co>", "<b>", "</b>", "<br>", "<!-- c -->", "<span class=\"x\">", "a<i>b</i>c",
	"[[Wiki Title]]", "[[T|alias words]]", "[[ ]]", "[[a]b]]", "[[a|]]", "[^1]", "[^note x]", "[^ ]", "word[^2]more",
	"%%note%%", "%%two words%%", "%%", "%", "%%%%", "%%a", "b%%", "x%%y%%z", "\\%%", "\\%\\%",
	"\\*", "\\\\", "\\", "&amp;", "&nbsp;", "&#35;", "&#x41;", "&copy;", "&bogus;", "&", "&#0;", "&lt;b&gt;",
	"[ ]", "[x]", "[X] done", "[", "]", "(", ")", "[]", "[](http://z)", "[a]()", "[a](b \"t t\")",
	"[x](javascript:alert(1))", "[x](java&Tab;script:1)", "![i](data:text/html,x)", "[d](data:image/png;base64,AAA)", "<javascript:x>", "[f](file:///etc/passwd)",
	"http://auto.link/x", "a  b", "tab\tsep",
}

var sepBits = []string{" ", " ", " ", "", "\n", "  \n", "\\\n", "\n   ", " \n"}

func (g *gen) inline() string {
	n := 1 + g.r.Intn(7)
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(g.pick(sepBits))
		}
		b.WriteString(g.pick(inlineBits))
	}
	return b.String()
}

type gen struct{ r *rand.Rand }

func (g *gen) pick(xs []string) string { return xs[g.r.Intn(len(xs))] }

func prefix(s, first, rest string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if i == 0 {
			lines[i] = first + l
		} else if l == "" {
			lines[i] = strings.TrimRight(rest, " ")
		} else {
			lines[i] = rest + l
		}
	}
	return strings.Join(lines, "\n")
}

func (g *gen) block(depth int) string {
	k := g.r.Intn(30)
	switch {
	case k < 8:
		return g.inline()
	case k < 10:
		return strings.Repeat("#", 1+g.r.Intn(6)) + " " + g.inline()
	case k == 10:
		return g.inline() + "\n" + g.pick([]string{"===", "---", "-----"})
	case k < 14 && depth < 3:
		inner := g.blocks(1+g.r.Intn(3), depth+1, "\n\n")
		return prefix(inner, "> ", "> ")
	case k < 17 && depth < 3:
		var items []string
		ordered := g.r.Intn(3) == 0
		sep := g.pick([]string{"\n", "\n", "\n\n"})
		for i, n := 0, 1+g.r.Intn(3); i < n; i++ {
			inner := g.blocks(1+g.r.Intn(2), depth+1, g.pick([]string{"\n\n", "\n", "\n\n"}))
			mark := "- "
			if ordered {
				mark = strconv.Itoa(i+1) + ". "
			}
			switch g.r.Intn(5) {
			case 0:
				inner = "[ ] " + inner
			case 1:
				inner = "[x] " + inner
			}
			items = append(items, prefix(inner, mark, strings.Repeat(" ", len(mark))))
		}
		return strings.Join(items, sep)
	case k == 17:
		return "```" + g.pick([]string{"", "go", "text"}) + "\n" + g.inline() + "\n\n" + g.inline() + "\n```"
	case k == 18:
		return "    " + g.inline()
	case k == 19 && depth == 0:
		return "| a | b |\n|---|:-:|\n| " + g.pick(inlineBits) + " | " + g.pick(inlineBits) + " |"
	case k == 20:
		return g.pick([]string{"<div>\n" + g.inline() + "\n</div>", "<!-- " + g.inline() + " -->", "<script>\nx " + g.inline() + "\n</script>", "<p>" + g.inline() + "</p>", "<!-- scriobh:alts {\"a\":1} -->"})
	case k < 23:
		d := g.pick([]string{":::note", "::: warn this", ":::", ":::::box", ":::x"})
		if g.r.Intn(3) == 0 {
			return d + "\n" + g.inline() // unclosed
		}
		return d + "\n" + g.inline() + "\n\n" + g.inline() + "\n" + g.pick([]string{":::", ":::::", "::::"})
	case k < 25:
		return "[^" + g.pick([]string{"1", "note", "a-b"}) + "]: " + g.inline() + g.pick([]string{"", "\n    " + g.inline(), "\n\n    " + g.inline(), "\nlazy " + g.inline()})
	case k == 25:
		return g.pick([]string{"* * *", "---", "***", "___"})
	case k == 26:
		return g.pick([]string{"[ref]: http://x.y \"Title Here\"", "[ref]: <http://x.y> 'T'", "[a]: javascript:alert(1)", "[ref]: /local"})
	case k == 27:
		return "~~~\n" + g.inline() + "\n~~~"
	default:
		return g.inline()
	}
}

// join puts sep between blocks, except that tables, `:::` blocks, footnote
// definitions and link reference definitions are always set off by blank
// lines. Where one of them touches another line inside a list item, a quote or
// a footnote (a lazy continuation, or a block that ends at a different line)
// the editor's parser and goldmark draw the boundary differently in ways the
// Go counter does not mirror; those inputs are outside what this test asks.
func join(parts []string, sep string) string {
	special := func(b string) bool {
		for _, p := range []string{"|", "> |", ":::", "::: ", "[^", "[ref]:", "[a]:", "> [^", "> [ref]:"} {
			if strings.HasPrefix(b, p) {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			if special(p) || special(parts[i-1]) {
				b.WriteString("\n\n")
			} else {
				b.WriteString(sep)
			}
		}
		b.WriteString(p)
	}
	return b.String()
}

func (g *gen) blocks(n, depth int, sep string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = g.block(depth)
	}
	return join(parts, sep)
}

func (g *gen) doc() string {
	var b strings.Builder
	if g.r.Intn(10) == 0 {
		b.WriteString(g.pick([]string{"---\ntitle: x y\n---\n\n", "---\n# c\nz: 1\n...\n", "---\n\n", "---\nnot yaml\n---\n"}))
	}
	n := 1 + g.r.Intn(5)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = g.block(0)
	}
	b.WriteString(join(parts, g.pick([]string{"\n\n", "\n\n", "\n\n", "\n"})))
	if g.r.Intn(2) == 0 {
		b.WriteString("\n")
	}
	return b.String()
}

func nodeCounts(t *testing.T, docs []string) []int {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		if inCI() {
			t.Fatalf("node is required for the differential test in CI: %v", err)
		}
		t.Skip("node not found; skipping the differential test (it fails when CI is set)")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("web", "editor", "wordcount.mjs"), "--impl", "bundle", "--stdin")
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("wordcount.mjs --stdin failed: %v\n%s", err, stderr.String())
	}
	var out []int
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != len(docs) {
		t.Fatalf("node counted %d of %d documents", len(out), len(docs))
	}
	return out
}

func TestWordCountDifferential(t *testing.T) {
	seed, cases := int64(20261112), 3000
	if v, err := strconv.ParseInt(os.Getenv("DIFF_SEED"), 10, 64); err == nil {
		seed = v
	}
	if v, err := strconv.Atoi(os.Getenv("DIFF_CASES")); err == nil {
		cases = v
	}
	g := &gen{r: rand.New(rand.NewSource(seed))}
	docs := make([]string, cases)
	for i := range docs {
		docs[i] = g.doc()
	}
	want := nodeCounts(t, docs)
	type miss struct {
		doc       string
		want, got int
	}
	var misses []miss
	for i, d := range docs {
		got, perr := func() (n int, perr any) {
			defer func() { perr = recover() }()
			return WordCount(d), nil
		}()
		if perr != nil {
			t.Errorf("WordCount panicked on %q: %v", d, perr)
			continue
		}
		if got != want[i] {
			misses = append(misses, miss{d, want[i], got})
		}
	}
	if len(misses) == 0 {
		return
	}
	sort.Slice(misses, func(i, j int) bool { return len(misses[i].doc) < len(misses[j].doc) })
	var b strings.Builder
	for i, m := range misses {
		if i == 12 {
			break
		}
		if os.Getenv("DIFF_SHRINK") == "" {
			fmt.Fprintf(&b, "\n  editor %d, Go %d: %q", m.want, m.got, m.doc)
			continue
		}
		small := shrink(t, m.doc)
		fmt.Fprintf(&b, "\n  editor %d, Go %d: %q\n      from %q", nodeCounts(t, []string{small})[0], WordCount(small), small, m.doc)
	}
	t.Errorf("%d of %d random documents disagree with the editor (seed %d); the shortest:%s", len(misses), cases, seed, b.String())
}

// shrink reduces a disagreeing document to a small one that still disagrees,
// by deleting lines and then characters while the two counters stay apart.
func shrink(t *testing.T, doc string) string {
	t.Helper()
	disagree := func(ds []string) []bool {
		want := nodeCounts(t, ds)
		out := make([]bool, len(ds))
		for i, d := range ds {
			out[i] = WordCount(d) != want[i]
		}
		return out
	}
	for round := 0; round < 200; round++ {
		var cands []string
		lines := strings.Split(doc, "\n")
		for i := range lines {
			cands = append(cands, strings.Join(append(append([]string{}, lines[:i]...), lines[i+1:]...), "\n"))
		}
		rs := []rune(doc)
		for i := range rs {
			cands = append(cands, string(rs[:i])+string(rs[i+1:]))
		}
		bad := disagree(cands)
		next := ""
		for i, c := range cands {
			if bad[i] && (next == "" || len(c) < len(next)) {
				next = c
			}
		}
		if next == "" {
			return doc
		}
		doc = next
	}
	return doc
}
