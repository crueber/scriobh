package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The publish flow: connect an account at GitHub or Forgejo, choose or create a repository, review the plan, publish,
// and release an edition. It is its own page rather than a dialog: every step is a server-rendered page that survives a
// reload, a refused plan and a phone, and nothing changes anywhere until the last button of the plan. Everything goes
// through the repo.* ops (session only; the screens are their only path). A token is typed into a write-only field and
// is not in the page the form came back to, in a URL, a data attribute or the browser's storage.

func (s *Server) registerPublishUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/git/publish", s.publishPage)
	s.uiRoute("POST /ui/projects/{id}/publish/{action}", s.uiPublish)
	m.HandleFunc("GET /p/{slug}/git/release", s.releasePage)
	m.HandleFunc("GET /ui/projects/{id}/release/{rid}", s.releaseFragment)
	s.uiRoute("POST /ui/projects/{id}/release/{action}", s.uiRelease)
}

// ---- the publish page ------------------------------------------------------------------------------

type publishWithheld struct {
	Path, Title, Reason string
}

type publishBlocker struct{ Code, Text string }

type publishData struct {
	Pg        Page
	Project   projects.Project
	Slug      string
	CSRF      string
	Accounts  []gitmirror.Account
	Account   *gitmirror.Account
	Values    map[string]string
	Error     string
	Field     string
	Mode      string
	Repos     *gitmirror.RepoList
	ReposErr  string
	Page      int
	PrevPage  int
	Query     string
	Plan      *gitmirror.RepoPlan
	Size      string
	Withheld  []publishWithheld
	Blockers  []publishBlocker
	Incomplet *publishIncomplete
	Remotes   int
	CanStore  bool // the server can store a token
	Flash     string
	PlanID    string // the reviewed plan this page shows, by its id
}

// publishIncomplete is the truthful state after a repository was made and a later step failed.
type publishIncomplete struct {
	Message    string
	Repository string
	Owner      string
	Name       string
	Registered bool
}

var publishBlockerCodes = strings.Fields("name_taken repo_not_found repo_not_empty read_only archived public_full_mirror secret_in_tree mirror_not_ready too_many_remotes no_signing_key no_secret_key nothing_to_push remote_differs")

// publishBlockerText is the catalogue's sentence for a blocker's code, and the op's own for the three that name what to look at.
func publishBlockerText(T tfn, b gitmirror.Blocker) string {
	for _, c := range publishBlockerCodes {
		if c == b.Code {
			if c == "mirror_not_ready" || c == "secret_in_tree" || c == "remote_differs" {
				break
			}
			return T("git.publish.blocker_" + c)
		}
	}
	return b.Message
}

var publishWithheldReasons = map[string]string{"note": "git.publish.withheld_note", "do_not_compile": "git.publish.withheld_do_not_compile", "not_done": "git.publish.withheld_not_done",
	"cannot_be_cleaned": "git.publish.withheld_cannot_be_cleaned", "trashed": "git.publish.withheld_trashed", "stale_mirror": "git.publish.withheld_stale_mirror"}

// publishVal reads a field from the form that was posted, else from the address.
func publishVal(r *http.Request, k string) string {
	if v := r.PostFormValue(k); v != "" {
		return v
	}
	return r.URL.Query().Get(k)
}

// A link may carry which account, which of the two ways, the repository's owner and name and the list's page and search; it
// may not carry what the repository will hold or who can read it. Those are chosen on the form and kept on the server.
var publishURLFields = strings.Fields("account_id mode owner name q")

func publishLinkValues(r *http.Request) map[string]string {
	v := map[string]string{}
	for _, k := range publishURLFields {
		if x := r.URL.Query().Get(k); x != "" {
			v[k] = x
		}
	}
	return scrubValues(v)
}

// publishFormValues keeps what a person typed so the page comes back as it was. The token is never among them.
func publishFormValues(r *http.Request) map[string]string {
	v := map[string]string{}
	for _, k := range strings.Fields("account_id mode owner name description visibility branch path_prefix audience only_done include_notes sign_commits push_mode q provider host label") {
		if x := r.PostFormValue(k); x != "" {
			v[k] = x
		}
	}
	return scrubValues(v)
}

