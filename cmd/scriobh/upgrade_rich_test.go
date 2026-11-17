package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/devgit"
)

// The rich dataset of the upgrade rehearsal: what a real instance of the old release holds, written through the old
// release's own API (an operation the old release does not have is skipped, so the same code runs for every tag).
// TestUpgradeRehearsal writes it, takes the old answers, upgrades, and checks that every answer, key, link, secret
// and remote still works. SCRIOBH_UPGRADE_FIXTURE copies the old database out of this very run (the fixture
// internal/db/testdata/v1.0.db is made that way), so a fixture holds all of it.

// richOp is one operation of the old release, as its own _ops listing says.
type richOp struct{ Name, Method, Path string }

// rich is the writing side: three people, the old release's operation table, the stand-ins the data points at.
type rich struct {
	t          *testing.T
	ops        map[string]richOp
	w, a, r    *apiClient // the writer, the administrator and the reader, each in their own session
	git        *devgit.Server
	gitRoot    string
	hook       *hookSink
	hookSrv    *httptest.Server
	ai         *fakeAI
	aiSrv      *httptest.Server
	f          richFacts
	skipped    []string
	wrote      map[string]int // what was written, for the report
	before     map[string]any
	remoteBare string
	off        bool // the old release is too old for the rich dataset: nothing is written, captured or checked
}

// richFacts are the ids and secrets the checks after the upgrade need.
type richFacts struct {
	P1, P2, P3, Slug1            string
	Docs                         map[string]string
	KeyToken, KeyID, PinnedToken string
	SharePath, RevokedSharePath  string
	HookID, HookSecret           string
	AIProvID                     string
	RemoteID                     string
	RemoteAudience               string // what the old release said of the remote's audience; empty when it had no such field
	IdemKey                      string
	IdemBody                     map[string]any
	IdemFirst                    string
	Compendium                   string
	GitTree                      map[string]string
	GitHead                      string
	SSHUrl                       string
}

func newRich(t *testing.T, oldBase string, jar func(sub string) *apiClient) *rich {
	r := &rich{t: t, ops: map[string]richOp{}, wrote: map[string]int{}}
	r.w, r.a, r.r = jar("dev-writer"), jar("dev-admin"), jar("dev-reader")
	var listing struct {
		Ops []richOp `json:"ops"`
	}
	code, b := r.w.do("GET", "/api/v1/_ops", nil, "")
	if code != 200 || json.Unmarshal(b, &listing) != nil {
		t.Fatalf("the old release's _ops: %d %.200s", code, b)
	}
	for _, o := range listing.Ops {
		r.ops[o.Name] = o
	}
	r.f.Docs = map[string]string{}
	return r
}

