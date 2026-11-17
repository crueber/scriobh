package server

import (
	"html/template"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
)

// Fixtures for the entity bible's pages and fragments, appended to the template lint's own lists: every template
// and partial is rendered with data that fills each branch, and held to the CSP and privacy rules.

func entComp(role string) compendiums.Compendium {
	return compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible", Kind: "fiction", Role: role}
}

func entRows() []entityRow {
	return []entityRow{
		{ID: "ent_a", Kind: "character", KindLabel: "Character", Name: "Máire Nolan", Summary: "A ferrywoman with a <b>long</b> memory.", Color: "#2F5D50", Aliases: []string{"The Ferrywoman", "Mam"}},
		{ID: "ent_b", Kind: "place", KindLabel: "Place", Name: "Salt Quay", Summary: "Where the road meets the water."},
		{ID: "ent_c", Kind: "item", KindLabel: "Item", Name: "The brass lantern", Trashed: true},
	}
}

func entListFixture(role string, rows []entityRow, mod func(*entityListData)) entityListData {
	c := entComp(role)
	d := entityListData{Compendium: c, Slug: c.Slug, CSRF: "tok_writer", CanEdit: compEditable(c), RoleWords: roleWords(role), Kinds: entityKinds, State: "live",
		Rows: rows, Total: len(rows), Values: map[string]string{}, Live: true, Src: "/c/the-salt-bible/entities?frag=1"}
	if mod != nil {
		mod(&d)
	}
	return d
}

func entSheetFixture(role string, mod func(*entityPageData)) entityPageData {
	c := entComp(role)
	e := entities.Detail{Entity: entities.Entity{ID: "ent_a", CompendiumID: "cmp_a", Kind: "character", Name: "Máire Nolan", SortName: "Nolan, Máire", Summary: "A ferrywoman.", Body: "She knows **every** tide.\n\n<script>alert(1)</script>",
		Fields: map[string]string{"age": "34", "born": "1790-03", "stray": "kept"}, Color: "#2F5D50", Revision: 4,
		Aliases: []entities.Alias{{ID: "ali_1", Alias: "The Ferrywoman", Notes: "what the quay calls her"}, {ID: "ali_2", Alias: "Mam"}}},
		Relations: []entities.Relation{
			{ID: "rel_1", Direction: "out", OtherID: "ent_b", OtherName: "Salt Quay", Kind: "custom", Label: "keeps the ferry at", Notes: "since the flood"},
			{ID: "rel_2", Direction: "both", OtherID: "ent_d", OtherName: "Tadhg Ó Néill", Kind: "rival", Label: "rival of"},
			{ID: "rel_3", Direction: "in", OtherID: "ent_e", OtherName: "Old Nolan", Kind: "family", Label: "daughter of"},
		}}
	d := entityPageData{Compendium: c, Slug: c.Slug, CSRF: "tok_writer", CanEdit: compEditable(c), E: e, Kind: "Character", Color: e.Color, RoleWords: roleWords(role),
		BodyHTML: template.HTML("<p>She knows <strong>every</strong> tide.</p>"), Swatches: projects.Swatches(), Values: map[string]string{}, Live: true,
		Fields: []fieldRow{
			{FieldDef: entities.FieldDef{Kind: "character", Key: "age", Label: "Age", Type: "number"}, Value: "34"},
			{FieldDef: entities.FieldDef{Kind: "character", Key: "born", Label: "Born", Type: "date"}, Value: "1790-03"},
			{FieldDef: entities.FieldDef{Kind: "character", Key: "side", Label: "Allegiance", Type: "choice", Choices: []string{"Crown", "Quay"}}, Value: "Quay"},
			{FieldDef: entities.FieldDef{Kind: "character", Key: "eyes", Label: "Eyes", Type: "text"}},
		},
		Orphans:   []fieldRow{{FieldDef: entities.FieldDef{Key: "stray", Label: "stray", Type: "text"}, Value: "kept"}},
		Kinds:     []string{"family", "ally", "rival", "mentor", "romantic", "member", "custom"},
		Projects:  []linkTarget{{ID: "prj_a", Title: "The Salt Road"}},
		Relations: []relRow{{Relation: e.Relations[0]}, {Relation: e.Relations[1]}, {Relation: e.Relations[2]}},
		SavedText: "Saved."}
	if mod != nil {
		mod(&d)
	}
	return d
}

