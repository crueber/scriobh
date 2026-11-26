package server

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// The role each project op needs, audited. The rule the ops follow is: a GET needs the reader role and
// anything else needs the editor; the table below is every op that is not that (and why). A new op that
// departs from the rule has to be added here, with a reason, or this test fails; so does one that moves.
//
// Roles, in short: reader reads the manuscript; reviewer also writes comments and suggestions and reads
// everyone's (never edits text); editor edits the text and decides on suggestions; owner changes the project,
// who is on it, its share links, its Git remote and its AI policy.
var roleExceptions = map[string]string{
	// reading that is not the reader's: the writer's working material and the writer's own controls
	"ai.chat.get": "editor", "ai.chat.list": "editor", "ai.defaults.get": "editor", "ai.exclude.list": "editor", "ai.lab.marks": "editor",
	"ai.policy.get": "editor", "ai.review.get": "editor", "ai.review.list": "editor", "ai.run.list": "editor", "ai.style.get": "editor",
	"ai.summary.rolling.get": "editor", "ai.usage.project": "owner",
	"git.push_get": "editor", "git.push_history": "editor", "git.remote.get": "editor", "git.remote.list": "editor",
	"stash.get": "editor", "stash.list": "editor", // unused paragraphs are the writer's cut material
	"review.queue":        "editor",                                                     // the writer's queue (a reviewer reads threads with comment.list)
	"compile.contact.get": "editor",                                                     // the contact block is a person's home address and phone: not for readers or reviewers
	"compile.preset.list": "editor",                                                     // compile settings belong to the people who compile
	"site.options.get":    "editor", "site.doc.list": "editor", "site.status": "editor", // what is about to be published is the editors' to see: a reader sees the site once it is out
	"project.member.list": "owner",  // who is on the project is the owner's to know
	"edition.download":    "editor", // the book as it was received, whole: for the people who edit it (a reader reads the edition's record, edition.get)
	"edition.compare":     "editor", // it reads two books and shows their text, as the download does: for the people who edit them (a reader reads edition.diff's steps)
	"import.source.list":  "editor", // the names of the files a person chose to bring in, and which of their documents have been edited since, are for the people who edit the project
	"kindle.log":          "editor", // who mailed which edition of the book to which domain is for the people who write it
	// writing that is not the editor's
	"ai.policy.set": "owner", "ai.style.set": "owner",
	"git.push_force": "owner", "git.remote.create": "owner", "git.remote.delete": "owner", "git.remote.hostkey_accept": "owner", "git.remote.keygen": "owner",
	"git.remote.secret_ack": "owner", "git.remote.update": "owner",
	// signing, a trigger and publishing decide what leaves the instance, and what vouches for it: the owner's
	"git.signing.get": "owner", "git.signing.generate": "owner",
	"git.trigger.get": "owner", "git.trigger.set": "owner", "git.trigger.delete": "owner", "git.trigger.test": "owner",
	"repo.list": "owner", "repo.plan": "owner", "repo.publish": "owner", "repo.release": "owner", "repo.release_get": "owner", "repo.release_list": "owner",
	"mention.rebuild": "owner", "project.archive": "owner", "project.compendium.create": "owner", "project.compendium.ref.add": "owner",
	"project.compendium.ref.remove": "owner", "project.compendium.set": "owner", "project.member.add": "owner", "project.member.remove": "owner",
	"project.member.update": "owner", "project.purge": "owner", "project.restore": "owner", "project.trash": "owner", "project.unarchive": "owner",
	"project.update": "owner", "revision.prune": "owner", "scene.link_entities": "owner",
	// the passphrase gate (F-252) and the primary Compendium: the owner's settings; unlocking and locking are a member's own session state
	"project.passphrase.set": "owner", "project.passphrase.clear": "owner", "compendium.primary.preview": "owner", "compendium.primary.set": "owner",
	"project.unlock": "reader", "project.lock": "reader",
	// presence (F-244): a member's own session state, held in memory; what each role is told is decided inside the op, not by the role it needs
	"presence.beat": "reader", "presence.leave": "reader",
	"share.create": "owner", "share.list": "owner", "share.revoke": "owner", "share.update": "owner", "share.usage": "owner", // a link is a way in for a stranger
	// a webhook sends the project's notifications out of the instance: where, and what, is the owner's to know and change
	"webhook.create": "owner", "webhook.delete": "owner", "webhook.deliveries": "owner", "webhook.get": "owner", "webhook.list": "owner",
	"webhook.redeliver": "owner", "webhook.rotate_secret": "owner", "webhook.test": "owner", "webhook.update": "owner",
	// semantic search: the status is the editors' to see (an error class can map the server's network); the switches decide
	// what leaves the instance, so they are the owner's, as the AI policy is
	"search.semantic": "editor", "search.semantic.status": "editor", "search.semantic.preview": "owner", "search.semantic.enable": "owner", "search.semantic.disable": "owner",
	"search.semantic.rebuild": "owner", "search.semantic.purge": "owner",
	// per-person state that every member may keep, and their own private scratchpad
	"project.open": "reader", "project.pin": "reader", "project.scratch.set": "reader",
	// a POST that only reads (it takes a body, a print trim and a profile): the layout it measures is the reader's to see (project.cover_layout.get)
	"project.cover_layout.check": "reader",
	// review: reviewers write comments and suggestions and read all of them; deciding is the editor's
	"comment.create": "reviewer", "comment.delete": "reviewer", "comment.get": "reviewer", "comment.list": "reviewer", "comment.people": "reviewer",
	"comment.reanchor": "reviewer", "comment.reopen": "reviewer", "comment.resolve": "reviewer", "comment.update": "reviewer",
	"suggestion.create": "reviewer", "suggestion.get": "reviewer", "suggestion.list": "reviewer", "suggestion.withdraw": "reviewer",
}

