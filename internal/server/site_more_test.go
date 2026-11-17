package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// A part is a container: its title is its chapters' category only when the part
// itself could be published.
func TestAPartThatIsNotPublishedGivesNoCategory(t *testing.T) {
	w := newSiteWorld(t, nil)
	done := w.doc(t, "Done Part", "part", "", "", "done", "")
	draft := w.doc(t, "Draft Part CANARY_PART_TITLE", "part", "", "", "draft", "")
	skipped := w.doc(t, "Skipped Part CANARY_SKIPPED_TITLE", "part", "", "", "done", "")
	cut := w.doc(t, "Cut Part CANARY_CUT_TITLE", "part", "", "", "done", `,"include_in_compile":false`)
	texty := w.doc(t, "Texty Part", "part", "", "Words that are the part's own.", "done", "")
	for name, parent := range map[string]string{"In done": done, "In draft": draft, "In skipped": skipped, "In texty": texty} {
		w.doc(t, name, "chapter", parent, "Body of "+name+".", "done", "")
	}
	_ = cut
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+skipped, `{"role":"skip"}`)
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	cats := map[string]string{}
	for _, p := range prev["posts"].([]any) {
		m := p.(map[string]any)
		c, _ := m["category"].(string)
		cats[m["title"].(string)] = c
	}
	if cats["In done"] != "Done Part" || cats["In texty"] != "Texty Part" {
		t.Errorf("categories: %v", cats)
	}
	if _, there := cats["In draft"]; !there || cats["In draft"] != "" {
		t.Errorf("a chapter of a draft part is a post without its category: %v", cats)
	}
	if _, there := cats["In skipped"]; there {
		t.Errorf("role skip on a part hides what is inside it: %v", cats)
	}
	var texted bool
	for _, e := range prev["excluded"].([]any) {
		m := e.(map[string]any)
		texted = texted || (m["title"] == "Texty Part" && strings.Contains(m["reason"].(string), "container"))
	}
	if !texted {
		t.Errorf("a part's own text is dropped without a word: %v", prev["excluded"])
	}
	_, files := w.build(t, w.owner)
	for name, data := range files {
		for _, c := range []string{"CANARY_PART_TITLE", "CANARY_SKIPPED_TITLE", "CANARY_CUT_TITLE", "Words that are the part's own"} {
			if strings.Contains(string(data), c) {
				t.Errorf("%q is in %s", c, name)
			}
		}
	}
}

// A draft scene inside a done chapter is left out, and its words with it.
func TestADraftSceneInADoneChapterIsLeftOut(t *testing.T) {
	w := newSiteWorld(t, nil)
	ch := w.doc(t, "Done chapter", "chapter", "", "The chapter.", "done", "")
	w.doc(t, "Draft scene", "scene", ch, "CANARY_DRAFT_SCENE", "draft", "")
	w.doc(t, "Done scene", "scene", ch, "A finished scene.", "done", "")
	_, files := w.build(t, w.owner)
	page := string(files["posts/done-chapter.html"])
	if strings.Contains(page, "CANARY_DRAFT_SCENE") || !strings.Contains(page, "A finished scene.") {
		t.Errorf("scenes: %s", page)
	}
}

// A trashed project cannot be published; what was published stays until the
// project is purged (the folder is the administrator's to take down sooner).
func TestATrashedProjectCannotBePublished(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	rec := w.must(t, w.owner, "POST", w.base()+"/site/publish", "")
	loc := decodeObj(t, rec)["location"].(string)
	w.must(t, w.owner, "POST", w.base()+"/trash", "")
	if r := w.s.do(w.owner, "POST", w.base()+"/site/publish", ""); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "trash") {
		t.Errorf("publish on a trashed project: %d %s", r.Code, r.Body)
	}
	for _, p := range []string{"/site/build", "/site/preview"} { // like every export, the owner's own
		if r := w.s.do(w.owner, "POST", w.base()+p, ""); r.Code != 200 {
			t.Errorf("%s on a trashed project (the owner's): %d", p, r.Code)
		}
	}
	for _, c := range []caller{w.editor, w.reader} {
		if r := w.s.do(c, "POST", w.base()+"/site/preview", ""); r.Code != 404 {
			t.Errorf("a member other than the owner sees the trashed project's site: %d", r.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(loc), "index.html")); err != nil {
		t.Errorf("what was published went away with the trash: %v", err)
	}
}

// Publishing makes text public, so no key may do it, whatever its scope; both may still preview.
func TestNoAPIKeyMayPublishButBothMayPreview(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	for _, scope := range []string{"read", "write"} {
		tok, _ := w.s.mintKey(t, w.owner, fmt.Sprintf(`{"name":"site %s","scope":"%s"}`, scope, scope))
		if r := w.s.doKey(tok, "POST", w.base()+"/site/publish", ""); r.Code != 403 {
			t.Errorf("a %s key publishing: %d, want 403: %s", scope, r.Code, r.Body)
		}
		if r := w.s.doKey(tok, "POST", w.base()+"/site/preview", ""); r.Code != 200 {
			t.Errorf("a %s key previewing: %d", scope, r.Code)
		}
	}
}

// The registry-level timing the generator's own test cannot give: the database
// read, the selection and the images included, for 2,000 chapters.
func TestSitePreviewOf2000Chapters(t *testing.T) {
	if testing.Short() || siteRace {
		t.Skip("slow, and a timing test: not under the race detector")
	}
	// The push gate (SCRIOBH_TIMING=off) runs the same preview and build through the registry on 100 chapters, for
	// fixture cost; the budget is asserted, at 2,000, only when timing tests run.
	chapters := 2000
	if testutil.TimingOff() {
		chapters = 100
	}
	w := newSiteWorld(t, nil)
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"base_url":"https://example.org/","posts_per_page":20}}`)
	para := strings.Repeat("The quick brown fox jumps over the lazy dog and keeps going for a while longer. ", 3)
	p := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter}
	for i := 0; i < chapters; i++ {
		body := fmt.Sprintf("%s\n\nSecond paragraph %d with *emphasis* and a [link](https://example.org/%d).\n\n## Part\n\n%s\n", para, i, i, para)
		out, err := w.s.Reg.Call(t.Context(), p, "doc.create", docs.CreateIn{ProjectID: w.pid, Title: fmt.Sprintf("Post %d", i), Kind: "chapter", Content: body})
		if err != nil {
			t.Fatal(err)
		}
		st := "done"
		if _, err := w.s.Reg.Call(t.Context(), p, "doc.update", docs.UpdateIn{ID: out.(docs.Item).Item.ID, BaseRevision: 1, Status: &st}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	rec := w.must(t, w.owner, "POST", w.base()+"/site/preview", "")
	took := time.Since(start)
	m := decodeObj(t, rec)
	words := 0.0
	for _, x := range m["posts"].([]any) {
		words += x.(map[string]any)["words"].(float64)
	}
	t.Logf("preview through the registry: %d posts, %.0f words, %v", len(m["posts"].([]any)), words, took)
	start = time.Now()
	rec = w.must(t, w.owner, "POST", w.base()+"/site/build", "")
	t.Logf("build through the registry and REST: %v, %d bytes of zip", time.Since(start), rec.Body.Len())
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		budget := 10 * time.Second
		if took > budget {
			t.Errorf("preview took %v, budget %v", took, budget)
		}
	})
}