func entMergeFixture(plan bool) mergeData {
	c := entComp("editor")
	src := entities.Detail{Entity: entities.Entity{ID: "ent_a", CompendiumID: "cmp_a", Kind: "character", Name: "Maire"}}
	d := mergeData{Compendium: c, Slug: c.Slug, CSRF: "tok_writer", Source: src,
		Candidates: []entityRow{{ID: "ent_b", Kind: "character", KindLabel: "Character", Name: "Máire Nolan", Summary: "A ferrywoman.", Aliases: []string{"Mam"}, Color: "#2F5D50"}}}
	if !plan {
		return d
	}
	d.Candidates = nil
	d.Into = "ent_b"
	d.Choices = map[string]string{"age": "source"}
	d.Plan = &entities.MergePlan{SourceID: "ent_a", SourceName: "Maire", TargetID: "ent_b", TargetName: "Máire Nolan", Kind: "character",
		AliasesMoved: []string{"Mairín"}, AliasesDuplicate: []string{"Mam"}, AliasesBlocked: []entities.AliasBlock{{Alias: "Nolan", EntityID: "ent_z", Name: "Old Nolan", Via: "name"}},
		NameAsAlias: "Maire", FieldsTaken: []string{"born"},
		FieldConflicts: []entities.FieldConflict{{Key: "age", Label: "Age", Source: "34", Target: "35", Choice: "source", Resolved: true}, {Key: "eyes", Label: "Eyes", Source: "grey", Target: "green"}},
		SummaryFrom:    "target", BodyFrom: "both", RelationsRewired: 2, RelationsDropped: []entities.RelationDrop{{ID: "rel_1", Reason: "self"}},
		OverridesMoved: 1, OverrideConflicts: []entities.OverrideConflict{{ProjectID: "prj_a", Key: "override:prj_a"}}, OverridesDropped: 0, LinksMoved: 1, LinksDropped: 1,
		SceneRefsRepointed: 3, RedirectsRepointed: 1, Extra: map[string]int{"mentions": 4}, Unresolved: []string{"eyes", "override:prj_a"}}
	d.Overrides = []mergeOverride{{OverrideConflict: d.Plan.OverrideConflicts[0], ProjectName: "The Salt Road"}}
	return d
}

func entFieldsFixture(role string, errScope string) fieldsData {
	c := entComp(role)
	d := fieldsData{Compendium: c, Slug: c.Slug, CSRF: "tok_writer", CanEdit: compEditable(c), Types: []string{"text", "number", "date", "choice"}, Values: map[string]string{"kind": "place", "key": "Old"}, Saved: "The field is saved."}
	for _, k := range entityKinds {
		g := fieldKindGroup{Kind: k.ID, Plural: k.Plural, Singular: k.Singular}
		if k.ID == "character" {
			g.Defs = []entities.FieldDef{{Kind: "character", Key: "age", Label: "Age", Type: "number"}, {Kind: "character", Key: "side", Label: "Allegiance", Type: "choice", Choices: []string{"Crown", "Quay"}}}
		}
		d.Groups = append(d.Groups, g)
	}
	if errScope != "" {
		d.Scope, d.Error = errScope, "A field is already called that for this kind."
	}
	return d
}

func entProjList(mod func(*projEntityListData)) projEntityListData {
	d := projEntityListData{Project: fixtureProjects()[0], Slug: "p-a", CSRF: "tok_writer", Kinds: entityKinds, Live: true, Src: "/p/p-a/entities?frag=1", Total: 2,
		Rows: []projEntityRow{
			{ID: "ent_a", Kind: "character", KindLabel: "Character", Name: "Máire Nolan", Summary: "A ferrywoman.", Color: "#2F5D50", Via: "primary", ViaLabel: viaLabels["primary"], Compendium: "The Salt Road Bible", Overridden: true},
			{ID: "ent_b", Kind: "place", KindLabel: "Place", Name: "Salt Quay", Via: "link", ViaLabel: viaLabels["link"], Compendium: "A shared bible"},
		}}
	if mod != nil {
		mod(&d)
	}
	return d
}

