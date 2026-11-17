package server

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// A guest cannot find out what is in the private notes, the ghosted text, the HTML comments or the front
// matter by offering guesses as selections: every selection that cannot be placed gets the same answer, so a
// guess that is in hidden text cannot be told from one that is in no text at all.
func TestAGuestCannotConfirmGuessesAboutHiddenText(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment"}`)
	var ref string
	tries := 0
	for name, sel := range map[string]string{
		"in a private note":        "SECRETNOTE do not show",
		"part of a private note":   "SECRETNOTE",
		"across a note's marker":   "paragraph %%SECRETNOTE",
		"in ghosted text":          "GHOSTWORDS cut sentence",
		"in front matter":          "SECRETFRONT",
		"in an HTML comment":       "SECRETCOMMENT",
		"across an HTML comment":   "remains. <!-- SECRETCOMMENT",
		"in no text at all":        "words that appear nowhere in the book",
		"across paragraphs":        "stood alone on the hill.\n\nSecond paragraph",
		"in the alternatives line": "ALTVARIANT",
		"empty after trimming":     " . ",
	} {
		if tries++; tries%8 == 0 { // a link takes only so many comments a minute: a fresh one
			_, tok = w.link(t, `{"scope":"comment"}`)
		}
		rec := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+w.doc1+`","selection":`+jsonString(sel)+`,"body":"guess","name":"Eve"}`)
		if rec.Code != 422 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			continue
		}
		if ref == "" {
			ref = rec.Body.String()
		}
		if rec.Body.String() != ref {
			t.Errorf("%s: an answer that differs from the others: %s (the others: %s)", name, rec.Body, ref)
		}
		noLeak(t, name, rec.Body.String())
	}
	if n := countRows(t, w.s, `SELECT count(*) FROM comments WHERE share_id IS NOT NULL`); n != 0 {
		t.Errorf("%d guest comments were written by guesses", n)
	}
	// A member, who can read the notes, is told why.
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", `{"document_id":"`+w.doc1+`","quote":"SECRETNOTE do not show","body":"x"}`)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "private note") {
		t.Errorf("a member: %d %s", rec.Code, rec.Body)
	}
}

// The plain context a reading view is given comes only from inside the block of the comment's own
// words, with notes, ghosts and HTML comments cut out, however long they are and wherever the
// window would have started: not a byte of front matter, of a long comment or of a long note.
func TestPlainContextNeverCarriesHiddenText(t *testing.T) {
	w := newShareWorld(t, nil)
	long := strings.Repeat("LEAKMARK zzz ", 40) // 520 bytes
	content := "---\nkey: " + long + "\n---\n" +
		"<!--\n" + long + "\n-->\n\n" +
		"Before the note %%" + long + "%% and after it comes the commented sentence here, then <!-- " + long + " --> a tail that goes on and on.\n\n" +
		"Next paragraph: ordinary visible words.\n"
	// the author writes it; the markers are in front matter, a block comment, a note and an inline comment
	doc := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", `{"title":"Leaky","kind":"chapter","content":`+jsonString(content)+`}`)
	var d struct{ Item struct{ ID string } }
	decode(t, doc, &d)
	_, tok := w.link(t, `{"scope":"comment","document_ids":["`+d.Item.ID+`"]}`)
	rec := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+d.Item.ID+`","selection":"the commented sentence here","body":"nice","name":"Sam"}`)
	if rec.Code != 201 {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Thread struct{ Quote, Before, After string }
	}
	decode(t, rec, &made)
	if made.Thread.Quote != "the commented sentence here" || !strings.Contains(made.Thread.Before, "after it comes") {
		t.Errorf("the plain forms: %+v", made.Thread)
	}
	noLeakWord := func(what, body string) {
		t.Helper()
		if strings.Contains(body, "LEAKMARK") {
			t.Errorf("%s carries hidden text: %.300s", what, body)
		}
	}
	noLeakWord("the guest's answer", rec.Body.String())
	for _, path := range []string{"/s/" + tok + "/api/comments?document_id=" + d.Item.ID, "/s/" + tok + "/api/docs/" + d.Item.ID, "/s/" + tok + "/api/manifest"} {
		noLeakWord(path, w.pub("GET", path, "").Body.String())
	}
	// the same for the plain form a member's reading view asks for
	cr := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", `{"document_id":"`+d.Item.ID+`","quote":"the commented sentence here","body":"mine"}`)
	if cr.Code != 201 {
		t.Fatalf("%d %s", cr.Code, cr.Body)
	}
	lst := w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/comments?document_id="+d.Item.ID+"&plain=true", "")
	if !strings.Contains(lst.Body.String(), `"quote_plain":"the commented sentence here"`) {
		t.Fatalf("no plain forms: %s", lst.Body)
	}
	for _, field := range []string{"before_plain", "after_plain"} {
		i := strings.Index(lst.Body.String(), `"`+field+`"`)
		if i < 0 {
			t.Errorf("no %s", field)
			continue
		}
		rest := lst.Body.String()[i:]
		if j := strings.Index(rest, ","); j > 0 {
			rest = rest[:j]
		}
		noLeakWord(field, rest)
	}
	// the unit-level promise, for any window: a hinter never reads outside the block
	body := comanchor.Body(content)
	h := comanchor.NewHinter(body)
	a := strings.Index(body, "the commented sentence here")
	p := h.For(a, a+len("the commented sentence here"))
	if strings.Contains(p.Before+p.After+p.Quote, "LEAKMARK") {
		t.Errorf("the hinter: %+v", p)
	}
	// a comment cannot be put inside a long comment or note whatever the window
	for _, sel := range []string{"LEAKMARK zzz LEAKMARK", "key: LEAKMARK"} {
		r := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+d.Item.ID+`","selection":`+jsonString(sel)+`,"body":"x","name":"Eve"}`)
		if r.Code != 422 {
			t.Errorf("%q: %d", sel, r.Code)
		}
	}
	_ = context.Background
	_ = docs.DocIn{}
	_ = ops.ProjectOwner
}

