package text

import (
	"strings"
	"testing"
)

func TestCleanMarkdownCutsWhatTheReadingViewLeavesOut(t *testing.T) {
	in := "---\nsecret: FRONT\n---\n" +
		"Visible one. %%NOTE hidden%% Visible two[^1] and[^2]. <!-- INLINECOMMENT -->\n\n" +
		"<!--\nBLOCKCOMMENT\n-->\n\n" +
		"A private %%note\nacross two lines%% done.\n\n" +
		"[^1]: shown footnote\n\n" +
		"Private ref %%hidden[^3]%% only.\n\n" +
		"[^3]: HIDDENFOOTNOTE body\n\n" +
		"[^2]: second shown\n\n" +
		"```\ncode keeps %%these%% and <!-- these --> markers\n```\n\n" +
		"Inline `code %%keeps%% too`.\n"
	out, ok := CleanMarkdown(in)
	if !ok {
		t.Fatal("not placed")
	}
	for _, gone := range []string{"FRONT", "NOTE hidden", "INLINECOMMENT", "BLOCKCOMMENT", "across two lines", "HIDDENFOOTNOTE", "[^3]"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survived:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"Visible one.", "Visible two[^1]", "shown footnote", "second shown", "code keeps %%these%% and <!-- these --> markers", "`code %%keeps%% too`", "A private  done."} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was lost:\n%s", kept, out)
		}
	}
	// A footnote referenced both from a note and from the text stays.
	out, _ = CleanMarkdown("Text[^1] and %%a note[^1]%%.\n\n[^1]: STILLSHOWN\n")
	if !strings.Contains(out, "STILLSHOWN") {
		t.Errorf("a footnote that is also referenced in the open was cut: %q", out)
	}
	// The cut agrees with the reading view: what the renderer shows of the clean text is what it shows of the original.
	for _, md := range []string{in, "Text[^1] and %%a note[^1]%%.\n\n[^1]: S\n", "a %%b%% c %%d\n\ne%% f\n"} {
		clean, _ := CleanMarkdown(md)
		if a, b := strings.ReplaceAll(RenderHTML(md, HTMLOptions{}), " </p>", "</p>"), RenderHTML(clean, HTMLOptions{}); a != b {
			t.Errorf("the reading view of the cleaned text differs:\n%s\n---\n%s", a, b)
		}
	}
}

func TestPlainBlocksAreWhatAReaderSees(t *testing.T) {
	md := "---\nsecret: the lantern was lit\n---\n" +
		"The *lantern was lit* at dusk, see [[Marlowe|the office]] and ![alt TEXTALT](http://x/IMGURL.png) and [a link](http://x/URLTARGET).\n\n" +
		"Hidden %%the lantern was lit%% here.\n\n" +
		"Para with a note[^1].\n\n[^1]: the lantern was lit\n"
	bs, ok := PlainBlocks(md)
	if !ok {
		t.Fatal("not placed")
	}
	var all []string
	for _, b := range bs {
		all = append(all, b.Text)
	}
	joined := strings.Join(all, "\n")
	for _, absent := range []string{"secret", "Marlowe", "TEXTALT", "IMGURL", "URLTARGET"} {
		if strings.Contains(joined, absent) {
			t.Errorf("%q is in the visible text: %s", absent, joined)
		}
	}
	if strings.Count(joined, "lantern was lit") != 1 || !strings.Contains(joined, "the office") || !strings.Contains(joined, "a link") {
		t.Errorf("visible text: %q", joined)
	}
	// a hit maps back to the source, markup inside it included
	b := bs[0]
	i := strings.Index(b.Text, "lantern was lit at dusk")
	lo, hi, ok := b.Source(i, i+len("lantern was lit at dusk"))
	if !ok || !strings.HasPrefix(md[lo:], "lantern was lit") || !strings.Contains(md[lo:hi], "* at dusk") {
		t.Errorf("source of a selection across emphasis: %q (%v)", md[lo:hi], ok)
	}
}
