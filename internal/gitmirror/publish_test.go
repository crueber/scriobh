package gitmirror_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The publish flow against the fake hosts (see fakehost_test.go: no real GitHub or Forgejo was used).

var flavors = []string{"github", "forgejo"}

func setupPublish(t *testing.T, flavor string) (*harness, *fakeHost, gitmirror.Account) {
	t.Helper()
	f := newFakeHost(t, flavor)
	h := newHarness(t)
	h.start()
	return h, f, h.connect(f)
}

func TestPublishCreatesARepositoryRegistersItAndPushes(t *testing.T) {
	for _, flavor := range flavors {
		t.Run(flavor, func(t *testing.T) {
			h, f, acct := setupPublish(t, flavor)
			if acct.Login != "mairead" || acct.Provider != flavor || acct.ID == "" {
				t.Fatalf("account = %+v", acct)
			}
			list := h.must(h.owner, "repo.list", gitmirror.RepoListIn{ProjectID: h.pid, AccountID: acct.ID}).(gitmirror.RepoList)
			if len(list.Items) != 0 {
				t.Fatalf("list = %+v", list)
			}
			before := f.writes()
			in := h.planIn(acct.ID, func(in *gitmirror.RepoPlanIn) { in.Description = "A novel"; in.Branch = "main" })
			pl := h.plan(in)
			if f.writes() != before {
				t.Errorf("a plan changed something at the host: %v", f.requests())
			}
			if len(pl.Blockers) != 0 || !pl.WillCreate || pl.Repository.Visibility != "private" || pl.Audience != "public" || pl.NeedsConfirm {
				t.Fatalf("plan = %+v", pl)
			}
			if pl.FileCount < 3 || pl.TotalBytes == 0 || len(pl.Files) != pl.FileCount || strings.Join(pl.Requests, ",") != "create_repository,register_remote,push" {
				t.Errorf("plan files = %d (%d bytes) %v requests %v", pl.FileCount, pl.TotalBytes, pl.Files, pl.Requests)
			}
			var noteListed bool
			for _, w := range pl.Withheld {
				noteListed = noteListed || (w.Path == "notes/01-ideas.md" && w.Reason == gitmirror.WithheldNote)
			}
			if !noteListed {
				t.Errorf("the plan does not say the notes are withheld: %+v", pl.Withheld)
			}
			if f.writes() != before {
				t.Fatalf("a plan made a change at the host: %v", f.requests())
			}

			out, push := h.publish(in, "")
			if !out.Created || out.Remote.CreatedVia != "publish" || out.Remote.Audience != "public" || out.Remote.Preset != flavor || !out.Remote.HasSecret || out.Remote.Username != "mairead" {
				t.Fatalf("published = %+v", out)
			}
			if push.Status != "ok" || push.Outcome != "pushed" {
				t.Fatalf("push = %+v", push)
			}
			rp := f.repos["mairead/salt-road"]
			if rp == nil || !rp.private || rp.desc != "A novel" {
				t.Fatalf("repository at the host = %+v", rp)
			}
			if f.head("mairead", "salt-road", "main") == "" {
				t.Fatal("the branch main was not pushed")
			}
			if rp.branch != "main" {
				t.Errorf("the default branch is %q: it was not pointed at the pushed branch", rp.branch)
			}
			tree := treeOf(t, f, "mairead", "salt-road", "main")
			if _, ok := tree["notes/01-ideas.md"]; ok || tree["manuscript/01-the-harbour.md"] == "" {
				t.Errorf("tree = %v", sortedKeys(tree))
			}
			if got := h.remote(); got.ID != out.Remote.ID || got.Status.State != "synced" {
				t.Errorf("remote = %+v", got)
			}
			// The token went only in the Authorization header of requests to the host, as the host's own scheme says.
			for _, r := range f.requests() {
				if strings.Contains(r.Path, f.token) {
					t.Errorf("the token is in a path: %s", r.Path)
				}
			}
		})
	}
}