func publishValuesOf(in gitmirror.RepoPlanIn) map[string]string {
	v := map[string]string{"account_id": in.AccountID, "mode": in.Mode, "owner": in.Owner, "name": in.Name, "description": in.Description, "visibility": in.Visibility,
		"branch": in.Branch, "path_prefix": in.PathPrefix, "audience": in.Audience, "push_mode": in.PushMode}
	for k, on := range map[string]bool{"only_done": in.OnlyDone, "include_notes": in.IncludeNotes, "sign_commits": in.SignCommits} {
		if on {
			v[k] = "1"
		}
	}
	return v
}

// publishFlash is what a refused form hands to the page after its redirect.
type publishFlash struct {
	Error, Field string
	Values       map[string]string
	Incomplete   *publishIncomplete
	Notice       string
}

func (s *Server) publishStash(r *http.Request, f publishFlash) {
	if b, err := json.Marshal(f); err == nil {
		s.flashSet(r, map[string]string{"pf": string(b)})
	}
}

func (s *Server) publishTake(r *http.Request) (publishFlash, bool) {
	fl := s.flashTake(r)
	var f publishFlash
	if fl == nil || fl["pf"] == "" || json.Unmarshal([]byte(fl["pf"]), &f) != nil {
		return f, false
	}
	return f, true
}

func (s *Server) publishBase(w http.ResponseWriter, r *http.Request, proj projects.Project, vals map[string]string) (publishData, bool) {
	pg := s.page(r, "", nil)
	d := publishData{Pg: pg, Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, Values: vals, Mode: "create"}
	if d.Values["mode"] == "existing" {
		d.Mode = "existing"
	}
	if d.Values["visibility"] == "" {
		d.Values["visibility"] = "private"
	}
	if r.URL.Query().Get("connected") == "1" {
		d.Flash = pg.T("git.pub.connected")
	} else if r.URL.Query().Get("disconnected") == "1" {
		d.Flash = pg.T("git.pub.disconnected")
	}
	p := s.principal(r)
	ao, err := s.Reg.Call(r.Context(), p, "repo.account.list", struct{}{})
	if err != nil {
		s.fail(w, r, err)
		return d, false
	}
	d.Accounts = ao.(gitmirror.AccountList).Items
	for i := range d.Accounts {
		if d.Accounts[i].ID == d.Values["account_id"] {
			d.Account = &d.Accounts[i]
		}
	}
	if d.Account == nil && len(d.Accounts) == 1 {
		d.Account = &d.Accounts[0]
		d.Values["account_id"] = d.Account.ID
	}
	if lo, err := s.Reg.Call(r.Context(), p, "git.remote.list", gitmirror.ProjectIn{ProjectID: proj.ID}); err == nil {
		l := lo.(gitmirror.RemoteList)
		d.Remotes, d.CanStore = len(l.Items), l.Enabled
	}
	return d, true
}

func (s *Server) publishOwnerProject(w http.ResponseWriter, r *http.Request, byID bool) (projects.Project, bool) {
	var proj projects.Project
	var ok bool
	if byID {
		proj, ok = s.goalsProjectByID(w, r, r.PathValue("id"))
	} else {
		proj, ok = s.goalsProject(w, r)
	}
	if !ok {
		return proj, false
	}
	if !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden(s.tr(r, "git.err.owner")))
		return proj, false
	}
	return proj, true
}

// publishList reads one page of the account's repositories for the "use an empty repository" choice.
func (s *Server) publishList(r *http.Request, d *publishData) {
	if d.Account == nil || d.Mode != "existing" {
		return
	}
	d.Page = atoi(publishVal(r, "page"))
	if d.Page < 1 {
		d.Page = 1
	}
	d.Query = strings.TrimSpace(d.Values["q"])
	out, err := s.Reg.Call(r.Context(), s.principal(r), "repo.list", gitmirror.RepoListIn{ProjectID: d.Project.ID, AccountID: d.Account.ID, Page: d.Page, Query: d.Query})
	if err != nil {
		d.ReposErr = publishErrorText(d.Pg.T, err)
		return
	}
	l := out.(gitmirror.RepoList)
	d.Repos = &l
	if d.Page > 1 {
		d.PrevPage = d.Page - 1
	}
}

