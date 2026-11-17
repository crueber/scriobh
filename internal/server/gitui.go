package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The Git sync page: where a project's Markdown is pushed, how that is going, and what to do when it is not.
// Everything on it is read through the git.* ops and every change goes through them. The ops return data (a
// state, a code, a checklist); the sentences are written here, for a person. Push-only is the contract: nothing
// on this page pulls, merges or reads the repository, and the two ways out of a refused push (force it, or import
// the repository into a new project) say so.

// ---- view models ------------------------------------------------------------------------------

// tfn looks a message up in the page's language: the words every part of this page and its handlers use.
type tfn func(key string, args ...any) string

type gitBanner struct {
	Tone     string // ok, info, warn, danger
	Headline string
	Detail   string
	Hint     string
}

type gitStep struct {
	Done  bool
	Title string
}

type gitPushRow struct {
	gitmirror.Push
	When   string
	Short  string // the commit, shortened
	Word   string // what happened, in words
	Tone   string
	Detail string
}

type gitScan struct {
	Host    string
	Keys    []gitmirror.HostKey
	Pinned  *gitmirror.PinnedKey
	Changed bool
	Matches bool
	Docs    string // where the host publishes its fingerprints, when a preset knows
}

type gitTest struct {
	OK      bool
	Message string
	Warning string
	Hint    string
	Empty   bool
}

// gitRemoteRow is one line of the list of the project's remotes: what it carries, where it goes, how the last push went
// and when the next try is.
type gitRemoteRow struct {
	gitmirror.Remote
	Selected bool
	State    string // the state in words
	Tone     string
	LastPush string // "Pushed 13 Nov 2026, 09:00", or what is true instead
	Backoff  string // "Next automatic try: ...", only while a push is waiting
}

type gitData struct {
	Pg         Page // the page's language, for the fragments that have no page of their own
	Project    projects.Project
	Slug       string
	CSRF       string
	IsOwner    bool
	CanEdit    bool
	Enabled    bool // the server can store credentials
	Disabled   string
	Remote     *gitmirror.Remote
	Rows       []gitRemoteRow // every remote of the project, the selected one marked
	CanAdd     bool           // the owner may add another (fewer than MaxRemotes)
	MaxRemotes int
	Presets    []gitmirror.Preset
	Preset     gitmirror.Preset // the one the add form follows
	Banner     gitBanner
	Steps      []gitStep
	Pushes     []gitPushRow
	Mirror     *mirrorView
	KeyLink    string // where the deploy key is added, when the host is a known one
	FingerDoc  string
	Poll       bool
	Saved      string
	SavedText  string
	Error      string
	Field      string
	Values     map[string]string // what was typed in the add form (only when that form was the one refused)
	SetValues  map[string]string // what was typed in the selected remote's settings form (only when that was refused)
	Action     string            // the action whose form was refused, when one was
	Scan       *gitScan
	Test       *gitTest
	Created    bool
	Zone       string
	Secrets    []gitSecretRow
	AddressOK  bool
	// the project's signing key (owner only) and the trigger secret that was just made, shown by the answer that made it
	Signing    *gitmirror.SigningOut
	NewSecret  string
	SecretGone bool   // the page was asked for again after the secret had been shown
	TriggerOK  string // the result line of a test delivery
	CanPublish bool
}

// AddView is the page's data for the add form while a remote is shown: the form starts from the defaults, not from the
// settings of the remote that happens to be selected.
func (d gitData) AddView() gitData {
	d.Remote = nil
	if d.Action != "create" { // a refused form of another kind says nothing about this one
		d.Field, d.Values = "", map[string]string{}
	}
	return d
}

type gitSecretRow struct {
	gitmirror.SecretFindingView
	Short string
}

// maxRemotes is the number of remotes a project may have (the op's own limit, F-362).
const maxRemotes = 5

// ---- words --------------------------------------------------------------------------------------

// gitHintCodes are the error codes that have advice. The advice is in the catalogue as git.hint.<code>; the op gives the
// code and the sentence about what happened.
var gitHintCodes = strings.Fields("host_key_unpinned host_key_changed auth_failed repo_not_found unreachable timeout address_blocked remote_ahead " +
	"rejected_by_remote secret_in_tree credentials_unreadable credentials_missing no_mirror mirror_off bundle_failed too_large bad_url no_secret_key")

// gitHint is the advice for an error code, "" when there is none.
func gitHint(T tfn, code string) string {
	for _, c := range gitHintCodes {
		if c == code {
			return T("git.hint." + code)
		}
	}
	return ""
}

func gitWhen(ts string, loc *time.Location) string { return whenIn(ts, loc) }

// utcWhen writes a time from the remote (always UTC) for a person.
func utcWhen(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2 Jan 2006, 15:04") + " UTC"
}