func TestPublishRefusesAnExistingNameAndOneThatIsTakenMeanwhile(t *testing.T) {
	for _, flavor := range flavors {
		t.Run(flavor, func(t *testing.T) {
			h, f, acct := setupPublish(t, flavor)
			f.addRepo("mairead", "salt-road", true)
			pl := h.plan(h.planIn(acct.ID))
			if len(pl.Blockers) != 1 || pl.Blockers[0].Code != "name_taken" || pl.WillCreate {
				t.Fatalf("plan = %+v", pl.Blockers)
			}
			_, err := h.call(h.owner, "repo.publish", publishFrom(h.planIn(acct.ID), pl.PlanToken, ""))
			if h.code(err) != ops.CodeConflict {
				t.Fatalf("publish over an existing name: %v", err)
			}

			// A name that is free at the plan and taken by the time of the create: the host says so, and nothing is registered.
			in := h.planIn(acct.ID, func(in *gitmirror.RepoPlanIn) { in.Name = "second" })
			pl = h.plan(in)
			f.mu.Lock()
			f.createFails = 1
			f.mu.Unlock()
			_, err = h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, ""))
			if h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("a name taken meanwhile: %v", err)
			}
			if rs := h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList); len(rs.Items) != 0 {
				t.Errorf("a remote was registered: %+v", rs.Items)
			}
		})
	}
}

func TestPublishNeedsThePlansTokenAndItsFiles(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	in := h.planIn(acct.ID)
	pl := h.plan(in)
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, "", "")); h.code(err) != ops.CodeInvalid {
		t.Errorf("no plan token: %v", err)
	}
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, "0000", "")); h.code(err) != ops.CodeConflict {
		t.Errorf("a wrong plan token: %v", err)
	}
	// The same fields with another visibility, name, branch or audience are another plan.
	for name, mut := range map[string]func(*gitmirror.RepoPlanIn){
		"name":       func(i *gitmirror.RepoPlanIn) { i.Name = "other" },
		"branch":     func(i *gitmirror.RepoPlanIn) { i.Branch = "dev" },
		"only_done":  func(i *gitmirror.RepoPlanIn) { i.OnlyDone = true },
		"visibility": func(i *gitmirror.RepoPlanIn) { i.Visibility = "public" },
		"push_mode":  func(i *gitmirror.RepoPlanIn) { i.PushMode = "on_save" },
	} {
		in2 := in
		mut(&in2)
		_, err := h.call(h.owner, "repo.publish", publishFrom(in2, pl.PlanToken, in2.Name))
		if h.code(err) != ops.CodeConflict && h.code(err) != ops.CodeInvalid {
			t.Errorf("%s changed after the plan: %v", name, err)
		}
	}
	// The set of files is bound: a document added after the plan is a plan that changed.
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-the-lighthouse.md", "chapter", "The Lighthouse", 40, "A light.\n"}))
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, "")); h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "different now") {
		t.Errorf("a file added after the plan: %v", err)
	}
	if f.writes() != 0 {
		t.Errorf("a refused publish changed something at the host: %v", f.requests())
	}
	// Planned again, it goes.
	h.publish(in, "")
}

