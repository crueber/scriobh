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

// ---- re-attachment is never a guess (review item 4) -------------------------------

func TestADeletedSentenceDoesNotReattachToALikePassage(t *testing.T) {
	old := "He said the quiet word. She said the quiet word.\n"
	it := item(t, old, "word", "quiet", "still")
	nw := "She said the quiet word.\n"
	out, rep := rebase(t, old, nw, it)
	if !out[0].Orphan || rep.Orphaned != 1 {
		t.Fatalf("deleting the sentence the anchor was in orphans it; it must not move to the other quiet: %+v", out[0])
	}
	// The same when the second sentence is a longer, rewritten one with the same words.
	nw = "Then she said the quiet word again.\n"
	out, _ = rebase(t, old, nw, it)
	if !out[0].Orphan {
		t.Fatalf("a few words of shared context are not enough: %+v", out[0])
	}
}

// ---- typing on at the edge of an anchored word (item 7) ----------------------------

func TestTypingAtTheEdgeOfAnAnchoredWordBelongsToTheWord(t *testing.T) {
	old := "The quiet house was empty.\n"
	for _, tc := range []struct{ name, nw, want string }{
		{"end", "The quiets house was empty.\n", "quiets"},
		{"end, more letters", "The quietly house was empty.\n", "quietly"},
		{"start", "The equiet house was empty.\n", "equiet"},
		{"start, more letters", "The unquiet house was empty.\n", "unquiet"},
		{"end, then punctuation", "The quiets, house was empty.\n", "quiets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := item(t, old, "word", "quiet", "still")
			out, rep := rebase(t, old, tc.nw, it)
			if out[0].Orphan || rep.Adopted != 1 || out[0].ChosenText() != tc.want {
				t.Fatalf("want the anchor to be %q: %+v %+v", tc.want, out[0], rep)
			}
			// The original is untouched, and the typed word is a new wording; choosing
			// still gives whole words, never "stills".
			if o, _ := out[0].Original(); o.Text != "quiet" || len(out[0].Variants) != 3 {
				t.Fatalf("variants: %+v", out[0].Variants)
			}
		})
	}
	// Typing that does not continue the word stays outside it.
	for name, nw := range map[string]string{
		"a space then a word":   "The quiet old house was empty.\n",
		"a comma":               "The quiet, house was empty.\n",
		"before, after a space": "The old quiet house was empty.\n",
	} {
		it := item(t, old, "word", "quiet", "still")
		out, rep := rebase(t, old, nw, it)
		if out[0].Orphan || rep.Adopted != 0 || out[0].ChosenText() != "quiet" {
			t.Errorf("%s: %+v %+v", name, out[0], rep)
		}
	}
}

// ---- a comment-placed item is checked like a new one (item 5) -----------------------