// publishErrorText is what a refused or failed op says, in words: the host's rate limit with its wait, otherwise the op's
// own sentence (a fixed sentence for a status; nothing the host said is in it).
func publishErrorText(T tfn, err error) string {
	var oe *ops.Error
	if errors.As(err, &oe) {
		if oe.Code == ops.CodeRateLimited {
			return T("git.publish.rate_limited", "n", oe.RetryAfterSeconds)
		}
		return oe.Message
	}
	return T("git.pub.failed")
}

func (s *Server) publishPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.publishOwnerProject(w, r, false)
	if !ok {
		return
	}
	vals := publishLinkValues(r)
	var flash publishFlash
	hasFlash := false
	if f, ok := s.publishTake(r); ok {
		flash, hasFlash = f, true
		for k, v := range f.Values { // what was typed in the form that was refused, back in its fields
			vals[k] = v
		}
	}
	var in gitmirror.RepoPlanIn
	planID := r.URL.Query().Get("plan")
	planFound := false
	if planID != "" && !hasFlash {
		if in, planFound = s.planGet(r, planID); planFound && in.ProjectID == proj.ID {
			for k, v := range publishValuesOf(in) {
				vals[k] = v
			}
		} else {
			planFound = false
		}
	} else if planID != "" {
		if in, planFound = s.planGet(r, planID); planFound && in.ProjectID == proj.ID {
			for k, v := range publishValuesOf(in) {
				if _, typed := vals[k]; !typed {
					vals[k] = v
				}
			}
		} else {
			planFound = false
		}
	}
	d, ok := s.publishBase(w, r, proj, vals)
	if !ok {
		return
	}
	T := d.Pg.T
	if hasFlash {
		d.Error, d.Field, d.Incomplet = flash.Error, flash.Field, flash.Incomplete
		if flash.Notice != "" {
			d.Flash = flash.Notice
		}
	}
	if planID != "" && !planFound && !hasFlash {
		d.Error = T("git.pub.plan_expired")
	}
	if planFound {
		d.PlanID = planID
		po, err := s.Reg.Call(r.Context(), s.principal(r), "repo.plan", in)
		if err != nil {
			if e := ops.AsError(err); e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict || e.Code == ops.CodeRateLimited || e.Code == ops.CodeProvider {
				d.Error = publishErrorText(T, err)
				if e.Code != ops.CodeRateLimited {
					d.Field = e.Field
				}
			} else {
				s.fail(w, r, err)
				return
			}
		} else {
			s.publishFillPlan(&d, po.(gitmirror.RepoPlan))
			if d.Error == "" {
				d.Error = ""
			}
		}
	}
	s.publishList(r, &d)
	s.publishRender(w, r, http.StatusOK, d)
}

func (s *Server) publishRender(w http.ResponseWriter, r *http.Request, status int, d publishData) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "publish", s.page(r, d.Pg.T("git.publish.title"), d))
}

// publishInput reads the plan's fields from the form. What the repository carries is not defaulted: a form that does not say
// is refused, as the Git page's is.
func (s *Server) publishInput(r *http.Request, proj projects.Project, T tfn) (gitmirror.RepoPlanIn, error) {
	in := gitmirror.RepoPlanIn{ProjectID: proj.ID, AccountID: r.PostFormValue("account_id"), Mode: r.PostFormValue("mode"), Owner: strings.TrimSpace(r.PostFormValue("owner")),
		Name: strings.TrimSpace(r.PostFormValue("name")), Description: strings.TrimSpace(r.PostFormValue("description")), Visibility: r.PostFormValue("visibility"),
		Branch: strings.TrimSpace(r.PostFormValue("branch")), PathPrefix: strings.TrimSpace(r.PostFormValue("path_prefix")), Audience: r.PostFormValue("audience"),
		OnlyDone: formBool(r, "only_done"), IncludeNotes: formBool(r, "include_notes"), SignCommits: formBool(r, "sign_commits"), PushMode: r.PostFormValue("push_mode")}
	if in.Audience != "public" && in.Audience != "private" {
		return in, ops.Invalid("audience", T("git.err.audience_required"))
	}
	if err := s.refuseStrayBox(r, in.Audience); err != nil {
		return in, err
	}
	if in.Mode != "existing" {
		in.Mode = "create"
	}
	if in.Visibility != "public" {
		in.Visibility = "private"
	}
	return in, nil
}

