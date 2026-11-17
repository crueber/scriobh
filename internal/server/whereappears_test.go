package server

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/ops"
)

// "Where this appears": a book's own sheet lists its documents with a snippet and marks the first appearance; a
// Compendium's sheet lists the books the caller can open and only counts the rest, never naming them.
func TestWhereThisAppears(t *testing.T) {
	e := newEntEnv(t)
	op := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	mk := func(title, content string) {
		t.Helper()
		if _, err := e.s.Reg.Call(context.Background(), op, "doc.create", docs.CreateIn{ProjectID: e.pid, Title: title, Kind: "chapter", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	mk("Low Water", "Máire Nolan took the ferry out. Máire Nolan did not look back.")
	mk("High Water", "Salt Quay was empty.")
	if _, err := mentions.Settle(context.Background(), e.s.DB); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(e.owner, "/p/"+e.pslug+"/e/"+e.maire)
	flat := strings.Join(strings.Fields(between(body, `id="where"`, "</section>")), " ")
	for _, want := range []string{"Where this appears", "Low Water", "First appearance", "named 2 times", "Máire Nolan took the ferry out", "Named 2 times in 1 document"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the book's sheet lacks %q:\n%s", want, flat)
		}
	}
	if strings.Contains(flat, "High Water") {
		t.Error("a document that does not name the entity is listed")
	}
	// An entity nothing names says so, in plain words.
	_, body = e.get(e.owner, "/p/"+e.pslug+"/e/"+e.quay)
	if !strings.Contains(body, "High Water") {
		t.Error("the place the second chapter names is not listed")
	}
	other := e.entity(t, "character", "Nobody Yet", "")
	if _, err := mentions.Settle(context.Background(), e.s.DB); err != nil {
		t.Fatal(err)
	}
	if _, body = e.get(e.owner, "/p/"+e.pslug+"/e/"+other); !strings.Contains(body, "No document in this book names it yet") {
		t.Error("an entity nothing names does not say so")
	}
	// The Compendium's sheet names the books its reader can open.
	_, body = e.get(e.owner, "/c/"+e.slug+"/e/"+e.maire)
	flat = strings.Join(strings.Fields(between(body, `id="where"`, "</section>")), " ")
	if !strings.Contains(flat, "The Salt Road") || !strings.Contains(flat, "2 mentions in 1 document") || !strings.Contains(flat, "Low Water") {
		t.Errorf("the owner's Compendium sheet:\n%s", flat)
	}
	// A reader of the Compendium who cannot open the book gets a count and no name, no document, no title.
	code, body := e.get(e.reader, "/c/"+e.slug+"/e/"+e.maire)
	flat = strings.Join(strings.Fields(between(body, `id="where"`, "</section>")), " ")
	if code != 200 || strings.Contains(body, "The Salt Road") || strings.Contains(body, "Low Water") || !strings.Contains(flat, "1 other book also mentions it") {
		t.Errorf("the reader's Compendium sheet (%d) leaks or lacks the count:\n%s", code, flat)
	}
	// Paging: more documents than a page are reached by the link the section draws.
	for i := 0; i < wherePage+2; i++ {
		mk("Scene "+strings.Repeat("x", i+1), "Máire Nolan again.")
	}
	if _, err := mentions.Settle(context.Background(), e.s.DB); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(e.owner, "/p/"+e.pslug+"/e/"+e.maire)
	if !strings.Contains(body, `href="/p/`+e.pslug+`/e/`+e.maire+`?w=20#where"`) {
		t.Error("no link to the next page of documents")
	}
	if _, body = e.get(e.owner, "/p/"+e.pslug+"/e/"+e.maire+"?w=20"); !strings.Contains(body, "Earlier documents") {
		t.Error("the second page has no link back")
	}
}
