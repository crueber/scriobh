package server

import (
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Fixtures for the template lint: every Compendium page and fragment in each state a person can reach.

func compFixture(role string) compendiums.Compendium {
	return compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible", Kind: "fiction", Role: role, EntityCount: 42,
		Description: "Everyone and everywhere in the Salt Road books."}
}

func compendiumsFixture(state string, writer bool) compendiumsData {
	d := compendiumsData{State: state, CSRF: "tok_writer", IsWriter: writer}
	a, b := compFixture("owner"), compFixture("reader")
	b.ID, b.Slug, b.Name, b.Kind = "cmp_b", "tides", "Tides and Harbours", "nonfiction"
	switch state {
	case "archived":
		a.Archived = true
		d.Items = []compendiums.Compendium{a}
	case "trashed":
		a.Trashed = true
		d.Items = []compendiums.Compendium{a}
	case "empty":
		d.State = "active"
	default:
		d.Items = []compendiums.Compendium{a, b}
	}
	return d
}

func compOverviewFixture(role string) compOverview {
	c := compFixture(role)
	d := compOverview{Compendium: c, Slug: c.Slug, CSRF: "tok_writer", IsOwner: role == "owner", CanEdit: role != "reader", RoleWords: compRoleWords[role], Values: map[string]string{},
		Detail: compendiums.Detail{Compendium: c, MemberCount: 3,
			Projects:   []compendiums.ProjectUse{{ID: "prj_a", Title: "The Salt Road", Relation: "primary"}, {ID: "prj_d", Title: "Hedgerow", Relation: "reference"}},
			References: []compendiums.Reference{{ID: "ref_1", CompendiumID: "cmp_b", Name: "Tides and Harbours", Mode: "reference"}}, HiddenProjects: 1, HiddenRefs: 1, ReferencedBy: 2}}
	if role == "owner" {
		d.ShowPeople = true
		d.Members = []compendiums.Member{{UserID: "usr_1", DisplayName: "Wren Writer", Email: "wren@example.com", Role: "owner"}, {UserID: "usr_2", DisplayName: "Ciara Reader", Email: "ciara@example.com", Role: "reader"}}
		d.RefChoices = []compendiums.Compendium{{ID: "cmp_c", Name: "A Shared Cosmology"}}
	}
	return d
}

func projCompFixture(primary, owner, preview bool) projCompData {
	proj := ownerFixture(fixtureProjects()[0])
	d := projCompData{Project: proj, Slug: proj.Slug, CSRF: "tok_writer", IsOwner: owner, Values: map[string]string{}, Slugs: map[string]string{"cmp_a": "the-salt-bible", "cmp_b": "tides"}}
	if primary {
		d.Wiring = compendiums.Wiring{HasPrimary: true, Primary: &compendiums.Reference{CompendiumID: "cmp_a", Name: "The Salt Road Bible"},
			References: []compendiums.Reference{{ID: "ref_1", CompendiumID: "cmp_b", Name: "Tides and Harbours"}}, Hidden: 1}
	}
	if owner {
		d.Choices = []compendiums.Compendium{{ID: "cmp_c", Name: "A Shared Cosmology", Role: "owner"}}
		d.Editable = d.Choices
	}
	if preview {
		d.Preview = &compPreview{TargetID: "cmp_c", TargetName: "A Shared Cosmology", Out: compendiums.PrimaryPlan{SetOut: compendiums.SetOut{Changed: true, EntitiesEntering: 12, EntitiesLeaving: 40, OverridesKept: 3, OverridesDormant: 2, ReferenceDropped: true}, MentionRowsLeaving: 7, KeysLosingBible: 1, Fingerprint: "ab12"}}
	}
	return d
}

func peopleFixture(owner, primary bool) peopleData {
	proj := ownerFixture(fixtureProjects()[0])
	if !owner {
		proj.Role = ops.ProjectEditor
	}
	d := peopleData{Project: proj, Slug: proj.Slug, CSRF: "tok_writer", IsOwner: owner, HasPrimary: primary, Values: map[string]string{}}
	if owner {
		d.ShowMembers = true
		d.Members = []projects.Member{{UserID: "usr_1", DisplayName: "Wren Writer", Email: "wren@example.com", Role: "owner"},
			{UserID: "usr_2", DisplayName: "Ciara Editor", Email: "ciara@example.com", Role: "editor", CompendiumRole: "editor"},
			{UserID: "usr_3", DisplayName: "Dara Reader", Email: "dara@example.com", Role: "reader"}}
		d.Result = grantWords["not_compendium_owner"]
		d.SavedText = "Ciara Editor is on the project."
	}
	return d
}

func compNewFixture(errs bool) compNewData {
	d := compNewData{CSRF: "tok_writer", Values: map[string]string{"kind": "fiction", "starter": "1"}, Errors: map[string]string{},
		Projects: []projects.Project{ownerFixture(fixtureProjects()[0])}}
	if errs {
		d.Values["name"] = " "
		d.Errors["name"] = "Give the Compendium a name."
		d.Errors["form"] = "Something went wrong."
	}
	return d
}

func compPurgeFixture(withError bool) compPurgeData {
	c := compFixture("owner")
	c.Trashed = true
	d := compPurgeData{C: c, CSRF: "tok_writer", Detail: compendiums.Detail{Compendium: c, HiddenProjects: 2,
		Projects: []compendiums.ProjectUse{{ID: "prj_a", Title: "The Salt Road", Relation: "primary"}, {ID: "prj_d", Title: "Hedgerow", Relation: "reference"}}}}
	if withError {
		d.Error, d.Typed = "The name does not match.", "The Salt"
	}
	return d
}
