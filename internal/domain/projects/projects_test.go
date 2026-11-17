package projects_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"The Salt Road":            "the-salt-road",
		"Scríobh: a Quiet Room!":   "scriobh-a-quiet-room",
		"  --Ünïcode & Ça va--  ":  "unicode-ca-va",
		"???":                      "project",
		"日本語":                      "project",
		strings.Repeat("abc ", 40): strings.Repeat("abc-", 14) + "abc",
		"Chapter 12 — The Harbour": "chapter-12-the-harbour",
	}
	for in, want := range cases {
		if got := projects.Slugify(in); got != want {
			t.Errorf("projects.Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateListGetAndSlugCollisions(t *testing.T) {
	d := testutil.DB(t)
	reg := registry.Build(d, nil, "")
	ctx := context.Background()
	owner := ops.Principal{UserID: testutil.User(t, d, "writer", "o@test.local"), Role: ops.RoleWriter}
	other := ops.Principal{UserID: testutil.User(t, d, "writer", "x@test.local"), Role: ops.RoleWriter}

	var slugs []string
	for i := 0; i < 3; i++ {
		out, err := reg.Call(ctx, owner, "project.create", projects.CreateIn{Title: "Atlas", Kind: "novel", WordGoal: 80000})
		if err != nil {
			t.Fatal(err)
		}
		p := out.(projects.Item).Item
		if p.Role != ops.ProjectOwner || p.Status != "idea" || p.Kind != "novel" {
			t.Errorf("created %+v", p)
		}
		slugs = append(slugs, p.Slug)
	}
	if slugs[0] != "atlas" || slugs[1] != "atlas-2" || slugs[2] != "atlas-3" {
		t.Errorf("slugs = %v", slugs)
	}

	out, _ := reg.Call(ctx, owner, "project.list", projects.ListIn{})
	if n := len(out.(projects.ListOut).Items); n != 3 {
		t.Errorf("owner sees %d projects", n)
	}
	out, _ = reg.Call(ctx, owner, "project.list", projects.ListIn{Q: "tla"})
	if n := len(out.(projects.ListOut).Items); n != 3 {
		t.Errorf("filter found %d", n)
	}
	out, _ = reg.Call(ctx, owner, "project.list", projects.ListIn{Q: "100%_"})
	if n := len(out.(projects.ListOut).Items); n != 0 {
		t.Errorf("LIKE wildcards must be escaped, found %d", n)
	}
	// Slugs are per owner: another person choosing the same title learns nothing.
	o2, err := reg.Call(ctx, other, "project.create", projects.CreateIn{Title: "Atlas"})
	if err != nil || o2.(projects.Item).Item.Slug != "atlas" {
		t.Errorf("a second owner must get the plain slug, got %v %v", o2, err)
	}
	if id, ok := projects.IDBySlug(ctx, d, other.UserID, "atlas"); !ok || id != o2.(projects.Item).Item.ID {
		t.Error("slug lookup must resolve among the caller's own projects")
	}
	out, _ = reg.Call(ctx, other, "project.list", projects.ListIn{})
	if n := len(out.(projects.ListOut).Items); n != 1 {
		t.Errorf("a stranger sees %d projects, want only their own", n)
	}
}

func TestCreateValidation(t *testing.T) {
	cases := []struct {
		name  string
		in    projects.CreateIn
		field string
	}{
		{"empty title", projects.CreateIn{Title: "   "}, "title"},
		{"long title", projects.CreateIn{Title: strings.Repeat("a", 201)}, "title"},
		{"bad kind", projects.CreateIn{Title: "x", Kind: "epic"}, "kind"},
		{"negative goal", projects.CreateIn{Title: "x", WordGoal: -1}, "word_goal"},
		{"off-palette colour", projects.CreateIn{Title: "x", CoverColor: "#123456"}, "cover_color"},
		{"markup in language", projects.CreateIn{Title: "x", Language: "en<script>"}, "language"},
	}
	for _, tc := range cases {
		in := tc.in
		err := in.Validate()
		if e, ok := err.(*ops.Error); !ok || e.Field != tc.field {
			t.Errorf("%s: got %v, want invalid on %s", tc.name, err, tc.field)
		}
	}
	in := projects.CreateIn{Title: "  Fine  "}
	if err := in.Validate(); err != nil || in.Title != "Fine" || in.Kind != "blank" || in.Language != "en" || in.CoverColor != projects.CoverPalette[0] {
		t.Errorf("defaults not applied: %+v %v", in, err)
	}
}

func TestRoleFunc(t *testing.T) {
	d := testutil.DB(t)
	reg := registry.Build(d, nil, "")
	ctx := context.Background()
	owner := testutil.User(t, d, "writer", "o@test.local")
	admin := testutil.User(t, d, "admin", "a@test.local")
	stranger := testutil.User(t, d, "writer", "s@test.local")
	out, _ := reg.Call(ctx, ops.Principal{UserID: owner, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "P"})
	id := out.(projects.Item).Item.ID
	role := projects.RoleFunc(d)
	if r, ok := role(ctx, owner, id); !ok || r != ops.ProjectOwner {
		t.Errorf("owner: %s %v", r, ok)
	}
	if r, ok := role(ctx, admin, id); !ok || r != ops.ProjectOwner {
		t.Errorf("admin acts as owner: %s %v", r, ok)
	}
	if _, ok := role(ctx, stranger, id); ok {
		t.Error("a stranger must have no role")
	}
	if _, ok := role(ctx, admin, "prj_missing"); ok {
		t.Error("a missing project has no role, even for an admin")
	}
}