func gitShort(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// gitStatusBanner writes the one sentence that says where a remote stands.
func gitStatusBanner(rem gitmirror.Remote, loc *time.Location, enabled bool, T tfn) gitBanner {
	st := rem.Status
	host := rem.Host
	switch {
	case !enabled:
		return gitBanner{Tone: "warn", Headline: T("git.banner.no_secret"), Detail: T("git.hint.no_secret_key")}
	case st.LastErrorCode == "credentials_unreadable":
		// A restore with another secret key switches such a remote off; the reason comes before the switch.
		return gitBanner{Tone: "danger", Headline: T("git.banner.cred_unreadable_off"), Detail: st.LastError, Hint: T("git.hint.credentials_unreadable")}
	case rem.RestorePaused:
		return gitBanner{Tone: "warn", Headline: T("git.banner.paused_restore"), Detail: T("git.remotes.paused")}
	case !rem.Enabled:
		return gitBanner{Tone: "info", Headline: T("git.banner.off"), Detail: T("git.banner.off_detail", "host", host)}
	case st.Running:
		return gitBanner{Tone: "info", Headline: T("git.banner.running", "host", host), Detail: T("git.banner.running_detail")}
	case len(rem.Setup) > 0:
		return gitBanner{Tone: "warn", Headline: T("git.banner.setup"), Detail: T("git.banner.setup_detail")}
	}
	code := st.LastErrorCode
	switch st.State {
	case "rejected":
		return gitBanner{Tone: "danger", Headline: T("git.banner.rejected"), Detail: T("git.banner.rejected_detail", "host", host)}
	case "blocked":
		b := gitBanner{Tone: "danger", Headline: T("git.banner.blocked"), Detail: st.LastError, Hint: gitHint(T, code)}
		switch code {
		case "host_key_changed":
			b.Headline = T("git.banner.host_key", "host", host)
		case "secret_in_tree":
			b.Headline = T("git.banner.secret")
			b.Detail = ""
		case "credentials_unreadable":
			b.Headline = T("git.banner.cred_unreadable")
		case "rejected_by_remote":
			b.Headline = T("git.banner.refused", "host", host)
		}
		return b
	case "behind":
		b := gitBanner{Tone: "warn", Headline: T("git.banner.behind"), Detail: st.LastError, Hint: gitHint(T, code)}
		if at := whenIn(st.NextAttemptAt, loc); at != "" {
			b.Detail = strings.TrimSpace(b.Detail + " " + T("git.banner.next_try", "when", at))
		}
		return b
	case "synced":
		d := ""
		if at := whenIn(st.LastPushAt, loc); at != "" {
			d = T("git.banner.reached", "host", host, "when", at)
		}
		if st.InSync {
			return gitBanner{Tone: "ok", Headline: T("git.banner.up_to_date"), Detail: d}
		}
		return gitBanner{Tone: "ok", Headline: T("git.banner.pushed"), Detail: strings.TrimSpace(d + " " + T("git.banner.newer"))}
	}
	return gitBanner{Tone: "info", Headline: T("git.banner.ready"), Detail: T("git.banner.ready_detail")}
}

func gitPushWord(p gitmirror.Push, T tfn) (word, tone string) {
	switch p.Status {
	case "running":
		return T("git.push.running"), "info"
	case "cancelled":
		return T("git.push.cancelled"), "info"
	case "rejected":
		return T("git.push.rejected"), "danger"
	case "error":
		return T("git.push.failed"), "warn"
	}
	if p.Forced {
		return T("git.push.forced"), "warn"
	}
	if p.Outcome == "up_to_date" {
		return T("git.push.nothing_new"), "info"
	}
	return T("git.push.pushed"), "ok"
}

// gitRowState says in a few words where one remote stands, for the list of remotes.
func gitRowState(rem gitmirror.Remote, loc *time.Location, T tfn) (state, tone, last, backoff string) {
	st := rem.Status
	switch {
	case rem.RestorePaused:
		state, tone = T("git.state.paused_restore"), "warn"
	case !rem.Enabled:
		state, tone = T("git.state.off"), "info"
	case st.Running:
		state, tone = T("git.state.running"), "info"
	case len(rem.Setup) > 0:
		state, tone = T("git.state.setup"), "warn"
	default:
		switch st.State {
		case "rejected":
			state, tone = T("git.state.rejected"), "danger"
		case "blocked":
			state, tone = T("git.state.blocked"), "danger"
		case "behind":
			state, tone = T("git.state.behind"), "warn"
		case "synced":
			state, tone = T("git.state.synced"), "ok"
		default:
			state, tone = T("git.state.new"), "info"
		}
	}
	if at := whenIn(st.LastPushAt, loc); at != "" {
		last = T("git.row.last_push", "when", at)
	} else {
		last = T("git.row.never")
	}
	if st.State == "behind" && rem.Enabled {
		if at := whenIn(st.NextAttemptAt, loc); at != "" {
			backoff = T("git.banner.next_try", "when", at)
		}
	}
	return
}

// repoParts reads host, owner and repository out of a remote's address, for the link to the host's key settings.
func repoParts(raw string) (host, owner, repo string) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 && i < strings.IndexAny(s+"/", "/") {
		s = s[i+1:]
	}
	var path string
	if i := strings.IndexAny(s, "/:"); i >= 0 {
		host, path = s[:i], s[i+1:]
		if strings.HasPrefix(path, "/") {
			path = path[1:]
		}
	} else {
		return s, "", ""
	}
	if j := strings.Index(host, ":"); j >= 0 {
		host = host[:j]
	}
	// A port number may sit between the host and the path (ssh://git@host:2222/owner/repo.git).
	if n := strings.Index(path, "/"); n > 0 {
		if _, err := strconv.Atoi(path[:n]); err == nil && strings.Contains(raw, ":"+path[:n]+"/") {
			path = path[n+1:]
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		return host, parts[len(parts)-2], parts[len(parts)-1]
	}
	return host, "", ""
}

func fillPreset(shape, host, owner, repo string) string {
	r := strings.NewReplacer("{host}", host, "{owner}", owner, "{repo}", repo)
	return r.Replace(shape)
}

// ---- reading ------------------------------------------------------------------------------------

// gitOwner reports whether the caller counts as the project's owner for Git sync: the owner, or an administrator
// (the registry treats administrators as owners everywhere).
func (s *Server) gitOwner(r *http.Request, proj projects.Project) bool {
	return proj.Role == ops.ProjectOwner || s.principal(r).Role == ops.RoleAdmin
}

func (s *Server) gitProject(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	return s.goalsProject(w, r)
}

// wantRemote is the remote the request names: the page's ?remote=, or the form's remote_id when a form is answered.
func wantRemote(r *http.Request) string {
	if v := r.URL.Query().Get("remote"); v != "" {
		return v
	}
	return r.PostFormValue("remote_id")
}

func (s *Server) gitView(r *http.Request, proj projects.Project) (gitData, error) {
	p := s.principal(r)
	ctx := r.Context()
	owner := s.gitOwner(r, proj)
	pg := s.page(r, "", nil)
	T := pg.T
	d := gitData{Pg: pg, Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF, IsOwner: owner, MaxRemotes: maxRemotes,
		CanEdit: owner || proj.Role == ops.ProjectEditor, Values: map[string]string{}, SetValues: map[string]string{}}
	lo, err := s.Reg.Call(ctx, p, "git.remote.list", gitmirror.ProjectIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	list := lo.(gitmirror.RemoteList)
	d.Enabled, d.Disabled = list.Enabled, list.DisabledNotes
	d.CanAdd = owner && len(list.Items) < maxRemotes
	d.CanPublish = owner
	po, err := s.Reg.Call(ctx, p, "git.presets", struct{}{})
	if err == nil {
		// the op's presets are static English data; the words come from the catalogue, on a copy
		for _, pr := range po.(gitmirror.PresetList).Items {
			if T("git.preset."+pr.ID) != "git.preset."+pr.ID {
				pr.Label, pr.Notes = T("git.preset."+pr.ID), T("git.preset."+pr.ID+".note")
			}
			d.Presets = append(d.Presets, pr)
		}
	}
	d.Preset = gitmirror.Preset{ID: "custom"}
	want := r.URL.Query().Get("preset")
	for _, pr := range d.Presets {
		if pr.ID == want || (want == "" && pr.ID == "custom") {
			d.Preset = pr
		}
	}
	loc := s.userZone(r)
	d.Zone = loc.String()
	if len(list.Items) > 0 {
		pick := 0
		if id := wantRemote(r); id != "" {
			for i, it := range list.Items {
				if it.ID == id {
					pick = i
				}
			}
		}
		for i, it := range list.Items {
			row := gitRemoteRow{Remote: it, Selected: i == pick}
			row.State, row.Tone, row.LastPush, row.Backoff = gitRowState(it, loc, T)
			d.Rows = append(d.Rows, row)
		}
		rem := list.Items[pick]
		d.Remote = &rem
		d.Poll = rem.Status.Running
		d.Banner = gitStatusBanner(rem, loc, d.Enabled, T)
		d.Steps = gitSteps(rem, T)
		d.Secrets = nil
		for _, f := range rem.Status.Secrets {
			d.Secrets = append(d.Secrets, gitSecretRow{SecretFindingView: f, Short: gitShort(f.SHA)})
		}
		if host, owner, repo := repoParts(rem.URL); owner != "" && repo != "" {
			for _, pr := range d.Presets {
				if pr.ID == rem.Preset || (rem.Preset == "" && (host == "github.com" && pr.ID == "github")) {
					if pr.KeySettingsURL != "" && strings.HasPrefix(pr.KeySettingsURL, "https://") {
						d.KeyLink = fillPreset(pr.KeySettingsURL, host, owner, repo)
					}
					if pr.FingerprintsDocs != "" {
						d.FingerDoc = pr.FingerprintsDocs
					}
				}
			}
		}
		ho, err := s.Reg.Call(ctx, p, "git.push_history", gitmirror.PushHistoryIn{ProjectID: proj.ID, RemoteID: rem.ID, Limit: 15})
		if err == nil {
			for _, pu := range ho.(gitmirror.PushHistoryOut).Items {
				word, tone := gitPushWord(pu, T)
				row := gitPushRow{Push: pu, When: gitWhen(pu.StartedAt, loc), Short: gitShort(pu.CommitSHA), Word: word, Tone: tone}
				if pu.Status == "error" {
					row.Detail = pu.ErrorMessage
				}
				d.Pushes = append(d.Pushes, row)
			}
		}
	}
	if owner {
		if so, err := s.Reg.Call(ctx, p, "git.signing.get", gitmirror.ProjectIn{ProjectID: proj.ID}); err == nil {
			sg := so.(gitmirror.SigningOut)
			d.Signing = &sg
		}
	}
	d.Mirror = s.mirrorFor(r, proj.ID, d.CanEdit)
	return d, nil
}

func gitSteps(rem gitmirror.Remote, T tfn) []gitStep {
	miss := map[string]bool{}
	for _, m := range rem.Setup {
		miss[m] = true
	}
	var out []gitStep
	if rem.AuthKind == "ssh_deploy_key" {
		out = append(out, gitStep{Done: !miss["deploy_key"], Title: T("git.step.deploy_key")}, gitStep{Done: !miss["host_key"], Title: T("git.step.host_key")})
	} else {
		out = append(out, gitStep{Done: !miss["token"], Title: T("git.step.token")})
	}
	return out
}

// gitFlash is what a refused or answered form hands to the page that follows it (post, redirect, get): the refusal with the
// values the person typed, a host key scan, a connection test, a trigger test, and the trigger's secret (which is held here,
// once, and never put in an address). It lives on the server against the session for two minutes and is read once.
type gitFlash struct {
	Error, Field, Action string
	Values, SetValues    map[string]string
	Scan                 *gitScan
	Test                 *gitTest
	TriggerOK            string
	Secret               string
}

func (s *Server) gitStash(r *http.Request, f gitFlash) {
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	s.flashSet(r, map[string]string{"g": string(b)})
}

func (s *Server) gitTakeFlash(r *http.Request) (gitFlash, bool) {
	fl := s.flashTake(r)
	var f gitFlash
	if fl == nil || fl["g"] == "" || json.Unmarshal([]byte(fl["g"]), &f) != nil {
		return f, false
	}
	return f, true
}

// gitBack sends the browser to the page of the remote, where the stashed answer is shown. Every answer a form of this page
// gets is a redirect, so the address in the browser is one that can be reloaded, and a reload repeats nothing.
func (s *Server) gitBack(w http.ResponseWriter, r *http.Request, proj projects.Project, anchor string, once bool) {
	u := "/p/" + proj.Slug + "/git"
	sep := "?"
	if id := wantRemote(r); id != "" {
		u += "?remote=" + url.QueryEscape(id)
		sep = "&"
	}
	if once {
		u += sep + "once=1"
	}
	if anchor != "" {
		u += "#" + anchor
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u, http.StatusSeeOther)
}

// ---- pages --------------------------------------------------------------------------------------

func (s *Server) gitPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.gitProject(w, r)
	if !ok {
		return
	}
	d, err := s.gitView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	d.SavedText = gitSavedText(d.Pg.T, d.Saved)
	d.Created = d.Saved == "created"
	if f, ok := s.gitTakeFlash(r); ok {
		d.Error, d.Field, d.Action = f.Error, f.Field, f.Action
		d.Values, d.SetValues = f.Values, f.SetValues
		if d.Values == nil {
			d.Values = map[string]string{}
		}
		if d.SetValues == nil {
			d.SetValues = map[string]string{}
		}
		d.Scan, d.Test, d.TriggerOK, d.NewSecret = f.Scan, f.Test, f.TriggerOK, f.Secret
		if f.Secret != "" {
			d.SavedText = d.Pg.T("git.saved.trigger_secret")
		}
		if f.Scan != nil {
			d.Scan.Docs = d.FingerDoc
		}
	} else if r.URL.Query().Get("once") == "1" {
		d.SecretGone = true // the secret was shown once and this is a reload, or a second look
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "git", s.page(r, d.Pg.T("git.page.title", "project", proj.Title), d))
}

// gitFragment is the part of the page that changes while a push runs; the page's script asks for it every few seconds.
func (s *Server) gitFragment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	d, err := s.gitView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Remote == nil {
		// The remote was deleted (another tab, another person): there is nothing
		// to poll. A 404 tells the page's script to stop.
		s.fail(w, r, ops.NotFound("remote"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "git-live", "git-live", d)
}

type gitForceData struct {
	Pg      Page
	Project projects.Project
	Slug    string
	CSRF    string
	Remote  gitmirror.Remote
	Error   string
	Typed   string
}

func (s *Server) gitForceData(r *http.Request, proj projects.Project) (gitForceData, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "git.remote.get", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: wantRemote(r)})
	if err != nil {
		return gitForceData{}, err
	}
	return gitForceData{Pg: s.page(r, "", nil), Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, Remote: out.(gitmirror.RemoteItem).Item}, nil
}

