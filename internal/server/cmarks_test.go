package server

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// The comment marks in the text (web/static/js/cmarks.js) are loaded by the writing page only when the person looking at it has an
// open comment on that chapter that they can read. A comment only its author can see does not make the file load for anyone else.
var reWriteExt = regexp.MustCompile(`data-ext="([^"]*)"`)

func TestWritePageLoadsCommentMarksOnlyForAVisibleComment(t *testing.T) {
	w := newWCWorld(t)
	rec := w.s.do(w.ada, "GET", w.base(), "")
	var pr struct {
		Item struct {
			Slug string `json:"slug"`
		} `json:"item"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &pr) != nil || pr.Item.Slug == "" {
		t.Fatalf("the project: %d %s", rec.Code, rec.Body)
	}
	page := func(c caller) string {
		t.Helper()
		rec := w.s.do(c, "GET", "/p/"+pr.Item.Slug+"/d/"+w.doc1, "")
		if rec.Code != 200 {
			t.Fatalf("%s opens the chapter: %d", c.name, rec.Code)
		}
		m := reWriteExt.FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatalf("%s: the writing page has no data-ext list", c.name)
		}
		return m[1]
	}
	const marks = "cmarks"
	if strings.Contains(page(w.ben), marks) {
		t.Fatal("a chapter with no comments loads the marks file")
	}
	w.private(t, w.ben, w.doc1, "quiet", "BEN-ONLY note")
	if !strings.Contains(page(w.ben), marks) {
		t.Error("the author's own open comment does not load the marks file for the author")
	}
	if strings.Contains(page(w.ada), marks) {
		t.Error("an editor's private comment loads the marks file for the owner, who cannot see it")
	}
	shared := w.write(t, w.cy, `{"document_id":"`+w.doc1+`","quote":"warmth","body":"a shared remark"}`)
	if !strings.Contains(page(w.ada), marks) {
		t.Error("a shared open comment does not load the marks file for the owner")
	}
	if !strings.Contains(page(w.ben), marks) {
		t.Error("a shared comment does not load the marks file for the author of a private one")
	}
	if strings.Contains(page(w.cy), marks) {
		t.Error("a reviewer, who cannot edit, is given the marks file")
	}
	_ = shared
}
