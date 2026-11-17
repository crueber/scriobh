package alts

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

var seq int

func newID(prefix string) string { seq++; return fmt.Sprintf("%s_%04d", prefix, seq) }

func opts() Options { return Options{Actor: "usr_a", Now: "2026-01-01T00:00:00.000Z", NewID: newID} }

// item places an alternative over the first occurrence of quote in body.
func item(t testing.TB, body, scope, quote string, others ...string) Item {
	t.Helper()
	a := strings.Index(body, quote)
	if a < 0 {
		t.Fatalf("%q not in %q", quote, body)
	}
	ix := NewIndex(body)
	it := Item{ID: newID("alt"), Scope: scope, Start: ix.Unit(a), End: ix.Unit(a + len(quote)),
		Before: contextBefore(body, a), After: contextAfter(body, a+len(quote))}
	orig := Variant{ID: newID("alv"), Text: quote, Original: true, Source: SourceManual}
	it.Variants = append(it.Variants, orig)
	it.Chosen = orig.ID
	for _, o := range others {
		it.Variants = append(it.Variants, Variant{ID: newID("alv"), Text: o, Source: SourceManual})
	}
	return it
}

func bodyAt(t testing.TB, body string, it Item) string {
	t.Helper()
	ix := NewIndex(body)
	a, ok1 := ix.Byte(it.Start)
	b, ok2 := ix.Byte(it.End)
	if !ok1 || !ok2 {
		t.Fatalf("anchor %d..%d does not fall on characters of %q", it.Start, it.End, body)
	}
	return body[a:b]
}

func TestSplitComposeRoundTrip(t *testing.T) {
	line := Serialize([]Item{item(t, "The quiet house.\n", "word", "quiet", "still")})
	cases := []string{
		"Text.\n\n" + line + "\n",
		line + "\n",
		"Text.\n" + line + "\n",
		"Text.\n\n\n" + line,
		"Text.\n\n" + line + "\n\n\n",
		"no comment here\n",
		"",
		"A\n\n<!-- scriobh:alts not json at all -->\n",
	}
	for _, c := range cases {
		p := Split(c)
		if got := p.Compose(p.Line); got != c {
			t.Errorf("compose(split(%q)) = %q", c, got)
		}
		if p.HasLine && p.Body+p.Sep+p.Line+p.Tail != c {
			t.Errorf("parts of %q do not add up: %#v", c, p)
		}
	}
	p := Split("Text.\n\n" + line + "\n")
	if !p.HasLine || p.Body != "Text.\n" || p.Sep != "\n" || p.Tail != "\n" {
		t.Errorf("parts: %#v", p)
	}
	// Adding a comment to a body without one, and dropping it again.
	q := Split("Plain")
	if got := q.Compose(line); got != "Plain\n\n"+line+"\n" {
		t.Errorf("add: %q", got)
	}
	if got := Split(Split("Plain").Compose(line)).Compose(""); got != "Plain\n" {
		t.Errorf("drop: %q", got)
	}
	// Only the last line counts, and a comment in the middle is just text.
	mid := line + "\n\nmore text\n"
	if Split(mid).HasLine {
		t.Error("a comment that is not last must not be read as the annotation")
	}
}