// A phrase that is also in a private note elsewhere is the same selection to a guest as one that is not:
// hidden text cannot make it ambiguous, so the answer (and whether it is widened) tells nothing of the note.
func TestHiddenOccurrencesDoNotChangeAGuestsAnswer(t *testing.T) {
	w := newShareWorld(t, nil)
	mk := func(content string) (string, string) {
		rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", `{"title":"T","kind":"chapter","content":`+jsonString(content)+`}`)
		var d struct{ Item struct{ ID string } }
		decode(t, rec, &d)
		_, tok := w.link(t, `{"scope":"comment","document_ids":["`+d.Item.ID+`"]}`)
		return d.Item.ID, tok
	}
	ask := func(id, tok string) string {
		rec := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+id+`","selection":"the lantern was lit","body":"x","name":"Sam"}`)
		var out struct {
			Widened bool
			Thread  struct{ Quote, Anchor string }
		}
		decode(t, rec, &out)
		return strings.Join([]string{http200(rec.Code), out.Thread.Quote, out.Thread.Anchor, map[bool]string{true: "widened", false: "exact"}[out.Widened]}, "|")
	}
	plain, tok1 := mk("Chapter text. The *lantern was lit* at dusk, as always.\n\nAnother paragraph.\n")
	noted, tok2 := mk("Chapter text. The *lantern was lit* at dusk, as always.\n\nAnother %%the lantern was lit%% paragraph.\n")
	a, b := ask(plain, tok1), ask(noted, tok2)
	if a != b {
		t.Errorf("a hidden occurrence changed the answer: %q against %q", a, b)
	}
}

func http200(c int) string { return map[bool]string{true: "ok", false: "no"}[c == 201] }

// A key pinned to the project reads comments at the read scope and writes them at the write scope.
func TestKeysReadCommentsWithReadAndWriteThemWithWrite(t *testing.T) {
	w := newShareWorld(t, nil)
	readKey, _ := w.s.mintKey(t, w.owner, `{"name":"r","scope":"read","project_id":"`+w.pid+`"}`)
	writeKey, _ := w.s.mintKey(t, w.owner, `{"name":"w","scope":"write","project_id":"`+w.pid+`"}`)
	base := "/api/v1/projects/" + w.pid + "/comments"
	body := `{"document_id":"` + w.doc2 + `","body":"from an agent"}`
	if r := w.s.doKey(readKey, "GET", base, ""); r.Code != 200 {
		t.Errorf("a read key lists: %d %s", r.Code, r.Body)
	}
	if r := w.s.doKey(readKey, "POST", base, body); r.Code != 403 {
		t.Errorf("a read key writes: %d", r.Code)
	}
	if r := w.s.doKey(writeKey, "POST", base, body); r.Code != 201 {
		t.Errorf("a write key writes: %d %s", r.Code, r.Body)
	}
	// and a key never manages links or notifications
	if r := w.s.doKey(writeKey, "GET", "/api/v1/projects/"+w.pid+"/shares", ""); r.Code != 403 {
		t.Errorf("a key lists links: %d", r.Code)
	}
	if r := w.s.doKey(writeKey, "GET", "/api/v1/notifications", ""); r.Code != 403 {
		t.Errorf("a pinned key reads notifications: %d", r.Code)
	}
	// a share manifest carries no label of the owner's
	_, tok := w.link(t, `{"label":"PRIVATELABEL for my agent"}`)
	if r := w.pub("GET", "/s/"+tok+"/api/manifest", ""); strings.Contains(r.Body.String(), "PRIVATELABEL") {
		t.Errorf("the owner's label is in the guest's manifest: %s", r.Body)
	}
}

func countRows(t *testing.T, s *Server, q string) int {
	t.Helper()
	var n int
	if err := s.DB.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
