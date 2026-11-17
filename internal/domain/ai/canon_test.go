package ai_test

import (
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
)

// CanonText is the one rule for what may leave in an AI request or an embedding request.
func TestCanonTextRemovesEverythingTheReadingViewHidesAndKeepsTheRest(t *testing.T) {
	doc := "---\ntitle: T\nsecret: FRONT\n---\n" +
		"Visible one. %%PRIVNOTE%% more.\n\n" +
		"<!-- COMMENT -->\n\n" +
		"Text <!-- INLINECOMMENT --> here.\n\n" +
		"| a | b |\n|---|---|\n| 1 <!-- CELLCOMMENT --> | 2 |\n\n" +
		"Seen[^1] and hidden%%[^2]%% and ghost<scriobh-ghost>[^3]</scriobh-ghost> and half\n\n" +
		"Shared[^5]%%[^5]%%.\n\nThen a note nobody closes %%[^4]\n\n" +
		"```\n<!-- inside code -->\n```\n\n" +
		"[^1]: KEPTNOTE\n\n[^2]: PRIVFN\n\n[^3]: GHOSTFN\n\n[^4]: HALFFN\n\n[^5]: SHAREDFN\n"
	got := aicore.CanonText(doc)
	for _, gone := range []string{"FRONT", "PRIVNOTE", "COMMENT", "INLINECOMMENT", "CELLCOMMENT", "PRIVFN", "GHOSTFN", "HALFFN", "title: T"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is still in the text:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"Visible one.", "more.", "KEPTNOTE", "SHAREDFN", "inside code", "| 1", "Shared"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was lost:\n%s", kept, got)
		}
	}
	// A footnote nobody refers to, hidden or not, is not made private by being unreferenced.
	if got := aicore.CanonText("Body.\n\n[^9]: LONELY\n"); !strings.Contains(got, "LONELY") {
		t.Errorf("an unreferenced definition was cut: %q", got)
	}
	// Plain text and the empty text come back as they are.
	if got := aicore.CanonText("Just words.\n"); strings.TrimSpace(got) != "Just words." {
		t.Errorf("%q", got)
	}
	if aicore.CanonText("") != "" {
		t.Error("the empty text is not empty")
	}
}

// Text the parser cannot place is sent as nothing: a line of more than 64 container markers, or more than 2,000
// delimiter characters in one paragraph, makes the sanitiser change the text, and CleanMarkdown then refuses it.
// An ordinary manuscript, long paragraphs and long lists included, is never refused.
func TestCanonTextFailsClosedOnTextTheParserCannotPlace(t *testing.T) {
	if got := aicore.CanonText(strings.Repeat("> ", 100) + "quoted\n"); got != "" {
		t.Errorf("a line of 100 quote markers was sent: %q", got)
	}
	if got := aicore.CanonText(strings.Repeat("*a* ", 1200)); got != "" {
		t.Errorf("a paragraph of 2,400 delimiters was sent: %.40q", got)
	}
	long := strings.Repeat("A sentence with *one emphasis* and a [link](http://x.example) in it. ", 400) + "\n\n" + strings.Repeat("- a list item\n", 500)
	if got := aicore.CanonText(long); !strings.Contains(got, "A sentence with") || !strings.Contains(got, "a list item") {
		t.Errorf("an ordinary long text was refused: %.60q", got)
	}
}
