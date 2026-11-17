package gitmirror_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// ---- whose token it is ------------------------------------------------------------

func TestAnAccountBelongsToThePersonWhoConnectedIt(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	// Another person, even one with a role on the project, does not see it or use it; an administrator neither.
	for who, p := range map[string]ops.Principal{"editor": h.editor, "admin": h.admin()} {
		list := h.must(p, "repo.account.list", struct{}{}).(gitmirror.AccountList)
		if len(list.Items) != 0 {
			t.Errorf("%s sees accounts: %+v", who, list)
		}
	}
	admin := h.admin()
	if _, err := h.call(admin, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("an administrator listing the owner's repositories: %v", err)
	}
	if _, err := h.call(admin, "repo.plan", h.planIn(acct.ID)); h.code(err) != ops.CodeNotFound {
		t.Errorf("an administrator planning with the owner's account: %v", err)
	}
	if _, err := h.call(admin, "repo.account.disconnect", gitmirror.AccountRef{AccountID: acct.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("an administrator disconnecting it: %v", err)
	}
	before := len(f.requests())
	if _, err := h.call(h.owner, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: "gac_nothere"}); h.code(err) != ops.CodeNotFound {
		t.Errorf("an account that does not exist: %v", err)
	}
	if len(f.requests()) != before {
		t.Errorf("a refused call reached the host")
	}
	// Connecting again with the same token replaces it, it does not add one.
	again := h.connect(f)
	if again.ID != acct.ID {
		t.Errorf("reconnecting made a second account: %+v %+v", acct, again)
	}
	if got := h.must(h.owner, "repo.account.list", struct{}{}).(gitmirror.AccountList); len(got.Items) != 1 {
		t.Errorf("accounts = %+v", got)
	}
	// A wrong token is refused by the host and nothing is stored.
	if _, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: f.host(), Token: "tok_wrong_wrong_wrong_wrong"}); err == nil || h.code(err) != ops.CodeInvalid {
		t.Errorf("a wrong token: %v", err)
	}
	if got := h.must(h.owner, "repo.account.list", struct{}{}).(gitmirror.AccountList); len(got.Items) != 1 {
		t.Errorf("accounts after a wrong token = %+v", got)
	}
	// The limit.
	for i := 0; i < 5; i++ {
		f2 := newFakeHost(t, "forgejo")
		f2.login = "u" + string(rune('a'+i))
		_, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: f2.host(), Token: f2.token})
		if i < 4 && err != nil {
			t.Fatalf("account %d: %v", i+2, err)
		}
		if i == 4 && h.code(err) != ops.CodeConflict {
			t.Errorf("the sixth account: %v", err)
		}
	}
	// Disconnecting forgets it, and a remote made with it keeps working on its own copy.
	out, _ := h.publish(h.planIn(acct.ID), "")
	h.must(h.owner, "repo.account.disconnect", gitmirror.AccountRef{AccountID: acct.ID})
	if _, err := h.call(h.owner, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("a disconnected account: %v", err)
	}
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	p := h.pushRemote(out.Remote.ID)
	if p.Status != "ok" {
		t.Errorf("the remote does not push on its own token: %+v", p)
	}
}

