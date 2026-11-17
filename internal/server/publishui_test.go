package server

import (
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The publish and release pages are the owner's; a stranger finds nothing, an editor is refused, and a token typed into the
// connect form is not in the page it comes back to, whatever went wrong.
func TestThePublishPagesAreTheOwnersAndHoldNoToken(t *testing.T) {
	e := newWriteEnv(t)
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	page := "/p/" + e.slug + "/git/publish"
	if rec := e.s.do(caller{name: "anon"}, "GET", page, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := e.s.do(stranger, "GET", page, ""); rec.Code != 404 {
		t.Errorf("stranger: %d", rec.Code)
	}
	if rec := e.s.do(editor, "GET", page, ""); rec.Code != 403 {
		t.Errorf("editor: %d", rec.Code)
	}
	own := e.s.do(e.owner, "GET", page, "")
	body := stdhtml.UnescapeString(own.Body.String())
	if own.Code != 200 || !strings.Contains(body, "Connect an account") || !strings.Contains(body, `id="pc-token"`) || !strings.Contains(body, `type="password"`) {
		t.Fatalf("the owner's page: %d", own.Code)
	}
	if strings.Contains(body, `id="fields"`) || strings.Contains(body, "/publish/plan") {
		t.Error("the plan form is offered before there is an account")
	}
	for _, a := range []string{"connect", "disconnect", "plan", "publish"} {
		v := url.Values{"provider": {"forgejo"}, "host": {"127.0.0.1:1"}, "token": {"TOKEN-CANARY-ABCDEF123456"}}
		if rec := e.s.post(t, editor, "/ui/projects/"+e.pid+"/publish/"+a, v, "", false); rec.Code != 403 {
			t.Errorf("an editor ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, stranger, "/ui/projects/"+e.pid+"/publish/"+a, v, "", false); rec.Code != 404 {
			t.Errorf("a stranger ran %s: %d", a, rec.Code)
		}
	}
	// a connection that cannot be made says one thing and keeps nothing
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/publish/connect", url.Values{"provider": {"forgejo"}, "host": {"127.0.0.1:1"}, "token": {"TOKEN-CANARY-ABCDEF123456"}, "label": {"x"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a connection to nowhere: %d %s", rec.Code, rec.Body)
	}
	shown := followAs(t, e.s, e.owner, rec)
	if strings.Contains(rec.Header().Get("Location"), "CANARY") || strings.Contains(shown.Body.String(), "TOKEN-CANARY-ABCDEF123456") || !strings.Contains(shown.Body.String(), `role="alert"`) {
		t.Error("the refusal echoes the token or says nothing")
	}
	// the plan form is the owner's, and a plan with no audience is refused before the host is asked
	rec = e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/publish/plan", url.Values{"account_id": {"gac_x"}, "name": {"x"}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(stdhtml.UnescapeString(followAs(t, e.s, e.owner, rec).Body.String()), "Nothing is chosen for you") {
		t.Errorf("a plan without an audience: %d", rec.Code)
	}
}