func entProjSheet(canEdit, link bool, errText string) projSheetData {
	pr := fixtureProjects()[0]
	over := "Mairín"
	pe := entities.ProjectEntity{ID: "ent_a", Kind: "character", Name: over, Summary: "A ferrywoman.", Body: "She knows tides.", Fields: map[string]string{"age": "36"}, Aliases: []string{"Mam"}, Color: "#2F5D50",
		CompendiumID: "cmp_a", CompendiumName: "The Salt Road Bible", Via: "primary", Overridden: true,
		Override: &entities.OverrideView{Name: &over, Fields: map[string]string{"age": "36", "eyes": ""}, Notes: "Older in this book."},
		Base:     &entities.Base{Name: "Máire Nolan", Summary: "A ferrywoman.", Fields: map[string]string{"age": "34"}}}
	if link {
		pe.Via = "link"
	}
	d := projSheetData{Project: pr, Slug: pr.Slug, CSRF: "tok_writer", CanEdit: canEdit, E: pe, Kind: "Character", Color: "#2F5D50", ViaLabel: viaLabels[pe.Via],
		BodyHTML: template.HTML("<p>She knows tides.</p>"), Notes: "Older in this book.", CompSlug: "the-salt-bible", Saved: projSheetSaved["override"], Values: map[string]string{},
		OverrideSet: true, BaseName: "Máire Nolan", BaseSummary: "A ferrywoman.", OvName: over, IsLink: link, Live: true,
		Fields: []projSheetField{
			{FieldDef: entities.FieldDef{Kind: "character", Key: "age", Label: "Age", Type: "number"}, Effective: "36", Base: "34", Override: "36", Has: true},
			{FieldDef: entities.FieldDef{Kind: "character", Key: "eyes", Label: "Eyes", Type: "text"}, Base: "green", Override: "", Blanked: true, Has: true},
			{FieldDef: entities.FieldDef{Kind: "character", Key: "side", Label: "Allegiance", Type: "choice", Choices: []string{"Crown", "Quay"}}, Effective: "Quay", Base: "Quay"},
		}}
	d.Error = errText
	return d
}

func entityPageFixtures() []fixture {
	writer := func(title, path string, data any) fixture {
		return fixture{"", Page{Title: title, Path: path, User: fixtureUser("writer"), CSRF: "tok_writer", Data: data}}
	}
	mk := func(name, title, path string, data any) fixture {
		f := writer(title, path, data)
		f.name = name
		return f
	}
	search := entListFixture("owner", []entityRow{{ID: "ent_a", Kind: "character", KindLabel: "Character", Name: "Máire Nolan", MatchedIn: "alias",
		Parts: []snipPart{{Text: "The "}, {Text: "Ferry", Mark: true}, {Text: "woman"}}}}, func(d *entityListData) {
		d.Q, d.QEsc, d.Searching, d.Kind = "ferry", "ferry", true, "character"
	})
	return []fixture{
		mk("entities", "Entities", "/c/the-salt-bible/entities", entListFixture("owner", entRows(), func(d *entityListData) { d.Next = "/c/the-salt-bible/entities?cursor=x" })),
		mk("entities", "Entities", "/c/the-salt-bible/entities", entListFixture("owner", nil, func(d *entityListData) { d.Empty = true })),
		mk("entities", "Entities", "/c/the-salt-bible/entities", search),
		mk("entities", "Entities", "/c/the-salt-bible/entities", entListFixture("reader", entRows()[:2], nil)),
		mk("entities", "Entities", "/c/the-salt-bible/entities", entListFixture("editor", nil, func(d *entityListData) {
			d.Error, d.Field, d.Values = "Name the entity.", "name", map[string]string{"name": "<b>x</b>", "kind": "place"}
		})),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("owner", nil)),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("reader", nil)),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("editor", func(d *entityPageData) {
			d.Followed, d.Merged, d.Scope, d.Field = true, "ent_z", "alias", "alias"
			d.Error = "Another entity already answers to that word."
			d.Collision = &entityCollision{ID: "ent_b", Name: "Salt Quay", Via: "alias"}
			d.Values = map[string]string{"alias": "Quay", "notes": "n"}
		})),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("editor", func(d *entityPageData) {
			d.Scope = "details"
			d.Error = "This entity changed since you opened it."
			theirs := d.E
			theirs.Name, theirs.Body = "Máire Nic Nolan", "Theirs."
			d.Conflict = &entityConflict{Revision: 5, Theirs: theirs}
		})),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("editor", func(d *entityPageData) { d.E.Trashed = true })),
		mk("entity", "Máire Nolan", "/c/the-salt-bible/e/ent_a", entSheetFixture("editor", func(d *entityPageData) {
			d.Compendium.Trashed, d.CanEdit = true, false
			d.E.Aliases, d.E.Body, d.E.Summary, d.Relations, d.Color = nil, "", "", nil, ""
		})),
		mk("entity-merge", "Merge Maire", "/c/the-salt-bible/e/ent_a/merge", entMergeFixture(false)),
		mk("entity-merge", "Merge Maire", "/c/the-salt-bible/e/ent_a/merge", entMergeFixture(true)),
		mk("entity-merge", "Merge Maire", "/c/the-salt-bible/e/ent_a/merge", func() mergeData {
			d := entMergeFixture(true)
			d.Plan.Unresolved, d.CanMerge = nil, true
			d.Plan.FieldConflicts, d.Overrides, d.Error = nil, nil, "Those entities are of different kinds."
			return d
		}()),
		mk("entity-fields", "Fields", "/c/the-salt-bible/fields", entFieldsFixture("owner", "")),
		mk("entity-fields", "Fields", "/c/the-salt-bible/fields", entFieldsFixture("editor", "new")),
		mk("entity-fields", "Fields", "/c/the-salt-bible/fields", entFieldsFixture("editor", "character:age")),
		mk("entity-fields", "Fields", "/c/the-salt-bible/fields", entFieldsFixture("reader", "")),
		mk("project-entities", "Entities of The Salt Road", "/p/p-a/entities", entProjList(func(d *projEntityListData) { d.Next, d.Q = "/p/p-a/entities?offset=50", "ma" })),
		mk("project-entities", "Entities of The Salt Road", "/p/p-a/entities", entProjList(func(d *projEntityListData) { d.Rows, d.Total, d.Hidden = nil, 0, true })),
		mk("project-entities", "Entities of The Salt Road", "/p/p-a/entities", entProjList(func(d *projEntityListData) { d.Rows, d.Total, d.NoBible = nil, 0, true })),
		mk("project-entity", "Máire Nolan", "/p/p-a/e/ent_a", entProjSheet(true, false, "")),
		mk("project-entity", "Máire Nolan", "/p/p-a/e/ent_a", entProjSheet(true, true, "That value is not allowed.")),
		mk("project-entity", "Máire Nolan", "/p/p-a/e/ent_a", entProjSheet(false, false, "")),
	}
}