func TestEveryNewOpIsForTheOwnerAndTheRepoOpsForSessionsOnly(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	pl := h.plan(h.planIn(acct.ID))
	calls := map[string]any{
		"repo.account.list":       struct{}{},
		"repo.account.connect":    gitmirror.AccountConnectIn{Provider: "forgejo", Host: "git.example.org", Token: "tok_abcdefghijklmnopqrstuvwxyz"},
		"repo.account.disconnect": gitmirror.AccountRef{AccountID: acct.ID},
		"repo.list":               gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID},
		"repo.plan":               h.planIn(acct.ID),
		"repo.publish":            publishFrom(h.planIn(acct.ID), pl.PlanToken, ""),
		"repo.release":            gitmirror.RepoReleaseIn{ProjectID: h.pid, Tag: "v1", Format: "epub"},
		"repo.release_get":        gitmirror.ReleaseGetIn{ProjectID: h.pid, ReleaseID: "grl_x"},
		"repo.release_list":       gitmirror.ReleaseListIn{ProjectID: h.pid},
		"git.signing.get":         gitmirror.ProjectIn{ProjectID: h.pid},
		"git.signing.generate":    gitmirror.SigningGenerateIn{ProjectID: h.pid},
		"git.trigger.get":         gitmirror.RemoteQuery{ProjectID: h.pid},
		"git.trigger.set":         gitmirror.TriggerSetIn{ProjectID: h.pid, URL: ptr("https://ci.example.org/hook")},
		"git.trigger.delete":      gitmirror.RemoteQuery{ProjectID: h.pid},
		"git.trigger.test":        gitmirror.RemoteBody{ProjectID: h.pid},
	}
	for op, in := range calls {
		// A key of any scope, pinned or not, is refused by the repo ops (and by the account ops).
		if strings.HasPrefix(op, "repo.") {
			for _, scope := range []string{"read", "write", "admin"} {
				k := keyPrincipal(h, scope)
				if _, err := h.call(k, op, in); h.code(err) != ops.CodeForbidden && h.code(err) != ops.CodeNotFound {
					t.Errorf("%s key calling %s: %v", scope, op, err)
				}
				k.ProjectID = h.pid
				if _, err := h.call(k, op, in); h.code(err) != ops.CodeForbidden && h.code(err) != ops.CodeNotFound {
					t.Errorf("pinned %s key calling %s: %v", scope, op, err)
				}
			}
		}
		if !strings.HasPrefix(op, "repo.account.") {
			for who, p := range map[string]ops.Principal{"editor": h.editor, "reader": h.reader} {
				if _, err := h.call(p, op, in); h.code(err) != ops.CodeForbidden {
					t.Errorf("%s calling %s: %v", who, op, err)
				}
			}
			if _, err := h.call(h.outside, op, in); h.code(err) != ops.CodeNotFound {
				t.Errorf("a stranger calling %s: %v", op, err)
			}
		}
	}
	if f.writes() != 0 {
		t.Errorf("a refused call changed something at the host")
	}
}

// ---- a trashed project ------------------------------------------------------------

func TestATrashedProjectRefusesPublishingReleasingTriggersAndSigning(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	out, _ := h.publish(h.planIn(acct.ID), "")
	h.must(h.owner, "project.trash", projects.StateIn{ID: h.pid})
	before := len(f.requests())
	calls := map[string]any{
		"repo.list":            gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID},
		"repo.plan":            h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "other" }),
		"repo.publish":         publishFrom(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "other" }), "x", ""),
		"repo.release":         gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: out.Remote.ID, Tag: "v1", Format: "epub"},
		"git.trigger.set":      gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: out.Remote.ID, URL: ptr("https://ci.example.org/hook")},
		"git.trigger.test":     gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: out.Remote.ID},
		"git.signing.generate": gitmirror.SigningGenerateIn{ProjectID: h.pid},
		"git.push":             gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: out.Remote.ID},
	}
	for op, in := range calls {
		_, err := h.call(h.owner, op, in)
		ae := ops.AsError(err)
		if ae.Code != ops.CodeConflict || ae.Data["reason"] != ops.ReasonProjectTrashed {
			t.Errorf("%s on a trashed project: %v", op, err)
		}
	}
	if len(f.requests()) != before {
		t.Errorf("a trashed project reached the host: %v", f.requests()[before:])
	}
	// Restored, it works again.
	h.must(h.owner, "project.restore", projects.StateIn{ID: h.pid})
	h.must(h.owner, "repo.plan", h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "other" }))
}

// ---- what a host may do to us -------------------------------------------------------