func (s *Server) uiPublish(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.publishOwnerProject(w, r, true)
	if !ok {
		return
	}
	p, ctx := s.principal(r), r.Context()
	T := s.page(r, "", nil).T
	action := r.PathValue("action")
	base := "/p/" + proj.Slug + "/git/publish"
	// refuse puts the refusal, and what was typed, where the page after the redirect shows it: the address in the browser stays
	// one that can be reloaded, and a reload repeats nothing.
	refuse := func(err error, f publishFlash, query, anchor string) {
		e := ops.AsError(err)
		switch e.Code {
		case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge, ops.CodeRateLimited, ops.CodeProvider:
		default:
			s.fail(w, r, err)
			return
		}
		f.Error = redactPosted(r, publishErrorText(T, err))
		if e.Code != ops.CodeRateLimited {
			f.Field = e.Field
		}
		s.publishStash(r, f)
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, base+query+anchor, http.StatusSeeOther)
	}
	query := func(extra ...string) string {
		q := url.Values{}
		if v := scrubValue(r.PostFormValue("account_id")); v != "" {
			q.Set("account_id", v)
		}
		if r.PostFormValue("mode") == "existing" {
			q.Set("mode", "existing")
		}
		for i := 0; i+1 < len(extra); i += 2 {
			q.Set(extra[i], extra[i+1])
		}
		if len(q) == 0 {
			return ""
		}
		return "?" + q.Encode()
	}
	if err := s.refuseAnyCredentials(r); err != nil {
		refuse(err, publishFlash{Values: publishFormValues(r)}, query(), "#pub-account")
		return
	}
	switch action {
	case "connect":
		in := gitmirror.AccountConnectIn{Provider: r.PostFormValue("provider"), Host: strings.TrimSpace(r.PostFormValue("host")), Token: r.PostFormValue("token"), Label: strings.TrimSpace(r.PostFormValue("label"))}
		out, err := s.Reg.Call(ctx, p, "repo.account.connect", in)
		if err != nil {
			refuse(err, publishFlash{Values: map[string]string{"provider": in.Provider, "host": in.Host, "label": in.Label}}, "", "#pub-account")
			return
		}
		a := out.(gitmirror.Account)
		http.Redirect(w, r, base+"?account_id="+url.QueryEscape(a.ID)+"&connected=1#repo", http.StatusSeeOther)
	case "disconnect":
		if _, err := s.Reg.Call(ctx, p, "repo.account.disconnect", gitmirror.AccountRef{AccountID: r.PostFormValue("account_id")}); err != nil {
			refuse(err, publishFlash{}, "", "#pub-account")
			return
		}
		http.Redirect(w, r, base+"?disconnected=1", http.StatusSeeOther)
	case "plan":
		in, err := s.publishInput(r, proj, T)
		if err != nil {
			refuse(err, publishFlash{Values: publishFormValues(r)}, query(), "#fields")
			return
		}
		// the plan is a page of its own: what it is made from is kept for this session, and the address holds only an id
		id := s.planPut(r, in)
		http.Redirect(w, r, base+query("plan", id)+"#plan", http.StatusSeeOther)
	case "publish":
		in, err := s.publishInput(r, proj, T)
		if err != nil {
			refuse(err, publishFlash{Values: publishFormValues(r)}, query(), "#fields")
			return
		}
		// the plan the person reviewed is the one that goes: the op recomputes it and refuses a changed one
		pin := publishFrom(in, r.PostFormValue("plan_token"), strings.TrimSpace(r.PostFormValue("confirm")))
		out, err := s.Reg.Call(ctx, p, "repo.publish", pin)
		if err == nil {
			res := out.(gitmirror.RepoPublishOut)
			http.Redirect(w, r, "/p/"+proj.Slug+"/git?remote="+url.QueryEscape(res.Remote.ID)+"&saved=published", http.StatusSeeOther)
			return
		}
		e := ops.AsError(err)
		again := func(f publishFlash, anchor string) { // back to the plan, made fresh from what is now
			refuse(err, f, query("plan", s.planPut(r, in)), anchor)
		}
		if e.Code == ops.CodeConflict {
			gc, _ := e.Data["git_code"].(string)
			switch gc {
			case gitmirror.CodePublishIncomplete:
				repo, _ := e.Data["created_repository"].(string)
				reg, _ := e.Data["remote_registered"].(bool)
				owner, name, _ := strings.Cut(repo, "/")
				f := publishFlash{Incomplete: &publishIncomplete{Message: e.Message, Repository: repo, Owner: owner, Name: name, Registered: reg}}
				s.publishStash(r, f)
				q := url.Values{"account_id": {in.AccountID}, "mode": {"existing"}, "owner": {owner}, "name": {name}}
				http.Redirect(w, r, base+"?"+q.Encode()+"#incomplete", http.StatusSeeOther)
				return
			case gitmirror.CodePlanChanged:
				s.publishStash(r, publishFlash{Error: T("git.publish.plan_changed")})
				http.Redirect(w, r, base+query("plan", s.planPut(r, in))+"#plan", http.StatusSeeOther)
				return
			}
		}
		again(publishFlash{}, "#plan")
	default:
		refuse(ops.Invalid("", T("git.err.unknown")), publishFlash{}, "", "")
	}
}

