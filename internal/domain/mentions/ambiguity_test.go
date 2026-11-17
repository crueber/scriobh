package mentions_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/ops"
)

// The stored ambiguity flag counts every entity in the project's scope, so no
// read may show it as it stands: a reader who cannot read the other meaning of
// a word must not learn that it exists, in mention.by_entity or scene.entities
// any more than in mention.list.
func TestAmbiguityIsNotRevealedAcrossTheBibleBoundary(t *testing.T) {
	x := newH(t)
	primary := x.compendium(x.owner, "Primary")
	other := x.compendium(x.owner, "Other")
	a := x.ent(x.owner, primary, "character", "Bran")
	x.ent(x.owner, other, "character", "Bran the Elder", "Bran")
	pid := x.project(x.owner, "Book", primary)
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: other})
	d := x.doc(x.owner, pid, "One", "Bran sat by the fire.")
	x.settle()

	byEntity := func(p ops.Principal) mentions.ByEntityOut {
		return x.call(p, "mention.by_entity", mentions.ByEntityIn{ID: pid, Entity: a}).(mentions.ByEntityOut)
	}
	sidebar := func(p ops.Principal) scenes.EntitiesOut {
		return x.call(p, "scene.entities", scenes.EntitiesIn{ID: d.ID}).(scenes.EntitiesOut)
	}
	flagged := func(o scenes.EntitiesOut) bool {
		for _, it := range o.Items {
			if it.Entity.ID == a {
				return it.Ambiguous
			}
		}
		t.Fatalf("Bran is not in the sidebar: %+v", o)
		return false
	}
	// A reader of both Compendiums is told.
	if b := byEntity(x.owner); len(b.Items) != 1 || !b.Items[0].Ambiguous {
		t.Errorf("by_entity should flag it for a reader of both: %+v", b.Items)
	}
	if !flagged(sidebar(x.owner)) {
		t.Errorf("scene.entities should flag it for a reader of both")
	}
	// A project member who can read only the primary Compendium is not.
	x.projectMember(pid, x.editor, ops.ProjectEditor)
	x.member(primary, x.editor, "reader")
	if b := byEntity(x.editor); len(b.Items) != 1 || b.Items[0].Ambiguous {
		t.Errorf("by_entity leaks that a hidden entity shares the word: %+v", b.Items)
	}
	if flagged(sidebar(x.editor)) {
		t.Errorf("scene.entities leaks that a hidden entity shares the word")
	}
	// Nothing in either answer names the hidden entity.
	if s := sidebar(x.editor); len(s.Items) != 1 {
		t.Errorf("sidebar items: %+v", s.Items)
	}
}