func TestAHostThatMisbehavesIsBoundedAndNothingItSaysIsKept(t *testing.T) {
	hostile := &atomic.Int64{}
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostile.Add(1) }))
	defer elsewhere.Close()
	cases := []struct {
		name, mode string
		wantCode   ops.Code
		wantGit    string
		hasWords   string // what the host said that must not come back
	}{
		{"rate limit by header", "ratelimit", ops.CodeRateLimited, "provider_rate_limited", "SECRET-RATE-WORDS"},
		{"rate limit by status", "ratelimit429", ops.CodeRateLimited, "provider_rate_limited", "SECRET-RATE-WORDS"},
		{"single sign-on", "sso", ops.CodeProvider, "sso_required", "SSO-URL-CANARY"},
		{"bad gateway", "5xx", ops.CodeProvider, "provider_unavailable", "SECRET-5XX-WORDS"},
		{"a refused token", "unauth", ops.CodeProvider, "auth_failed", "SECRET-AUTH-WORDS"},
		{"a huge answer", "huge", ops.CodeProvider, "provider_bad_response", ""},
		{"malformed JSON", "malformed", ops.CodeProvider, "provider_bad_response", ""},
		{"a redirect", "redirect", ops.CodeProvider, "provider_bad_response", ""},
		{"a slow answer", "slow", ops.CodeProvider, "timeout", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, f, acct := setupPublish(t, "forgejo")
			f.mu.Lock()
			f.mode, f.redirectTo, f.slow = c.mode, elsewhere.URL+"/loot", 5*time.Second
			f.mu.Unlock()
			gitmirror.SetProviderTimeout(400 * time.Millisecond)
			before := hostile.Load()
			start := time.Now()
			_, err := h.call(h.owner, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID})
			took := time.Since(start)
			t.Run("time", func(t *testing.T) {
				testutil.Timing(t)
				if took > 3*time.Second {
					t.Errorf("took %v", took)
				}
			})
			ae := ops.AsError(err)
			if err == nil || ae.Code != c.wantCode || ae.Data["git_code"] != c.wantGit {
				t.Fatalf("error = %+v, want %s %s", ae, c.wantCode, c.wantGit)
			}
			if c.hasWords != "" && strings.Contains(err.Error()+mustJSON(t, ae), c.hasWords) {
				t.Errorf("what the host said is in the error: %v", err)
			}
			if strings.Contains(err.Error()+mustJSON(t, ae), f.token) {
				t.Errorf("the token is in the error")
			}
			if strings.Contains(h.logs.String(), f.token) {
				t.Errorf("the token is in the log")
			}
			if hostile.Load() != before {
				t.Errorf("a redirect was followed, with the token, to another server")
			}
			if c.wantCode == ops.CodeRateLimited && (ae.RetryAfterSeconds < 7 || ae.RetryAfterSeconds > 3600) {
				t.Errorf("retry after %d", ae.RetryAfterSeconds)
			}
		})
	}
}

func TestAHostOnAPrivateAddressIsRefusedUnlessAllowed(t *testing.T) {
	f := newFakeHost(t, "forgejo")
	h := newHarness(t, func(o *gitmirror.Options) { o.AllowPrivate = false })
	_, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: f.host(), Token: f.token})
	if err == nil || !strings.Contains(err.Error(), "could not be reached or did not accept the token") {
		t.Fatalf("connect to a loopback host: %v", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("the host was reached %d times", n)
	}
	// And the names a person may type: no scheme, no path, no credentials.
	for _, bad := range []string{"https://git.example.org", "git.example.org/x", "user@git.example.org", "git.example.org?x=1", "-bad.example.org", ""} {
		if _, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: bad, Token: "tok_abcdefghijklmnopqrstuvwxyz"}); h.code(err) != ops.CodeInvalid {
			t.Errorf("host %q: %v", bad, err)
		}
	}
	if _, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "gitlab", Host: "gitlab.com", Token: "tok_abcdefghijklmnopqrstuvwxyz"}); h.code(err) != ops.CodeInvalid {
		t.Errorf("an unsupported provider: %v", err)
	}
}

