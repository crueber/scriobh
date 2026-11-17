package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// The key sweep: every registered op, called with real API keys (made with
// key.create, sent as Bearer tokens over REST) pinned to a project and pinned to
// a Compendium, at every scope the owner may grant, against the pinned
// resource's neighbours (a Compendium the project references, an entity it
// links, another project that shares its Compendium) and against a second
// person's resources.
//
// The allowed set is derived from each op's declared auth kind, not listed by
// hand per op:
//
//   - an instance-wide op (writer, admin, authenticated) is out of reach of any
//     pinned key, except the ops named in pinnedMayCall;
//   - a project op is in reach of a project key for its own project (and the
//     documents in it) and of no Compendium key for anything;
//   - a Compendium op is in reach of a Compendium key for its own Compendium
//     (and its entities and events), and of a project key for the project's
//     primary Compendium only.
//
// Everything else must answer 404 for an id outside the allowed set (403 when
// the key's scope is too low for the op, which is judged first), and no response
// to a pinned key may carry the name or id of anything it cannot reach.

// pinnedMayCall lists the ops that are not project or Compendium ops and that a
// pinned key may still call. It is empty on purpose: a pinned key reaches its
// resource and nothing instance-wide. Adding an op here is a decision.
var pinnedMayCall = map[string]string{}

var scopeOrder = map[string]int{ops.ScopeRead: 1, ops.ScopeWrite: 2, ops.ScopeAdmin: 3}

type cand struct {
	id       string
	projKey  bool // reachable by a key pinned to the project
	compKey  bool // reachable by a key pinned to the Compendium
	describe string
}

type sweepWorld struct {
	s         *Server
	owner     caller
	ownerP    ops.Principal
	universes map[string][]cand // by the path segment that precedes the id
	leaksProj []string          // what a project key must never see in any response
	leaksComp []string          // what a Compendium key must never see
	projKeys  map[string]string // scope -> secret
	compKeys  map[string]string
}