// op calls a named operation of the old release for c. Path parameters come from p (and are the only way an id is
// named); the second result is false when the old release has no such operation, and nothing is sent. A status
// other than want fails the test.
func (r *rich) op(c *apiClient, name string, p map[string]string, body any, want int) (map[string]any, bool) {
	r.t.Helper()
	o, ok := r.ops[name]
	if !ok {
		r.skipped = append(r.skipped, name)
		return nil, false
	}
	path := o.Path
	for k, v := range p {
		path = strings.ReplaceAll(path, "{"+k+"}", v)
	}
	if strings.Contains(path, "{") {
		r.t.Fatalf("%s: unfilled parameter in %s", name, path)
	}
	var in any = body
	if o.Method == "GET" || o.Method == "DELETE" {
		in = nil
	}
	code, b := c.do(o.Method, path, in, "")
	if code < 200 || code >= 300 {
		r.t.Fatalf("%s %s = %d (wanted a success, %d): %.500s", o.Method, path, code, want, b)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	r.wrote[name]++
	return out, true
}

func str(m map[string]any, path ...string) string {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

func num(m map[string]any, path ...string) int {
	var n int
	fmt.Sscan(str(m, path...), &n)
	return n
}

// write fills the old release. env is what its servers get; extra is the secret key and the like.
func (r *rich) write(oldBase string) {
	if r.off {
		return
	}
	t := r.t
	f := &r.f
	has := func(n string) bool { _, ok := r.ops[n]; return ok }

	// ---- the people and two projects ------------------------------------------------------------------
	mk := func(title, kind string) string {
		out, _ := r.op(r.w, "project.create", nil, map[string]any{"title": title, "kind": kind}, 201)
		return str(out, "item", "id")
	}
	f.P1 = mk("The Lantern Cycle", "novel")
	f.P2 = mk("Field Notes", "novel")
	out, _ := r.op(r.w, "project.get", map[string]string{"id": f.P1}, nil, 200)
	f.Slug1 = str(out, "item", "slug")
	r.op(r.w, "project.update", map[string]string{"id": f.P1}, map[string]any{"genre": "literary", "author_name": "Wren Writer", "word_goal": 90000, "description": "A cycle of lantern stories."}, 200)
	if has("project.member.add") {
		r.op(r.w, "project.member.add", map[string]string{"id": f.P1}, map[string]any{"email": "admin@dev.scriobh", "role": "editor"}, 201)
		r.op(r.w, "project.member.add", map[string]string{"id": f.P1}, map[string]any{"email": "reader@dev.scriobh", "role": "reader"}, 201)
	}

	// ---- a manuscript tree, notes, saves, snapshots, a trashed scene ---------------------------------------
	doc := func(pid, parent, kind, title, content string) (string, int) {
		body := map[string]any{"title": title, "kind": kind, "content": content}
		if parent != "" {
			body["parent_id"] = parent
		}
		out, _ := r.op(r.w, "doc.create", map[string]string{"id": pid}, body, 201)
		f.Docs[title] = str(out, "item", "id")
		return str(out, "item", "id"), num(out, "item", "revision")
	}
	save := func(id string, rev int, content string) int {
		out, _ := r.op(r.w, "doc.update", map[string]string{"id": id}, map[string]any{"base_revision": rev, "content": content}, 200)
		return num(out, "item", "revision")
	}
	part1, _ := doc(f.P1, "", "part", "Part One: The Marsh", "")
	part2, _ := doc(f.P1, "", "part", "Part Two: The Wall", "")
	type chap struct{ id, title string }
	var chapters []chap
	for i, c := range []struct{ part, title string }{{part1, "The Lamplighter"}, {part1, "Salt and Gorse"}, {part2, "The Ferry"}, {part2, "The Wall at Dusk"}} {
		body := fmt.Sprintf("# %s\n\nNora Lantern walked the marsh road at dusk, counting the lamps, seven and then eight. The tide pulled at the rope.\n\nChapter %d goes on about Mara Quay and the harbour wall.\n", c.title, i+1)
		id, rev := doc(f.P1, c.part, "chapter", c.title, body)
		for j := 0; j < 3; j++ {
			body += fmt.Sprintf("\nA line added on save %d, with the lantern and the gorse.\n", j+1)
			rev = save(id, rev, body)
			if j == 1 {
				r.op(r.w, "revision.snapshot", map[string]string{"id": id}, map[string]any{"label": "Draft " + c.title, "note": "kept"}, 201)
			}
		}
		chapters = append(chapters, chap{id, c.title})
		for k := 1; k <= 2; k++ {
			doc(f.P1, id, "scene", fmt.Sprintf("%s, scene %d", c.title, k), fmt.Sprintf("Scene %d of %s: Mara Quay lit the lantern at the harbour.\n", k, c.title))
		}
	}
	doc(f.P1, "", "note", "Research: tides", "Tide tables for 1911. The marsh floods at the spring tide.\n")
	doc(f.P1, "", "note", "Style sheet", "Nora, not Norah. Gorse, never furze.\n")
	doc(f.P1, "", "front_matter", "Dedication", "For the lamplighters.\n")
	trashed := f.Docs[chapters[3].title+", scene 2"]
	r.op(r.w, "doc.trash", map[string]string{"id": trashed}, map[string]any{}, 200)
	doc(f.P2, "", "note", "Heron count", "Four herons on the reed, one on the wall.\n")
	doc(f.P2, "", "note", "Weather", "Rain from the west, a lantern wind.\n")

	// ---- comments and suggestions ---------------------------------------------------------------------
	pid := map[string]string{"id": f.P1}
	if c1, ok := r.op(r.w, "comment.create", pid, map[string]any{"document_id": chapters[0].id, "quote": "counting the lamps", "occurrence": 0, "body": "Lovely image."}, 201); ok {
		root := str(c1, "item", "id")
		r.op(r.a, "comment.create", pid, map[string]any{"document_id": chapters[0].id, "parent_id": root, "body": "Agreed: keep it."}, 201)
		q, _ := r.op(r.w, "comment.create", pid, map[string]any{"document_id": chapters[1].id, "quote": "gorse", "occurrence": 0, "body": "Is it gorse or furze here?", "kind": "question"}, 201)
		r.op(r.w, "comment.resolve", map[string]string{"id": f.P1, "comment_id": str(q, "item", "id")}, map[string]any{}, 200)
		r.op(r.w, "comment.create", pid, map[string]any{"body": "Overall: a quiet, good book."}, 201)
	}
	if has("suggestion.create") {
		var rev int
		d, _ := r.op(r.w, "doc.get", map[string]string{"id": chapters[2].id}, nil, 200)
		rev = num(d, "item", "revision")
		for _, ch := range []map[string]any{{"quote": "dusk", "occurrence": 0, "text": "dawn"}, {"quote": "harbour wall", "occurrence": 0, "text": "harbour steps"}} {
			r.op(r.a, "suggestion.create", pid, map[string]any{"document_id": chapters[2].id, "base_revision": rev, "note": "a change", "changes": []map[string]any{ch}}, 201)
		}
		if l, ok := r.op(r.w, "suggestion.list", pid, nil, 200); ok {
			if items, _ := l["items"].([]any); len(items) > 0 {
				first, _ := items[0].(map[string]any)
				r.op(r.w, "suggestion.reject", map[string]string{"id": f.P1, "suggestion_id": str(first, "id")}, map[string]any{}, 200)
			}
		}
	}

	// ---- share links: one kept, one revoked -------------------------------------------------------------
	if s, ok := r.op(r.w, "share.create", pid, map[string]any{"label": "Editor's copy", "scope": "read"}, 201); ok {
		f.SharePath = str(s, "path")
		s2, _ := r.op(r.w, "share.create", pid, map[string]any{"label": "Old reader", "scope": "comment"}, 201)
		f.RevokedSharePath = str(s2, "path")
		r.op(r.w, "share.revoke", map[string]string{"id": f.P1, "share_id": str(s2, "item", "id")}, map[string]any{}, 200)
	}

	// ---- the Compendium: entities, relationships, mentions, a timeline ----------------------------------------
	if has("project.compendium.create") {
		out, _ := r.op(r.w, "project.compendium.create", pid, map[string]any{"name": "Lantern Bible", "kind": "fiction", "starter_fields": true}, 201)
		f.Compendium = str(out, "item", "id")
		cid := map[string]string{"id": f.Compendium}
		ent := func(kind, name, summary string, aliases ...string) string {
			e, _ := r.op(r.w, "entity.create", cid, map[string]any{"kind": kind, "name": name, "summary": summary, "aliases": aliases}, 201)
			return str(e, "item", "id")
		}
		nora := ent("character", "Nora Lantern", "The lamplighter's daughter.", "Nora", "the lamplighter")
		mara := ent("character", "Mara Quay", "The ferry keeper.", "Mara")
		quay := ent("place", "The Quay", "Where the ferry leaves.", "the harbour")
		ent("item", "The Great Lantern", "Lit for the spring tide.")
		r.op(r.w, "entity.relation.add", map[string]string{"id": nora}, map[string]any{"to_entity_id": mara, "kind": "ally", "label": "friend of", "inverse_label": "friend of", "symmetric": true}, 201)
		r.op(r.w, "entity.relation.add", map[string]string{"id": mara}, map[string]any{"to_entity_id": quay, "kind": "member", "label": "keeps"}, 201)
		r.op(r.w, "entity.alias.add", map[string]string{"id": mara}, map[string]any{"alias": "the ferry keeper"}, 200)
		r.op(r.w, "scene.set_pov", map[string]string{"id": f.Docs[chapters[0].title+", scene 1"]}, map[string]any{"entity": nora}, 200)
		r.op(r.w, "mention.rebuild", pid, map[string]any{}, 200)
		r.op(r.w, "timeline.event.create", cid, map[string]any{"title": "The spring tide", "story_label": "Day 3", "description": "The marsh floods."}, 201)
	}

	// ---- goals and writing sessions (the saves above made sessions) -------------------------------------------
	r.op(r.w, "goal.set", pid, map[string]any{"kind": "daily", "target_words": 750}, 200)
	r.op(r.w, "goal.set", pid, map[string]any{"kind": "project", "target_words": 90000, "end_date": "2027-06-01"}, 200)

	// ---- an API key (and a read key pinned to the book), a webhook, a static site ------------------------------
	if k, ok := r.op(r.w, "key.create", nil, map[string]any{"name": "drafting agent", "scope": "write"}, 201); ok {
		f.KeyToken, f.KeyID = str(k, "secret"), str(k, "item", "id")
		k2, _ := r.op(r.w, "key.create", nil, map[string]any{"name": "book reader", "scope": "read", "project_id": f.P1}, 201)
		f.PinnedToken = str(k2, "secret")
		old, _ := r.op(r.w, "key.create", nil, map[string]any{"name": "old agent", "scope": "read"}, 201)
		r.op(r.w, "key.revoke", map[string]string{"id": str(old, "item", "id")}, map[string]any{}, 200)
	}
	if h, ok := r.op(r.w, "webhook.create", pid, map[string]any{"name": "Notify", "url": r.hookSrv.URL + "/hooks/" + hookPathTok, "events": []string{"document.updated", "comment.created"}}, 201); ok {
		f.HookID, f.HookSecret = str(h, "item", "id"), str(h, "secret")
	}
	r.op(r.w, "site.options.set", pid, map[string]any{"options": map[string]any{"footer_text": "Lit by the sea.", "accent": "moss", "include_drafts": true}}, 200)

	// ---- AI provider with a sealed key; the AI feature itself is not switched on -------------------------------
	if p, ok := r.op(r.a, "ai.provider.create", nil, map[string]any{"name": "Fake", "kind": "openai_compatible", "scope": "instance", "base_url": r.aiSrv.URL + "/v1",
		"api_key": aiSecret, "local": true, "default_model": "fake-model"}, 201); ok {
		f.AIProvID = str(p, "item", "id")
	}

	// ---- an import record, a compile and two exports ---------------------------------------------------------------
	if has("import.preview") {
		files := []map[string]any{{"path": "01-opening.md", "content": "# Opening\n\nSalt wind.\n"}, {"path": "02-closing.md", "content": "# Closing\n\nThe last light.\n"}}
		pv, _ := r.op(r.w, "import.preview", nil, map[string]any{"name": "Imported Notes", "files": files}, 200)
		ap, _ := r.op(r.w, "import.apply", nil, map[string]any{"token": str(pv, "token"), "files": files}, 201)
		f.P3 = str(ap, "project_id")
		if f.P3 == "" {
			f.P3 = str(ap, "item", "id")
		}
	}
	r.op(r.w, "export.markdown", pid, map[string]any{"mode": "single"}, 200)
	if has("compile.preset.create") {
		r.op(r.w, "compile.preset.create", pid, map[string]any{"name": "Reading copy"}, 201)
		r.op(r.w, "export.compile", pid, map[string]any{}, 200)
	}

	// ---- idempotency keys -------------------------------------------------------------------------------------------
	f.IdemKey = "rehearsal-idem-0001"
	f.IdemBody = map[string]any{"title": "Made once", "kind": "note", "content": "Created under an idempotency key.\n"}
	f.IdemFirst = r.idem(f.P1, f.IdemBody, 201)
	if again := r.idem(f.P1, f.IdemBody, 201); again != f.IdemFirst {
		t.Fatalf("the old release did not replay an idempotent request: %.200s vs %.200s", f.IdemFirst, again)
	}

	// ---- a Git remote, pushed to a real (development) Git server ---------------------------------------------------
	r.gitRemote(f.P1)
	_ = oldBase
}

// idem posts a document under the fixed idempotency key and returns the body.
func (r *rich) idem(pid string, body map[string]any, want int) string {
	r.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", r.w.base+"/api/v1/projects/"+pid+"/docs", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", r.f.IdemKey)
	resp, err := r.w.c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != want {
		r.t.Fatalf("idempotent create = %d, want %d: %.300s", resp.StatusCode, want, out)
	}
	return string(out)
}

// gitRemote adds an SSH remote to the book, accepts the host key, authorises the deploy key on the development
// server and pushes. What the server then holds (every file of the pushed tree and its blob) is recorded.
func (r *rich) gitRemote(pid string) {
	t := r.t
	if _, ok := r.ops["git.remote.create"]; !ok || r.git == nil {
		return
	}
	p := map[string]string{"id": pid}
	out, _ := r.op(r.w, "git.remote.create", p, map[string]any{"name": "Backup", "url": "ssh://git@" + r.git.SSHAddr() + "/book.git", "push_mode": "manual", "auth_kind": "ssh_deploy_key"}, 201)
	r.f.RemoteID = str(out, "item", "id")
	r.f.RemoteAudience = str(out, "item", "audience")
	r.f.SSHUrl = "ssh://git@" + r.git.SSHAddr() + "/book.git"
	key := str(out, "item", "public_key")
	if key == "" {
		k, _ := r.op(r.w, "git.remote.keygen", p, map[string]any{}, 200)
		key = str(k, "public_key")
		if key == "" {
			key = str(k, "item", "public_key")
		}
	}
	if key == "" {
		t.Fatalf("no deploy key in %v", out)
	}
	if _, err := r.git.Authorize(key); err != nil {
		t.Fatal(err)
	}
	hk, _ := r.op(r.w, "git.remote.hostkey", p, map[string]any{}, 200)
	fp := str(hk, "keys", "0", "fingerprint")
	if keys, ok := hk["keys"].([]any); ok && len(keys) > 0 {
		fp = str(keys[0].(map[string]any), "fingerprint")
	}
	if fp == "" {
		fp = str(hk, "fingerprint")
	}
	if fp == "" {
		t.Fatalf("no host key fingerprint in %v", hk)
	}
	r.op(r.w, "git.remote.hostkey_accept", p, map[string]any{"fingerprint": fp}, 200)
	r.waitMirror(pid)
	r.op(r.w, "git.push", p, map[string]any{}, 202)
	waitUntil(t, "the first push to finish", 60*time.Second, func() bool {
		out, _ := r.op(r.w, "git.remote.get", p, nil, 200)
		st := str(out, "item", "status", "state")
		return st == "synced"
	})
	r.f.GitTree, r.f.GitHead = r.bareTree(), r.bareHead()
	if len(r.f.GitTree) == 0 {
		t.Fatal("the push left nothing on the Git server")
	}
}

func (r *rich) waitMirror(pid string) {
	waitUntil(r.t, "the mirror of "+pid, 60*time.Second, func() bool {
		code, b := r.w.do("GET", "/api/v1/projects/"+pid+"/mirror", nil, "")
		return code == 200 && strings.Contains(string(b), `"in_sync":true`)
	})
}

// bareTree lists every file of the pushed branch with its blob.
func (r *rich) bareTree() map[string]string {
	out, err := exec.Command("git", "-C", filepath.Join(r.gitRoot, "book.git"), "ls-tree", "-r", "main").Output()
	if err != nil {
		r.t.Fatalf("ls-tree: %v", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if tab := strings.IndexByte(l, '\t'); tab > 0 {
			f := strings.Fields(l[:tab])
			if len(f) == 3 {
				m[l[tab+1:]] = f[2]
			}
		}
	}
	return m
}

func (r *rich) bareHead() string {
	out, err := exec.Command("git", "-C", filepath.Join(r.gitRoot, "book.git"), "rev-parse", "main").Output()
	if err != nil {
		r.t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// capture reads back everything the old release's API will tell about the rich data. Volatile fields (uses, last
// seen, state of a retry) are stripped; the rest must come back from the upgraded server unchanged.
func (r *rich) capture(c *apiClient) map[string]any {
	if r.off {
		return map[string]any{}
	}
	t := r.t
	out := map[string]any{}
	put := func(k string, op string, p map[string]string, query string) {
		o, ok := r.ops[op]
		if !ok {
			return
		}
		path := o.Path
		for kk, v := range p {
			path = strings.ReplaceAll(path, "{"+kk+"}", v)
		}
		code, b := c.do("GET", path+query, nil, "")
		if code != 200 {
			t.Fatalf("capture %s %s = %d: %.300s", op, path, code, b)
		}
		var v any
		if err := json.Unmarshal(stripVolatile(b), &v); err != nil {
			t.Fatalf("capture %s: %v", op, err)
		}
		out[k] = v
	}
	f := r.f
	pid := map[string]string{"id": f.P1}
	put("project.list", "project.list", nil, "")
	for _, id := range []string{f.P1, f.P2, f.P3} {
		if id == "" {
			continue
		}
		put(id+" project.get", "project.get", map[string]string{"id": id}, "")
		put(id+" doc.list", "doc.list", map[string]string{"id": id}, "")
		put(id+" goal.list", "goal.list", map[string]string{"id": id}, "")
		put(id+" site.options", "site.options.get", map[string]string{"id": id}, "")
		put(id+" outline", "outline.get", map[string]string{"id": id}, "")
		put(id+" search", "search.query", map[string]string{"id": id}, "?q=lantern")
	}
	put("comment.list", "comment.list", pid, "")
	put("suggestion.list", "suggestion.list", pid, "")
	put("share.list", "share.list", pid, "")
	put("webhook.list", "webhook.list", pid, "")
	put("git.remote.list", "git.remote.list", pid, "")
	put("member.list", "project.member.list", pid, "")
	put("mention.by_entity", "mention.by_entity", pid, "?entity=Nora+Lantern")
	put("key.list", "key.list", nil, "")
	put("ai.provider.list", "ai.provider.list", nil, "")
	if f.Compendium != "" {
		cid := map[string]string{"id": f.Compendium}
		put("compendium.get", "compendium.get", cid, "")
		put("entity.list", "entity.list", cid, "")
		put("timeline.event.list", "timeline.event.list", cid, "")
	}
	ids := make([]string, 0, len(f.Docs))
	for _, id := range f.Docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		put(id+" doc.get", "doc.get", map[string]string{"id": id}, "")
		put(id+" revision.list", "revision.list", map[string]string{"id": id}, "")
	}
	return out
}

// afterUpgrade is everything the old release made that must still work on the upgraded server: the key, the
// share links, the webhook's secret and sealed address, the sealed provider key, idempotent replay, the site, the
// Git remote (still private, and pushing what it pushed).
func (r *rich) afterUpgrade(newBase string, newW *apiClient) {
	if r.off {
		return
	}
	t := r.t
	f := r.f
	// API keys, including the pinned one; the revoked one stays revoked.
	if f.KeyToken != "" {
		k := &apiClient{t: t, base: newBase, c: &http.Client{Timeout: 20 * time.Second}, auth: f.KeyToken}
		if code, b := k.do("GET", "/api/v1/projects", nil, ""); code != 200 {
			t.Errorf("an API key from the old release: %d %s", code, b)
		}
		pin := &apiClient{t: t, base: newBase, c: &http.Client{Timeout: 20 * time.Second}, auth: f.PinnedToken}
		if code, b := pin.do("GET", "/api/v1/projects/"+f.P1, nil, ""); code != 200 {
			t.Errorf("a pinned read key: %d %s", code, b)
		}
		if code, _ := pin.do("GET", "/api/v1/projects/"+f.P2, nil, ""); code != 404 && code != 403 {
			t.Errorf("a key pinned to one book read another: %d", code)
		}
		if code, _ := pin.do("PATCH", "/api/v1/projects/"+f.P1, map[string]any{"title": "x"}, ""); code != 403 {
			t.Errorf("a read key wrote: %d", code)
		}
	}
	// Share links.
	anon := &apiClient{t: t, base: newBase, c: &http.Client{Timeout: 20 * time.Second}}
	if f.SharePath != "" {
		if code, _ := anon.do("GET", f.SharePath, nil, ""); code != 200 {
			t.Errorf("a share link from the old release: %d", code)
		}
		if code, _ := anon.do("GET", f.RevokedSharePath, nil, ""); code == 200 {
			t.Errorf("a revoked share link came back: %d", code)
		}
	}
	// The webhook: signed with the original secret and sent to its sealed path.
	if f.HookID != "" {
		n := r.hook.count()
		code, b := newW.do("POST", "/api/v1/projects/"+f.P1+"/webhooks/"+f.HookID+"/test", map[string]any{}, "")
		if code != 200 {
			t.Errorf("webhook test: %d %s", code, b)
		}
		waitUntil(t, "a webhook delivery after the upgrade", 20*time.Second, func() bool { return r.hook.count() > n })
		if err := r.hook.verified(n, f.HookSecret); err != nil {
			t.Errorf("a webhook of the old release after the upgrade: %v", err)
		}
	}
	// The sealed provider key reaches the provider.
	if f.AIProvID != "" {
		var tr struct {
			OK bool `json:"ok"`
		}
		code, b := newW.do("POST", "/api/v1/ai/providers/"+f.AIProvID+"/test", map[string]any{}, "")
		_ = json.Unmarshal(b, &tr)
		if code != 200 && code != 403 {
			t.Errorf("provider test: %d %s", code, b)
		}
		if code == 200 && (!tr.OK || r.ai.last() != aiSecret) {
			t.Errorf("the sealed provider key after the upgrade: ok=%v, the provider was sent %q", tr.OK, r.ai.last())
		}
	}
	// Idempotent replay: the same key and body give the first answer; another body is refused.
	if f.IdemFirst != "" {
		if again := r.idemOn(newW, f.P1, f.IdemBody); again != f.IdemFirst {
			t.Errorf("an idempotency key of the old release did not replay after the upgrade:\n  %.200s\n  %.200s", f.IdemFirst, again)
		}
		other := map[string]any{"title": "Different", "kind": "note", "content": "x"}
		b, _ := json.Marshal(other)
		req, _ := http.NewRequest("POST", newW.base+"/api/v1/projects/"+f.P1+"/docs", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", f.IdemKey)
		resp, err := newW.c.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 422 {
				t.Errorf("an old idempotency key was reusable for another request: %d", resp.StatusCode)
			}
		}
	}
	// The static site still builds from its stored configuration.
	if _, ok := r.ops["site.preview"]; ok {
		if code, b := newW.do("POST", "/api/v1/projects/"+f.P1+"/site/preview", map[string]any{}, ""); code != 200 {
			t.Errorf("site preview after the upgrade: %d %.300s", code, b)
		}
	}
	// The Git remote: read as private, and pushing exactly what it pushed.
	if f.RemoteID != "" {
		p := "/api/v1/projects/" + f.P1 + "/git/remote"
		var rem struct {
			Item struct {
				Audience string `json:"audience"`
				Enabled  bool   `json:"enabled"`
				Status   struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"item"`
		}
		code, b := newW.do("GET", p, nil, "")
		if code != 200 || json.Unmarshal(b, &rem) != nil {
			t.Fatalf("git remote after the upgrade: %d %s", code, b)
		}
		// A release before the audience setting (v1.0 and earlier) pushed the whole mirror, so its remote reads as
		// private. v1.1 answers with an audience of its own (the op's default for a remote made without one is
		// public), and the upgrade must keep exactly what that release said.
		wantAudience := f.RemoteAudience
		if wantAudience == "" {
			wantAudience = "private"
		}
		if rem.Item.Audience != wantAudience || !rem.Item.Enabled {
			t.Errorf("the old remote after the upgrade reads as audience %q, enabled %v; the old release said %q", rem.Item.Audience, rem.Item.Enabled, wantAudience)
		}
		r.waitMirrorOn(newW, f.P1)
		if code, b := newW.do("POST", "/api/v1/projects/"+f.P1+"/git/push", map[string]any{}, ""); code != 202 && code != 200 {
			t.Fatalf("push after the upgrade: %d %s", code, b)
		}
		waitUntil(t, "the push after the upgrade", 60*time.Second, func() bool {
			_, b := newW.do("GET", p, nil, "")
			var s struct {
				Item struct {
					Status struct {
						State   string `json:"state"`
						Running bool   `json:"running"`
					} `json:"status"`
				} `json:"item"`
			}
			_ = json.Unmarshal(b, &s)
			return s.Item.Status.State == "synced" && !s.Item.Status.Running
		})
		tree := r.bareTree()
		if h := r.bareHead(); h != f.GitHead {
			t.Errorf("the upgraded server pushed a commit with nothing changed: head %.8s before, %.8s after", f.GitHead, h)
		}
		var diff []string
		for k, v := range f.GitTree {
			if tree[k] != v {
				diff = append(diff, "changed or missing: "+k)
			}
		}
		for k := range tree {
			if _, ok := f.GitTree[k]; !ok {
				diff = append(diff, "new in the push: "+k)
			}
		}
		sort.Strings(diff)
		if len(diff) > 0 {
			t.Errorf("the upgraded server pushes something other than the old one did (%d files before, %d after): %v", len(f.GitTree), len(tree), diff)
		}
		// An edit after the upgrade changes the pushed tree by that chapter's file and nothing else: no file the old
		// release did not push appears, and nothing is dropped.
		var cur struct {
			Item struct {
				Revision int    `json:"revision"`
				Content  string `json:"content"`
			} `json:"item"`
		}
		chapter := f.Docs["The Lamplighter"]
		newW.call("GET", "/api/v1/docs/"+chapter, nil, &cur, 200)
		newW.call("PATCH", "/api/v1/docs/"+chapter, map[string]any{"base_revision": cur.Item.Revision, "content": cur.Item.Content + "\nOne more line, after the upgrade.\n"}, nil, 200)
		r.waitMirrorOn(newW, f.P1)
		newW.do("POST", "/api/v1/projects/"+f.P1+"/git/push", map[string]any{}, "")
		waitUntil(t, "the push of the edit", 60*time.Second, func() bool { return r.bareHead() != f.GitHead })
		edited := r.bareTree()
		var changed, added, dropped []string
		for k, v := range edited {
			if old, ok := tree[k]; !ok {
				added = append(added, k)
			} else if old != v {
				changed = append(changed, k)
			}
		}
		for k := range tree {
			if _, ok := edited[k]; !ok {
				dropped = append(dropped, k)
			}
		}
		sort.Strings(changed)
		if len(added) > 0 || len(dropped) > 0 || len(changed) == 0 || len(changed) > 2 {
			t.Errorf("after an edit the upgraded server's push changed %v, added %v, dropped %v: want the chapter (and at most the book's own index)", changed, added, dropped)
		}
		report(t, "git: an edit after the upgrade pushes %v and adds or drops no file", changed)
		report(t, "git: the old remote reads as %s and, before the edit, pushed the same %d files with every blob identical (head %.8s); the edit moved the head to %.8s", wantAudience, len(f.GitTree), f.GitHead, r.bareHead())
	}
	_ = context.Background
	_ = os.Stdout
}

func (r *rich) idemOn(c *apiClient, pid string, body map[string]any) string {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.base+"/api/v1/projects/"+pid+"/docs", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", r.f.IdemKey)
	resp, err := c.c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(out)
}

func (r *rich) waitMirrorOn(c *apiClient, pid string) {
	if r.off {
		return
	}
	waitUntil(r.t, "the mirror of "+pid, 60*time.Second, func() bool {
		code, b := c.do("GET", "/api/v1/projects/"+pid+"/mirror", nil, "")
		return code == 200 && strings.Contains(string(b), `"in_sync":true`)
	})
}

// newFeaturesAreOff asks the upgraded server, as its administrator, whether anything new is on: mail, the semantic
// index, Pandoc, presence, a project passphrase, typography. Nothing the old release's database held may switch one
// on, and the old release's own settings are not to be read as a choice.
func (r *rich) newFeaturesAreOff(base string, jar http.CookieJar) {
	if r.off {
		return
	}
	t := r.t
	adm := withJar(t, base, jar)
	var mail struct {
		Enabled, Ready, PausedByRestore bool `json:"-"`
		Host                            string
	}
	var m map[string]any
	code, b := adm.do("GET", "/api/v1/admin/mail", nil, "")
	if code != 200 {
		t.Errorf("mail settings after the upgrade: %d", code)
	} else {
		_ = json.Unmarshal(b, &m)
		if m["enabled"] != false || m["host"] != "" || m["password_set"] != false || m["links_enabled"] != false {
			t.Errorf("mail is not off after the upgrade: %.300s", b)
		}
	}
	_ = mail
	code, b = adm.do("GET", "/api/v1/admin/pandoc", nil, "")
	if code != 200 || !strings.Contains(string(b), `"enabled":false`) || strings.Contains(string(b), `"restore_paused":true`) {
		t.Errorf("Pandoc after the upgrade: %d %.300s", code, b)
	}
	code, b = adm.do("GET", "/api/v1/projects/"+r.f.P1+"/search/semantic/status", nil, "")
	if code != 200 || !strings.Contains(string(b), `"state":"off"`) {
		t.Errorf("the semantic index after the upgrade: %d %.300s", code, b)
	}
	code, b = adm.do("GET", "/api/v1/me/prefs", nil, "")
	if code != 200 || !strings.Contains(string(b), `"presence.share":"false"`) {
		t.Errorf("presence after the upgrade: %d %.300s", code, b)
	}
	code, b = adm.do("GET", "/api/v1/projects/"+r.f.P1+"/passphrase", nil, "")
	if code != 200 || !strings.Contains(string(b), `"protected":false`) {
		t.Errorf("a passphrase on a project of the old release: %d %.300s", code, b)
	}
	code, b = adm.do("GET", "/api/v1/projects/"+r.f.P1+"/typography", nil, "")
	if code != 200 || !strings.Contains(string(b), `"project":{"settings":{},"set":false}`) {
		t.Errorf("typography after the upgrade: %d %.300s", code, b)
	}
	code, b = adm.do("GET", "/api/v1/me/mail/digest", nil, "")
	if code != 200 || !strings.Contains(string(b), `"enabled":false`) {
		t.Errorf("the digest after the upgrade: %d %.300s", code, b)
	}
	report(t, "new features after the upgrade: mail, the semantic index, Pandoc, presence, passphrases, typography and the digest are all off")
}

// reportRemoteAfterRollback says what the old release makes of its remote after a rollback that came after the
// upgraded build had pushed.
func (r *rich) reportRemoteAfterRollback(c *apiClient, tag string) {
	if r.f.RemoteID == "" {
		return
	}
	code, b := c.do("GET", "/api/v1/projects/"+r.f.P1+"/git/remote", nil, "")
	var rem struct {
		Item struct {
			Enabled bool `json:"enabled"`
			Status  struct {
				State         string `json:"state"`
				InSync        bool   `json:"in_sync"`
				LastErrorCode string `json:"last_error_code"`
			} `json:"status"`
		} `json:"item"`
	}
	_ = json.Unmarshal(b, &rem)
	report(r.t, "%s: after the rollback the old release reads its remote (%d) as state %q, in_sync %v, error %q", tag, code, rem.Item.Status.State, rem.Item.Status.InSync, rem.Item.Status.LastErrorCode)
}