// The release page needs a remote at GitHub or Forgejo that was made with a token; an ssh remote is told so, and the page
// holds the two forms and no secret.
func TestTheReleasePageSaysWhoCanUseIt(t *testing.T) {
	e := newWriteEnv(t)
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	page := "/p/" + e.slug + "/git/release?remote=" + id
	if rec := e.s.do(editor, "GET", page, ""); rec.Code != 403 {
		t.Errorf("editor: %d", rec.Code)
	}
	rec := e.s.do(e.owner, "GET", page, "")
	body := stdhtml.UnescapeString(rec.Body.String())
	if rec.Code != 200 || !strings.Contains(body, "A release needs a remote at GitHub or Forgejo") || strings.Contains(body, `id="rl-tag"`) {
		t.Errorf("an ssh remote: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/release/start", url.Values{"remote_id": {id}, "tag": {"v1"}, "edition": {"epub"}}, "", false); strings.Contains(rec.Header().Get("Location"), "release=") {
		t.Error("a release was started on an ssh remote")
	}
}

// The created-but-incomplete state tells the truth: the repository exists, nothing was pushed, and the way on is to plan
// again with the empty repository, which is already chosen.
func TestACreatedButIncompleteRepositoryIsShownAsSuch(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	d := publishData{Pg: Page{}, Slug: e.slug, CSRF: "tok", Values: map[string]string{"visibility": "private", "mode": "existing", "owner": "mairead", "name": "salt-road"}, Mode: "existing",
		Incomplet: &publishIncomplete{Message: "The repository mairead/salt-road was created at host (empty, and private). The step that failed was connecting the project to it. No remote was registered in this project. Nothing was pushed.", Repository: "mairead/salt-road", Owner: "mairead", Name: "salt-road"}}
	d.Project = ownerFixture(fixtureProjects()[0])
	html := renderPage(t, s, fixture{"publish", Page{Title: "Publish", Path: "/p/the-salt-road/git/publish", User: fixtureUser("writer"), CSRF: "tok", Data: d}})
	text := stdhtml.UnescapeString(html)
	for _, need := range []string{"The repository was created, but the project is not connected to it", "Nothing was pushed", "mairead/salt-road", "nothing is created a second time"} {
		if !strings.Contains(text, need) {
			t.Errorf("the page lacks %q", need)
		}
	}
	if strings.Contains(text, "/publish/publish") {
		t.Error("the page offers to create the repository again")
	}
}

var reMirrorLine = regexp.MustCompile(`(?is)<div class="mirror-line".*?</div>`)

// Every word on the Git page's remotes, audience, signing and trigger parts, the publish page and the release page comes from
// the catalogue: in the pseudo-locale no plain English is left. (The Markdown copy's status line comes from the mirror's own
// older summary and is not part of this page's catalogue; it is let through by its element.)
func TestThePseudoLocaleLeavesNoEnglishOnTheGitAndPublishPages(t *testing.T) {
	e := newWriteEnv(t)
	if rec := e.s.do(e.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	pub := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	e.makeRemote(t, "Backup", "ssh://git@forge.example.com/a/b.git", "private", url.Values{"include_notes": {"1"}})
	if rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/signing-generate", url.Values{"remote_id": {pub}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("signing: %d", rec.Code)
	}
	set := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/trigger-set", url.Values{"remote_id": {pub}, "trigger_url": {"https://hooks.example.org/x"}}, "", false)
	if set.Code != http.StatusSeeOther {
		t.Fatalf("trigger: %d", set.Code)
	}
	secretPage := followAs(t, e.s, e.owner, set)
	pages := map[string]string{
		"git, a public remote":  e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub, "").Body.String(),
		"git, just made":        e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub+"&saved=audience#carries", "").Body.String(),
		"the trigger secret":    secretPage.Body.String(),
		"publish":               e.s.do(e.owner, "GET", "/p/"+e.slug+"/git/publish", "").Body.String(),
		"release, not eligible": e.s.do(e.owner, "GET", "/p/"+e.slug+"/git/release?remote="+pub, "").Body.String(),
		"force":                 e.s.do(e.owner, "GET", "/p/"+e.slug+"/git/force?remote="+pub, "").Body.String(),
	}
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/audience", url.Values{"remote_id": {pub}, "audience": {"private"}}, "", false)
	pages["a refused change"] = followAs(t, e.s, e.owner, rec).Body.String()
	allowed := map[string]bool{"mirror": true, "backup": true, "salt": true, "road": true, "the": true, "forge": true, "example": true, "com": true, "ssh": true, "main": true, "git": true, "hooks": true, "org": true, "https": true, "http": true, "mairead": true, "ed": true, "wren": true, "scriobh": true}
	for name, body := range pages {
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale", name)
			continue
		}
		var bad []string
		for _, word := range englishIn(reCodeOrOp.ReplaceAllString(reMirrorLine.ReplaceAllString(body, " "), " ")) {
			if !allowed[strings.ToLower(word)] {
				bad = append(bad, word)
			}
		}
		if len(bad) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(bad, " "))
		}
	}
	_ = gitmirror.Preset{}
}

// ---- fixtures for the template lint ----------------------------------------------------------------