// gitForcePage is the confirmation for replacing the remote's history: its own page, also loaded into the dialog.
func (s *Server) gitForcePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.gitProject(w, r)
	if !ok {
		return
	}
	if !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden(s.tr(r, "git.err.owner_force")))
		return
	}
	d, err := s.gitForceData(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "git-force", s.page(r, d.Pg.T("git.force.title", "name", d.Remote.Name), d))
}

func (s *Server) gitForceFragment(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden(s.tr(r, "git.err.owner_force")))
		return
	}
	d, err := s.gitForceData(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.fragment(w, r, http.StatusOK, "git-force", "git-force", d)
}

type gitPushPage struct {
	Pg       Page
	Project  projects.Project
	Slug     string
	Push     gitPushRow
	Files    []gitManifestRow
	Hidden   int
	Host     string
	Remote   string
	Branch   string
	Subject  string
	Docs     []gitDocRow
	Foreign  []gitmirror.ForeignCommit
	MoreFor  bool
	Bundle   string
	Started  string
	Finished string
}

type gitManifestRow struct{ Path, Hash string }
type gitDocRow struct {
	Path, Change string
	Words        int
	Abs          int
}

const gitManifestShown = 400

func (s *Server) gitPushDetail(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.gitProject(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "git.push_get", gitmirror.PushGetIn{ProjectID: proj.ID, PushID: r.PathValue("pid")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pu := out.(gitmirror.PushItem).Item
	loc := s.userZone(r)
	pg := s.page(r, "", nil)
	word, tone := gitPushWord(pu, pg.T)
	d := gitPushPage{Pg: pg, Project: proj, Slug: proj.Slug, Push: gitPushRow{Push: pu, When: gitWhen(pu.StartedAt, loc), Short: gitShort(pu.CommitSHA), Word: word, Tone: tone},
		Branch: pu.Branch, Subject: pu.Subject, Foreign: pu.RemoteCommits, MoreFor: pu.RemoteCommitsMore, Bundle: pu.Bundle,
		Started: gitWhen(pu.StartedAt, loc), Finished: gitWhen(pu.FinishedAt, loc)}
	for _, f := range pu.Documents {
		a := f.Delta
		if a < 0 {
			a = -a
		}
		d.Docs = append(d.Docs, gitDocRow{Path: f.Path, Change: f.Change, Words: f.Delta, Abs: a})
	}
	paths := make([]string, 0, len(pu.Manifest))
	for p := range pu.Manifest {
		paths = append(paths, p)
	}
	sortStrings(paths)
	for i, p := range paths {
		if i >= gitManifestShown {
			d.Hidden = len(paths) - gitManifestShown
			break
		}
		d.Files = append(d.Files, gitManifestRow{Path: p, Hash: pu.Manifest[p]})
	}
	s.render(w, r, http.StatusOK, "git-push", s.page(r, pg.T("git.pushpage.title", "commit", d.Push.Short), d))
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ---- changes ------------------------------------------------------------------------------------

// gitSavedKeys are the notes shown after a change, as git.saved.<key>.
var gitSavedKeys = strings.Fields("created settings address enabled disabled keygen hostkey pushed cancelled force acked unacked token deleted " +
	"audience signing sign_on sign_off trigger trigger_rotated trigger_on trigger_off trigger_deleted")

func gitSavedText(T tfn, k string) string {
	for _, x := range gitSavedKeys {
		if x == k {
			return T("git.saved." + k)
		}
	}
	return ""
}

var gitOwnerOnly = map[string]bool{"create": true, "settings": true, "address": true, "enable": true, "disable": true, "keygen": true,
	"hostkey-accept": true, "force": true, "secret-ack": true, "secret-unack": true, "delete": true,
	"audience": true, "signing-generate": true, "signing-remote": true, "trigger-set": true, "trigger-rotate": true, "trigger-switch": true,
	"trigger-test": true, "trigger-delete": true}

func formBool(r *http.Request, k string) bool {
	v := r.PostFormValue(k)
	return v == "1" || v == "on" || v == "true"
}

// gitStoreValues keeps what a person typed so a refused form comes back filled in. A token, a trigger address (its path
// is stored sealed) and a confirmation never do.
func (s *Server) gitStoreValues(r *http.Request) map[string]string {
	v := map[string]string{}
	for k, vs := range r.PostForm {
		if len(vs) > 0 && k != "_csrf" && k != "token" && k != "trigger_url" && k != "confirm" {
			v[k] = vs[0]
		}
	}
	return scrubValues(v)
}

// gitAnchor is where on the page the answer to each action belongs.
var gitAnchor = map[string]string{"create": "add", "settings": "settings", "address": "settings", "audience": "carries", "keygen": "setup", "hostkey-scan": "hostkey",
	"hostkey-accept": "hostkey", "test": "test", "signing-generate": "signing", "signing-remote": "signing", "trigger-set": "trigger", "trigger-rotate": "trigger",
	"trigger-switch": "trigger", "trigger-test": "trigger", "trigger-delete": "trigger", "delete": "danger", "secret-ack": "secrets", "secret-unack": "secrets"}

// gitFail shows a refused value beside the form it came from, in plain words: the refusal is kept for the page that follows
// the redirect, so the address in the browser is the page's own. Anything but a refused value is the ordinary error page
// (not found, not allowed, not signed in).
func (s *Server) gitFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge, ops.CodeRateLimited, ops.CodeProvider:
	default:
		s.fail(w, r, err)
		return
	}
	if isFetch(r) { // the palette's push: an answer a script can read, never a page
		s.fail(w, r, err)
		return
	}
	f := gitFlash{Error: redactPosted(r, e.Message), Field: e.Field, Action: r.PathValue("action")}
	if gc, _ := e.Data["git_code"].(string); gc == gitmirror.CodeConfirmationRequired {
		f.Field = "confirm" // the op wants the remote's name typed: the form shows its box
	}
	if vals := s.gitStoreValues(r); f.Action == "settings" {
		f.SetValues = vals // only the settings form of the remote shown takes these back
	} else {
		f.Values = vals
	}
	s.gitStash(r, f)
	s.gitBack(w, r, proj, gitAnchor[f.Action], false)
}

func (s *Server) gitRedirect(w http.ResponseWriter, r *http.Request, proj projects.Project, saved, anchor string) {
	s.gitRedirectTo(w, r, proj, wantRemote(r), saved, anchor)
}

func (s *Server) gitRedirectTo(w http.ResponseWriter, r *http.Request, proj projects.Project, remote, saved, anchor string) {
	u := "/p/" + proj.Slug + "/git?saved=" + url.QueryEscape(saved)
	if remote != "" {
		u += "&remote=" + url.QueryEscape(remote)
	}
	if anchor != "" {
		u += "#" + anchor
	}
	if isHTMX(r) { // a form in the dialog: the whole page follows, not a copy of it inside the dialog
		w.Header().Set("HX-Redirect", u)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// gitWidens reports whether a change to what a remote carries lets more reach it than before: a public remote made
// private (the whole copy), or the done-only filter taken off. For a remote that is public, or was made by the publish
// flow, that is the change that can put words in a public place, so the page asks for the remote's name first.
func gitWidens(rem gitmirror.Remote, audience string, onlyDone, notes bool) bool {
	exposed := rem.Audience == "public" || rem.CreatedVia == "publish"
	if !exposed {
		return false
	}
	switch {
	case rem.Audience == "public" && audience == "private":
		return true
	case rem.Audience == "public" && audience == "public" && rem.OnlyDone && !onlyDone:
		return true
	case rem.Audience == "private" && audience == "private" && notes && !rem.IncludeNotes:
		return true
	}
	return false
}

// uiGit applies one action of the page. The path names it.
func (s *Server) uiGit(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	p := s.principal(r)
	ctx := r.Context()
	T := s.page(r, "", nil).T
	rid := r.PostFormValue("remote_id")
	action := r.PathValue("action")
	// Everything that decides where the manuscript and the credential go is the owner's. The ops say so too; this
	// answers before any of them is asked, so an editor never gets a message about a form they cannot see.
	if gitOwnerOnly[action] && !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden(T("git.err.owner")))
		return
	}
	var err error
	if err = s.refuseAnyCredentials(r); err == nil && strings.HasPrefix(action, "trigger-") {
		err = s.refuseCredentials(r, "trigger_url")
	}
	if err != nil {
		s.gitFail(w, r, proj, err)
		return
	}
	switch action {
	case "create":
		in := gitmirror.CreateIn{ProjectID: proj.ID, Name: strings.TrimSpace(r.PostFormValue("name")), URL: strings.TrimSpace(r.PostFormValue("url")),
			Preset: r.PostFormValue("preset"), Branch: strings.TrimSpace(r.PostFormValue("branch")), PathPrefix: strings.TrimSpace(r.PostFormValue("path_prefix")),
			Username: strings.TrimSpace(r.PostFormValue("username")), Token: r.PostFormValue("token"), PushMode: r.PostFormValue("push_mode"),
			IntervalMinutes: atoi(r.PostFormValue("interval_minutes")), AuthorName: strings.TrimSpace(r.PostFormValue("author_name")), AuthorEmail: strings.TrimSpace(r.PostFormValue("author_email"))}
		// What the remote carries is a choice the person makes, with nothing chosen for them: a form that does not say is
		// refused here, whatever the op would default to. (The op's default is the strict public filter; a backup that
		// silently got that filter, or a public mirror that silently got the full copy, would each be a surprise.)
		in.Audience = r.PostFormValue("audience")
		if in.Audience != "public" && in.Audience != "private" {
			err = ops.Invalid("audience", T("git.err.audience_required"))
			break
		}
		if err = s.refuseStrayBox(r, in.Audience); err != nil {
			break
		}
		if in.Audience == "private" {
			n := formBool(r, "include_notes")
			in.IncludeNotes = &n
		} else {
			od := formBool(r, "only_done")
			in.OnlyDone = &od
		}
		sc := formBool(r, "sign_commits")
		in.SignCommits = &sc
		in.ConfirmName = strings.TrimSpace(r.PostFormValue("confirm"))
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.remote.create", in); err == nil {
			s.gitRedirectTo(w, r, proj, out.(gitmirror.RemoteItem).Item.ID, "created", "setup")
			return
		}
	case "settings":
		pm := r.PostFormValue("push_mode")
		br, pp, an, ae, un := strings.TrimSpace(r.PostFormValue("branch")), strings.TrimSpace(r.PostFormValue("path_prefix")), strings.TrimSpace(r.PostFormValue("author_name")), strings.TrimSpace(r.PostFormValue("author_email")), strings.TrimSpace(r.PostFormValue("username"))
		name, iv := strings.TrimSpace(r.PostFormValue("name")), atoi(r.PostFormValue("interval_minutes"))
		// What the remote carries is not here: a routine save cannot change it (see "audience").
		in := gitmirror.UpdateIn{ProjectID: proj.ID, RemoteID: rid, Name: &name, Branch: &br, PathPrefix: &pp, PushMode: &pm, AuthorName: &an, AuthorEmail: &ae, Username: &un}
		if iv > 0 {
			in.IntervalMinutes = &iv
		}
		saved := "settings"
		if t := r.PostFormValue("token"); t != "" {
			in.Token = &t
			saved = "token"
		}
		if _, err = s.Reg.Call(ctx, p, "git.remote.update", in); err == nil {
			s.gitRedirect(w, r, proj, saved, "settings")
			return
		}
	case "audience":
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.remote.get", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: rid}); err != nil {
			break
		}
		rem := out.(gitmirror.RemoteItem).Item
		aud := r.PostFormValue("audience")
		if aud != "public" && aud != "private" {
			err = ops.Invalid("audience", T("git.err.audience_required"))
			break
		}
		// a box that belongs to the other audience is refused in words when the person ticked it now; one that is only the
		// remote's present state (the box under the choice being left) goes with that choice
		if (aud == "private" && formBool(r, "only_done") && !rem.OnlyDone) || (aud == "public" && formBool(r, "include_notes") && !rem.IncludeNotes) {
			if err = s.refuseStrayBox(r, aud); err != nil {
				break
			}
		}
		od, notes := aud == "public" && formBool(r, "only_done"), aud == "private" && formBool(r, "include_notes")
		if aud == rem.Audience && od == rem.OnlyDone && (aud == "public" || notes == rem.IncludeNotes) {
			err = ops.Invalid("audience", T("git.err.audience_same"))
			break
		}
		changesHistory := aud != rem.Audience || od != rem.OnlyDone
		if changesHistory && !formBool(r, "understood") {
			err = ops.Invalid("understood", T("git.err.audience_understood"))
			break
		}
		if gitWidens(rem, aud, od, notes) && strings.TrimSpace(r.PostFormValue("confirm")) != rem.Name {
			err = ops.Invalid("confirm", T("git.err.widen_confirm", "name", rem.Name))
			break
		}
		in := gitmirror.UpdateIn{ProjectID: proj.ID, RemoteID: rid, Audience: &aud, OnlyDone: &od, ConfirmName: strings.TrimSpace(r.PostFormValue("confirm"))}
		if aud == "private" {
			in.IncludeNotes = &notes
		}
		if _, err = s.Reg.Call(ctx, p, "git.remote.update", in); err == nil {
			s.gitRedirect(w, r, proj, "audience", "carries")
			return
		}
	case "address":
		if !formBool(r, "understood") {
			err = ops.Invalid("understood", T("git.err.address_understood"))
			break
		}
		u := strings.TrimSpace(r.PostFormValue("url"))
		if _, err = s.Reg.Call(ctx, p, "git.remote.update", gitmirror.UpdateIn{ProjectID: proj.ID, RemoteID: rid, URL: &u, ConfirmName: strings.TrimSpace(r.PostFormValue("confirm"))}); err == nil {
			s.gitRedirect(w, r, proj, "address", "setup")
			return
		}
	case "enable", "disable":
		on := action == "enable"
		if _, err = s.Reg.Call(ctx, p, "git.remote.update", gitmirror.UpdateIn{ProjectID: proj.ID, RemoteID: rid, Enabled: &on}); err == nil {
			s.gitRedirect(w, r, proj, map[bool]string{true: "enabled", false: "disabled"}[on], "status")
			return
		}
	case "keygen":
		replace := formBool(r, "replace")
		if replace && !formBool(r, "understood") {
			err = ops.Invalid("understood", T("git.err.keygen_understood"))
			break
		}
		if _, err = s.Reg.Call(ctx, p, "git.remote.keygen", gitmirror.KeygenIn{ProjectID: proj.ID, RemoteID: rid, Replace: replace}); err == nil {
			s.gitRedirect(w, r, proj, "keygen", "setup")
			return
		}
	case "signing-generate":
		replace := formBool(r, "replace")
		if replace && !formBool(r, "understood") {
			err = ops.Invalid("understood", T("git.err.signing_understood"))
			break
		}
		if _, err = s.Reg.Call(ctx, p, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: proj.ID, Replace: replace}); err == nil {
			s.gitRedirect(w, r, proj, "signing", "signing")
			return
		}
	case "signing-remote":
		on := formBool(r, "sign_commits")
		if _, err = s.Reg.Call(ctx, p, "git.remote.update", gitmirror.UpdateIn{ProjectID: proj.ID, RemoteID: rid, SignCommits: &on}); err == nil {
			s.gitRedirect(w, r, proj, map[bool]string{true: "sign_on", false: "sign_off"}[on], "signing")
			return
		}
	case "trigger-set", "trigger-rotate":
		in := gitmirror.TriggerSetIn{ProjectID: proj.ID, RemoteID: rid, RotateSecret: action == "trigger-rotate"}
		if u := strings.TrimSpace(r.PostFormValue("trigger_url")); u != "" {
			in.URL = &u
		}
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.trigger.set", in); err == nil {
			to := out.(gitmirror.TriggerOut)
			if to.Secret == "" {
				s.gitRedirect(w, r, proj, "trigger", "trigger")
				return
			}
			// The secret is held on the server against this session, once, and shown by the page the redirect leads to; it is
			// never in the address, so a reload or a second look shows that it was shown and makes no new secret.
			s.gitStash(r, gitFlash{Action: action, Secret: to.Secret})
			s.gitBack(w, r, proj, "git-once", true)
			return
		}
	case "trigger-switch":
		on := formBool(r, "enabled")
		if _, err = s.Reg.Call(ctx, p, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: proj.ID, RemoteID: rid, Enabled: &on}); err == nil {
			s.gitRedirect(w, r, proj, map[bool]string{true: "trigger_on", false: "trigger_off"}[on], "trigger")
			return
		}
	case "trigger-test":
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.trigger.test", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			to := out.(gitmirror.TriggerTestOut)
			s.gitStash(r, gitFlash{Action: action, TriggerOK: map[bool]string{true: "ok", false: "failed"}[to.OK] + ": " + to.Status})
			s.gitBack(w, r, proj, "trigger", false)
			return
		}
	case "trigger-delete":
		if _, err = s.Reg.Call(ctx, p, "git.trigger.delete", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			s.gitRedirect(w, r, proj, "trigger_deleted", "trigger")
			return
		}
	case "hostkey-scan":
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.remote.hostkey", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			ho := out.(gitmirror.HostKeyOut)
			s.gitStash(r, gitFlash{Action: action, Scan: &gitScan{Host: ho.Host, Keys: ho.Keys, Pinned: ho.Pinned, Changed: ho.Changed, Matches: ho.PinnedFound}})
			s.gitBack(w, r, proj, "hostkey", false)
			return
		}
	case "hostkey-accept":
		replace := formBool(r, "replace")
		if !formBool(r, "compared") {
			err = ops.Invalid("compared", T("git.err.compared"))
			break
		}
		if replace && !formBool(r, "understood") {
			err = ops.Invalid("understood", T("git.err.hostkey_understood"))
			break
		}
		if _, err = s.Reg.Call(ctx, p, "git.remote.hostkey_accept", gitmirror.HostKeyAcceptIn{ProjectID: proj.ID, RemoteID: rid, Fingerprint: r.PostFormValue("fingerprint"), Replace: replace}); err == nil {
			s.gitRedirect(w, r, proj, "hostkey", "setup")
			return
		}
	case "test":
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.remote.test", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			to := out.(gitmirror.TestOut)
			t := &gitTest{OK: to.OK, Message: to.Message, Warning: to.Warning, Empty: to.Empty}
			if !to.OK {
				t.Hint = gitHint(T, to.Code)
			}
			s.gitStash(r, gitFlash{Action: action, Test: t})
			s.gitBack(w, r, proj, "test", false)
			return
		}
	case "push":
		if rid == "" && isFetch(r) {
			// The command palette's "Push to Git now" names no remote: it pushes each that is on and not paused.
			s.gitPushAll(w, r, proj)
			return
		}
		var out any
		if out, err = s.Reg.Call(ctx, p, "git.push", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			if isFetch(r) {
				msg := T("git.push.started")
				if out.(gitmirror.PushOut).AlreadyRunning {
					msg = T("git.push.already")
				}
				w.Header().Set("Content-Type", "application/json")
				b, _ := json.Marshal(map[string]string{"message": msg, "url": "/p/" + proj.Slug + "/git"})
				_, _ = w.Write(b)
				return
			}
			s.gitRedirect(w, r, proj, "pushed", "status")
			return
		}
	case "cancel":
		if _, err = s.Reg.Call(ctx, p, "git.push_cancel", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			s.gitRedirect(w, r, proj, "cancelled", "status")
			return
		}
	case "force":
		typed := r.PostFormValue("confirm")
		if _, err = s.Reg.Call(ctx, p, "git.push_force", gitmirror.ForceIn{ProjectID: proj.ID, RemoteID: rid, Confirm: typed}); err == nil {
			s.gitRedirect(w, r, proj, "force", "status")
			return
		}
		if e := ops.AsError(err); e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict {
			if fd, ferr := s.gitForceData(r, proj); ferr == nil {
				fd.Error, fd.Typed = e.Message, typed
				if isHTMX(r) {
					s.fragment(w, r, http.StatusUnprocessableEntity, "git-force", "git-force", fd)
					return
				}
				s.render(w, r, http.StatusUnprocessableEntity, "git-force", s.page(r, fd.Pg.T("git.force.title", "name", fd.Remote.Name), fd))
				return
			}
		}
	case "secret-ack", "secret-unack":
		_, err = s.Reg.Call(ctx, p, "git.remote.secret_ack", gitmirror.SecretAckIn{ProjectID: proj.ID, RemoteID: rid, Path: r.PostFormValue("path"), SHA256: r.PostFormValue("sha256"), Revoke: action == "secret-unack"})
		if err == nil {
			s.gitRedirect(w, r, proj, map[string]string{"secret-ack": "acked", "secret-unack": "unacked"}[action], "secrets")
			return
		}
	case "delete":
		out, gerr := s.Reg.Call(ctx, p, "git.remote.get", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: rid})
		if gerr != nil {
			err = gerr
			break
		}
		if r.PostFormValue("confirm") != out.(gitmirror.RemoteItem).Item.Name {
			err = ops.Invalid("confirm", T("git.err.delete_confirm"))
			break
		}
		if _, err = s.Reg.Call(ctx, p, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: proj.ID, RemoteID: rid}); err == nil {
			http.Redirect(w, r, "/p/"+proj.Slug+"/git?saved=deleted", http.StatusSeeOther)
			return
		}
	default:
		err = ops.Invalid("", T("git.err.unknown"))
	}
	s.gitFail(w, r, proj, err)
}

