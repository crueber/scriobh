package text

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

// The reading view lets a person select text in the rendered page and sends it as the quote of a comment
// (plain: true), which the server looks for in PlainBlocks. So the text of each prose block of the rendered page,
// as a browser shows it (tags dropped, white space collapsed), must be one of the blocks PlainBlocks lists.
var reParityTag = regexp.MustCompile(`(?s)<[^>]*>`)

// blockTexts is the text of each block element of the rendered HTML that holds prose: paragraphs, headings and
// list items that hold text directly.
func blockTexts(h string) []string {
	var out []string
	re := regexp.MustCompile(`(?s)<(p|h[1-6]|li|td|th)[ >].*?</(p|h[1-6]|li|td|th)>`)
	for _, m := range re.FindAllString(h, -1) {
		// a list item that holds a nested list is cut at the nested list
		if i := strings.Index(m, "<ul"); i > 0 && strings.HasPrefix(m, "<li") {
			m = m[:i]
		}
		if i := strings.Index(m, "<ol"); i > 0 && strings.HasPrefix(m, "<li") {
			m = m[:i]
		}
		s := html.UnescapeString(reParityTag.ReplaceAllString(m, ""))
		s = strings.Join(strings.Fields(s), " ")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

const parityFixture = "---\ntitle: Front matter is not text\n---\n" +
	"# A chapter heading\n\n" +
	"The *quiet* house, **bold words** and `code span` stood [[Hill|alone]] and a [link here](https://example.com/x) too.\n\n" +
	"A [[Plain Wiki]] link and a footnote[^1].\n\n" +
	"Second %%PRIVATENOTE hidden%% paragraph with a <scriobh-ghost>ghosted words</scriobh-ghost> tail. <!-- an html comment -->\n\n" +
	"> A quoted *line* of text.\n\n" +
	"- first item\n- second *item*\n  - nested item\n\n" +
	"1. numbered one\n2. numbered two\n\n" +
	"Line one  \nline two after a hard break, and a\nsoft break.\n\n" +
	"[^1]: The footnote body.\n"

func TestRenderedBlocksAreVisibleBlocks(t *testing.T) {
	rendered := RenderHTML(StripGhosts(parityFixture, true), HTMLOptions{HeadingShift: 2, LocalFiles: true})
	pb, ok := PlainBlocks(parityFixture)
	if !ok {
		t.Fatal("PlainBlocks could not place the fixture")
	}
	visible := map[string]bool{}
	for _, b := range pb {
		visible[b.Text] = true
	}
	for _, bt := range blockTexts(rendered) {
		if strings.Contains(bt, "The footnote body") {
			continue // a footnote body is deliberately never offered
		}
		bt = strings.ReplaceAll(bt, "footnote1.", "footnote.") // the reference's number: the page strips it from a selection
		if !visible[bt] {
			var all []string
			for _, b := range pb {
				all = append(all, b.Text)
			}
			t.Errorf("rendered block %q is not a visible block of PlainBlocks:\n%s", bt, strings.Join(all, "\n"))
		}
	}
	for _, b := range pb {
		for _, hidden := range []string{"PRIVATENOTE", "ghosted words", "an html comment", "Front matter"} {
			if strings.Contains(b.Text, hidden) {
				t.Errorf("a visible block holds %q: %q", hidden, b.Text)
			}
		}
	}
	for _, hidden := range []string{"PRIVATENOTE", "ghosted words", "an html comment", "Front matter"} {
		if strings.Contains(rendered, hidden) {
			t.Errorf("the rendered page shows %q", hidden)
		}
	}
	t.Logf("rendered blocks: %q", blockTexts(rendered))
}

// What a selection cannot place, so the page says so rather than guessing: a table, a struck-through run (the
// source keeps the marks), an image's note. Pinned so a change to either side is noticed.
func TestWhatTheReadingViewCannotSelect(t *testing.T) {
	md := "A ~~struck~~ word.\n\n| a | b |\n|---|---|\n| cell one | cell two |\n\nAn image ![alt words](pic.png) here.\n"
	pb, _ := PlainBlocks(md)
	var all []string
	for _, b := range pb {
		all = append(all, b.Text)
	}
	joined := strings.Join(all, "|")
	if strings.Contains(joined, "cell one") {
		t.Errorf("a table cell became selectable: %s", joined)
	}
	if !strings.Contains(joined, "~~struck~~") {
		t.Errorf("struck text is spelt differently now: %s", joined)
	}
	if strings.Contains(joined, "alt words") {
		t.Errorf("an image's alt text is selectable: %s", joined)
	}
}