func TestPlacementFromACommentIsChecked(t *testing.T) {
	body := "Another thing, then code.\n\n```\ncode here\n```\n\nSecond paragraph, with the word.\n"
	line := Marker + `{"version":1,"items":[` +
		`{"quote":"the","variants":["a"]},` + // would land inside "Another"
		`{"scope":"word","quote":"code","variants":["text"]},` + // first hit is in prose, "code." has a full stop: see below
		`{"scope":"word","quote":"here","variants":["there"]}]} -->`
	items, err := Parse(line, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Freshen(items, MergeOptions{NewID: newID, Now: "t"}, false); err != nil {
		t.Fatal(err)
	}
	out, rep := Rebase("", body, items, nil, opts())
	if !out[0].Orphan {
		t.Errorf("a word cut out of Another must not be placed: %+v", out[0])
	}
	if !out[2].Orphan { // "here" is only in the fenced block
		t.Errorf("text in a code block must not be placed: %+v", out[2])
	}
	_ = rep
	// Every placement leaves the text at its anchor, in prose.
	for _, it := range out {
		if !it.Orphan {
			if got := bodyAt(t, body, it); got != it.ChosenText() || strings.Contains(got, "\n") {
				t.Errorf("placed on %q", got)
			}
		}
	}
}

// ---- ranges that touch ghost tags, and emoji sequences (items 6 and 14) --------------

func TestRangesNeverTouchAMarkerOrCutACharacter(t *testing.T) {
	body := "One <scriobh-ghost>two three</scriobh-ghost> four. A 👨‍👩‍👧 family, a 👍🏽 thumb, ❤️ heart, 1️⃣ key, 🇮🇪🇫🇷 flags, café.\n"
	blocks, ok := text.Blocks(body)
	if !ok {
		t.Fatal("blocks")
	}
	var tags [][2]int
	for _, m := range []string{text.GhostOpen, text.GhostClose} {
		for from := 0; ; {
			i := strings.Index(body[from:], m)
			if i < 0 {
				break
			}
			tags = append(tags, [2]int{from + i, from + i + len(m)})
			from += i + 1
		}
	}
	rs := []rune(body)
	offsets := make([]int, 0, len(rs)+1)
	for i := range body {
		offsets = append(offsets, i)
	}
	offsets = append(offsets, len(body))
	rng := rand.New(rand.NewSource(3))
	accepted := 0
	for n := 0; n < 20000; n++ {
		a := offsets[rng.Intn(len(offsets))]
		b := offsets[rng.Intn(len(offsets))]
		if a >= b {
			continue
		}
		for _, scope := range []string{"word", "sentence"} {
			if CheckRange(body, a, b, scope, blocks, Limits{}) != nil {
				continue
			}
			accepted++
			for _, tg := range tags {
				if a < tg[1] && b > tg[0] {
					t.Fatalf("%s range %q intersects a ghost tag", scope, body[a:b])
				}
			}
			if TouchesMarker(body, a, b) {
				t.Fatalf("accepted a range that touches a marker: %q", body[a:b])
			}
			for _, at := range []int{a, b} {
				if CutsGrapheme(body, at) {
					t.Fatalf("%s range %q cuts a character at %d", scope, body[a:b], at)
				}
			}
		}
	}
	if accepted < 100 {
		t.Fatalf("only %d ranges were accepted: the fuzz is not exercising anything", accepted)
	}
	// The cases by hand.
	at := func(s string) int { return strings.Index(body, s) }
	for name, r := range map[string][2]int{
		"half a closer":        {at("ghost>two"), at("two") + 3},
		"half an opener":       {at("two"), at("two three</scriobh") + len("two three</scriobh")},
		"into the tag, quote":  {at("ghost>two three</scriobh"), at("ghost>two three</scriobh") + len("ghost>two three</scriobh")},
		"ZWJ family, a member": {at("👨"), at("👨") + len("👨")},
		"thumb without tone":   {at("👍"), at("👍") + len("👍")},
		"heart without form":   {at("❤"), at("❤") + len("❤")},
		"keycap, the digit":    {at("1️⃣"), at("1️⃣") + 1},
		"one flag half":        {at("🇮🇪"), at("🇮🇪") + len("🇮")},
		"flag across pairs":    {at("🇮🇪") + len("🇮"), at("🇫🇷") + len("🇫")},
	} {
		if err := CheckRange(body, r[0], r[1], "sentence", blocks, Limits{}); err == nil {
			t.Errorf("%s: %q must be refused", name, body[r[0]:r[1]])
		}
	}
	for name, s := range map[string]string{"family": "👨‍👩‍👧", "thumb": "👍🏽", "heart": "❤️", "keycap": "1️⃣", "flags": "🇮🇪🇫🇷", "one flag": "🇮🇪", "accent": "café"} {
		a := at(s)
		if err := CheckRange(body, a, a+len(s), "word", blocks, Limits{}); err != nil {
			t.Errorf("%s: whole is fine: %v", name, err)
		}
	}
}

// ---- the search is bounded (item 2) -----------------------------------------------------

func TestOrphansDoNotMakeASaveSlow(t *testing.T) {
	testutil.Timing(t)
	var b strings.Builder
	filler := strings.Fields("the quiet house was empty and cold then she left a bird sang over river stones dark light")
	rng := rand.New(rand.NewSource(5))
	for b.Len() < 1_500_000 {
		for w := 0; w < 40; w++ {
			b.WriteString(filler[rng.Intn(len(filler))])
			b.WriteByte(' ')
		}
		b.WriteString("\n\n")
	}
	old := b.String()
	items := make([]Item, 200)
	for i := range items {
		items[i] = Item{ID: fmt.Sprintf("alt_%d", i), Scope: ScopeWord, Start: 0, End: 0, Orphan: true, Chosen: fmt.Sprintf("v%da", i),
			Before: "she left a ", After: " sang over river",
			Variants: []Variant{{ID: fmt.Sprintf("v%da", i), Text: "bird", Original: true, Source: SourceManual}, {ID: fmt.Sprintf("v%db", i), Text: "fowl", Source: SourceManual},
				{ID: fmt.Sprintf("v%dc", i), Text: "swallow of the dark river", Source: SourceManual}}}
	}
	for name, nw := range map[string]string{
		"typing at the top":    "x" + old,
		"typing in the middle": old[:len(old)/2] + "x" + old[len(old)/2:],
		"a rewrite":            strings.ReplaceAll(old, "empty", "hollow"),
	} {
		start := time.Now()
		_, rep := Rebase(old, nw, items, nil, opts())
		d := time.Since(start)
		t.Logf("%s: %v (exhausted: %v)", name, d, rep.Exhausted)
		if d > 250*time.Millisecond*timingFactor {
			t.Errorf("%s: %v with 200 orphans in 1.5 MB", name, d)
		}
	}
}

// ---- OmitGhosts, whatever the comment is (item 1) -----------------------------------------

func TestOmitGhostsLeavesNoGhostedTextWhateverTheComment(t *testing.T) {
	body := "The quiet house. <scriobh-ghost>A ghosted sentence here.</scriobh-ghost> Next words.\n"
	good := Serialize([]Item{item(t, strings.Replace(strings.ReplaceAll(body, text.GhostOpen, ""), text.GhostClose, "", 1), "word", "quiet", "still")})
	var many []string
	for i := 0; i < 300; i++ {
		many = append(many, fmt.Sprintf(`{"id":"alt_%d","scope":"word","chosen":"v%da","variants":[{"id":"v%da","text":"quiet","original":true},{"id":"v%db","text":"x%d"}]}`, i, i, i, i, i))
	}
	for name, line := range map[string]string{
		"valid":       good,
		"unparseable": Marker + `{nope} -->`,
		"short form":  Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still"]}]} -->`,
		"too many":    Marker + `{"version":1,"items":[` + strings.Join(many, ",") + `]} -->`,
		"bad version": Marker + `{"version":9,"items":[]} -->`,
	} {
		out := OmitGhosts(body + "\n" + line + "\n")
		if strings.Contains(out, "ghosted sentence") || strings.Contains(out, "scriobh-ghost") {
			t.Errorf("%s: ghosted text leaked: %q", name, out)
		}
		if !strings.Contains(out, "The quiet house.") || !strings.Contains(out, "Next words.") {
			t.Errorf("%s: the rest must stay: %q", name, out)
		}
		if name == "short form" || name == "valid" || name == "too many" {
			if items, err := Parse(Split(out).Line, bigLimits); err != nil || len(items) == 0 || items[0].ID == "" {
				t.Errorf("%s: the comment is mended and readable: %v %q", name, err, Split(out).Line)
			}
		}
	}
	// Without a comment, and with CRLF.
	if out := OmitGhosts("a <scriobh-ghost>b</scriobh-ghost> c\r\n"); out != "a  c\n" {
		t.Errorf("no comment: %q", out)
	}
}

// ---- the comment that stands for the alternatives (item 11) ---------------------------------

func TestCollapse(t *testing.T) {
	line := Serialize([]Item{item(t, "The quiet house.\n", "word", "quiet", "still")})
	other := Serialize([]Item{item(t, "A quiet room.\n", "word", "quiet", "calm")})
	// The normal case is left alone.
	if _, _, ok := Collapse("The quiet house.\n\n"+line+"\n", Limits{}); ok {
		t.Error("one comment, last: nothing to collapse")
	}
	// Text after the comment.
	body, l, ok := Collapse("The quiet house.\n\n"+line+"\nAppended text.\n", Limits{})
	if !ok || l != line || body != "The quiet house.\n\nAppended text.\n" {
		t.Fatalf("after: %v %q %q", ok, body, l)
	}
	// Two comments: the last one stands, the earlier one goes.
	body, l, ok = Collapse("A.\n\n"+line+"\n\nB.\n\n"+other+"\n", Limits{})
	if !ok || l != other || strings.Contains(body, Marker) {
		t.Fatalf("two: %v %q %q", ok, body, l)
	}
	// An invalid line is ordinary text; a comment shown in a code block is code.
	if _, _, ok := Collapse("A.\n\n"+Marker+`{nope} -->`+"\nB.\n", Limits{}); ok {
		t.Error("an invalid line is not a comment")
	}
	code := "A sample:\n\n```\n" + line + "\n```\n\nAfter.\n"
	if _, _, ok := Collapse(code, Limits{}); ok {
		t.Error("a comment inside a fenced code block is left alone")
	}
}

// ---- a comment is only as good as its sig (item 9) -----------------------------------------------

func TestSigTellsAnOlderCopyFromAnEdit(t *testing.T) {
	items := []Item{item(t, "The quiet house.\n", "word", "quiet", "still")}
	line := Serialize(items)
	got, sig, err := ParseSig(line, Limits{})
	if err != nil || sig == "" || sig != Sig(got) {
		t.Fatalf("an unedited comment agrees with its sig: %v %q %q", err, sig, Sig(got))
	}
	// Anchors, contexts and the chosen wording are not part of it.
	moved := append([]Item(nil), items...)
	moved[0].Start, moved[0].Before, moved[0].Chosen = 99, "zzz", moved[0].Variants[1].ID
	if Sig(moved) != Sig(items) {
		t.Error("anchors and the chosen wording are the server's")
	}
	// A new wording is.
	edited := items[0].Clone()
	edited.Variants = append(edited.Variants, Variant{ID: "alv_new", Text: "calm", Source: SourceManual})
	if Sig([]Item{edited}) == Sig(items) {
		t.Error("a new wording changes the sig")
	}
	// Edited by hand, the sig no longer agrees.
	hand := strings.Replace(line, `"still"`, `"stiller"`, 1)
	h, hs, _ := ParseSig(hand, Limits{})
	if hs == Sig(h) {
		t.Error("a hand edit must be visible")
	}
}