func buildSweepWorld(t *testing.T, s *Server, ownerRole, tag string, foreign caller, foreignP ops.Principal) *sweepWorld {
	t.Helper()
	ctx := context.Background()
	owner := s.newCaller(t, "sweep-owner-"+tag, ownerRole)
	p := ops.Principal{UserID: owner.userID, Role: ownerRole}
	call := func(pr ops.Principal, op string, in any) any {
		t.Helper()
		out, err := s.Reg.Call(ctx, pr, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	name := func(s string) string { return s + " " + tag }
	cmp := func(pr ops.Principal, n string) string {
		return call(pr, "compendium.create", compendiums.CreateIn{Name: n}).(compendiums.Item).Item.ID
	}
	ent := func(pr ops.Principal, cid, n string) string {
		return call(pr, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: n}).(entities.Item).Item.ID
	}
	ev := func(pr ops.Principal, cid, n string) string {
		return call(pr, "timeline.event.create", timeline.CreateIn{ID: cid, Title: n, StoryDate: "1990"}).(timeline.EventItem).Item.ID
	}
	proj := func(pr ops.Principal, n string) string {
		return call(pr, "project.create", projects.CreateIn{Title: n}).(projects.Item).Item.ID
	}
	doc := func(pr ops.Principal, pid, n string) string {
		return call(pr, "doc.create", docs.CreateIn{ProjectID: pid, Title: n, Content: "Words for " + n}).(docs.Item).Item.ID
	}
	names := map[string]string{
		"one": name("Primary Bible"), "two": name("Referenced Bible"), "three": name("Cameo Bible"),
		"ada": name("Primary Hero"), "bea": name("Referenced Hero"), "cal": name("Cameo Hero"),
		"evOne": name("Primary Event"), "evTwo": name("Referenced Event"), "evThree": name("Cameo Event"),
		"p": name("Pinned Book"), "sib": name("Sibling Book"),
		"dp": name("Pinned Chapter"), "ds": name("Sibling Chapter"),
	}
	one, two, three := cmp(p, names["one"]), cmp(p, names["two"]), cmp(p, names["three"])
	ada, bea, cal := ent(p, one, names["ada"]), ent(p, two, names["bea"]), ent(p, three, names["cal"])
	evOne, evTwo, evThree := ev(p, one, names["evOne"]), ev(p, two, names["evTwo"]), ev(p, three, names["evThree"])
	pid, sib := proj(p, names["p"]), proj(p, names["sib"])
	call(p, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: one})
	call(p, "project.compendium.set", compendiums.SetIn{ID: sib, CompendiumID: one})
	call(p, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: two})
	call(p, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: cal})
	dp, ds := doc(p, pid, names["dp"]), doc(p, sib, names["ds"])
	// The second person's: a Compendium and a project of their own.
	fnames := map[string]string{"c": "Foreign Bible " + tag, "e": "Foreign Hero " + tag, "ev": "Foreign Event " + tag, "p": "Foreign Book " + tag, "d": "Foreign Chapter " + tag}
	fc := cmp(foreignP, fnames["c"])
	fe, fev := ent(foreignP, fc, fnames["e"]), ev(foreignP, fc, fnames["ev"])
	fp := proj(foreignP, fnames["p"])
	call(foreignP, "project.compendium.set", compendiums.SetIn{ID: fp, CompendiumID: fc})
	fd := doc(foreignP, fp, fnames["d"])

	w := &sweepWorld{s: s, owner: owner, ownerP: p, projKeys: map[string]string{}, compKeys: map[string]string{}}
	w.universes = map[string][]cand{
		"projects":    {{pid, true, false, "the pinned project"}, {sib, false, false, "a sibling project"}, {fp, false, false, "a foreign project"}},
		"docs":        {{dp, true, false, "a document of the pinned project"}, {ds, false, false, "a document of the sibling"}, {fd, false, false, "a foreign document"}},
		"compendiums": {{one, true, true, "the primary Compendium"}, {two, false, false, "a referenced Compendium"}, {three, false, false, "the Compendium of a linked entity"}, {fc, false, false, "a foreign Compendium"}},
		"entities":    {{ada, true, true, "an entity of the primary"}, {bea, false, false, "an entity of a referenced Compendium"}, {cal, false, false, "a linked entity"}, {fe, false, false, "a foreign entity"}},
		"events":      {{evOne, true, true, "an event of the primary"}, {evTwo, false, false, "an event of a referenced Compendium"}, {evThree, false, false, "an event of a linked entity's Compendium"}, {fev, false, false, "a foreign event"}},
	}
	// What a project key must never see: everything that is not the project, its documents and its primary
	// Compendium (and the Compendium key's: everything that is not the Compendium).
	for _, k := range []string{"two", "three", "bea", "cal", "evTwo", "evThree", "sib", "ds"} {
		w.leaksProj = append(w.leaksProj, names[k])
	}
	for _, k := range []string{"two", "three", "bea", "cal", "evTwo", "evThree", "sib", "ds", "p", "dp"} {
		w.leaksComp = append(w.leaksComp, names[k])
	}
	for _, f := range fnames {
		w.leaksProj = append(w.leaksProj, f)
		w.leaksComp = append(w.leaksComp, f)
	}
	for _, c := range w.universes["compendiums"][1:] {
		w.leaksProj, w.leaksComp = append(w.leaksProj, c.id), append(w.leaksComp, c.id)
	}
	for _, c := range w.universes["projects"][1:] {
		w.leaksProj, w.leaksComp = append(w.leaksProj, c.id), append(w.leaksComp, c.id)
	}
	w.leaksComp = append(w.leaksComp, pid)
	// Keys, made the way an agent's are: through key.create over REST.
	scopes := []string{ops.ScopeRead, ops.ScopeWrite}
	if ownerRole == ops.RoleAdmin {
		scopes = append(scopes, ops.ScopeAdmin)
	}
	for _, sc := range scopes {
		w.projKeys[sc], _ = s.mintKey(t, owner, fmt.Sprintf(`{"name":"sweep project %s","scope":%q,"project_id":%q}`, sc, sc, pid))
		w.compKeys[sc], _ = s.mintKey(t, owner, fmt.Sprintf(`{"name":"sweep compendium %s","scope":%q,"compendium_id":%q}`, sc, sc, one))
	}
	if ownerRole != ops.RoleAdmin {
		if rec := s.do(owner, "POST", "/api/v1/keys", fmt.Sprintf(`{"name":"too strong","scope":"admin","project_id":%q}`, pid)); rec.Code != http.StatusForbidden {
			t.Errorf("a writer minting an admin-scope pinned key: %d", rec.Code)
		}
	}
	_ = foreign
	return w
}

