package gitmirror_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// An owner's API key must not be able to widen what reaches a repository people can read. The chain the review
// found: publish a public repository (a person, a browser), then through a key flip the audience to private, switch
// the notes on, force-push. Every link is refused for a key, and the push itself asks the host.
func TestAKeyCannotWidenWhatReachesAPublicRepository(t *testing.T) {
	for _, scope := range []string{"write", "admin"} {
		t.Run(scope, func(t *testing.T) {
			h, f, acct := setupPublish(t, "github")
			in := h.planIn(acct.ID, func(in *gitmirror.RepoPlanIn) { in.Visibility = "public" })
			out, p := h.publish(in, "salt-road")
			if p.Status != "ok" {
				t.Fatalf("publish: %+v", p)
			}
			key := keyPrincipal(h, scope)
			rid := out.Remote.ID
			for name, upd := range map[string]gitmirror.UpdateIn{
				"audience":      {Audience: ptr("private")},
				"notes":         {IncludeNotes: ptr(true)},
				"only_done off": {OnlyDone: ptr(false)},
				"only_done on":  {OnlyDone: ptr(true)},
				"re-point":      {URL: ptr("https://" + f.host() + "/mairead/elsewhere.git")},
			} {
				upd.ProjectID, upd.RemoteID = h.pid, rid
				if _, err := h.call(key, "git.remote.update", upd); h.code(err) != ops.CodeForbidden {
					t.Errorf("%s by a key: %v", name, err)
				}
			}
			if _, err := h.call(key, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: rid, Confirm: out.Remote.Name}); h.code(err) != ops.CodeForbidden {
				t.Errorf("force push by a key: %v", err)
			}
			// Settings a key may still change on it: the push mode is not what reaches the host.
			if _, err := h.call(key, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: rid, PushMode: ptr("manual")}); err != nil {
				t.Errorf("a key changing the push mode: %v", err)
			}
			got := h.remoteByID(rid)
			if got.Audience != "public" || got.IncludeNotes || got.OnlyDone {
				t.Errorf("the remote changed under a key: %+v", got)
			}
			// Delete and make it again as private, at the same address: the address is marked, so a key cannot.
			h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: rid})
			mk := func(p ops.Principal, name string) error {
				_, err := h.call(p, "git.remote.create", gitmirror.CreateIn{ProjectID: h.pid, Name: name, URL: "https://" + f.host() + "/mairead/salt-road.git", Audience: "private",
					Token: f.token, Preset: f.flavor, Username: "mairead", PushMode: "manual", ConfirmName: name})
				return err
			}
			if err := mk(key, "Again"); h.code(err) != ops.CodeForbidden {
				t.Errorf("a key making a private remote at a public address: %v", err)
			}
			// A private remote elsewhere, re-pointed by a key onto that address.
			other := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Name = "Elsewhere"; in.Token = "tok_abcdefghijklmnop" })
			if _, err := h.call(key, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: other.ID, URL: ptr("https://" + f.host() + "/mairead/salt-road.git")}); h.code(err) != ops.CodeForbidden {
				t.Errorf("a key re-pointing a private remote at a public address: %v", err)
			}
			// A person may; and even then the push asks the host, which says the repository is public, and nothing is pushed.
			if err := mk(h.owner, "By a person"); err != nil {
				t.Fatalf("a person making it: %v", err)
			}
			var rem gitmirror.Remote
			for _, r := range h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList).Items {
				if r.Name == "By a person" {
					rem = r
				}
			}
			p2 := h.pushRemote(rem.ID)
			if p2.Status != "error" || p2.ErrorCode != "repository_is_public" {
				t.Fatalf("push of a full mirror to a public repository: %+v", p2)
			}
			if tree := treeOf(t, f, "mairead", "salt-road", "main"); tree["notes/01-ideas.md"] != "" {
				t.Errorf("the notes are in the public repository: %v", sortedKeys(tree))
			}
			if got := h.remoteByID(rem.ID); got.Status.State != "blocked" {
				t.Errorf("the refused remote is %+v", got.Status)
			}
		})
	}
}

// The review's exact chain, with a person where the plan needs one and keys everywhere else, ends with the notes
// not in the public repository (the original test skips when the flip is refused; this one asserts it).
func TestThePersonWhoWidensAPublicRemoteStillCannotPushTheFullMirrorThere(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	out, _ := h.publish(h.planIn(acct.ID, func(in *gitmirror.RepoPlanIn) { in.Visibility = "public" }), "salt-road")
	// A person at the browser flips it to private, notes on, and forces: allowed for them, refused by the host check.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: out.Remote.ID, Audience: ptr("private"), ConfirmName: out.Remote.Name})
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: out.Remote.ID, IncludeNotes: ptr(true), ConfirmName: out.Remote.Name})
	fo := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: out.Remote.ID, Confirm: out.Remote.Name}).(gitmirror.PushOut)
	p := h.settle(fo.Item.ID)
	if p.Status == "ok" || strings.Contains(everythingIn(t, f.repoDir("mairead", "salt-road")), "Maybe a storm") {
		t.Errorf("force push of the full mirror to a public repository: %+v", p)
	}
}

// A repository that was private when the remote was made, and has become public, gets no full mirror either.
func TestARepositoryThatBecomesPublicStopsGettingTheFullMirror(t *testing.T) {
	h, f, acct := setupPublish(t, "forgejo")
	f.addRepo("mairead", "backup", true)
	h.addRemote("https://"+f.host()+"/mairead/backup.git", func(in *gitmirror.CreateIn) {
		in.Name = "Backup repo"
		in.Token = f.token
		in.Preset = "forgejo"
		in.Username = "mairead"
	})
	p := h.pushRemote(h.remote().ID)
	if p.Status != "ok" {
		t.Fatalf("a private repository: %+v", p)
	}
	f.mu.Lock()
	f.repos["mairead/backup"].private = false
	f.mu.Unlock()
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	before := f.head("mairead", "backup", "main")
	p = h.pushRemote(h.remote().ID)
	if p.Status != "error" || p.ErrorCode != "repository_is_public" || f.head("mairead", "backup", "main") != before {
		t.Errorf("push to a repository that became public: %+v", p)
	}
	// And a failed question is a failed push, tried again later, not a push.
	f.mu.Lock()
	f.repos["mairead/backup"].private = true
	f.mode = "5xx"
	f.mu.Unlock()
	p = h.pushRemote(h.remote().ID)
	if p.Status != "error" || f.head("mairead", "backup", "main") != before {
		t.Errorf("push while the host cannot say: %+v", p)
	}
	_ = acct
}