// publishFrom is the publish input for the fields of a plan.
func publishFrom(in gitmirror.RepoPlanIn, token, confirm string) gitmirror.RepoPublishIn {
	return gitmirror.RepoPublishIn{ProjectID: in.ProjectID, AccountID: in.AccountID, Mode: in.Mode, Owner: in.Owner, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, Branch: in.Branch, PathPrefix: in.PathPrefix, Audience: in.Audience, OnlyDone: in.OnlyDone, IncludeNotes: in.IncludeNotes,
		SignCommits: in.SignCommits, PushMode: in.PushMode, PlanToken: token, Confirm: confirm}
}

func (s *Server) publishFillPlan(d *publishData, plan gitmirror.RepoPlan) {
	T := d.Pg.T
	d.Plan = &plan
	d.Size = humanBytes(plan.TotalBytes)
	d.Withheld, d.Blockers = nil, nil
	for _, w := range plan.Withheld {
		reason := T("git.publish.withheld_cannot_be_cleaned")
		if k, ok := publishWithheldReasons[w.Reason]; ok {
			reason = T(k)
		}
		d.Withheld = append(d.Withheld, publishWithheld{Path: w.Path, Title: w.Title, Reason: reason})
	}
	for _, b := range plan.Blockers {
		d.Blockers = append(d.Blockers, publishBlocker{Code: b.Code, Text: publishBlockerText(T, b)})
	}
	if d.Values["account_id"] == "" {
		d.Values["account_id"] = plan.Account.ID
	}
}

// ---- release ------------------------------------------------------------------------------------

type releaseData struct {
	Pg        Page
	Project   projects.Project
	Slug      string
	CSRF      string
	Remote    gitmirror.Remote
	Items     []releaseRow
	Current   *releaseRow // the attempt being followed or just made
	Preview   *gitmirror.RepoReleaseOut
	Values    map[string]string
	Error     string
	Field     string
	Poll      bool
	Eligible  bool
	Editions  []releaseEdition // the one choice of what to publish: a format, and for a print PDF its page size
	PreviewMB int64
}

type releaseEdition struct{ Value, Key string }

