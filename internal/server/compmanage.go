package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// Merging a Compendium into another, splitting one into a new Compendium, and the record each leaves (F-478).
// Every step is a form that works without script: the first posts the choices to a preview op and draws what would
// change; the second sends the same choices with the preview's fingerprint, and the op refuses (409 plan_changed) unless
// nothing it depends on moved in between, when the page draws a fresh preview and says so. A merge or a split cannot
// be undone by any op, so the confirmation is the Compendium's name, typed, as for deleting a project; the page is for
// the Compendium's owner, as the ops are, and nothing here is reachable by an API key.

func (s *Server) registerCompendiumManage(m *http.ServeMux) {
	m.HandleFunc("GET /c/{slug}/manage", s.cManagePage)
	m.HandleFunc("GET /c/{slug}/changes", s.cChangesPage)
	m.HandleFunc("GET /c/{slug}/changes/{cid}", s.cChangePage)
	s.uiRoute("POST /ui/compendiums/{id}/manage/{action}", s.uiCManage)
}

type cmRes struct{ Action, New, Into string }

// fcRow is one custom field (or a project's override) that two entities hold differently when a clash is settled by merging.
type fcRow struct {
	Key, Label, Source, Target, Choice string
}

// cmItem is an entity that needs the person's attention: it clashes with a name in the target, or its answer is in the plan.
type cmItem struct {
	entities.MoveItem
	Res       cmRes
	Conflicts []fcRow
}

type cmData struct {
	C        compendiums.Compendium
	Slug     string
	CSRF     string
	Targets  []compendiums.Compendium
	Entities []entities.Entry
	Total    int
	Q, Kind  string
	More     bool

	Op                        string // "", "merge" or "split"
	Plan                      *entities.MovePlan
	IntoID, IntoName, NewName string
	Desc                      string
	Picked                    map[string]bool
	Crossing, KeyPins         string
	Follow                    bool
	Attention                 []cmItem
	Moving                    []string
	MoreMoving                int
	CanConfirm                bool
	Typed                     string
	Error, Field, Stale       string
	Open                      string // which section starts open
	Saved                     string
}

func (s *Server) cManageOwner(w http.ResponseWriter, r *http.Request, c compendiums.Compendium) bool {
	if c.Role != "owner" || c.Trashed {
		msg := s.tr(r, "cmg.only_owner")
		if c.Trashed {
			msg = s.tr(r, "cmg.trashed")
		}
		s.fail(w, r, ops.Forbidden(msg))
		return false
	}
	return true
}

func (s *Server) cManageBase(r *http.Request, c compendiums.Compendium) cmData {
	d := cmData{C: c, Slug: c.Slug, CSRF: authFrom(r.Context()).CSRF, Picked: map[string]bool{}, Crossing: "refuse", KeyPins: "refuse"}
	if lo, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{}); err == nil {
		for _, o := range s.addressedAll(r, lo.(compendiums.ListOut).Items) {
			if o.ID != c.ID && o.Role == "owner" && !o.Trashed {
				d.Targets = append(d.Targets, o)
			}
		}
	}
	return d
}

// cManageEntities fills the list a split chooses from: at most 400 entities, narrowed by kind and by words.
func (s *Server) cManageEntities(r *http.Request, d *cmData) {
	q, kind := strings.TrimSpace(r.FormValue("q")), r.FormValue("kind")
	d.Q, d.Kind = q, kind
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.list", entities.ListIn{ID: d.C.ID, Q: q, Kind: kind, Limit: 400})
	if err != nil {
		return
	}
	lo := out.(entities.ListOut)
	d.Entities, d.Total, d.More = lo.Items, lo.Total, lo.NextCursor != ""
}

func (s *Server) cManagePage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok || !s.cManageOwner(w, r, c) {
		return
	}
	d := s.cManageBase(r, c)
	s.cManageEntities(r, &d)
	d.Open = r.URL.Query().Get("open")
	s.render(w, r, http.StatusOK, "compendium-manage", s.page(r, s.tr(r, "cmg.title"), d))
}