func TestEveryProjectOpNeedsTheRoleThePolicySays(t *testing.T) {
	s := newAuthzServer(t)
	seen := map[string]bool{}
	var wrong []string
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthProject {
			continue
		}
		want := ops.ProjectReader
		if e.Op.Method != http.MethodGet && e.Op.Method != http.MethodHead {
			want = ops.ProjectEditor
		}
		if r, ok := roleExceptions[e.Op.Name]; ok {
			want = r
			seen[e.Op.Name] = true
		}
		if e.Op.Auth.Role != want {
			wrong = append(wrong, e.Op.Name+" needs "+e.Op.Auth.Role+", the policy says "+want)
		}
	}
	sort.Strings(wrong)
	if len(wrong) > 0 {
		t.Errorf("roles that do not match the policy table:\n%s", strings.Join(wrong, "\n"))
	}
	for n := range roleExceptions {
		if !seen[n] {
			t.Errorf("%s is in the exception table but is not a project op", n)
		}
	}
	// The principles, stated so that a table edit cannot quietly break them.
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthProject {
			continue
		}
		n, role := e.Op.Name, e.Op.Auth.Role
		write := e.Op.Method != http.MethodGet && e.Op.Method != http.MethodHead
		if role == ops.ProjectReader && write && n != "project.open" && n != "project.pin" && n != "project.scratch.set" && n != "project.unlock" && n != "project.lock" && n != "presence.beat" && n != "presence.leave" && n != "project.cover_layout.check" {
			t.Errorf("%s: a reader can write", n)
		}
		if role == ops.ProjectReviewer && !(strings.HasPrefix(n, "comment.") || strings.HasPrefix(n, "suggestion.")) {
			t.Errorf("%s: a reviewer can do something that is not review", n)
		}
		if strings.HasPrefix(n, "doc.") && write && role != ops.ProjectEditor {
			t.Errorf("%s changes the manuscript and is not the editor's", n)
		}
		if strings.HasPrefix(n, "share.") && (role != ops.ProjectOwner || !e.Op.Auth.SessionOnly) {
			t.Errorf("%s must be the owner's, in a browser session", n)
		}
		if (strings.HasPrefix(n, "ai.") || strings.HasPrefix(n, "git.") || strings.HasPrefix(n, "stash.")) && role == ops.ProjectReader {
			t.Errorf("%s is open to readers", n)
		}
	}
}