func TestHostsAreFoundWhereTheirDocumentationSaysTheyAre(t *testing.T) {
	for _, c := range []struct{ kind, host, api, uploads, web string }{
		{"github", "", "https://api.github.com", "https://uploads.github.com", "github.com"},
		{"github", "GitHub.com", "https://api.github.com", "https://uploads.github.com", "github.com"},
		{"github", "ghe.example.org", "https://ghe.example.org/api/v3", "https://ghe.example.org/api/uploads", "ghe.example.org"},
		{"forgejo", "git.example.org", "https://git.example.org/api/v1", "", "git.example.org"},
		{"forgejo", "git.example.org:3000", "https://git.example.org:3000/api/v1", "", "git.example.org:3000"},
		{"forgejo", "git.example.org:443", "https://git.example.org/api/v1", "", "git.example.org"},
	} {
		api, up, web, err := gitmirror.TargetFor(c.kind, c.host)
		if err != nil || api != c.api || up != c.uploads || web != c.web {
			t.Errorf("%s %q: %q %q %q %v", c.kind, c.host, api, up, web, err)
		}
	}
	if _, _, _, err := gitmirror.TargetFor("forgejo", ""); err == nil {
		t.Error("a Forgejo with no host")
	}
}

// ---- no secret anywhere --------------------------------------------------------------

func TestTheTokenIsInNoLogNoAuditNoOutputNoFileNoRepository(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	var seen []string
	note := func(v any, err error) {
		if err != nil {
			seen = append(seen, err.Error(), mustJSON(t, ops.AsError(err)))
		}
		if v != nil {
			seen = append(seen, mustJSON(t, v))
		}
	}
	v, err := h.call(h.owner, "repo.account.list", struct{}{})
	note(v, err)
	v, err = h.call(h.owner, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID})
	note(v, err)
	in := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Visibility = "public" })
	v, err = h.call(h.owner, "repo.plan", in)
	note(v, err)
	// A refusal, a success, a failed push (the host then refuses the token), a conflict.
	_, err = h.call(h.owner, "repo.publish", publishFrom(in, "wrong", "salt-road"))
	note(nil, err)
	out, _ := h.publish(in, "salt-road")
	note(out, nil)
	f.mu.Lock()
	f.token = "tok_rotated_at_the_host_Zq7Vn3xK9wLm2Rt5"
	f.mu.Unlock()
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	p := h.pushRemote(out.Remote.ID)
	note(p, nil)
	for _, op := range []string{"git.remote.list", "git.push_history"} {
		var in any = gitmirror.ProjectIn{ProjectID: h.pid}
		if op == "git.push_history" {
			in = gitmirror.PushHistoryIn{ProjectID: h.pid}
		}
		v, err = h.call(h.owner, op, in)
		note(v, err)
	}
	pg := h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: p.ID})
	note(pg, nil)
	f.mu.Lock()
	f.token = fakeToken
	f.mu.Unlock()

	canary := fakeToken
	for _, s := range seen {
		if strings.Contains(s, canary) {
			t.Errorf("the token is in an op result or error: %.200s", s)
		}
	}
	if strings.Contains(h.logs.String(), canary) {
		t.Errorf("the token is in the log")
	}
	// The audit table and every Git table, as stored.
	for _, q := range []string{`SELECT group_concat(detail_json) FROM audit_log`, `SELECT group_concat(token_sealed || id || login) FROM git_accounts`,
		`SELECT group_concat(secret_cipher || url || last_error || name) FROM git_remotes`, `SELECT group_concat(error_message || detail_json || manifest_json) FROM git_pushes`} {
		var s *string
		if err := h.d.Read().QueryRow(q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != nil && strings.Contains(*s, canary) {
			t.Errorf("the token is in the database (%s)", q)
		}
	}
	// The local repositories, the data directory, and what the host holds.
	_ = filepath.Walk(h.dataDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && !strings.HasSuffix(path, ".db") && !strings.Contains(path, "-wal") && !strings.Contains(path, "-shm") {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), canary) {
				t.Errorf("the token is in %s", path)
			}
		}
		return nil
	})
	if strings.Contains(everythingIn(t, f.repoDir("mairead", "salt-road")), canary) {
		t.Errorf("the token was pushed")
	}
	if strings.Contains(everythingIn(t, h.svc.RepoDir(h.pid, out.Remote.ID)), canary) {
		t.Errorf("the token is in the local repository")
	}
}