func publishFixture() publishData {
	acct := gitmirror.Account{ID: "gac_1", Provider: "forgejo", Host: "git.example.org", Login: "wren", Label: "Home"}
	plan := gitmirror.RepoPlan{Account: acct, Repository: gitmirror.RepoRef{Provider: "forgejo", Host: "git.example.org", Owner: "wren", Name: "salt-road", FullName: "wren/salt-road", Visibility: "public"},
		WillCreate: true, Branch: "main", Audience: "public", OnlyDone: true, PushMode: "manual", RemoteName: "Forgejo: wren/salt-road", Requests: []string{"create_repository", "register_remote", "push"},
		FileCount: 2, TotalBytes: 2048, Files: []gitmirror.PlanFile{{Path: "manuscript/01-one.md", Bytes: 1200}, {Path: "scriobh.json", Bytes: 848}},
		Blockers: []gitmirror.Blocker{{Code: "name_taken", Message: "taken"}}, NeedsConfirm: true, ConfirmWith: "salt-road", PlanToken: "tok"}
	d := publishData{Pg: Page{}, Slug: "the-salt-road", CSRF: "tok", Accounts: []gitmirror.Account{acct}, Account: &acct, Mode: "existing", CanStore: true, Page: 2, PrevPage: 1, Query: "salt",
		Values: map[string]string{"account_id": "gac_1", "name": "salt-road", "audience": "public", "visibility": "public", "only_done": "1", "owner": "wren"}, Plan: &plan, Size: "2 KB",
		Repos:    &gitmirror.RepoList{Account: acct, Items: []gitmirror.RepoListItem{{Owner: "wren", Name: "empty-one", FullName: "wren/empty-one", Private: true, Empty: true}, {Owner: "wren", Name: "old", FullName: "wren/old", Archived: true}}, NextPage: 3},
		Withheld: []publishWithheld{{Path: "manuscript/02-two.md", Title: "Chapter Two", Reason: "Not done yet"}}, Blockers: []publishBlocker{{Code: "name_taken", Text: "A repository with that name already exists."}}}
	d.Project = ownerFixture(fixtureProjects()[0])
	return d
}

func releaseFixture() releaseData {
	rel := gitmirror.Release{ID: "grl_1", Tag: "v1.0", Format: "epub", Status: "ok", InputHash: "0123456789abcdef", FileSHA256: "fedcba9876543210", AssetName: "salt-road-epub3-v1.0-0123456789ab.epub", AssetBytes: 6000, CommitSHA: "abcdef0123456789", ReleaseURL: "https://git.example.org/wren/salt-road/releases/tag/v1.0", Reused: true}
	failed := gitmirror.Release{ID: "grl_2", Tag: "v1.1", Format: "pdf", Status: "error", ErrorCode: "tag_conflict"}
	row := releaseRow{Release: rel, Word: "Published", Tone: "ok", Size: "6 KB", When: "13 Nov 2026, 09:00", Reason: "Already there"}
	bad := releaseRow{Release: failed, Word: "Did not finish", Tone: "danger", When: "13 Nov 2026, 10:00", Reason: "That tag already has a different edition."}
	d := releaseData{Pg: Page{}, Slug: "the-salt-road", CSRF: "tok", Remote: gitRemoteFixture("synced"), Items: []releaseRow{row, bad}, Current: &bad, Values: map[string]string{"tag": "v1.1", "edition": "pdf:print-6x9"}, Eligible: true,
		Editions: releaseEditions, PreviewMB: 64,
		Preview: &gitmirror.RepoReleaseOut{Preview: true, Repository: "wren/salt-road", Commit: "abcdef0123456789", MaxBytes: 64 << 20, ReleaseExists: true}}
	d.Project = ownerFixture(fixtureProjects()[0])
	return d
}

// A link may name the account, the way, the owner and the name, and nothing about what is published or who can read it:
// the audience, the visibility and the two boxes never come from an address.
func TestALinkCannotPreselectWhatIsPublished(t *testing.T) {
	r := httptest.NewRequest("GET", "/p/x/git/publish?account_id=gac_1&mode=existing&owner=o&name=n&q=s&visibility=public&audience=public&only_done=1&include_notes=1&sign_commits=1&push_mode=on_save&description=x", nil)
	got := publishLinkValues(r)
	for _, k := range []string{"visibility", "audience", "only_done", "include_notes", "sign_commits", "push_mode", "description"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was taken from the address", k)
		}
	}
	for _, k := range []string{"account_id", "mode", "owner", "name", "q"} {
		if got[k] == "" {
			t.Errorf("%s was not taken from the address", k)
		}
	}
	// and a plan id that is not this session's is an expired plan, not a plan
	e := newWriteEnv(t)
	rec := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git/publish?plan=pln_nope&visibility=public&audience=public", "")
	body := stdhtml.UnescapeString(rec.Body.String())
	if rec.Code != 200 || !strings.Contains(body, "no longer kept") || strings.Contains(body, "checked") && strings.Contains(body, `value="public" checked`) {
		t.Errorf("an unknown plan: %d", rec.Code)
	}
}
