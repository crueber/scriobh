package server

import (
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// enT is the English catalogue, for the pieces that take a tfn.
func enT(key string, args ...any) string { return i18n.Default.T("en", key, args...) }

// ---- fixtures for the template lint ---------------------------------------------------------------

func gitRemoteFixture(state string) gitmirror.Remote {
	r := gitmirror.Remote{ID: "rem_a", ProjectID: "p-a", Name: "Backup", URL: "ssh://git@forge.example.com/wren/salt-road.git", Host: "forge.example.com",
		Branch: "main", AuthKind: "ssh_deploy_key", HasSecret: true, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample scriobh", KeyFingerprint: "SHA256:AbCdEf",
		HostKey: &gitmirror.PinnedKey{Type: "ssh-ed25519", Fingerprint: "SHA256:HostFinger"}, PushMode: "on_save", IntervalMinutes: 60, IncludeNotes: true,
		Author: "Scríobh <noreply@scriobh>", Enabled: true, Setup: []string{}, Acknowledged: []gitmirror.Ack{},
		Status: gitmirror.Status{State: state, MirrorHealthy: true, LastPushAt: "2026-11-13T09:00:00Z"}}
	switch state {
	case "new":
		r.HostKey = nil
		r.Setup = []string{"host_key"}
		r.Status.LastPushAt = ""
	case "synced":
		r.Status.InSync = true
	case "behind":
		r.Status.Behind = true
		r.Status.LastError = "forge.example.com could not be reached. The commit is kept locally and will be tried again."
		r.Status.LastErrorCode = "unreachable"
		r.Status.NextAttemptAt = "2026-11-13T10:00:00Z"
	case "running":
		r.Status.Running = true
	case "rejected":
		r.Status.Rejected = &gitmirror.RejectedInfo{PushID: "gp_1", RemoteHead: "0123456789abcdef", MoreCommits: true,
			Commits: []gitmirror.ForeignCommit{{SHA: "0123456789abcdef0123456789abcdef01234567", Author: "Someone Else", When: "2026-11-12T18:00:00Z", Subject: "Fix a typo in the README"}}}
	case "secret":
		r.Status.State = "blocked"
		r.Status.LastErrorCode = "secret_in_tree"
		r.Status.Secrets = []gitmirror.SecretFindingView{{Path: "manuscript/03-the-letter.md", Rule: "a private key", SHA: "deadbeefdeadbeefdeadbeef"}}
	case "hostkey":
		r.Status.State = "blocked"
		r.Status.LastErrorCode = "host_key_changed"
		r.Status.LastError = "The host key of forge.example.com has changed."
	}
	return r
}

func gitFixture(state string, owner bool) gitData {
	proj := ownerFixture(fixtureProjects()[0])
	if !owner {
		proj.Role = ops.ProjectEditor
	}
	rem := gitRemoteFixture(state)
	d := gitData{Project: proj, Slug: proj.Slug, CSRF: "tok_writer", IsOwner: owner, CanEdit: true, Enabled: true, Remote: &rem, Preset: gitmirror.Preset{ID: "custom"},
		Presets: []gitmirror.Preset{{ID: "forgejo", Label: "Forgejo", Notes: "Add the public key under the repository's Settings."}, {ID: "github", Label: "GitHub", DefaultHost: "github.com"}, {ID: "custom", Label: "Any Git server"}},
		Values:  map[string]string{}, Zone: "UTC", Poll: rem.Status.Running, KeyLink: "https://forge.example.com/wren/salt-road/settings/keys"}
	d.Banner = gitStatusBanner(rem, time.UTC, true, enT)
	d.Steps = gitSteps(rem, enT)
	d.Pg = Page{}
	d.Rows = []gitRemoteRow{{Remote: rem, Selected: true, State: "Pushed", Tone: "ok", LastPush: "Pushed 13 Nov 2026, 09:00."}}
	d.MaxRemotes, d.CanAdd, d.CanPublish = 5, owner, owner
	d.Mirror = &mirrorView{State: "ok", Line: "The Markdown copy is up to date.", Detail: "12 files."}
	for _, f := range rem.Status.Secrets {
		d.Secrets = append(d.Secrets, gitSecretRow{SecretFindingView: f, Short: gitShort(f.SHA)})
	}
	d.Pushes = []gitPushRow{{Push: gitmirror.Push{ID: "gp_1", Status: "ok", Trigger: "save", StartedAt: "2026-11-13T09:00:00Z", CommitSHA: "abcdef0123456789", Subject: "The Salt Road · ch 3: +214 words", FilesChanged: 2}, When: "13 Nov 2026, 09:00", Short: "abcdef0", Word: "Pushed", Tone: "ok"},
		{Push: gitmirror.Push{ID: "gp_0", Status: "error", Trigger: "retry", StartedAt: "2026-11-13T08:00:00Z", ErrorMessage: "forge.example.com could not be reached."}, When: "13 Nov 2026, 08:00", Word: "Failed", Tone: "warn", Detail: "forge.example.com could not be reached."}}
	return d
}

func gitEmptyFixture(owner bool) gitData {
	d := gitFixture("new", owner)
	d.Remote, d.Steps, d.Pushes = nil, nil, nil
	return d
}

// ---- the page for each reader and each state -------------------------------------------------------------

func TestGitPageStates(t *testing.T) {
	s := newWebServer(t)
	cases := []struct {
		name  string
		d     gitData
		want  []string
		avoid []string
	}{
		{"empty, owner", gitEmptyFixture(true), []string{"Add a remote", `action="/ui/projects/prj_a/git/create"`}, []string{`Push now</button>`}},
		{"empty, editor", gitEmptyFixture(false), []string{"Only the project's owner can add one"}, []string{`/git/create"`}},
		{"new, owner", gitFixture("new", true), []string{"Not ready yet", "Scan the host key", "Copy the key", "Test the connection", "git/delete"}, nil},
		{"new, editor", gitFixture("new", false), []string{"Scan the host key", "Test the connection"}, []string{"git/delete", "git/keygen", "git/settings", "git/address"}},
		{"synced", gitFixture("synced", true), []string{"Up to date.", "What was sent"}, []string{"Replace the history"}},
		{"behind", gitFixture("behind", true), []string{"A commit is waiting to be pushed", "Next automatic try"}, nil},
		{"running", gitFixture("running", true), []string{"Pushing to forge.example.com now", "Cancel the push", `data-poll="true"`}, []string{`Push now</button>`}},
		{"rejected, owner", gitFixture("rejected", true), []string{"The repository has other commits", "Fix a typo in the README", "Replace the history", "/import/git?project="}, nil},
		{"rejected, editor", gitFixture("rejected", false), []string{"Fix a typo in the README", "Only the project's owner can do this"}, []string{"Replace the history&hellip;", "Replace the history…", "/import/git?project="}},
		{"secret, owner", gitFixture("secret", true), []string{"manuscript/03-the-letter.md", "a private key", "It is not a secret, push this file", "covers exactly the text it has now"}, nil},
		{"secret, editor", gitFixture("secret", false), []string{"manuscript/03-the-letter.md"}, []string{"It is not a secret, push this file"}},
		{"host key changed", gitFixture("hostkey", true), []string{"The host key of forge.example.com has changed", "Nothing was sent"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			html := renderPage(t, s, fixture{"git", pageWith(c.d)})
			lint(t, c.name, html)
			text := stdhtml.UnescapeString(html) // the catalogue's words are escaped (an apostrophe) where the old literal text was not
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range c.avoid {
				if strings.Contains(text, w) {
					t.Errorf("should not contain %q", w)
				}
			}
			if strings.Contains(html, "token\" value") || strings.Contains(html, "BEGIN OPENSSH") {
				t.Error("a credential reached the page")
			}
		})
	}
}

func pageWith(d gitData) Page {
	return Page{Title: "Git sync", Path: "/p/the-salt-road/git", User: fixtureUser("writer"), CSRF: "tok_writer", Data: d}
}

func TestGitBannerWords(t *testing.T) {
	for state, want := range map[string]string{"new": "Not ready yet", "synced": "Up to date.", "behind": "A commit is waiting", "running": "Pushing to", "rejected": "changes this project did not make", "secret": "something looks like a secret", "hostkey": "has changed"} {
		r := gitRemoteFixture(state)
		if state == "new" {
			r.Setup = nil
			r.Status.State = "new"
		}
		b := gitStatusBanner(r, time.UTC, true, enT)
		if state == "new" {
			if !strings.Contains(b.Headline, "Ready") {
				t.Errorf("new: %q", b.Headline)
			}
			continue
		}
		if !strings.Contains(b.Headline+" "+b.Detail, want) {
			t.Errorf("%s: banner %q / %q does not say %q", state, b.Headline, b.Detail, want)
		}
		if b.Tone == "" {
			t.Errorf("%s: no tone", state)
		}
	}
	// Every code the ops can give has advice a person can act on, and none of it names a variable by accident.
	for _, code := range gitHintCodes {
		hint := gitHint(enT, code)
		if len(hint) < 20 || strings.Contains(hint, "!") || strings.HasPrefix(hint, "git.hint.") {
			t.Errorf("hint for %s is missing, thin or shouts: %q", code, hint)
		}
	}
	if !strings.Contains(gitHint(enT, "address_blocked"), "SCRIOBH_GIT_ALLOW_PRIVATE") {
		t.Error("the blocked address advice does not say how an administrator allows it")
	}
}

func TestRepoParts(t *testing.T) {
	for in, want := range map[string]string{
		"ssh://git@github.com/wren/salt-road.git":        "github.com|wren|salt-road",
		"git@github.com:wren/salt-road.git":              "github.com|wren|salt-road",
		"ssh://git@forge.example.com:2222/wren/book.git": "forge.example.com|wren|book",
		"https://forge.example.com/wren/book.git":        "forge.example.com|wren|book",
	} {
		h, o, r := repoParts(in)
		if got := h + "|" + o + "|" + r; got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

// ---- who may see and do what ------------------------------------------------------------------------------

func TestGitRoutesAreGuarded(t *testing.T) {
	e := newWriteEnv(t)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	reader := e.s.newCaller(t, "reader", ops.RoleReader)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	page := "/p/" + e.slug + "/git"
	pid := "/ui/projects/" + e.pid + "/git/"

	if rec := e.s.do(caller{name: "anon"}, "GET", page, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := e.s.do(stranger, "GET", page, ""); rec.Code != 404 {
		t.Errorf("a stranger sees the project's Git page: %d", rec.Code)
	}
	if rec := e.s.do(reader, "GET", page, ""); rec.Code != 403 && rec.Code != 404 {
		t.Errorf("a reader sees the Git page: %d", rec.Code)
	}
	// With no remote yet, the owner is offered the form and an editor is told who can add one.
	own := e.s.do(e.owner, "GET", page, "")
	if own.Code != 200 || !strings.Contains(own.Body.String(), "Add a remote") || !strings.Contains(own.Body.String(), "/git/create") {
		t.Errorf("owner, no remote: %d", own.Code)
	}
	ed := e.s.do(editor, "GET", page, "")
	if ed.Code != 200 || strings.Contains(ed.Body.String(), "/git/create") || !strings.Contains(stdhtml.UnescapeString(ed.Body.String()), "Only the project's owner can add one") {
		t.Errorf("editor, no remote: %d", ed.Code)
	}
	// An editor cannot add a remote, a stranger finds nothing, and a form without the session's token is refused.
	form := url.Values{"name": {"Backup"}, "url": {"ssh://git@forge.example.com/a/b.git"}, "audience": {"private"}, "include_notes": {"1"}}
	if rec := e.s.post(t, editor, pid+"create", form, "", false); rec.Code != 403 {
		t.Errorf("an editor created a remote: %d", rec.Code)
	}
	if rec := e.s.post(t, stranger, pid+"create", form, "", false); rec.Code != 404 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, pid+"create", form, "-", false); rec.Code != 403 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, pid+"nonsense", url.Values{}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("unknown action: %d", rec.Code)
	}
	// A refused value is shown beside the form in plain words.
	bad := e.s.post(t, e.owner, pid+"create", url.Values{"name": {"Backup"}, "url": {"http://forge.example.com/a/b.git"}, "audience": {"private"}}, "", false)
	if bad.Code != http.StatusSeeOther || !strings.Contains(followAs(t, e.s, e.owner, bad).Body.String(), `role="alert"`) {
		t.Errorf("a bad address: %d", bad.Code)
	}
	// The owner adds a remote; the page then shows its setup, and what an editor may and may not see.
	made := e.s.post(t, e.owner, pid+"create", form, "", false)
	if made.Code != http.StatusSeeOther || !strings.Contains(made.Header().Get("Location"), "saved=created") {
		t.Fatalf("create: %d %s", made.Code, made.Body)
	}
	ownPage := e.s.do(e.owner, "GET", page, "").Body.String()
	for _, need := range []string{"Setup", "Scan the host key", "Make a new key", "git/delete", "git/settings", "git/address", "Test the connection"} {
		if !strings.Contains(ownPage, need) {
			t.Errorf("the owner's page lacks %q", need)
		}
	}
	if strings.Contains(ownPage, "BEGIN OPENSSH") || strings.Contains(ownPage, "PRIVATE KEY") {
		t.Error("a private key reached the page")
	}
	edPage := stdhtml.UnescapeString(e.s.do(editor, "GET", page, "").Body.String())
	for _, need := range []string{"Scan the host key", "Test the connection", "Only the project's owner can change the remote"} {
		if !strings.Contains(edPage, need) {
			t.Errorf("the editor's page lacks %q", need)
		}
	}
	for _, gone := range []string{"git/delete", "git/keygen", "git/settings", "git/address", "git/hostkey-accept", "Make a new key"} {
		if strings.Contains(edPage, gone) {
			t.Errorf("the editor is offered %q", gone)
		}
	}
	// Owner-only actions are refused for an editor with the status the op gives, and never run.
	for _, a := range []string{"settings", "address", "enable", "disable", "keygen", "hostkey-accept", "secret-ack", "secret-unack", "delete", "force", "create"} {
		if rec := e.s.post(t, editor, pid+a, url.Values{"confirm": {"Backup"}, "understood": {"1"}, "compared": {"1"}}, "", false); rec.Code != 403 {
			t.Errorf("an editor ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, stranger, pid+a, url.Values{"confirm": {"Backup"}}, "", false); rec.Code != 404 {
			t.Errorf("a stranger ran %s: %d", a, rec.Code)
		}
	}
	// The fragment and the pages that read ops are guarded the same way.
	for _, p := range []string{"/p/" + e.slug + "/git/force", "/p/" + e.slug + "/git/pushes/gp_x", "/ui/projects/" + e.pid + "/git/status", "/ui/projects/" + e.pid + "/git/force"} {
		if rec := e.s.do(stranger, "GET", p, ""); rec.Code != 404 {
			t.Errorf("stranger %s: %d", p, rec.Code)
		}
		if rec := e.s.do(caller{name: "anon"}, "GET", p, ""); rec.Code != http.StatusSeeOther {
			t.Errorf("anonymous %s: %d", p, rec.Code)
		}
	}
	if rec := e.s.do(editor, "GET", "/p/"+e.slug+"/git/force", ""); rec.Code != 403 {
		t.Errorf("an editor opened the force page: %d", rec.Code)
	}
	if rec := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git/force", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Type the name of the remote to confirm") {
		t.Errorf("the owner's force page: %d", rec.Code)
	}
	// A force push needs the name typed exactly: a wrong one is shown beside the box and nothing runs.
	rid := regexp.MustCompile(`name="remote_id" value="([^"]+)"`).FindStringSubmatch(ownPage)[1]
	wrong := e.s.post(t, e.owner, pid+"force", url.Values{"confirm": {"backup"}, "remote_id": {rid}}, "", false)
	if wrong.Code != 422 || !strings.Contains(wrong.Body.String(), `role="alert"`) { // the dialog's fragment answers itself
		t.Errorf("a wrongly typed name: %d", wrong.Code)
	}
	// Removing a remote also needs its name.
	if rec := e.s.post(t, e.owner, pid+"delete", url.Values{"confirm": {"nope"}, "remote_id": {rid}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("delete with the wrong name: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, pid+"delete", url.Values{"confirm": {"Backup"}, "remote_id": {rid}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", rec.Code)
	}
	if b := e.s.do(e.owner, "GET", page, "").Body.String(); !strings.Contains(b, "Add a remote") {
		t.Error("the remote is still there after it was removed")
	}
}

func gitForceFixture() gitForceData {
	return gitForceData{Project: ownerFixture(fixtureProjects()[0]), Slug: "the-salt-road", CSRF: "tok_writer", Remote: gitRemoteFixture("rejected"), Error: "Type the remote's name exactly to confirm.", Typed: "Back"}
}

func gitPushFixture() gitPushPage {
	return gitPushPage{Project: ownerFixture(fixtureProjects()[0]), Slug: "the-salt-road", Branch: "main", Subject: "The Salt Road · ch 3: +214 words",
		Push:    gitPushRow{Push: gitmirror.Push{ID: "gp_1", Status: "ok", Trigger: "save", CommitSHA: "abcdef0123456789", FilesChanged: 2}, When: "13 Nov 2026, 09:00", Short: "abcdef0", Word: "Pushed", Tone: "ok"},
		Docs:    []gitDocRow{{Path: "manuscript/03-the-harbour.md", Change: "changed", Words: 214, Abs: 214}, {Path: "manuscript/02-low-water.md", Change: "changed", Words: -12, Abs: 12}},
		Files:   []gitManifestRow{{Path: "scriobh.json", Hash: "0123456789abcdef"}, {Path: "manuscript/03-the-harbour.md", Hash: "fedcba9876543210"}},
		Foreign: []gitmirror.ForeignCommit{{SHA: "0123456789abcdef", Author: "Someone Else", Subject: "Fix a typo"}}, Started: "13 Nov 2026, 09:00"}
}

func TestGitBannerSaysWhyCredentialsAreOff(t *testing.T) {
	r := gitRemoteFixture("synced")
	r.Enabled = false
	r.Status.State = "blocked"
	r.Status.LastErrorCode = "credentials_unreadable"
	r.Status.LastError = "The credential for \"Backup\" cannot be read with this installation's secret key."
	b := gitStatusBanner(r, time.UTC, true, enT)
	if !strings.Contains(b.Headline, "cannot be read") || b.Tone != "danger" || !strings.Contains(b.Hint, "new deploy key") {
		t.Errorf("a remote switched off for unreadable credentials is described as %+v", b)
	}
	r.Status.LastErrorCode = ""
	if b := gitStatusBanner(r, time.UTC, true, enT); !strings.Contains(b.Headline, "switched off") {
		t.Errorf("a remote the owner switched off: %+v", b)
	}
}

// The two ways out of a refused push do not depend on the list of the remote's commits having been read.
func TestRejectedExitsDoNotNeedTheCommitList(t *testing.T) {
	s := newWebServer(t)
	d := gitFixture("rejected", true)
	d.Remote.Status.Rejected = nil
	html := renderPage(t, s, fixture{"git", pageWith(d)})
	lint(t, "rejected without a list", html)
	for _, need := range []string{"Replace the history", "Import it", "the refusal stands"} {
		if !strings.Contains(html, need) {
			t.Errorf("missing %q", need)
		}
	}
}

func TestGitOwnerControlsFollowTheRole(t *testing.T) {
	e := newWriteEnv(t)
	admin := e.s.newCaller(t, "admin", ops.RoleAdmin)
	e.s.addMember(t, e.pid, admin.userID, ops.ProjectEditor)
	page := "/p/" + e.slug + "/git"
	if rec := e.s.do(admin, "GET", page, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/git/create") {
		t.Errorf("an administrator who is a member as an editor is not offered the owner's form: %d", rec.Code)
	}
}

// A token entered for an https remote is stored, never shown again, and the page says the user name only.
func TestAnHTTPSTokenNeverComesBack(t *testing.T) {
	e := newWriteEnv(t)
	pid := "/ui/projects/" + e.pid + "/git/"
	token := "tok-9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	made := e.s.post(t, e.owner, pid+"create", url.Values{"name": {"Backup"}, "url": {"https://forge.example.com/wren/book.git"}, "username": {"wren"}, "token": {token}, "audience": {"private"}, "include_notes": {"1"}}, "", false)
	if made.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", made.Code, made.Body)
	}
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git", "").Body.String()
	if strings.Contains(body, token) {
		t.Error("the token is on the page")
	}
	for _, need := range []string{"wren", "Replace the token", "Stored, not shown"} {
		if !strings.Contains(body, need) {
			t.Errorf("the page lacks %q", need)
		}
	}
	// Replacing it through the settings form still shows nothing.
	rid := regexp.MustCompile(`name="remote_id" value="([^"]+)"`).FindStringSubmatch(body)[1]
	other := "tok-00000000000000000000000000000000"
	rec := e.s.post(t, e.owner, pid+"settings", url.Values{"remote_id": {rid}, "name": {"Backup"}, "branch": {"main"}, "push_mode": {"manual"}, "include_notes": {"1"}, "username": {"wren"}, "token": {other}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=token") {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if after := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git", "").Body.String(); strings.Contains(after, other) || strings.Contains(after, token) {
		t.Error("a token is on the page after it was replaced")
	}
}

// Scanning a host this server may not connect to says why, in words, beside the button that was pressed.
func TestScanOfABlockedAddressExplainsItself(t *testing.T) {
	e := newWriteEnv(t)
	pid := "/ui/projects/" + e.pid + "/git/"
	if rec := e.s.post(t, e.owner, pid+"create", url.Values{"name": {"Backup"}, "url": {"ssh://git@127.0.0.1:1/a/b.git"}, "audience": {"private"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := e.s.post(t, e.owner, pid+"hostkey-scan", url.Values{}, "", false)
	body := followAs(t, e.s, e.owner, rec).Body.String() // the refusal is shown by the page after the redirect
	if rec.Code != http.StatusSeeOther || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "SCRIOBH_GIT_ALLOW_PRIVATE") {
		t.Errorf("a scan of a loopback address: %d, and the page names the setting: %v", rec.Code, strings.Contains(body, "ALLOW_PRIVATE"))
	}
	// A test of the same address (no host key accepted yet) is a result with advice, not an error page.
	rec = e.s.post(t, e.owner, pid+"test", url.Values{}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(followAs(t, e.s, e.owner, rec).Body.String(), "The connection failed.") {
		t.Errorf("a test of a loopback address: %d", rec.Code)
	}
}
