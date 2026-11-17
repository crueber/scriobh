package studio_test

import (
	"math"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
)

// F-154 and F-513: a word target on a document, and progress toward it, in the tree and the outline.
func TestDocumentTargetsShowTheirProgressInTheTreeAndTheOutline(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	chapter := w.doc(u, p)
	scene := func(title string) string {
		return w.call(u, "doc.create", docs.CreateIn{ProjectID: p, Title: title, Kind: "scene", ParentID: chapter}).(docs.Item).Item.ID
	}
	s1, s2 := scene("One"), scene("Two")
	target := func(id string, n int) {
		cur := w.call(u, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
		w.call(u, "doc.update", docs.UpdateIn{ID: id, BaseRevision: cur.Revision, TargetWords: &n})
	}
	target(s1, 200)
	target(s2, 100)
	w.save(u, s1, lorem(50, "a"))
	w.save(u, s2, lorem(150, "b")) // past its target: progress is above 1
	list := w.call(u, "doc.list", docs.ListIn{ProjectID: p}).(docs.ListOut)
	by := map[string]docs.Meta{}
	for _, m := range list.Items {
		by[m.ID] = m
	}
	if m := by[s1]; m.TargetWords != 200 || m.Progress != 0.25 {
		t.Errorf("scene one: %+v", m)
	}
	if m := by[s2]; m.TargetWords != 100 || m.Progress != 1.5 {
		t.Errorf("scene two: %+v", m)
	}
	if m := by[chapter]; m.TargetWords != 0 || m.Progress != 0 {
		t.Errorf("a chapter with no target of its own: %+v", m)
	}
	if got := w.call(u, "doc.get", docs.DocIn{ID: s1}).(docs.Item).Item.Progress; got != 0.25 {
		t.Errorf("doc.get progress %v", got)
	}
	out := w.call(u, "outline.get", docs.OutlineIn{ProjectID: p}).(docs.OutlineOut)
	for _, it := range out.Items {
		if it.ID == chapter {
			// The chapter has no target of its own: its roll-up is the sum of its sections' (300), over their 200 words.
			if it.Rollup.TargetWords != 300 || it.Rollup.Words != 200 || math.Abs(it.Rollup.Progress-0.667) > 0.001 {
				t.Errorf("chapter roll-up: %+v", it.Rollup)
			}
		}
	}
	if out.Totals.TargetWords != 300 || out.Totals.Words != 200 {
		t.Errorf("totals %+v", out.Totals)
	}
}
