package server

import (
	"html/template"

	"github.com/crueber/scriobh/internal/domain/entities"
)

// Fixtures for the Compendium tab's fragments and the shared entry component, appended to the template lint's lists.

func lkTestPage() Page { return Page{Lang: "en", CSRF: "tok_writer", UIDir: "ltr"} }

func lkViewFixture(mod func(*lookupView)) lookupView {
	v := lookupView{Page: lkTestPage(), Base: "/ui/projects/prj_a/lookup", ProjectID: "prj_a", Owner: true, Kinds: entityKinds, Mode: "list"}
	if mod != nil {
		mod(&v)
	}
	return v
}

func lkRows() []lkRow {
	return []lkRow{
		{ID: "ent_a", Kind: "character", KindLabel: "Character", Name: "Máire Nolan", Summary: "A ferrywoman with a <b>long</b> memory.", Color: "#2F5D50", ViaLabel: "Primary Compendium", Overridden: true, CanTarget: true},
		{ID: "ent_b", Kind: "place", KindLabel: "Place", Name: "Salt Quay", ViaLabel: "Referenced: Shared", Why: "This entry lives in Shared."},
	}
}

func lkEntryFixture(scope string, mod func(*lkEntry)) *lkEntry {
	fields := []fieldRow{
		{FieldDef: entities.FieldDef{Kind: "character", Key: "age", Label: "Age", Type: "number"}, Value: "34"},
		{FieldDef: entities.FieldDef{Kind: "character", Key: "side", Label: "Allegiance", Type: "choice", Choices: []string{"Crown", "Quay"}}, Value: "Quay"},
		{FieldDef: entities.FieldDef{Kind: "character", Key: "born", Label: "Born", Type: "date"}},
		{FieldDef: entities.FieldDef{Kind: "character", Key: "eyes", Label: "Eyes", Type: "text"}},
	}
	e := &lkEntry{ID: "ent_a", Name: "Máire Nolan", Kind: "character", KindLabel: "Character", Summary: "A ferrywoman.", Body: "She knows **every** tide.", Color: "#2F5D50",
		Via: "primary", ViaLabel: "Primary Compendium", CompName: "Salt Bible", CompSlug: "salt-bible", Revision: 3, Scope: scope,
		Aliases: []lkAlias{{ID: "ali_1", Alias: "Mam", Notes: "what the quay calls her"}, {ID: "ali_2", Alias: "The Ferrywoman"}},
		Grid:    gridOf(fields, false), GridRO: gridOf(fields, true), HTML: template.HTML("<p>She knows <strong>every</strong> tide.</p>"), Words: 4,
		Relations: []relRow{{Relation: entities.Relation{ID: "rel_1", OtherID: "ent_b", OtherName: "Salt Quay", Label: "keeps the ferry at", Notes: "since the flood"}}},
		Book:      []lkBookField{{FieldDef: fields[0].FieldDef, Value: "40", Base: "34"}, {FieldDef: fields[2].FieldDef}, {FieldDef: fields[3].FieldDef, Blanked: true}},
		BookSet:   true, OvName: "Mairín", OvSummary: "Older.", OvNotes: "Older in book two.", BaseName: "Máire Nolan", BaseSummary: "A ferrywoman.",
		Sheet: "/p/the-salt-road/e/ent_a", CompHref: "/c/salt-bible/e/ent_a", WhereHref: "/p/the-salt-road/e/ent_a#where"}
	switch scope {
	case "record":
		e.CanRecord, e.CanBook, e.CanAlias, e.CanPOV = true, true, true, true
	case "book":
		e.CanBook, e.CanPOV, e.Why = true, true, "You can read this entry."
	default:
		e.Why = "You can read this entry. Only editors of this book can set its version."
	}
	if mod != nil {
		mod(e)
	}
	return e
}

