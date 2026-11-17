package ai_test

import (
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docs"
)

// A selection that begins inside a private note or a ghost (the opening marker is outside the
// range) must not carry the rest of it. The markers are found on the whole body first.
func TestASelectionStartingInsideHiddenTextLeavesItOut(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	content := "Intro line.\n\nBefore %%secret plan alpha\n\nsecret beta%% visible tail. More text here.\n\n" +
		"Ghost: <scriobh-ghost>ghosted words here</scriobh-ghost> after ghost. Done."
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = ? WHERE id = ?`, content, e.doc); err != nil {
		t.Fatal(err)
	}
	rev := e.call(e.owner, "doc.get", docs.DocIn{ID: e.doc}).(docs.Item).Item.Revision
	sel := func(from, to string) aicore.RunIn {
		start := strings.Index(content, from)
		end := strings.Index(content, to) + len(to)
		return aicore.RunIn{ProjectID: e.project, Recipe: "freeform", DocumentID: e.doc, Params: map[string]string{"instruction": "x"},
			Selection: &aicore.Selection{Start: &start, End: &end, BaseRevision: &rev},
			Chips:     []aicore.ChipSpec{{Kind: lib.ChipSelection}, {Kind: lib.ChipSentence}, {Kind: lib.ChipParagraph}}}
	}
	for name, in := range map[string]aicore.RunIn{
		"inside a private note":            sel("plan alpha", "visible tail."),
		"inside a ghost":                   sel("words here", "after ghost."),
		"from the note's second paragraph": sel("secret beta", "More text here."),
	} {
		pv := e.preview(e.editor, in)
		b := pv.Payload.Messages[len(pv.Payload.Messages)-1].Content
		for _, banned := range []string{"secret", "plan alpha", "beta", "ghosted words", "scriobh-ghost", "%%"} {
			if strings.Contains(b, banned) {
				t.Errorf("%s: %q was sent:\n%s", name, banned, b)
			}
		}
	}
	// What is visible still is.
	pv := e.preview(e.editor, sel("plan alpha", "visible tail."))
	b := pv.Payload.Messages[len(pv.Payload.Messages)-1].Content
	if !strings.Contains(b, "visible tail.") || !strings.Contains(b, "Before") {
		t.Errorf("the visible text went missing:\n%s", b)
	}
	if !strings.Contains(e.preview(e.editor, sel("words here", "after ghost.")).Payload.Messages[1].Content, "after ghost.") {
		t.Error("the visible text after a ghost went missing")
	}
}
