package server

import (
	"sort"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/domain/share"
)

const rvDoc = "---\ntitle: FMTITLE\nsecretfm: FMCANARY\n---\n" +
	"Visible para. %%PRIVINLINE secret%% after.\n\n" +
	"%%\nPRIVBLOCK multi\n%%\n\n" +
	"<!-- HTMLCOMMENT -->\n\n" +
	"Ghost <scriobh-ghost>GHOSTINLINE</scriobh-ghost> kept.\n\n" +
	"| h1 | h2 |\n|----|----|\n| a %%PRIVCELL%% | b <!-- CELLCOMMENT --> |\n| c <scriobh-ghost>GHOSTCELL</scriobh-ghost> | d |\n| e %%SPAN | f%% | g |\n| x | y | EXTRACELL |\n\n" +
	"> quote %%PRIVQUOTE%%\n\n- list %%PRIVLIST%%\n\n1. ol <scriobh-ghost>GHOSTOL</scriobh-ghost>\n\n" +
	"Heading text\n============\n\nSee note[^1] and%%[^2]%%.\n\n[^1]: ok note\n[^2]: PRIVFN\n\n" +
	"<scriobh-ghost>GHOSTBLOCK whole paragraph</scriobh-ghost>\n"

var rvMarkers = []string{"FMCANARY", "PRIVINLINE", "PRIVBLOCK", "HTMLCOMMENT", "GHOSTINLINE", "PRIVCELL", "CELLCOMMENT", "GHOSTCELL", "PRIVSPAN", "PRIVQUOTE", "PRIVLIST", "GHOSTOL", "PRIVFN", "GHOSTBLOCK"}

func rvFound(s string) []string {
	var f []string
	for _, m := range rvMarkers {
		if strings.Contains(s, m) {
			f = append(f, m)
		}
	}
	sort.Strings(f)
	return f
}

// What leaves the server for an embedding endpoint (semantic.EmbedBody), an AI request (ai.CanonText) and the search
// index (text.SearchBody) must follow the same rule as a share link, a public repository and every export: a footnote
// that is referenced only from a %%private note%% is private too (share.Clean and text.CleanMarkdown cut it).
func TestReviewInteg7FootnoteOfAPrivateNoteIsNotEmbeddedOrSent(t *testing.T) {
	clean, ok := share.Clean(rvDoc, "omit")
	if !ok || strings.Contains(clean, "PRIVFN") {
		t.Fatalf("the control failed: share.Clean ok=%v still holds PRIVFN", ok)
	}
	for name, got := range map[string]string{
		"semantic.EmbedBody": semantic.EmbedBody(rvDoc),
		"ai.CanonText":       aicore.CanonText(rvDoc),
	} {
		if strings.Contains(got, "PRIVFN") {
			t.Errorf("%s: the definition of a footnote whose only reference is inside a private note is still in the text:\n%s", name, got)
		}
	}
	// every other hidden shape (table cells, spanning notes, comments, ghosts) is gone from the embedding text
	for _, m := range rvFound(semantic.EmbedBody(rvDoc)) {
		if m != "PRIVFN" {
			t.Errorf("semantic.EmbedBody still holds %s", m)
		}
	}
}
