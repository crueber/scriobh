package gitmirror_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// keyPrincipal is an API key of the given scope, for the tests that say a key is refused.
func keyPrincipal(h *harness, scope string) ops.Principal {
	return ops.Principal{UserID: h.owner.UserID, Role: h.owner.Role, Email: h.owner.Email, Via: "apikey", IsAgent: true, Scope: scope, KeyID: "key_test"}
}

func (h *harness) connect(f *fakeHost) gitmirror.Account {
	h.t.Helper()
	return h.must(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: f.flavor, Host: f.host(), Token: f.token, Label: "test"}).(gitmirror.Account)
}

func (h *harness) planIn(acct string, mut ...func(*gitmirror.RepoPlanIn)) gitmirror.RepoPlanIn {
	in := gitmirror.RepoPlanIn{ProjectID: h.pid, AccountID: acct, Name: "salt-road"}
	for _, m := range mut {
		m(&in)
	}
	return in
}

func (h *harness) plan(in gitmirror.RepoPlanIn) gitmirror.RepoPlan {
	h.t.Helper()
	return h.must(h.owner, "repo.plan", in).(gitmirror.RepoPlan)
}

func publishFrom(in gitmirror.RepoPlanIn, token, confirm string) gitmirror.RepoPublishIn {
	return gitmirror.RepoPublishIn{ProjectID: in.ProjectID, AccountID: in.AccountID, Mode: in.Mode, Owner: in.Owner, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, Branch: in.Branch, PathPrefix: in.PathPrefix, Audience: in.Audience, OnlyDone: in.OnlyDone, IncludeNotes: in.IncludeNotes,
		SignCommits: in.SignCommits, PushMode: in.PushMode, PlanToken: token, Confirm: confirm}
}

// publish plans, publishes and waits for the first push.
func (h *harness) publish(in gitmirror.RepoPlanIn, confirm string) (gitmirror.RepoPublishOut, gitmirror.Push) {
	h.t.Helper()
	pl := h.plan(in)
	if len(pl.Blockers) > 0 {
		h.t.Fatalf("the plan is blocked: %+v", pl.Blockers)
	}
	out := h.must(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, confirm)).(gitmirror.RepoPublishOut)
	return out, h.settle(out.Push.ID)
}

// treeOf lists the files of a bare repository's branch by their paths, with their contents.
func treeOf(t *testing.T, f *fakeHost, owner, name, branch string) map[string]string {
	t.Helper()
	out := map[string]string{}
	list := f.gitOut(owner, name, "ls-tree", "-r", "--name-only", branch)
	for _, p := range strings.Split(list, "\n") {
		if p == "" {
			continue
		}
		out[p] = f.gitOut(owner, name, "show", branch+":"+p)
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (h *harness) admin() ops.Principal {
	return ops.Principal{UserID: testutil.User(h.t, h.d, "admin", fmt.Sprintf("admin%d@test.local", time.Now().UnixNano())), Role: "admin", Email: "admin@test.local"}
}

func trashProject(t *testing.T, h *harness) {
	t.Helper()
	h.must(h.owner, "project.trash", projects.StateIn{ID: h.pid})
}

func restoreProject(t *testing.T, h *harness) {
	t.Helper()
	h.must(h.owner, "project.restore", projects.StateIn{ID: h.pid})
}

// pausedByRestore runs the steps a restore runs after it, with the installation's key.
func pausedByRestore(t *testing.T, h *harness) string {
	t.Helper()
	var notes []string
	for _, step := range backup.PostRestore {
		msg, err := step(t.Context(), h.d, []byte("test-secret-key-test-secret-key-0123456789"))
		if err != nil {
			t.Fatal(err)
		}
		if msg != "" {
			notes = append(notes, msg)
		}
	}
	return strings.Join(notes, " ")
}