func lookupFragmentFixtures() []fragmentFixture {
	entry := func(scope string, mod func(*lkEntry)) lookupView {
		return lkViewFixture(func(v *lookupView) { v.Mode, v.E = "entry", lkEntryFixture(scope, mod); v.Status = "Added." })
	}
	pg := lkTestPage()
	return []fragmentFixture{
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Rows, v.Total, v.NextOffset, v.Hidden = lkRows(), 40, 30, true })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Q, v.Error = "zz", "That search is not allowed." })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Q = "zzz" })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) {
			v.Sel, v.Rows, v.Total, v.Exact = "Mrs Riviera", lkRows(), 2, &lkRow{ID: "ent_a", Name: "Máire Nolan"}
		})},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Sel, v.Alias, v.Rows, v.Total = "Mrs Riviera", true, lkRows(), 2 })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Mode = "none" })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Mode, v.Owner = "none", false })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Mode = "closed" })},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) {
			v.Mode, v.CanCreate, v.CompName, v.NewName = "new", true, "Salt Bible", "Captain Vell"
		})},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) {
			v.Mode, v.CanCreate, v.NewName, v.NewKind, v.Error, v.Existing = "new", true, "Mam", "character", "An entry already goes by “Mam”.", &lkRow{ID: "ent_a", Name: "Máire Nolan"}
		})},
		{"lookup", "lk-main", lkViewFixture(func(v *lookupView) { v.Mode, v.CreateWhy = "new", "This book has no Compendium to add to." })},
		{"lookup", "lk-main", entry("record", nil)},
		{"lookup", "lk-main", entry("record", func(e *lkEntry) { e.Aliases = nil })},
		{"lookup", "lk-main", entry("book", nil)},
		{"lookup", "lk-main", entry("ro", func(e *lkEntry) { e.BookSet, e.Relations, e.Aliases = false, nil, nil })},
		{"lookup", "lk-rows", lkViewFixture(func(v *lookupView) { v.Rows, v.Total = lkRows(), 2 })},
		{"lookup", "lk-more", lkViewFixture(func(v *lookupView) { v.Rows, v.NextOffset, v.Q = lkRows(), 60, "ma" })},
		{"lookup", "lk-aka-box", entry("record", nil)},
		{"lookup", "lk-status", lkViewFixture(func(v *lookupView) {
			v.Status, v.StatusBad = "This entry changed.", true
			v.Conflict = &lkConflict{Revision: 5, Rows: []lkConflictRow{{Label: "Summary", Theirs: "Theirs."}, {Label: "Description", Theirs: "Their <b>description</b>."}}}
			v.Collision = &lkRow{ID: "ent_b", Name: "Salt Quay"}
		})},
		{"lookup", "lk-status", lkViewFixture(func(v *lookupView) { v.Status = "Saved." })},
		{"lookup", "lk-status", lkViewFixture(func(v *lookupView) { v.Status, v.StatusBad, v.Conflict = "Changed.", true, &lkConflict{Revision: 5} })},
		{"lookup", "lk-saved", lkViewFixture(func(v *lookupView) {
			v.Status, v.E, v.Hashes = "Saved.", &lkEntry{Revision: 4}, map[string]string{"summary": "00", "f.age": "11"}
		})},
		{"entry", "entry-fields", map[string]any{"G": gridOf(lkEntryFixture("record", nil).Grid.Fields, false), "P": pg, "RO": false}},
		{"entry", "entry-fields", map[string]any{"G": gridOf(manyFields(12), false), "P": pg, "RO": false}},
		{"entry", "entry-fields", map[string]any{"G": gridOf(manyFields(12), true), "P": pg, "RO": true}},
		{"entry", "entry-fields", map[string]any{"G": gridOf(nil, true), "P": pg, "RO": true}},
		{"entry", "entry-desc", map[string]any{"P": pg, "Body": "Text", "Words": 1, "CSRF": "tok", "RO": false}},
		{"entry", "entry-desc", map[string]any{"P": pg, "HTML": template.HTML("<p>Text</p>"), "Words": 1, "RO": true}},
		{"entry", "entry-desc", map[string]any{"P": pg, "HTML": template.HTML(""), "Words": 0, "RO": true}},
	}
}

func manyFields(n int) []fieldRow {
	var rows []fieldRow
	for i := 0; i < n; i++ {
		key := string(rune('a' + i))
		f := fieldRow{FieldDef: entities.FieldDef{Kind: "character", Key: "k" + key, Label: "Field " + key, Type: "text"}}
		if i < 3 {
			f.Value = "filled " + key
		}
		rows = append(rows, f)
	}
	return rows
}
