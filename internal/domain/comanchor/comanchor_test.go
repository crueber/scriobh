package comanchor

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
)

func TestCheckRefusesWhatACommentMayNotTouch(t *testing.T) {
	body := "Plain words here. %%private aside%% More. <!-- an html comment -->\n\n```\ncode\n```\n\nThe <scriobh-ghost>ghosted bit</scriobh-ghost> stays.\n"
	for name, q := range map[string]string{"a note": "private aside", "across a note": "here. %%private", "an html comment": "an html comment", "code": "code", "ghosted text": "ghosted bit", "a marker": "<scriobh-ghost>ghosted"} {
		a := strings.Index(body, q)
		if a < 0 {
			t.Fatalf("%s: the test text lacks %q", name, q)
		}
		if err := Check(body, a, a+len(q)); err == nil {
			t.Errorf("%s: a comment may be anchored there", name)
		}
	}
	a := strings.Index(body, "Plain words")
	if err := Check(body, a, a+len("Plain words")); err != nil {
		t.Errorf("plain words: %v", err)
	}
	if err := Check(body, a, a); err == nil {
		t.Error("an empty range")
	}
	if err := Check(body, a, a+len("Plain words ")); err == nil {
		t.Error("a range that ends in a space")
	}
}

func TestResolveOffsetsNeedTheCurrentRevisionAndQuotesDoNot(t *testing.T) {
	body := "The quiet house. The loud house.\n"
	s, e := 4, 9
	if _, _, err := Resolve(body, Range{Start: &s, End: &e}, 5, 4); err != ErrStale {
		t.Errorf("offsets from an older revision: %v", err)
	}
	if a, b, err := Resolve(body, Range{Start: &s, End: &e}, 5, 5); err != nil || body[a:b] != "quiet" {
		t.Errorf("offsets at the current revision: %q %v", body[a:b], err)
	}
	if a, b, err := Resolve(body, Range{Quote: "quiet"}, 5, 1); err != nil || body[a:b] != "quiet" {
		t.Errorf("a quote from an older revision: %v", err)
	}
	if _, _, err := Resolve(body, Range{Quote: "house"}, 5, 5); err == nil {
		t.Error("an ambiguous quote was guessed")
	}
	if a, b, err := Resolve(body, Range{Quote: "house", Before: "The loud "}, 5, 5); err != nil || a != strings.LastIndex(body, "house") || body[a:b] != "house" {
		t.Errorf("told apart by context: %v", err)
	}
}

func TestAPlainSelectionIsPlacedInTheSourceThroughTheVisibleText(t *testing.T) {
	body := "First paragraph with *emphasis* inside it.\n\nSecond paragraph, plain.\n\nThird paragraph with *emphasis* inside it too.\n"
	a, b, err := Resolve(body, Range{Quote: "Second paragraph, plain", Plain: true}, 1, 1)
	if err != nil || body[a:b] != "Second paragraph, plain" {
		t.Errorf("a plain selection that is in the source: %q %v", body[a:b], err)
	}
	// formatting inside the selection is no obstacle: it is placed exactly, markup and all
	a, b, err = Resolve(body, Range{Quote: "paragraph with emphasis inside", Before: "First ", Plain: true}, 1, 1)
	if err != nil || body[a:b] != "paragraph with *emphasis* inside" {
		t.Errorf("across emphasis: %q %v", body[a:b], err)
	}
	// two paragraphs hold it and the words around it do not say which
	if _, _, err := Resolve(body, Range{Quote: "with emphasis inside it", Plain: true}, 1, 1); err == nil {
		t.Error("an ambiguous selection was guessed")
	}
	if _, _, err := Resolve(body, Range{Quote: "words that are nowhere", Plain: true}, 1, 1); err == nil {
		t.Error("a selection that is not there")
	}
}

// What is not rendered cannot be found, made ambiguous or confirmed by a plain selection: the answer is the same
// with the phrase in the hidden place as with other words there.
func TestAPlainSelectionNeverSeesWhatIsNotRendered(t *testing.T) {
	visible := "Chapter text. The lantern was lit at dusk, see [[Marlowe|the office]] now.\n\nAnother paragraph.\n"
	templates := map[string]string{
		"front matter":                     "---\nsecret: %s\n---\n" + visible,
		"a private note":                   visible + "\nA %%%s%% note.\n",
		"a footnote only a note refers to": visible + "\nPrivate %%x[^9]%% ref.\n\n[^9]: %s\n",
		"an html comment":                  visible + "\n<!-- %s -->\n",
		"a link target":                    visible + "\nA [link](http://example.com/%u) here.\n",
		"an image alt":                     visible + "\nAn ![%s](http://example.com/i.png) here.\n",
	}
	answer := func(body, sel string) string {
		a, b, err := Resolve(body, Range{Quote: sel, Plain: true}, 1, 1)
		if err != nil {
			return "error: " + err.Error()
		}
		return body[a:b]
	}
	for name, tpl := range templates {
		for _, sel := range []string{"lantern was lit", "Marlowe", "office"} {
			fill := func(v string) string {
				return strings.Replace(strings.Replace(tpl, "%s", v, 1), "%u", strings.ReplaceAll(v, " ", "%20"), 1)
			}
			with, without := answer(fill(sel), sel), answer(fill("zzz"), sel)
			if with != without {
				t.Errorf("%s, selecting %q: %q with the phrase hidden there, %q without", name, sel, with, without)
			}
		}
	}
	if got := answer(visible, "Marlowe"); !strings.HasPrefix(got, "error") {
		t.Errorf("a wiki link's target is not shown: %q", got)
	}
	if got := answer(visible, "lantern was lit"); got != "lantern was lit" {
		t.Errorf("the visible phrase: %q", got)
	}
}

// The hinter reads the context only from inside the block, with everything hidden cut out first.
func TestTheHinterNeverReadsHiddenText(t *testing.T) {
	long := strings.Repeat("LEAK secret filler ", 15)
	body := "<!--\n" + long + "\n-->\n\nBefore %%" + long + "%% the target words come <!-- " + long + " --> and after.\n\n" + long + "\n"
	h := NewHinter(body)
	a := strings.Index(body, "the target words")
	p := h.For(a, a+len("the target words"))
	if p.Quote != "the target words" || !strings.Contains(p.Before, "Before") || !strings.Contains(p.After, "come") {
		t.Errorf("hints: %+v", p)
	}
	if strings.Contains(p.Before+p.After+p.Quote, "LEAK") {
		t.Errorf("hidden text in the hints: %+v", p)
	}
}

func TestAfterWriteHelpersAreStable(t *testing.T) {
	if QuoteHash("a") == QuoteHash("b") || len(QuoteHash("a")) != 64 {
		t.Error("QuoteHash")
	}
	if Body("one\r\ntwo\r\n") != "one\ntwo\n" {
		t.Errorf("Body normalises line endings: %q", Body("one\r\ntwo\r\n"))
	}
	withLine := "Text.\n\n<!-- scriobh:alts {\"version\":1,\"items\":[]} -->\n"
	if Body(withLine) != "Text.\n" {
		t.Errorf("Body drops the alternatives line: %q", Body(withLine))
	}
	if alts.Len16("a😀") != 3 {
		t.Error("utf16")
	}
}