func entityFragmentFixtures() []fragmentFixture {
	return []fragmentFixture{
		{"entity-list", "entity-list", entListFixture("owner", entRows(), func(d *entityListData) {
			d.Next, d.Prev, d.SavedText = "/c/x/entities?cursor=a", "/c/x/entities", "Saved."
		})},
		{"entity-list", "entity-list", entListFixture("reader", nil, func(d *entityListData) { d.Empty = true })},
		{"entity-list", "entity-list", entListFixture("owner", nil, func(d *entityListData) { d.State, d.Total = "trashed", 0 })},
		{"entity-list", "entity-list", entListFixture("owner", nil, func(d *entityListData) { d.Q, d.Searching, d.Degraded = "zzz", true, true })},
		{"entity-list", "project-entity-list", entProjList(nil)},
		{"entity-list", "project-entity-list", entProjList(func(d *projEntityListData) { d.Rows, d.Total, d.Hidden, d.Error = nil, 0, true, "Something." })},
		{"entity-list", "entity-swatch", "#2F5D50"},
		{"entity-list", "entity-swatch", ""},
		{"entity-preview", "entity-preview", struct{ HTML template.HTML }{"<p>Hi</p>"}},
		{"entity-preview", "entity-preview", struct{ HTML template.HTML }{""}},
		{"where", "where-project", &whereData{Rows: []whereRow{{Title: "The ledger", Crumb: "Part One / Low Water", Href: "/p/x/d/doc_a", Snippet: "Ada carried the lamp.", Count: 3, Explicit: 1, First: true, Ambiguous: true}}, Documents: 1, Mentions: 3, FirstHref: "/p/x/d/doc_a", FirstText: "Low Water / The ledger", Indexing: true, Next: "/p/x/e/ent_a?w=20#where", Prev: "/p/x/e/ent_a#where"}},
		{"where", "where-project", &whereData{Indexing: true}},
		{"where", "where-project", &whereData{}},
		{"where", "where-project", &whereData{Failed: true}},
		{"where", "where-comp", &whereData{Books: []whereBook{{Title: "Salt Road", Href: "/p/salt-road", Documents: 4, Mentions: 11, FirstHref: "/p/salt-road/d/doc_a", FirstText: "Low Water"}, {Title: "No first", Href: "/p/nf", Documents: 1, Mentions: 1}}, Documents: 5, Mentions: 12, Hidden: 2, Indexing: true}},
		{"where", "where-comp", &whereData{Hidden: 1}},
		{"where", "where-comp", &whereData{}},
		{"where", "where-comp", &whereData{Failed: true}},
	}
}