func TestPublicVisibilityNeedsTheNameTypedAndTheStrictAudience(t *testing.T) {
	for _, flavor := range flavors {
		t.Run(flavor, func(t *testing.T) {
			h, f, acct := setupPublish(t, flavor)
			in := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Visibility = "public" })
			pl := h.plan(in)
			if !pl.NeedsConfirm || pl.ConfirmWith != "salt-road" || pl.Repository.Visibility != "public" {
				t.Fatalf("plan = %+v", pl)
			}
			for _, bad := range []string{"", "Salt-Road", "salt-road ", "mairead/salt-road"} {
				if _, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, bad)); h.code(err) != ops.CodeInvalid {
					t.Errorf("confirm %q: %v", bad, err)
				}
			}
			if f.writes() != 0 {
				t.Fatalf("a refused confirmation created something: %v", f.requests())
			}
			out, push := h.publish(in, "salt-road")
			if !out.Created || push.Status != "ok" || out.Repository.Visibility != "public" || f.repos["mairead/salt-road"].private {
				t.Errorf("out = %+v push %+v", out, push)
			}

			// A public repository never gets the full mirror.
			in2 := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "second"; i.Visibility = "public"; i.Audience = "private" })
			pl2 := h.plan(in2)
			if len(pl2.Blockers) == 0 || pl2.Blockers[0].Code != "public_full_mirror" {
				t.Errorf("a public repository with the private audience: %+v", pl2.Blockers)
			}
			// Notes are for the private audience only.
			in3 := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "third"; i.IncludeNotes = true })
			if _, err := h.call(h.owner, "repo.plan", in3); h.code(err) != ops.CodeInvalid {
				t.Errorf("notes for a public audience: %v", err)
			}
		})
	}
}

func TestPublishToAnExistingEmptyRepositoryOnlyAndOnlyWhenEmpty(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	f.addRepo("mairead", "empty-one", true)
	in := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing"; i.Name = "empty-one" })
	out, push := h.publish(in, "")
	if out.Created || push.Status != "ok" || f.head("mairead", "empty-one", "main") == "" {
		t.Fatalf("out %+v push %+v", out, push)
	}

	// A repository with content is refused: Scríobh pushes, it never merges.
	f.addRepo("mairead", "busy", true)
	f.gitOut("mairead", "busy", "hash-object", "-w", "--stdin") // makes the object store non-trivial
	tree := f.gitOut("mairead", "busy", "mktree")
	commit := f.gitOut("mairead", "busy", "-c", "user.name=x", "-c", "user.email=x@x", "commit-tree", tree, "-m", "someone else's")
	f.gitOut("mairead", "busy", "update-ref", "refs/heads/main", commit)
	pl := h.plan(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing"; i.Name = "busy" }))
	if len(pl.Blockers) != 1 || pl.Blockers[0].Code != "repo_not_empty" {
		t.Errorf("a repository with content: %+v", pl.Blockers)
	}
	// A repository that does not exist, one that is archived, one the token cannot write to.
	f.addRepo("mairead", "gone", true).archived = true
	f.addRepo("mairead", "ro", true).noPush = true
	for name, want := range map[string]string{"missing": "repo_not_found", "gone": "archived", "ro": "read_only"} {
		pl := h.plan(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing"; i.Name = name }))
		if len(pl.Blockers) == 0 || pl.Blockers[0].Code != want {
			t.Errorf("%s: %+v, want %s", name, pl.Blockers, want)
		}
	}
}

func TestPublishCreatedButIncompleteSaysSoAndCanBeResumed(t *testing.T) {
	h, f, acct := setupPublish(t, "github")
	in := h.planIn(acct.ID)
	pl := h.plan(in)
	gitmirror.SetAfterCreateHook(func() error { return errTest })
	t.Cleanup(func() { gitmirror.SetAfterCreateHook(nil) })
	_, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, ""))
	gitmirror.SetAfterCreateHook(nil)
	ae := ops.AsError(err)
	if ae.Code != ops.CodeConflict || ae.Data["created_repository"] != "mairead/salt-road" || ae.Data["resume_mode"] != "existing" {
		t.Fatalf("error = %+v", ae)
	}
	if f.repos["mairead/salt-road"] == nil || f.isEmpty("mairead", "salt-road") != true {
		t.Fatal("the repository should exist and be empty")
	}
	if rs := h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList); len(rs.Items) != 0 {
		t.Fatalf("a remote was registered: %+v", rs.Items)
	}
	// Run again with mode existing: it takes the empty repository the first call made.
	out, push := h.publish(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing" }), "")
	if out.Created || push.Status != "ok" || f.head("mairead", "salt-road", "main") == "" {
		t.Errorf("resumed: %+v %+v", out, push)
	}
	// Publishing again to the same repository reuses the remote and adds none.
	pl2 := h.plan(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing" }))
	if len(pl2.Blockers) == 0 && !pl2.Registered {
		t.Errorf("a second plan for the same address: %+v", pl2)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("after create")

func TestThePlanRefusesWhatThePushAlreadyRefusesAndNamesNoBiblePath(t *testing.T) {
	// The push never carries the Compendium projection: a manifest that lists a file under compendium/ stops the
	// plan as it stops a push, and what the owner is told does not name the path (it could be an entity).
	h, f, acct := setupPublish(t, "forgejo")
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"compendium/entities/the-hidden-name.md", "note", "Entity", 5, "x\n"}))
	pl := h.plan(h.planIn(acct.ID))
	if len(pl.Blockers) != 1 || pl.Blockers[0].Code != "mirror_not_ready" || strings.Contains(mustJSON(t, pl), "the-hidden-name") {
		t.Fatalf("plan = %+v", pl.Blockers)
	}
	if _, err := h.call(h.owner, "repo.publish", publishFrom(h.planIn(acct.ID), pl.PlanToken, "")); h.code(err) != ops.CodeConflict {
		t.Errorf("publish over a blocked plan: %v", err)
	}
	if f.writes() != 0 {
		t.Errorf("a blocked plan created something at the host")
	}
	// A secret in the text blocks it too, naming the file and never the text.
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-oops.md", "chapter", "Oops", 5, "key: sk-proj-Zq7Vn3xK9wLm2Rt5Yp8Bd4Hc6Jf1Ga0S\n"}))
	pl = h.plan(h.planIn(acct.ID))
	if len(pl.Findings) != 1 || pl.Findings[0].Path != "manuscript/03-oops.md" || strings.Contains(mustJSON(t, pl), "Zq7Vn3xK9wLm2Rt5Yp8Bd4Hc6Jf1Ga0S") {
		t.Errorf("findings = %+v", pl.Findings)
	}
	if len(pl.Blockers) == 0 || pl.Blockers[0].Code != "secret_in_tree" {
		t.Errorf("blockers = %+v", pl.Blockers)
	}
}