func TestSerializeParseRoundTrip(t *testing.T) {
	body := "The quiet house was empty. A second --> sentence, with <b>html</b> & \"quotes\" and 😀 emoji.\n"
	a := item(t, body, "word", "quiet", "still", "low --> <!-- x -->")
	b := item(t, body, "sentence", "A second --> sentence, with <b>html</b> & \"quotes\" and 😀 emoji.", "Other")
	b.Variants[1].Source = SourceGenerated
	b.Variants[1].At = "2026-02-02T00:00:00.000Z"
	line := Serialize([]Item{b, a})
	if strings.Count(line, "-->") != 1 || strings.Contains(line, "<!-- x") || strings.Contains(line[len(Marker):], "<") {
		t.Fatalf("the JSON must not be able to close or open a comment: %s", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatal("the comment is one line")
	}
	got, err := Parse(line, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("items in document order: %+v", got)
	}
	if got[0].Variants[2].Text != "low --> <!-- x -->" || got[1].Variants[1].Source != SourceGenerated || got[1].Variants[1].At != "2026-02-02T00:00:00.000Z" {
		t.Errorf("variants lost something: %+v", got)
	}
	// Whatever Parse reads, Serialize writes back byte for byte once anchors are put back.
	again := got
	for i := range again {
		again[i].Start, again[i].End = 0, 0
	}
	if Serialize(again) != line {
		t.Errorf("not stable:\n%s\n%s", line, Serialize(again))
	}
	if !Same(got, []Item{a, b}) {
		t.Error("Same must hold for the same items")
	}
}

func TestParseShortFormAndErrors(t *testing.T) {
	short := Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still","low","hushed"],"chosen":"quiet"}]}` + " -->"
	items, err := Parse(short, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	it := items[0]
	if len(it.Variants) != 4 || !it.Variants[0].Original || it.Variants[0].Text != "quiet" || it.Placed() {
		t.Fatalf("short form: %+v", it)
	}
	// No scope, as in the first sketch: inferred.
	noScope := Marker + `{"version":1,"items":[{"quote":"a_b_c *x* [y] # z > w","variants":["1.","- 2"],"chosen":"\\"}]}` + " -->"
	items, err = Parse(noScope, Limits{})
	if err != nil || items[0].Scope != ScopeSentence {
		t.Fatalf("no scope: %v %+v", err, items)
	}
	for name, line := range map[string]string{
		"not json":    Marker + `{oops` + " -->",
		"trailing":    Marker + `{"version":1,"items":[]} {}` + " -->",
		"version":     Marker + `{"version":2,"items":[]}` + " -->",
		"no quote":    Marker + `{"version":1,"items":[{"variants":["a"]}]}` + " -->",
		"empty":       Marker + `` + " -->",
		"too many":    Marker + `{"version":1,"items":[{"quote":"a","variants":["b"]},{"quote":"c","variants":["d"]}]}` + " -->",
		"not comment": "hello",
	} {
		lim := Limits{}
		if name == "too many" {
			lim.PerDoc = 1
		}
		if _, err := Parse(line, lim); err == nil {
			t.Errorf("%s: want an error", name)
		} else if _, ok := err.(*ParseError); !ok {
			t.Errorf("%s: %T", name, err)
		}
	}
}

func TestIndexUTF16(t *testing.T) {
	s := "a😀béc" // 😀 is two UTF-16 units and four bytes
	ix := NewIndex(s)
	if ix.Units() != 6 || Len16(s) != 6 {
		t.Fatalf("units %d %d", ix.Units(), Len16(s))
	}
	for u, want := range map[int]int{0: 0, 1: 1, 3: 5, 4: 6, 5: 8, 6: 9} {
		b, ok := ix.Byte(u)
		if !ok || b != want {
			t.Errorf("Byte(%d) = %d,%v want %d", u, b, ok, want)
		}
		if got := ix.Unit(want); got != u {
			t.Errorf("Unit(%d) = %d want %d", want, got, u)
		}
	}
	if _, ok := ix.Byte(2); ok {
		t.Error("inside a surrogate pair is not a place")
	}
	if _, ok := ix.Byte(7); ok {
		t.Error("past the end")
	}
	ascii := NewIndex("hello")
	if b, ok := ascii.Byte(5); !ok || b != 5 || ascii.Unit(3) != 3 {
		t.Error("ascii")
	}
}

// ---- rebase ----------------------------------------------------------------------

func rebase(t *testing.T, old, nw string, items ...Item) ([]Item, Report) {
	t.Helper()
	out, rep := Rebase(old, nw, items, nil, opts())
	for _, it := range out {
		if it.Orphan {
			continue
		}
		if got := bodyAt(t, nw, it); got != it.ChosenText() {
			t.Errorf("anchor holds %q, chosen is %q (body %q)", got, it.ChosenText(), nw)
		}
	}
	return out, rep
}

func TestRebaseEditsAroundAnAnchor(t *testing.T) {
	old := "First paragraph here.\n\nThe quiet house was empty, and the quiet stayed.\n\nLast one.\n"
	for _, tc := range []struct {
		name, nw string
		moved    int
	}{
		{"before", "Introduction added.\n\n" + old, 0},
		{"after", old + "Epilogue.\n", 0},
		{"far before, edit", strings.Replace(old, "First paragraph", "Opening paragraph", 1), 0},
		{"right before", strings.Replace(old, "The quiet", "The very quiet", 1), 0},
		{"right after", strings.Replace(old, "quiet house", "quiet, old house", 1), 0},
		{"typing after the word, a space", strings.Replace(old, "quiet house", "quiet  house", 1), 0},
		{"inside the context", strings.Replace(old, "The quiet house was", "The quiet old house was", 1), 0},
		{"two edits, one window", strings.Replace(strings.Replace(old, "First paragraph", "Opening", 1), "empty, and", "empty and also", 1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := item(t, old, "word", "quiet", "still")
			out, rep := rebase(t, old, tc.nw, it)
			if out[0].Orphan || rep.Orphaned != 0 || rep.Adopted != 0 {
				t.Fatalf("the anchor must survive: %+v %+v", out[0], rep)
			}
			if rep.Moved != tc.moved {
				t.Errorf("moved = %d, want %d", rep.Moved, tc.moved)
			}
		})
	}
}

func TestRebaseEditInsideAnAnchorAdoptsTheWording(t *testing.T) {
	old := "She said the house was quiet and cold. Then she left.\n"
	it := item(t, old, "sentence", "She said the house was quiet and cold.", "Quiet and cold, the house waited.")
	nw := strings.Replace(old, "quiet and cold", "very quiet and cold", 1)
	out, rep := rebase(t, old, nw, it)
	if rep.Adopted != 1 || out[0].Orphan {
		t.Fatalf("an edit inside the anchor adopts the new wording: %+v %+v", out[0], rep)
	}
	// The original is immutable, so the edited text is a new variant that is chosen.
	if len(out[0].Variants) != 3 || out[0].ChosenText() != "She said the house was very quiet and cold." {
		t.Fatalf("variants: %+v", out[0].Variants)
	}
	orig, _ := out[0].Original()
	if orig.Text != "She said the house was quiet and cold." {
		t.Error("the original must stay as it was")
	}
	// Editing again while a non-original is chosen edits that variant in place.
	nw2 := strings.Replace(nw, "very quiet", "very, very quiet", 1)
	out2, _ := rebase(t, nw, nw2, out[0])
	if len(out2[0].Variants) != 3 || out2[0].ChosenText() != "She said the house was very, very quiet and cold." {
		t.Fatalf("second edit: %+v", out2[0].Variants)
	}
	// An edit that makes the text equal to another variant chooses that variant.
	nw3 := strings.Replace(nw2, "She said the house was very, very quiet and cold.", "Quiet and cold, the house waited.", 1)
	out3, rep3 := rebase(t, nw2, nw3, out2[0])
	if rep3.Orphaned != 0 || out3[0].ChosenText() != "Quiet and cold, the house waited." || len(out3[0].Variants) != 3 {
		t.Fatalf("equal to a variant: %+v %+v", out3[0], rep3)
	}
}

func TestRebaseOrphansWhenTheTextIsGoneAndReattaches(t *testing.T) {
	old := "The quiet house was empty.\n"
	it := item(t, old, "word", "quiet", "still")
	// The word is deleted: nothing to attach to.
	gone := "The house was empty.\n"
	out, rep := rebase(t, old, gone, it)
	if !out[0].Orphan || rep.Orphaned != 1 || len(out[0].Variants) != 2 {
		t.Fatalf("deleting the word orphans it and keeps its variants: %+v %+v", out[0], rep)
	}
	// An edit that replaces the whole range and more is also no guess.
	big := "A different opening.\n"
	out2, _ := rebase(t, old, big, item(t, old, "word", "quiet", "still"))
	if !out2[0].Orphan {
		t.Fatal("must orphan")
	}
	// Undo: the exact text and context come back and the orphan is reattached.
	back, rep2 := rebase(t, gone, old, out[0])
	if back[0].Orphan || rep2.Reattached != 1 || back[0].Chosen != it.Chosen {
		t.Fatalf("reattach: %+v %+v", back[0], rep2)
	}
	// Somewhere else, with other context, it stays orphaned: no guessing.
	elsewhere := "The house. Elsewhere, a quiet room, and then more.\n"
	out3, _ := rebase(t, gone, elsewhere, out[0])
	if !out3[0].Orphan {
		t.Fatal("a quote found with other context must not reattach")
	}
}

func TestRebaseMovedText(t *testing.T) {
	old := "Alpha beta gamma.\n\nThe quiet house was empty.\n\nOmega delta.\n"
	it := item(t, old, "sentence", "The quiet house was empty.", "The still house was bare.")
	// The paragraph is cut and pasted elsewhere (a window that swallows the anchor's place).
	moved := "Alpha beta gamma.\n\nOmega delta.\n\nThe quiet house was empty.\n"
	out, rep := rebase(t, old, moved, it)
	if out[0].Orphan || rep.Moved != 1 {
		t.Fatalf("moved text is found by its quote and context? %+v %+v", out[0], rep)
	}
}

func TestRebaseRepeatedWordsStayPut(t *testing.T) {
	old := "the cat and the dog and the bird and the fish.\n"
	ix := strings.Index(old, "the dog")
	_ = ix
	it := item(t, old, "word", "dog", "hound")
	it2 := item(t, old, "word", "fish", "carp")
	nw := "the cat and the big dog and the bird and the large fish.\n"
	out, _ := rebase(t, old, nw, it, it2)
	for _, o := range out {
		if o.Orphan {
			t.Fatalf("both words survive: %+v", o)
		}
	}
	// Two identical words, one anchored: the anchor must not jump to the other.
	old2 := "go go go.\n"
	a := strings.Index(old2, "go")
	_ = a
	third := Item{ID: "alt_x", Scope: "word", Start: 6, End: 8, Variants: []Variant{{ID: "alv_x", Text: "go", Original: true, Source: SourceManual}}, Chosen: "alv_x"}
	third.Before, third.After = contextBefore(old2, 6), contextAfter(old2, 8)
	nw2 := "now go go go.\n"
	out2, _ := rebase(t, old2, nw2, third)
	if out2[0].Orphan || out2[0].Start != 10 {
		t.Fatalf("an anchor on the third go must stay on the third go: %+v", out2[0])
	}
}

func TestRebaseUnicodeAndEmoji(t *testing.T) {
	old := "Café 😀 naïve quiet 😀 tale.\n"
	it := item(t, old, "word", "quiet", "still")
	nw := "Added ☕ here. " + old
	out, _ := rebase(t, old, nw, it)
	if out[0].Orphan {
		t.Fatal("orphaned")
	}
	if out[0].Start != it.Start+len("Added ☕ here. ")-len("Added ☕ here. ")+Len16("Added ☕ here. ") {
		t.Errorf("offsets are UTF-16 units: %d", out[0].Start)
	}
}

func TestRebaseNewItemsFromAComment(t *testing.T) {
	body := "The quiet house was quiet again, and quiet.\n"
	line := Marker + `{"version":1,"items":[` +
		`{"scope":"word","quote":"quiet","variants":["still"]},` +
		`{"scope":"word","quote":"quiet","variants":["calm"]},` +
		`{"scope":"word","quote":"missing","variants":["x"]}]}` + " -->"
	items, err := Parse(line, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Freshen(items, MergeOptions{Actor: "usr_a", Now: "t", NewID: newID}, false); err != nil {
		t.Fatal(err)
	}
	out, rep := Rebase("", body, items, nil, opts())
	// The same quote twice is read in order: first and second occurrence.
	if rep.Placed != 2 || rep.Orphaned != 1 {
		t.Fatalf("report %+v", rep)
	}
	if out[0].Start != 4 || out[1].Start != 20 || !out[2].Orphan {
		t.Fatalf("sequential placement: %+v", out)
	}
}

func TestRebaseNeverLeavesOverlappingAnchors(t *testing.T) {
	old := "alpha beta gamma delta.\n"
	a := item(t, old, "word", "beta", "b")
	b := item(t, old, "word", "gamma", "g")
	// Adjacent anchors are fine.
	out, _ := Rebase(old, "alpha betagamma delta.\n", []Item{a, b}, nil, opts())
	if out[0].Orphan || out[1].Orphan {
		t.Fatalf("adjacent anchors may stand: %+v", out)
	}
	// Two items claiming one range cannot both stand; the second is orphaned, not merged.
	c := a.Clone()
	c.ID = "alt_dup"
	c.Variants[0].ID, c.Variants[1].ID, c.Chosen = "v1", "v2", "v1"
	out, rep := Rebase(old, old, []Item{a, c}, nil, opts())
	if out[0].Orphan || !out[1].Orphan || rep.Orphaned != 1 {
		t.Fatalf("overlap: %+v %+v", out, rep)
	}
}

// Random edits to a body must never leave an active anchor over text that is
// not its chosen wording, and must never drop an item.
func TestRebaseProperty(t *testing.T) {
	words := strings.Fields("the quiet house was empty and cold then she left a bird sang over river stones dark light 😀 café naïve")
	rng := rand.New(rand.NewSource(7))
	mkBody := func() string {
		var b strings.Builder
		for p := 0; p < 12; p++ {
			for w := 0; w < 25; w++ {
				if w > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(words[rng.Intn(len(words))])
			}
			b.WriteString(".\n\n")
		}
		return b.String()
	}
	iterations := 400
	if testing.Short() {
		iterations = 80
	}
	for n := 0; n < iterations; n++ {
		body := mkBody()
		var items []Item
		for k := 0; k < 6; k++ {
			ws := strings.Fields(body)
			w := ws[rng.Intn(len(ws))]
			w = strings.Trim(w, ".")
			if w == "" || strings.Contains(w, "\n") {
				continue
			}
			at := strings.Index(body, w)
			if at < 0 {
				continue
			}
			ok := true
			for _, o := range items {
				ix := NewIndex(body)
				a, _ := ix.Byte(o.Start)
				b, _ := ix.Byte(o.End)
				if at < b && at+len(w) > a {
					ok = false
				}
			}
			if !ok {
				continue
			}
			items = append(items, item(t, body, "word", w, "alt"+fmt.Sprint(k)))
		}
		cur := body
		for step := 0; step < 6; step++ {
			rs := []rune(cur)
			a := rng.Intn(len(rs))
			b := a + rng.Intn(min(12, len(rs)-a)+1)
			ins := []string{"", " ", "x", "new words here", "😀", "\n\n", "quiet "}[rng.Intn(7)]
			next := string(rs[:a]) + ins + string(rs[b:])
			var rep Report
			items, rep = Rebase(cur, next, items, nil, opts())
			_ = rep
			for _, it := range items {
				if it.Orphan {
					continue
				}
				if got := bodyAt(t, next, it); got != it.ChosenText() {
					t.Fatalf("iteration %d step %d: anchor holds %q, chosen %q\nold %q\nnew %q", n, step, got, it.ChosenText(), cur, next)
				}
			}
			cur = next
		}
		if len(items) == 0 {
			continue
		}
		for _, it := range items {
			if len(it.Variants) < 2 {
				t.Fatalf("variants lost: %+v", it)
			}
		}
	}
}

// ---- scope ----------------------------------------------------------------------------

func TestCheckRange(t *testing.T) {
	body := "# A title\n\nThe quiet house, naïve and 😀 bright. Second sentence here.\ncontinued line.\n\n> quoted line one\n> quoted line two\n\n```\ncode quiet\n```\n\nCJK 静かな家です。\n"
	blocks, ok := text.Blocks(body)
	if !ok {
		t.Fatal("blocks")
	}
	rng := func(s string) (int, int) {
		a := strings.Index(body, s)
		if a < 0 {
			t.Fatalf("%q", s)
		}
		return a, a + len(s)
	}
	check := func(scope, s string) error {
		a, b := rng(s)
		return CheckRange(body, a, b, scope, blocks, Limits{})
	}
	good := []struct{ scope, s string }{
		{"word", "quiet"}, {"word", "naïve"}, {"word", "😀"}, {"word", "house,"},
		{"sentence", "The quiet house, naïve and 😀 bright."}, {"sentence", "Second sentence here.\ncontinued line."},
		{"word", "A"}, {"paragraph", "A title"}, {"word", "静かな家です。"}, {"word", "静"},
		{"paragraph", "CJK 静かな家です。"},
	}
	for _, g := range good {
		if err := check(g.scope, g.s); err != nil {
			t.Errorf("%s %q: %v", g.scope, g.s, err)
		}
	}
	bad := []struct{ scope, s string }{
		{"word", "uiet"},                               // inside a word
		{"word", "quiet house"},                        // a space in a word
		{"sentence", " The quiet"},                     // leading space
		{"sentence", "bright. Second"},                 // not at word edges? it is: fine below
		{"word", "quiet\n\n> quoted"},                  // across blocks
		{"sentence", "quoted line one\n> quoted line"}, // across container lines
		{"word", "code quiet"},                         // in code
		{"paragraph", "The quiet house"},               // not the whole block
		{"word", "<scriobh-ghost>"},                    // a marker
	}
	for _, g := range bad {
		if g.s == "bright. Second" {
			continue
		}
		a := strings.Index(body, g.s)
		if a < 0 {
			continue
		}
		if err := check(g.scope, g.s); err == nil {
			t.Errorf("%s %q must be refused", g.scope, g.s)
		}
	}
	// A paragraph that sits in a quote is refused.
	if err := check("paragraph", "quoted line one\n> quoted line two"); err == nil {
		t.Error("a paragraph inside a quote is not a top-level block")
	}
	// Variants.
	for _, v := range []string{"", " x", "x ", "a\n\nb", "has <!-- scriobh:alts", "<scriobh-ghost>x"} {
		if CheckVariantText(v, "sentence", Limits{}) == nil {
			t.Errorf("variant %q must be refused", v)
		}
	}
	if CheckVariantText("one\ntwo", "word", Limits{}) == nil {
		t.Error("a word variant is one line")
	}
	if CheckVariantText("one\ntwo", "sentence", Limits{}) != nil {
		t.Error("a sentence may wrap")
	}
	if CheckVariantText("one\ntwo", "paragraph", Limits{}) != nil {
		t.Error("a paragraph variant may wrap")
	}
	if CheckVariantText("- not a paragraph", "paragraph", Limits{}) == nil || CheckVariantText("# heading", "paragraph", Limits{}) == nil {
		t.Error("a paragraph variant cannot be a list or heading")
	}
}

func TestResolveQuote(t *testing.T) {
	body := "the cat saw the dog. the cat left.\n"
	a, b, err := ResolveQuote(body, "dog", "", "", nil)
	if err != nil || body[a:b] != "dog" {
		t.Fatal(err)
	}
	if _, _, err := ResolveQuote(body, "the cat", "", "", nil); err == nil {
		t.Error("ambiguous quote needs context")
	}
	a, _, err = ResolveQuote(body, "the cat", "dog. ", " left", nil)
	if err != nil || a != strings.LastIndex(body, "the cat") {
		t.Fatalf("context picks the second: %d %v", a, err)
	}
	two := 1
	a, _, err = ResolveQuote(body, "the cat", "", "", &two)
	if err != nil || a != strings.LastIndex(body, "the cat") {
		t.Fatalf("occurrence: %d %v", a, err)
	}
	if _, _, err := ResolveQuote(body, "absent", "", "", nil); err == nil {
		t.Error("missing quote")
	}
}

// A text made to repeat one quote must not make a save do quadratic work, and
// must not attach an anchor to a guess.
func TestRebaseOnARepetitiveText(t *testing.T) {
	old := strings.Repeat("a ", 150000) + "needle " + strings.Repeat("a ", 10)
	it := item(t, old, "word", "needle", "pin")
	items := make([]Item, 0, 200)
	for i := 0; i < 200; i++ {
		c := it.Clone()
		c.ID = fmt.Sprintf("alt_%d", i)
		c.Variants[0].ID, c.Variants[1].ID, c.Chosen = fmt.Sprintf("v%d", 2*i), fmt.Sprintf("v%d", 2*i+1), fmt.Sprintf("v%d", 2*i)
		c.Start, c.End = -1, -1 // unplaced: each looks for "needle" with the same context
		items = append(items, c)
	}
	start := time.Now()
	_, _ = Rebase("", old, items, nil, opts())
	searches := time.Since(start)
	nw := "b " + old
	start = time.Now()
	out, _ := Rebase(old, nw, []Item{it}, nil, opts())
	save := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if searches > 5*time.Second {
			t.Fatalf("200 searches in a repetitive text took %v", searches)
		}
		if save > time.Second {
			t.Fatalf("a save took %v", save)
		}
	})
	if out[0].Orphan {
		t.Fatal("the unique word is still found")
	}
}