// releaseEditions is what can be published: each is a format, and a print PDF is one entry per page size, so there is one
// choice, with words, and nothing that only applies to one of the formats is shown for the others.
var releaseEditions = []releaseEdition{{"epub", "git.publish.release_epub"}, {"docx", "git.publish.release_docx"}, {"md", "git.publish.release_md"}, {"md_bundle", "git.publish.release_md_bundle"},
	{"pdf:print-6x9", "git.rel.pdf.print-6x9"}, {"pdf:print-5.5x8.5", "git.rel.pdf.print-5.5x8.5"}, {"pdf:print-5x8", "git.rel.pdf.print-5x8"}, {"pdf:print-a5", "git.rel.pdf.print-a5"},
	{"pdf:proof-letter", "git.rel.pdf.proof-letter"}, {"pdf:proof-a4", "git.rel.pdf.proof-a4"}}

// splitEdition reads "pdf:print-6x9" as a format and a preset; a format with no preset is itself.
func splitEdition(v string) (format, preset string) {
	f, p, _ := strings.Cut(v, ":")
	return f, p
}

type releaseRow struct {
	gitmirror.Release
	Word   string
	Tone   string
	Size   string
	When   string
	Reason string // why it failed or what a retry found, in words
}

func (s *Server) releaseRowFor(r *http.Request, rel gitmirror.Release, T tfn) releaseRow {
	row := releaseRow{Release: rel, Size: humanBytes(rel.AssetBytes), When: gitWhen(rel.StartedAt, s.userZone(r))}
	switch rel.Status {
	case "running":
		row.Word, row.Tone = T("git.publish.release_running"), "info"
	case "ok":
		row.Word, row.Tone = T("git.publish.release_done"), "ok"
		if rel.Reused {
			row.Reason = T("git.publish.release_reused")
		}
	default:
		row.Word, row.Tone = T("git.rel.failed"), "danger"
		switch rel.ErrorCode {
		case gitmirror.CodeTagConflict:
			row.Reason = T("git.publish.release_tag_conflict")
		case "too_large":
			row.Reason = T("git.publish.release_too_large")
		case gitmirror.CodeNotPushed:
			row.Reason = T("git.rel.not_pushed")
		case gitmirror.CodeReleaseBlocked:
			row.Reason = T("git.rel.unsupported")
		default:
			row.Reason = rel.ErrorMessage
		}
	}
	return row
}

func (s *Server) releaseView(r *http.Request, proj projects.Project, remoteID, releaseID string) (releaseData, error) {
	p, ctx := s.principal(r), r.Context()
	pg := s.page(r, "", nil)
	d := releaseData{Pg: pg, Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF, Values: map[string]string{},
		Editions: releaseEditions}
	ro, err := s.Reg.Call(ctx, p, "git.remote.get", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: remoteID})
	if err != nil {
		return d, err
	}
	d.Remote = ro.(gitmirror.RemoteItem).Item
	d.Eligible = d.Remote.AuthKind == "https_token" && (d.Remote.Preset == "github" || d.Remote.Preset == "forgejo") && !d.Remote.OnlyDone
	lo, err := s.Reg.Call(ctx, p, "repo.release_list", gitmirror.ReleaseListIn{ProjectID: proj.ID, RemoteID: d.Remote.ID})
	if err != nil {
		return d, err
	}
	for _, it := range lo.(gitmirror.ReleaseList).Items {
		d.Items = append(d.Items, s.releaseRowFor(r, it, pg.T))
		if it.Status == "running" {
			d.Poll = true
		}
	}
	if releaseID != "" {
		for i := range d.Items {
			if d.Items[i].ID == releaseID {
				c := d.Items[i]
				d.Current = &c
			}
		}
		if d.Current == nil {
			if got, err := s.Reg.Call(ctx, p, "repo.release_get", gitmirror.ReleaseGetIn{ProjectID: proj.ID, ReleaseID: releaseID}); err == nil {
				row := s.releaseRowFor(r, got.(struct {
					Item gitmirror.Release `json:"item"`
				}).Item, pg.T)
				d.Current = &row
				d.Poll = d.Poll || row.Status == "running"
			}
		}
	}
	return d, nil
}

