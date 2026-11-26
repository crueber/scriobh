package entities_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// The list and the lookup are answered by the database now. Each is checked, for every kind of caller and a spread of queries, against the
// whole-book read they replaced (entities.OracleList, OracleGet): the same entities, in the same order, with the same counts, the same
// Hidden flag, the same names, aliases, overrides and Editable, and the same refusals.
func TestProjectListAndLookupMatchTheWholeBookRead(t *testing.T) {
	x := newH(t)
	world := x.compendium(x.owner, "World")
	cosmos := x.compendium(x.owner, "Cosmology")
	cameos := x.compendium(x.owner, "Cameos")
	foreign := x.compendium(x.stranger, "Foreign")
	ada := x.ent(x.owner, world, "character", "Ada Lovelace")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: ada.ID, Alias: "Ada"})
	moon := x.ent(x.owner, cosmos, "place", "The Moon")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: moon.ID, Alias: "Luna"})
	eire := x.ent(x.owner, cosmos, "place", "Éire")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: eire.ID, Alias: "Ériu"})
	sam := x.ent(x.owner, cameos, "character", "Sam")
	key := x.ent(x.stranger, foreign, "item", "Foreign Key")
	gone := x.ent(x.owner, world, "item", "Old Coin")
	x.call(x.owner, "entity.trash", entities.StateIn{ID: gone.ID})
	kinds := []string{"character", "place", "item", "org", "concept"}
	var minor []entities.Entity
	for i := 0; i < 30; i++ {
		minor = append(minor, x.ent(x.owner, world, kinds[i%len(kinds)], fmt.Sprintf("Minor %02d", i)))
	}

	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: world})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: cosmos})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: sam.ID})
	x.projectMember(pid, x.stranger, ops.ProjectEditor)
	x.call(x.stranger, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: key.ID})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: moon.ID, Name: str("Luna's Moon"), Summary: str("Smaller here."), Notes: str("private")})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ada.ID, Summary: str("A mathematician.")})
	x.projectMember(pid, x.editor, ops.ProjectEditor)
	x.member(cosmos, x.editor, "reader")
	x.projectMember(pid, x.reader, ops.ProjectReader)

	pinned := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, Email: x.owner.Email, ProjectID: pid}
	principals := map[string]ops.Principal{"owner": x.owner, "editor": x.editor, "reader": x.reader, "stranger": x.stranger, "pinned key": pinned}
	queries := []entities.ProjectListIn{
		{ID: pid},
		{ID: pid, Limit: 5},
		{ID: pid, Limit: 5, Offset: 5},
		{ID: pid, Limit: 7, Offset: 27},
		{ID: pid, Limit: 500, Offset: 1000},
		{ID: pid, Limit: 500, Q: "lun"},
		{ID: pid, Limit: 500, Q: "ÉRI"},
		{ID: pid, Limit: 500, Q: "ériu"},
		{ID: pid, Limit: 500, Q: "  ada  "},
		{ID: pid, Limit: 500, Q: "moon"},
		{ID: pid, Limit: 500, Q: "s"},
		{ID: pid, Limit: 500, Q: "key"},
		{ID: pid, Limit: 500, Q: "zzz"},
		{ID: pid, Limit: 500, Q: "minor 1"},
		{ID: pid, Limit: 500, Kind: "place"},
		{ID: pid, Limit: 500, Kind: "character", Q: "a"},
		{ID: pid, Limit: 500, Compendium: cameos},
		{ID: pid, Limit: 500, Compendium: cosmos, Q: "e"},
		{ID: pid, Limit: 500, Compendium: foreign},
		{ID: pid, Limit: 4, Offset: 2, Kind: "item", Q: "m"},
	}
	for who, p := range principals {
		for _, in := range queries {
			if in.Limit == 0 { // the op's default page, as its validation sets it
				in.Limit = 100
			}
			want, err := entities.OracleList(x.ctx, x.d.Read(), p, in)
			if err != nil {
				t.Fatalf("%s: oracle: %v", who, err)
			}
			got, err := x.reg.Call(x.ctx, p, "project.entity.list", in)
			if err != nil {
				if ops.AsError(err).Code != ops.CodeNotFound {
					t.Fatalf("%s: list %+v: %v", who, in, err)
				}
				continue
			}
			if !reflect.DeepEqual(got, want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("%s: list %+v\n got %s\nwant %s", who, in, g, w)
			}
		}
	}

	refs := []string{ada.ID, moon.ID, eire.ID, sam.ID, key.ID, gone.ID, minor[3].ID, "Luna", "Ada", " ériu ", "Minor 03", "Luna's Moon", "nope", "ent_unknown"}
	for who, p := range principals {
		for _, ref := range refs {
			want, werr := entities.OracleGet(x.ctx, x.d.Read(), p, pid, ref)
			got, gerr := x.reg.Call(x.ctx, p, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: ref})
			switch {
			case werr != nil || gerr != nil:
				if werr == nil || gerr == nil || ops.AsError(werr).Code != ops.AsError(gerr).Code {
					t.Errorf("%s: get %q: got %v, want %v", who, ref, gerr, werr)
				}
			case !reflect.DeepEqual(got, want):
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(want)
				t.Errorf("%s: get %q\n got %s\nwant %s", who, ref, g, w)
			}
		}
	}
	// A reader of the project with no Compendium sees nothing of the book, and an entity of a Compendium nobody on the project may read is
	// not found by id.
	if _, err := x.reg.Call(x.ctx, x.reader, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: key.ID}); err == nil || ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("reader got an unreadable entity by id: %v", err)
	}
}