var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// segmentOf is the path segment before the placeholder.
func segmentOf(path, param string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "{"+param+"}" && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}

func TestKeySweep(t *testing.T) {
	s := newAuthzServer(t)
	foreign := s.newCaller(t, "sweep-foreign", ops.RoleWriter)
	foreignP := ops.Principal{UserID: foreign.userID, Role: ops.RoleWriter}
	worlds := []*sweepWorld{
		buildSweepWorld(t, s, ops.RoleWriter, "w", foreign, foreignP),
		// An administrator acts as owner of everything, a second person's resources
		// included: the pin is then the only thing between the key and all of it.
		buildSweepWorld(t, s, ops.RoleAdmin, "a", foreign, foreignP),
	}
	entries := s.Reg.Entries()
	if len(entries) < 200 {
		t.Fatalf("only %d ops", len(entries))
	}
	checked := 0
	for _, w := range worlds {
		for _, kind := range []string{"project", "compendium"} {
			keys := w.projKeys
			leaks := w.leaksProj
			if kind == "compendium" {
				keys, leaks = w.compKeys, w.leaksComp
			}
			scopes := make([]string, 0, len(keys))
			for sc := range keys {
				scopes = append(scopes, sc)
			}
			sort.Strings(scopes)
			for _, sc := range scopes {
				secret := keys[sc]
				t.Run(fmt.Sprintf("%s key/%s/owner %s", kind, sc, w.ownerP.Role), func(t *testing.T) {
					for _, e := range entries {
						checked += w.sweepOp(t, e, kind, sc, secret, leaks)
					}
				})
			}
		}
	}
	if checked < 5000 {
		t.Errorf("only %d calls were made; did the op list or the universes shrink?", checked)
	}
}