// gitPushAll starts a push of every remote of the project that is switched on and not paused, for the palette's command.
func (s *Server) gitPushAll(w http.ResponseWriter, r *http.Request, proj projects.Project) {
	p, ctx := s.principal(r), r.Context()
	T := s.page(r, "", nil).T
	lo, err := s.Reg.Call(ctx, p, "git.remote.list", gitmirror.ProjectIn{ProjectID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	started, running := 0, 0
	var last error
	for _, rem := range lo.(gitmirror.RemoteList).Items {
		if !rem.Enabled || rem.RestorePaused {
			continue
		}
		out, perr := s.Reg.Call(ctx, p, "git.push", gitmirror.RemoteBody{ProjectID: proj.ID, RemoteID: rem.ID})
		switch {
		case perr != nil:
			last = perr
		case out.(gitmirror.PushOut).AlreadyRunning:
			running++
		default:
			started++
		}
	}
	if started == 0 && running == 0 {
		if last == nil {
			last = ops.Conflict(T("git.err.none_on"))
		}
		s.fail(w, r, last)
		return
	}
	msg := T("git.push.started")
	if started+running > 1 {
		msg = T("git.push.started_many", "n", started+running)
	} else if started == 0 {
		msg = T("git.push.already")
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]string{"message": msg, "url": "/p/" + proj.Slug + "/git"})
	_, _ = w.Write(b)
}

// registerGit adds the Git sync pages and the routes behind their buttons.
func (s *Server) registerGit(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/git", s.gitPage)
	m.HandleFunc("GET /p/{slug}/git/force", s.gitForcePage)
	m.HandleFunc("GET /p/{slug}/git/pushes/{pid}", s.gitPushDetail)
	m.HandleFunc("GET /ui/projects/{id}/git/status", s.gitFragment)
	m.HandleFunc("GET /ui/projects/{id}/git/force", s.gitForceFragment)
	s.uiRoute("POST /ui/projects/{id}/git/{action}", s.uiGit)
}

// refuseStrayBox refuses a box that belongs to the other audience, in words, instead of dropping it without a word.
func (s *Server) refuseStrayBox(r *http.Request, audience string) error {
	switch {
	case audience == "public" && formBool(r, "include_notes"):
		return ops.Invalid("include_notes", s.tr(r, "git.err.notes_public"))
	case audience == "private" && formBool(r, "only_done"):
		return ops.Invalid("only_done", s.tr(r, "git.err.done_private"))
	}
	return nil
}