// Roles change: an editor demoted to reviewer can no longer edit and can still comment; a reader promoted
// to reviewer can; the owner's role is fixed.
func TestChangingARoleChangesWhatSomeoneCanDo(t *testing.T) {
	w := newShareWorld(t, nil)
	base := "/api/v1/projects/" + w.pid
	editBody := `{"base_revision":1,"content":"x"}`
	comment := `{"document_id":"` + w.doc2 + `","body":"hello"}`
	if r := w.s.do(w.editor, "PATCH", "/api/v1/docs/"+w.doc2, editBody); r.Code == 403 || r.Code == 404 {
		t.Fatalf("an editor cannot edit: %d", r.Code)
	}
	patch := func(user, role string) *http.Response {
		rec := w.s.do(w.owner, "PATCH", base+"/members", `{"user_id":"`+user+`","role":"`+role+`"}`)
		return rec.Result()
	}
	if r := patch(w.editor.userID, "reviewer"); r.StatusCode != 200 {
		t.Fatalf("demote: %d", r.StatusCode)
	}
	if r := w.s.do(w.editor, "PATCH", "/api/v1/docs/"+w.doc2, editBody); r.Code != 403 {
		t.Errorf("a demoted editor edits: %d", r.Code)
	}
	if r := w.s.do(w.editor, "POST", base+"/comments", comment); r.Code != 201 {
		t.Errorf("a reviewer comments: %d %s", r.Code, r.Body)
	}
	if r := w.s.do(w.editor, "GET", base+"/review/queue", ""); r.Code != 403 {
		t.Errorf("a reviewer has the queue: %d", r.Code)
	}
	if r := patch(w.editor.userID, "reader"); r.StatusCode != 200 {
		t.Fatalf("demote to reader: %d", r.StatusCode)
	}
	if r := w.s.do(w.editor, "POST", base+"/comments", comment); r.Code != 403 {
		t.Errorf("a reader comments: %d", r.Code)
	}
	if r := w.s.do(w.editor, "GET", "/api/v1/docs/"+w.doc2, ""); r.Code != 200 {
		t.Errorf("a reader reads: %d", r.Code)
	}
	if r := patch(w.editor.userID, "reviewer"); r.StatusCode != 200 {
		t.Fatalf("promote: %d", r.StatusCode)
	}
	if r := w.s.do(w.editor, "POST", base+"/comments", comment); r.Code != 201 {
		t.Errorf("a promoted reader comments: %d", r.Code)
	}
	if r := patch(w.owner.userID, "reader"); r.StatusCode != 409 {
		t.Errorf("changing the owner's role: %d", r.StatusCode)
	}
	// A removed member loses everything at once, the comments they wrote stay.
	rm := w.s.do(w.owner, "DELETE", base+"/members?user_id="+w.editor.userID, "")
	if rm.Code != 200 {
		t.Fatal(rm.Body)
	}
	if r := w.s.do(w.editor, "GET", base+"/comments", ""); r.Code != 404 {
		t.Errorf("a removed member lists comments: %d", r.Code)
	}
	if r := w.s.do(w.owner, "GET", base+"/comments", ""); !strings.Contains(r.Body.String(), "hello") {
		t.Error("the comments of a removed member are gone")
	}
	var n int
	_ = w.s.DB.Read().QueryRow(`SELECT count(*) FROM notifications WHERE user_id = ?`, w.editor.userID).Scan(&n)
	if r := w.s.do(w.editor, "GET", "/api/v1/notifications", ""); strings.Contains(r.Body.String(), "hello") {
		t.Error("a removed member can still read comment text through notifications")
	}
}