func TestAHandMadePrivateRemoteIsNeverReusedForAPublicPlan(t *testing.T) {
	// An empty repository that already has a remote of the project at its address, set to carry the full mirror:
	// reusing it would push notes, alternatives and outline details into what the plan showed as the reader's view.
	h, f, acct := setupPublish(t, "forgejo")
	f.addRepo("mairead", "salt-road", false) // a public repository
	h.addRemote("https://"+f.host()+"/mairead/salt-road.git", func(in *gitmirror.CreateIn) {
		in.Name = "By hand"
		in.Audience = "private"
		in.Token = f.token
		in.Preset = "forgejo"
		in.Username = "mairead"
	})
	in := h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Mode = "existing" })
	pl := h.plan(in)
	if !pl.Registered || len(pl.Blockers) == 0 || pl.Blockers[0].Code != "remote_differs" {
		t.Fatalf("plan = %+v", pl)
	}
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, "salt-road")); h.code(err) != ops.CodeConflict {
		t.Fatalf("publish: %v", err)
	}
	if !f.isEmpty("mairead", "salt-road") {
		t.Errorf("something was pushed to the public repository")
	}
	// Made to match, it is reused and nothing is added.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Audience: ptr("public")})
	pl = h.plan(in)
	if len(pl.Blockers) != 0 || !pl.Registered {
		t.Fatalf("plan after matching: %+v", pl)
	}
	out := h.must(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, "salt-road")).(gitmirror.RepoPublishOut)
	h.settle(out.Push.ID)
	if rs := h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList); len(rs.Items) != 1 {
		t.Errorf("remotes = %d", len(rs.Items))
	}
}