// moveOptsFrom reads the choices a preview form carries.
func moveOptsFrom(r *http.Request) (compendiums.MoveOpts, map[string]cmRes, map[string]map[string]string) {
	o := compendiums.MoveOpts{Crossing: r.PostFormValue("crossing"), KeyPins: r.PostFormValue("key_pins"), FollowProjects: r.PostFormValue("follow") == "1"}
	o.EntityIDs = r.PostForm["entity_id"]
	res := map[string]cmRes{}
	fcs := map[string]map[string]string{}
	for k, v := range r.PostForm {
		if len(v) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(k, "res."):
			x := res[k[4:]]
			x.Action = v[0]
			res[k[4:]] = x
		case strings.HasPrefix(k, "new."):
			x := res[k[4:]]
			x.New = v[0]
			res[k[4:]] = x
		case strings.HasPrefix(k, "into."):
			x := res[k[5:]]
			x.Into = v[0]
			res[k[5:]] = x
		case strings.HasPrefix(k, "fc."):
			rest := k[3:]
			i := strings.IndexByte(rest, '.')
			if i > 0 && (v[0] == "source" || v[0] == "target") {
				if fcs[rest[:i]] == nil {
					fcs[rest[:i]] = map[string]string{}
				}
				fcs[rest[:i]][rest[i+1:]] = v[0]
			}
		}
	}
	if len(res) > 0 {
		o.Resolutions = map[string]entities.Resolution{}
	}
	for id, x := range res {
		if x.Action == "" {
			continue // not answered
		}
		o.Resolutions[id] = entities.Resolution{Action: x.Action, NewName: x.New, IntoID: x.Into, FieldChoices: fcs[id]}
	}
	return o, res, fcs
}

// cManagePreview asks the preview op the form's question and draws the answer. stale says the confirmation was refused
// because the plan moved; errMsg is a message about one choice.
func (s *Server) cManagePreview(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, op, errMsg, stale string) {
	d := s.cManageBase(r, c)
	d.Op, d.Open = op, op
	opts, res, fcs := moveOptsFrom(r)
	d.Crossing, d.KeyPins, d.Follow = firstNonEmptyStr(opts.Crossing, "refuse"), firstNonEmptyStr(opts.KeyPins, "refuse"), opts.FollowProjects
	for _, id := range opts.EntityIDs {
		d.Picked[id] = true
	}
	d.IntoID, d.NewName, d.Desc = r.PostFormValue("into_id"), strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("description")
	d.Error, d.Stale, d.Typed = errMsg, stale, ""
	var out any
	var err error
	if op == "merge" {
		out, err = s.Reg.Call(r.Context(), s.principal(r), "compendium.merge.preview", compendiums.MergeIn{ID: c.ID, IntoID: d.IntoID, MoveOpts: opts})
	} else {
		out, err = s.Reg.Call(r.Context(), s.principal(r), "compendium.split.preview", compendiums.SplitIn{ID: c.ID, Name: d.NewName, Description: d.Desc, MoveOpts: opts})
	}
	status := http.StatusOK
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeForbidden && e.Code != ops.CodeNotFound {
			s.fail(w, r, err)
			return
		}
		d.Error, d.Field, status = e.Message, e.Field, http.StatusUnprocessableEntity
		d.Op = ""
		d.Open = op
		s.cManageEntities(r, &d)
		s.render(w, r, status, "compendium-manage", s.page(r, s.tr(r, "cmg.title"), d))
		return
	}
	pl := out.(compendiums.PlanOut).Plan
	d.Plan = &pl
	d.Slug = c.Slug
	for _, t := range d.Targets {
		if t.ID == d.IntoID {
			d.IntoName = t.Name
		}
	}
	unres := map[string]bool{}
	for _, u := range pl.Unresolved {
		unres[u] = true
	}
	for _, it := range pl.Items {
		if len(it.Clashes) == 0 && it.Merge == nil && it.Action == "move" {
			if len(d.Moving) < 200 {
				d.Moving = append(d.Moving, it.Name)
			} else {
				d.MoreMoving++
			}
			continue
		}
		ci := cmItem{MoveItem: it, Res: res[it.EntityID]}
		if it.Merge != nil {
			for _, k := range it.Merge.FieldConflicts {
				ci.Conflicts = append(ci.Conflicts, fcRow{Key: k.Key, Label: k.Label, Source: k.Source, Target: k.Target, Choice: fcs[it.EntityID][k.Key]})
			}
			for _, k := range it.Merge.OverrideConflicts {
				label := s.tr(r, "cmg.override_of")
				for _, pe := range pl.Projects {
					if pe.ProjectID == k.ProjectID {
						label = s.tr(r, "cmg.override_in", "title", pe.Title)
					}
				}
				ci.Conflicts = append(ci.Conflicts, fcRow{Key: k.Key, Label: label, Choice: fcs[it.EntityID][k.Key]})
			}
		}
		d.Attention = append(d.Attention, ci)
	}
	sort.SliceStable(d.Attention, func(i, j int) bool { return unres[d.Attention[i].EntityID] && !unres[d.Attention[j].EntityID] })
	d.CanConfirm = len(pl.Unresolved) == 0 && len(pl.Blocking) == 0 && pl.Moving > 0
	s.render(w, r, status, "compendium-manage", s.page(r, s.tr(r, "cmg.title"), d))
}