func (s *Server) releasePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.publishOwnerProject(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	d, err := s.releaseView(r, proj, q.Get("remote"), q.Get("release"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	T := d.Pg.T
	// the three things a release is made from may be in the address: none of them chooses anything that leaves the server by
	// itself, and a preview only reads
	for _, k := range []string{"tag", "edition"} {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			d.Values[k] = v
		}
	}
	if f, ok := s.publishTake(r); ok {
		d.Error, d.Field = f.Error, f.Field
		for k, v := range f.Values {
			d.Values[k] = v
		}
	}
	if q.Get("preview") == "1" && d.Eligible && d.Values["tag"] != "" {
		in := gitmirror.RepoReleaseIn{ProjectID: proj.ID, RemoteID: d.Remote.ID, Tag: d.Values["tag"], Preview: true}
		in.Format, in.Preset = splitEdition(d.Values["edition"])
		out, err := s.Reg.Call(r.Context(), s.principal(r), "repo.release", in)
		if err != nil {
			e := ops.AsError(err)
			switch e.Code {
			case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge, ops.CodeRateLimited, ops.CodeProvider:
				d.Error, d.Field = publishErrorText(T, err), e.Field
				if gc, _ := e.Data["git_code"].(string); gc == gitmirror.CodeTagConflict {
					d.Error, d.Field = T("git.publish.release_tag_conflict"), "tag"
				}
			default:
				s.fail(w, r, err)
				return
			}
		} else {
			o := out.(gitmirror.RepoReleaseOut)
			d.Preview = &o
			d.PreviewMB = o.MaxBytes >> 20
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "release", s.page(r, d.Pg.T("git.rel.title"), d))
}

func (s *Server) releaseFragment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.publishOwnerProject(w, r, true)
	if !ok {
		return
	}
	d, err := s.releaseView(r, proj, r.URL.Query().Get("remote"), r.PathValue("rid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "release-live", "release-live", d)
}

func (s *Server) uiRelease(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.publishOwnerProject(w, r, true)
	if !ok {
		return
	}
	p, ctx := s.principal(r), r.Context()
	T := s.page(r, "", nil).T
	rid := r.PostFormValue("remote_id")
	tag, edition := strings.TrimSpace(r.PostFormValue("tag")), r.PostFormValue("edition")
	format, preset := splitEdition(edition)
	q := url.Values{"remote": {rid}, "edition": {edition}, "tag": {tag}}
	base := "/p/" + proj.Slug + "/git/release"
	w.Header().Set("Cache-Control", "no-store")
	if err := s.refuseAnyCredentials(r); err != nil { // never into the address, and never shown again
		s.publishStash(r, publishFlash{Error: publishErrorText(T, err), Field: ops.AsError(err).Field})
		http.Redirect(w, r, base+"?remote="+url.QueryEscape(rid), http.StatusSeeOther)
		return
	}
	switch r.PathValue("action") {
	case "preview":
		// a preview is a page with an address (it only reads): the answer is a redirect to it
		q.Set("preview", "1")
		http.Redirect(w, r, base+"?"+q.Encode()+"#rel-preview", http.StatusSeeOther)
	case "start":
		out, err := s.Reg.Call(ctx, p, "repo.release", gitmirror.RepoReleaseIn{ProjectID: proj.ID, RemoteID: rid, Tag: tag, Format: format, Preset: preset})
		if err != nil {
			e := ops.AsError(err)
			switch e.Code {
			case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge, ops.CodeRateLimited, ops.CodeProvider:
			default:
				s.fail(w, r, err)
				return
			}
			f := publishFlash{Error: publishErrorText(T, err), Field: e.Field}
			if gc, _ := e.Data["git_code"].(string); gc == gitmirror.CodeTagConflict {
				f.Error, f.Field = T("git.publish.release_tag_conflict"), "tag"
			}
			s.publishStash(r, f)
			http.Redirect(w, r, base+"?"+q.Encode(), http.StatusSeeOther)
			return
		}
		id := ""
		if o := out.(gitmirror.RepoReleaseOut); o.Item != nil {
			id = o.Item.ID
		}
		http.Redirect(w, r, base+"?remote="+url.QueryEscape(rid)+"&release="+url.QueryEscape(id)+"#current", http.StatusSeeOther)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}
