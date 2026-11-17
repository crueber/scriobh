package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mail"
)

// kindleEditionText makes a one-chapter project with content, sends it to the editor's Kindle through the real ops,
// and returns the text of every part of the EPUB that waits in the outbox.
func kindleEditionText(t *testing.T, content string) string {
	t.Helper()
	w := newSiteWorld(t, nil)
	w.doc(t, "Opening", "chapter", "", content, "done", "")
	w.must(t, w.admin, "PUT", "/api/v1/admin/mail", `{"host":"127.0.0.1","port":2525,"security":"none","allow_private":true,"from_address":"scriobh@example.org","enabled":true}`)
	w.must(t, w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor_kindle@kindle.com"}`)
	pv := w.must(t, w.editor, "POST", w.base()+"/kindle/preview", `{}`)
	var preview mail.KindlePreviewOut
	if err := json.Unmarshal(pv.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	sent := w.must(t, w.editor, "POST", w.base()+"/kindle/send", fmt.Sprintf(`{"token":%q}`, preview.Token))
	var out mail.KindleSendOut
	if err := json.Unmarshal(sent.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(w.s.Cfg.DataDir, "mail", "outbox", out.ID))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for name, data := range unzip(t, b) {
		sb.WriteString(name)
		sb.Write(data)
	}
	return sb.String()
}

func requireNoCanary(t *testing.T, text string, canaries ...string) {
	t.Helper()
	for _, c := range canaries {
		if i := strings.Index(text, c); i >= 0 {
			t.Errorf("the Kindle edition holds %s: ...%s...", c, strings.ReplaceAll(text[max(0, i-90):min(len(text), i+90)], "\n", " "))
		}
	}
}

func requireVisible(t *testing.T, text string, words ...string) {
	t.Helper()
	for _, v := range words {
		if !strings.Contains(text, v) {
			t.Fatalf("%q, which is visible text, is not in the edition: the table may have been dropped, so a missing canary would prove nothing", v)
		}
	}
}

// Shapes the compile's table sweep (internal/text/tablecells.go) handles. A guard: it passes, and has a positive control.
func TestReviewKindleEditionHoldsNoHiddenTextInOrdinaryTables(t *testing.T) {
	text := kindleEditionText(t, strings.Join([]string{
		"The harbour woke.",
		"",
		"| a | b |",
		"|---|---|",
		"| one %%CELL-PRIVATE-CANARY%% | two <scriobh-ghost>CELL-GHOST-CANARY</scriobh-ghost> |",
		"| three <!-- CELL-COMMENT-CANARY --> | four |",
		"| five | six | %%EXTRA-COL-PRIVATE-CANARY%% | <scriobh-ghost>EXTRA-COL-GHOST-CANARY</scriobh-ghost> |",
		"",
		"- item",
		"",
		"  | l1 | l2 |",
		"  |----|----|",
		"  | listcell %%LIST-PRIVATE-CANARY%% | <scriobh-ghost>LIST-GHOST-CANARY</scriobh-ghost> <!-- LIST-COMMENT-CANARY --> |",
		"",
		"Text <!-- INLINE-COMMENT-CANARY --> more text %%INLINE-PRIVATE-CANARY%% and",
		"",
		"%%",
		"MULTILINE-PRIVATE-CANARY",
		"%%",
		"",
	}, "\n"))
	requireVisible(t, text, "harbour woke", "one", "three", "five", "listcell")
	requireNoCanary(t, text, "CELL-PRIVATE-CANARY", "CELL-GHOST-CANARY", "CELL-COMMENT-CANARY", "EXTRA-COL-PRIVATE-CANARY", "EXTRA-COL-GHOST-CANARY",
		"LIST-PRIVATE-CANARY", "LIST-GHOST-CANARY", "LIST-COMMENT-CANARY", "INLINE-COMMENT-CANARY", "INLINE-PRIVATE-CANARY", "MULTILINE-PRIVATE-CANARY")
}

// Ghosted text, which the Kindle preview says is omitted, in a table that does not start its rows with a pipe, and in
// a table inside a block quote: the rows() sweep only looks at lines that start with "|", so both reach the EPUB (as
// escaped markup with the ghosted words in it). These two tests are the acceptance test for the fix that is said to be
// on another branch.
func TestReviewKindleEditionHoldsNoGhostedTextInTablesWithoutALeadingPipeOrInAQuote(t *testing.T) {
	text := kindleEditionText(t, strings.Join([]string{
		"The harbour woke.",
		"",
		"cellA | cellB",
		"--- | ---",
		"nopipe %%NOPIPE-PRIVATE-CANARY%% | <scriobh-ghost>NOPIPE-GHOST-CANARY</scriobh-ghost> <!-- NOPIPE-COMMENT-CANARY -->",
		"",
		"> | q1 | q2 |",
		"> |----|----|",
		"> | quotecell %%QUOTE-PRIVATE-CANARY%% | <scriobh-ghost>QUOTE-GHOST-CANARY</scriobh-ghost> <!-- QUOTE-COMMENT-CANARY --> |",
		"",
	}, "\n"))
	requireVisible(t, text, "harbour woke", "nopipe", "quotecell")
	requireNoCanary(t, text, "NOPIPE-PRIVATE-CANARY", "NOPIPE-GHOST-CANARY", "NOPIPE-COMMENT-CANARY", "QUOTE-PRIVATE-CANARY", "QUOTE-GHOST-CANARY", "QUOTE-COMMENT-CANARY")
}

// A note or a ghost that opens in one cell and closes in the next (a selection across cells in hand-written
// Markdown). The sweep "cuts to the end of the cell" and leaves the rest of the span in the next cell.
func TestReviewKindleEditionHoldsNoNoteOrGhostThatSpansTwoCells(t *testing.T) {
	text := kindleEditionText(t, strings.Join([]string{
		"The harbour woke.",
		"",
		"| p | q |",
		"|---|---|",
		"| spans %%SPAN-PRIVATE-CANARY | still-in-note%% after | z |",
		"| <scriobh-ghost>SPAN-GHOST-CANARY | still-ghost</scriobh-ghost> after | y |",
		"",
	}, "\n"))
	requireVisible(t, text, "harbour woke", "spans", "after")
	requireNoCanary(t, text, "SPAN-PRIVATE-CANARY", "still-in-note", "SPAN-GHOST-CANARY", "still-ghost")
}