// sweepOp calls one op with one key against every candidate and checks the
// answers. It returns how many calls it made.
func (w *sweepWorld) sweepOp(t *testing.T, e *ops.Entry, keyKind, scope, secret string, leaks []string) int {
	t.Helper()
	s := w.s
	body := "{}"
	if e.Op.Method == http.MethodGet || e.Op.Method == http.MethodDelete {
		body = ""
	}
	enough := scopeOrder[scope] >= scopeOrder[e.Op.Scope]
	check := func(path string, wantAllowedKind bool, what string) {
		t.Helper()
		rec := s.doKey(secret, e.Op.Method, path, body)
		got := rec.Code
		text := rec.Body.String()
		for _, l := range leaks {
			if strings.Contains(text, l) {
				t.Errorf("%s as a %s key (%s scope): %s %s names %q: %s", e.Op.Name, keyKind, scope, e.Op.Method, path, l, text)
			}
		}
		if wantAllowedKind {
			return // in reach: the answer depends on the op; only the leak check applies
		}
		switch {
		case !enough && got != http.StatusForbidden && got != http.StatusNotFound:
			t.Errorf("%s (%s) as a %s key (%s scope) on %s: %d, want 403 or 404", e.Op.Name, e.Op.Scope, keyKind, scope, what, got)
		case enough && got != http.StatusNotFound && got != http.StatusForbidden:
			t.Errorf("%s as a %s key (%s scope) on %s: %s %s = %d, want 404: %s", e.Op.Name, keyKind, scope, what, e.Op.Method, path, got, text)
		case enough && got == http.StatusForbidden && e.Op.Auth.Kind != ops.AuthProject && e.Op.Auth.Kind != ops.AuthCompendium:
			// instance-wide ops: forbidden is the right answer (below)
		case enough && got == http.StatusForbidden:
			t.Errorf("%s as a %s key (%s scope) on %s: 403 where a foreign id must look missing (404): %s", e.Op.Name, keyKind, scope, what, text)
		}
	}
	n := 0
	if e.Op.Auth.SessionOnly {
		// For a person in a browser: any key is refused outright, before any id is looked at.
		universe := []cand{{id: bogusFor(""), describe: "an instance-wide route"}} // no path parameter: one call
		if m := placeholder.FindStringSubmatch(e.Op.Path); m != nil {
			universe = w.universes[segmentOf(e.Op.Path, m[1])]
		}
		for _, c := range universe {
			path := placeholder.ReplaceAllString(e.Op.Path, c.id)
			if rec := s.doKey(secret, e.Op.Method, path, body); rec.Code != http.StatusForbidden {
				t.Errorf("%s is session-only but a %s key (%s scope) got %d on %s", e.Op.Name, keyKind, scope, rec.Code, c.describe)
			}
			n++
		}
		return n
	}
	switch e.Op.Auth.Kind {
	case ops.AuthProject, ops.AuthCompendium:
		params := placeholder.FindAllStringSubmatch(e.Op.Path, -1)
		if len(params) == 0 {
			t.Fatalf("%s: no path parameter", e.Op.Name)
		}
		first := params[0][1]
		seg := segmentOf(e.Op.Path, first)
		universe, ok := w.universes[seg]
		if !ok {
			t.Errorf("%s: no universe of %q ids; add one to the key sweep", e.Op.Name, seg)
			return 0
		}
		for _, c := range universe {
			path := e.Op.Path
			for i, m := range params {
				id := c.id
				if i > 0 {
					id = bogusFor(m[1])
				}
				path = strings.Replace(path, m[0], id, 1)
			}
			reach := c.projKey
			if keyKind == "compendium" {
				reach = c.compKey
			}
			// A project op is in reach of a Compendium key for nothing; a Compendium op of a
			// project key only for the primary (the candidate's projKey says so).
			if keyKind == "compendium" && e.Op.Auth.Kind == ops.AuthProject {
				reach = false
			}
			if keyKind == "project" && e.Op.Auth.Kind == ops.AuthCompendium && !c.projKey {
				reach = false
			}
			if keyKind == "project" && e.Op.Auth.Kind == ops.AuthProject && (seg == "compendiums" || seg == "entities" || seg == "events") {
				reach = false
			}
			check(path, reach, c.describe)
			n++
		}
	default: // writer, admin, authenticated: instance-wide
		if _, ok := pinnedMayCall[e.Op.Name]; ok {
			return 0
		}
		path := placeholder.ReplaceAllStringFunc(e.Op.Path, func(m string) string { return bogusFor(m[1 : len(m)-1]) })
		rec := s.doKey(secret, e.Op.Method, path, body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s (%s) as a %s key (%s scope): %d, want 403: %s", e.Op.Name, e.Op.Auth.Kind, keyKind, scope, rec.Code, rec.Body)
		}
		for _, l := range leaks {
			if strings.Contains(rec.Body.String(), l) {
				t.Errorf("%s names %q to a pinned key", e.Op.Name, l)
			}
		}
		n++
	}
	return n
}

func bogusFor(param string) string {
	switch param {
	case "att_id":
		return "att_00000000000000000000000000"
	case "note_id":
		return "not_00000000000000000000000000"
	case "tag_id":
		return "tag_00000000000000000000000000"
	case "sid":
		return "ses_00000000000000000000000000"
	case "comment_id":
		return "cmt_00000000000000000000000000"
	case "suggestion_id":
		return "sug_00000000000000000000000000"
	case "share_id":
		return "shl_00000000000000000000000000"
	}
	return "xxx_00000000000000000000000000"
}
