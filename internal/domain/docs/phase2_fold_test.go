package docs_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/text"
)

// Titles typed on a Mac (decomposed accents) and links typed anywhere else
// (precomposed) are the same title, and so are "STRASSE" and "Straße".
func TestLinksMatchAcrossUnicodeNormalFormsAndCaseFolds(t *testing.T) {
	g := newFx2(t)
	cafe := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Café Rouge"}) // decomposed
	strasse := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Straße"})
	scene := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Content: "At [[Café Rouge]] on [[STRASSE]] and [[CAFÉ ROUGE|the cafe]]."})
	got := linkState(g.docLinks(scene.ID))
	if got["Café Rouge"] != cafe.ID || got["STRASSE"] != strasse.ID || got["CAFÉ ROUGE"] != cafe.ID {
		t.Fatalf("links %v", got)
	}
	// Renaming to the other spelling keeps the links; a different word breaks them.
	g.rename(cafe.ID, "Café Rouge")
	if got = linkState(g.docLinks(scene.ID)); got["Café Rouge"] != cafe.ID {
		t.Errorf("a rename to the other normal form: %v", got)
	}
	g.rename(cafe.ID, "Cafe Rouge")
	if got = linkState(g.docLinks(scene.ID)); got["Café Rouge"] != "" {
		t.Errorf("an accent is a difference: %v", got)
	}
	// Resolution through the op agrees.
	out, err := g.call(g.ownA, "doc.resolve_link", docs.ResolveLinkIn{ProjectID: g.pidA, Title: "strasse"})
	if err != nil || out.(docs.ResolveLinkOut).Item == nil || out.(docs.ResolveLinkOut).Item.ID != strasse.ID {
		t.Errorf("resolve %+v %v", out, err)
	}
}

// Keys stored by the first folding (lower case only) are recomputed by a
// rebuild, so existing links go on resolving after the folding changed.
func TestRebuildRecomputesKeysStoredByTheOldFolding(t *testing.T) {
	g := newFx2(t)
	strasse := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Straße"})
	cafe := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Café"})
	scene := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Content: "[[STRASSE]] [[Café]]"})
	// Put the index back as the old folding left it: lower-cased only, so neither link resolved.
	if _, err := g.d.Writer().Exec(`UPDATE document_links SET to_id = NULL, target_key = lower(link_text) WHERE from_id = ?`, scene.ID); err != nil {
		t.Fatal(err)
	}
	if got := linkState(g.docLinks(scene.ID)); got["STRASSE"] != "" || got["Café"] != "" {
		t.Fatalf("setup: %v", got)
	}
	if _, err := links.Rebuild(g.ctx, g.d, ""); err != nil {
		t.Fatal(err)
	}
	got := linkState(g.docLinks(scene.ID))
	if got["STRASSE"] != strasse.ID || got["Café"] != cafe.ID {
		t.Errorf("after the rebuild %v", got)
	}
	var key string
	if err := g.d.Read().QueryRow(`SELECT target_key FROM document_links WHERE from_id = ? AND link_text = 'STRASSE'`, scene.ID).Scan(&key); err != nil || key != text.FoldTitle("Straße") {
		t.Errorf("stored key %q %v", key, err)
	}
	// And a rename of the target re-resolves through the recomputed key.
	g.rename(strasse.ID, "Elsewhere")
	if got = linkState(g.docLinks(scene.ID)); got["STRASSE"] != "" {
		t.Errorf("after renaming away: %v", got)
	}
}
