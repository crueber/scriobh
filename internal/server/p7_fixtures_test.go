package server

import (
	"html/template"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
)

// Fixtures of the Phase 7 privacy and management pages, so the per-page stylesheet budget and the asset checks see them.
func p7PageFixtures() []fixture {
	proj := projects.Project{ID: "prj_a", Slug: "the-salt-road", Title: "The Salt Road", Role: "owner"}
	pass := func(protected, unlocked bool) passData {
		d := passData{ID: "prj_a", Slug: proj.Slug, Title: proj.Title, CSRF: "tok_writer", Role: "owner", IsOwner: true, Idle: 30}
		d.Status = projectgate.Status{ProjectID: "prj_a", Protected: protected, Unlocked: unlocked, IdleMinutes: 30}
		if protected {
			d.Status.Keys = []projectgate.KeyUse{{KeyID: "key_1", Name: "agent", Prefix: "scr_ab", Owner: "Wren Writer", Mine: true, Approved: true, How: "owner", Uses: 3}}
			d.Keys = []passKeyRow{{KeyUse: d.Status.Keys[0], First: "1 Jan 2026, 10:00 UTC", Last: "2 Jan 2026, 10:00 UTC"}}
			d.Was = []string{"key_1"}
		}
		return d
	}
	comp := compendiums.Compendium{ID: "cmp_a", Slug: "salt-bible", Name: "Salt Bible", Role: "owner"}
	manage := cmData{C: comp, Slug: comp.Slug, CSRF: "tok_writer", Picked: map[string]bool{}, Targets: []compendiums.Compendium{{ID: "cmp_b", Name: "The Wide World"}},
		Entities: []entities.Entry{{ID: "ent_1", Kind: "character", Name: "Mara Voss"}}, Total: 1, Crossing: "refuse", KeyPins: "refuse"}
	preview := manage
	preview.Op, preview.IntoID, preview.Picked = "merge", "cmp_b", map[string]bool{}
	preview.Plan = &entities.MovePlan{SourceName: "Salt Bible", TargetName: "The Wide World", Moving: 3, Staying: 0, Fingerprint: "ab12", Unresolved: []string{"ent_1"}}
	preview.Attention = []cmItem{{MoveItem: entities.MoveItem{EntityID: "ent_1", Name: "Mara Voss", Kind: "character", Clashes: []entities.Clash{{EntityID: "ent_9", Name: "Mara Voss", Via: "name", Word: "Mara Voss"}}}}}
	sem := func(state string, enabled bool) semData {
		return semData{Project: proj, Slug: proj.Slug, CSRF: "tok_writer", State: state, St: semantic.StatusOut{State: state, Enabled: enabled}, Providers: []semProvider{{ID: "ai_1", Name: "Local embeddings", Where: "On this machine or its network", Models: []string{"embed-a"}}}}
	}
	semPrev := sem("off", false)
	semPrev.Preview = &semantic.PreviewOut{Documents: 4, Chunks: 12, Words: 900, Bytes: 5000, ConsentToken: "t", Endpoint: semantic.EndpointInfo{Name: "Local embeddings", Model: "embed-a", Local: true}}
	semPrev.Pick, semPrev.Model, semPrev.Cost = "ai_1", "embed-a", "None."
	card := func(streak int) cardPage {
		v := studio.CardView{Day: "2026-03-13", Streak: streak, Longest: 5, HasGoal: true, WritingDays30: 12, GoalsActive: 2, GoalsDone: 1,
			Goals: []studio.CardGoal{{Period: "day", Measure: "words", Scope: "all", Target: 500, Value: 620, Percent: 124, Met: true}, {Period: "total", Measure: "words", Scope: "project", Target: 80000, Value: 31000, Percent: 38}}}
		tr := func(key string, args ...any) string { return i18n.Default.T("en", key, args...) }
		return cardPage{View: v, SVG: template.HTML(studio.CardSVG(v, tr)), SVGPath: "/card/crd_x/card.svg"}
	}
	return []fixture{
		{"progress-card", Page{Title: "Writing progress", Lang: "en", UIDir: "ltr", Bare: true, Data: card(3)}},
		{"progress-card", Page{Title: "Writing progress", Lang: "en", UIDir: "ltr", Bare: true, Data: card(1)}},
		{"passphrase", p5With("Passphrase", "/p/the-salt-road/passphrase", pass(false, true))},
		{"passphrase", p5With("Passphrase", "/p/the-salt-road/passphrase", pass(true, true))},
		{"passphrase", p5With("Passphrase", "/p/the-salt-road/passphrase", pass(true, false))},
		{"semantic", p5With("Search by meaning", "/p/the-salt-road/semantic", sem("off", false))},
		{"semantic", p5With("Search by meaning", "/p/the-salt-road/semantic", sem("ready", true))},
		{"semantic", p5With("Search by meaning", "/p/the-salt-road/semantic", semPrev)},
		{"compendium-manage", p5With("Merge and split", "/c/salt-bible/manage", manage)},
		{"compendium-manage", p5With("Merge and split", "/c/salt-bible/manage", preview)},
		{"compendium-changes", p5With("Record", "/c/salt-bible/changes", cChangesData{C: comp, Slug: comp.Slug})},
		{"compendium-change", p5With("Record of a change", "/c/salt-bible/changes/cch_1", cChangeData{C: comp, Slug: comp.Slug})},
	}
}

func p7FragmentFixtures() []fragmentFixture {
	return []fragmentFixture{
		{"semantic-parts", "sem-similar", similarData{Hits: []semHit{{Title: "The Quay", URL: "/p/the-salt-road/d/doc_1?find=boats", Meta: "Scene · Draft", Snippet: "At the quay the boats rocked."}}, Partial: "2 documents are not indexed yet, so they are not searched."}},
		{"semantic-parts", "sem-similar", similarData{Message: "No other scene is close to this one."}},
	}
}