func (s *Server) uiCManage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumByID(w, r, r.PathValue("id"))
	if !ok || !s.cManageOwner(w, r, c) {
		return
	}
	p, ctx := s.principal(r), r.Context()
	switch action := r.PathValue("action"); action {
	case "merge-preview":
		s.cManagePreview(w, r, c, "merge", "", "")
	case "split-preview":
		s.cManagePreview(w, r, c, "split", "", "")
	case "merge", "split":
		op := action
		typed := strings.TrimSpace(r.PostFormValue("confirm_name"))
		want := c.Name
		if op == "split" {
			want = strings.TrimSpace(r.PostFormValue("name"))
		}
		if typed != want || want == "" {
			s.cManagePreview(w, r, c, op, s.tr(r, "cmg.type_mismatch"), "")
			return
		}
		opts, _, _ := moveOptsFrom(r)
		fp := r.PostFormValue("fingerprint")
		var out any
		var err error
		if op == "merge" {
			out, err = s.Reg.Call(ctx, p, "compendium.merge", compendiums.MergeIn{ID: c.ID, IntoID: r.PostFormValue("into_id"), Fingerprint: fp, MoveOpts: opts})
		} else {
			out, err = s.Reg.Call(ctx, p, "compendium.split", compendiums.SplitIn{ID: c.ID, Name: want, Description: r.PostFormValue("description"), Fingerprint: fp, MoveOpts: opts})
		}
		if err != nil {
			e := ops.AsError(err)
			switch {
			case e.Code == ops.CodeConflict && e.Data["reason"] == "plan_changed":
				s.cManagePreview(w, r, c, op, "", s.tr(r, "compendium.manage.stale"))
			case e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict || e.Code == ops.CodeForbidden:
				s.cManagePreview(w, r, c, op, e.Message, "")
			default:
				s.fail(w, r, err)
			}
			return
		}
		ch := out.(compendiums.ChangeOut)
		http.Redirect(w, r, "/c/"+c.Slug+"/changes/"+ch.ChangeID+"?saved="+op, http.StatusSeeOther)
	default:
		s.fail(w, r, ops.Invalid("", "That form was not recognised."))
	}
}

// ---- the record ----------------------------------------------------------------------------------------

type cChangeRow struct {
	compendiums.Change
	When, Source, Target            string
	Moved, Staying, Merged, Dropped int
}

type cChangesData struct {
	C     compendiums.Compendium
	Slug  string
	Items []cChangeRow
}

// compName is the name of a Compendium the caller is on, or a general phrase for one they are not.
func (s *Server) compNames(r *http.Request) map[string]string {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.list", compendiums.ListIn{State: compendiums.StateAll})
	m := map[string]string{}
	if err != nil {
		return m
	}
	for _, c := range out.(compendiums.ListOut).Items {
		m[c.ID] = c.Name
	}
	return m
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func (s *Server) cChangesPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok || !s.cManageOwner(w, r, c) {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.change.list", compendiums.ChangeIn{ID: c.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	names, other := s.compNames(r), s.tr(r, "cmg.another")
	nm := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return other
	}
	d := cChangesData{C: c, Slug: c.Slug}
	for _, ch := range out.(compendiums.ChangeListOut).Items {
		d.Items = append(d.Items, cChangeRow{Change: ch, When: utcWhen(ch.CreatedAt), Source: nm(ch.SourceID), Target: nm(ch.TargetID),
			Moved: num(ch.Summary["moved"]), Staying: num(ch.Summary["staying"]), Merged: num(ch.Summary["merged"])})
	}
	s.render(w, r, http.StatusOK, "compendium-changes", s.page(r, s.tr(r, "cmg.changes_title"), d))
}

type cChangeData struct {
	C                     compendiums.Compendium
	Slug                  string
	Ch                    compendiums.Change
	When, Who, Src, Dst   string
	B                     entities.Bundle
	Saved                 string
	CanSplitBack          bool
	Moved, Staying        int
	DroppedRel, DroppedEv int
}

func (s *Server) cChangePage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok || !s.cManageOwner(w, r, c) {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.change.get", compendiums.ChangeGetIn{ID: c.ID, ChangeID: r.PathValue("cid")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ch := out.(compendiums.ChangeItem).Item
	d := cChangeData{C: c, Slug: c.Slug, Ch: ch, When: utcWhen(ch.CreatedAt), Saved: r.URL.Query().Get("saved")}
	_ = json.Unmarshal(ch.Before, &d.B)
	names, other := s.compNames(r), s.tr(r, "cmg.another")
	d.Src, d.Dst = firstNonEmptyStr(names[ch.SourceID], other), firstNonEmptyStr(names[ch.TargetID], other)
	d.Who = s.tr(r, "cmg.someone")
	if ch.ActorID != "" {
		if n := s.lookup(r.Context(), `SELECT display_name FROM users WHERE id = ?`, ch.ActorID); n != "" {
			d.Who = n
		}
	}
	d.Moved, d.Staying = num(ch.Summary["moved"]), num(ch.Summary["staying"])
	s.render(w, r, http.StatusOK, "compendium-change", s.page(r, s.tr(r, "cmg.record_title"), d))
}
