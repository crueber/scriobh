package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// doDry sends a request with the dry-run header.
func (s *Server) doDry(c caller, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Prefer", "dry-run")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeObj(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, rec.Body)
	}
	return m
}

// dryFixture is a project with documents in several states.
type dryFixture struct {
	owner                   caller
	project, trashedProject string
	archivedProject         string
	doc, doc2, trashedDoc   string
	docRev                  int
	revisionID              string
	keyID                   string
	projectTitle            string
	trashedProjectTitle     string
	ownerPrincipal          ops.Principal
	ctx                     context.Context
	srv                     *Server
}

func newDryFixture(t *testing.T, s *Server) *dryFixture {
	t.Helper()
	f := &dryFixture{srv: s, ctx: context.Background()}
	f.owner = s.newCaller(t, "owner", ops.RoleWriter)
	f.ownerPrincipal = ops.Principal{UserID: f.owner.userID, Role: ops.RoleWriter}
	call := func(name string, in any) any {
		out, err := s.Reg.Call(f.ctx, f.ownerPrincipal, name, in)
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		return out
	}
	f.projectTitle = "Dry Book"
	f.project = call("project.create", projects.CreateIn{Title: f.projectTitle}).(projects.Item).Item.ID
	f.trashedProjectTitle = "Dry Trashed"
	f.trashedProject = call("project.create", projects.CreateIn{Title: f.trashedProjectTitle}).(projects.Item).Item.ID
	call("project.trash", projects.StateIn{ID: f.trashedProject})
	f.archivedProject = call("project.create", projects.CreateIn{Title: "Dry Archived"}).(projects.Item).Item.ID
	call("project.archive", projects.StateIn{ID: f.archivedProject})
	d1 := call("doc.create", docs.CreateIn{ProjectID: f.project, Title: "One", Content: "First draft text here."}).(docs.Item).Item
	f.doc, f.docRev = d1.ID, d1.Revision
	f.doc2 = call("doc.create", docs.CreateIn{ProjectID: f.project, Title: "Two", Content: "Second."}).(docs.Item).Item.ID
	f.trashedDoc = call("doc.create", docs.CreateIn{ProjectID: f.project, Title: "Gone", Content: "Bye."}).(docs.Item).Item.ID
	call("doc.trash", docs.DocIn{ID: f.trashedDoc})
	rec := s.do(f.owner, "POST", "/api/v1/docs/"+f.doc+"/revisions", `{"label":"fixture"}`)
	if rec.Code >= 300 {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body)
	}
	rec = s.do(f.owner, "GET", "/api/v1/docs/"+f.doc+"/revisions", "")
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) == 0 {
		t.Fatalf("no revisions: %s", rec.Body)
	}
	f.revisionID = list.Items[0].ID
	kr := s.do(f.owner, "POST", "/api/v1/keys", `{"name":"fixture key"}`)
	var key struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	_ = json.Unmarshal(kr.Body.Bytes(), &key)
	if f.keyID = key.Item.ID; f.keyID == "" {
		t.Fatalf("create key: %d %s", kr.Code, kr.Body)
	}
	return f
}

// drive describes one valid dry-run request for an op.
type drive struct {
	path string
	body string
	// would says the preview must report a change; false for ops whose body
	// writes nothing on their own.
	would bool
}

// fixtures returns a valid request for every op the sweep knows how to drive.
func (f *dryFixture) fixtures(t *testing.T) map[string]drive {
	b := func(s string) string { return s }
	rev := strconv.Itoa
	files := `[{"path":"a.md","content":"Hello there."}]`
	token := func(path string) string {
		rec := f.srv.do(f.owner, "POST", path, `{"files":`+files+`}`)
		var out struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Token == "" {
			t.Fatalf("import preview %s: %d %.200s", path, rec.Code, rec.Body)
		}
		return out.Token
	}
	newTok, addTok := token("/api/v1/imports/preview"), token("/api/v1/projects/"+f.project+"/import/preview")
	// A second session of the owner, to revoke in a preview.
	if _, _, err := f.srv.Sess.Create(f.ctx, f.owner.userID, "second device", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	var sessions struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	_ = json.Unmarshal(f.srv.do(f.owner, "GET", "/api/v1/me/sessions", "").Body.Bytes(), &sessions)
	other := ""
	for _, x := range sessions.Items {
		if !x.Current {
			other = x.ID
		}
	}
	base := map[string]drive{
		"import.preview":        {"/api/v1/imports/preview", `{"files":` + files + `}`, false},
		"import.apply":          {"/api/v1/imports", `{"files":` + files + `,"token":"` + newTok + `","title":"Imported"}`, true},
		"import.append_preview": {"/api/v1/projects/" + f.project + "/import/preview", `{"files":` + files + `}`, false},
		"import.append":         {"/api/v1/projects/" + f.project + "/import", `{"files":` + files + `,"token":"` + addTok + `"}`, true},
		"revision.delete":       {"/api/v1/docs/" + f.doc + "/revision?revision_id=" + f.revisionID, "", true},
		"session.revoke":        {"/api/v1/me/sessions/" + other + "/revoke", "", true},
		"project.create":        {"/api/v1/projects", b(`{"title":"Previewed"}`), true},
		"project.update":        {"/api/v1/projects/" + f.project, b(`{"title":"Renamed"}`), true},
		"project.archive":       {"/api/v1/projects/" + f.project + "/archive", "", true},
		"project.unarchive":     {"/api/v1/projects/" + f.archivedProject + "/unarchive", "", true},
		"project.trash":         {"/api/v1/projects/" + f.project + "/trash", "", true},
		"project.restore":       {"/api/v1/projects/" + f.trashedProject + "/restore", "", true},
		"project.purge":         {"/api/v1/projects/" + f.trashedProject, `{"confirm_title":"` + f.trashedProjectTitle + `"}`, true},
		"project.pin":           {"/api/v1/projects/" + f.project + "/pin", `{"pinned":true}`, true},
		"project.open":          {"/api/v1/projects/" + f.project + "/open", `{"document_id":"` + f.doc + `"}`, true},
		"doc.create":            {"/api/v1/projects/" + f.project + "/docs", `{"title":"New chapter","content":"Words."}`, true},
		"doc.update":            {"/api/v1/docs/" + f.doc, `{"base_revision":` + rev(f.docRev) + `,"content":"A fresh paragraph of words."}`, true},
		"doc.move":              {"/api/v1/docs/" + f.doc2 + "/move", `{"parent_id":"","after":""}`, true},
		"doc.trash":             {"/api/v1/docs/" + f.doc2 + "/trash", "", true},
		"doc.restore":           {"/api/v1/docs/" + f.trashedDoc + "/restore", "", true},
		"doc.split":             {"/api/v1/docs/" + f.doc + "/split", `{"base_revision":` + rev(f.docRev) + `,"offset":5,"title":"Tail"}`, true},
		"doc.conflict":          {"/api/v1/docs/" + f.doc + "/conflict", `{"base_revision":1,"content":"local text"}`, false},
		"doc.resolve_link":      {"/api/v1/projects/" + f.project + "/links/resolve", `{"title":"Brand New Note","create":true}`, true},
		"export.markdown":       {"/api/v1/projects/" + f.project + "/export", `{"mode":"single"}`, true},
		"export.html":           {"/api/v1/projects/" + f.project + "/export/html", `{"mode":"single"}`, true},
		"goal.set":              {"/api/v1/projects/" + f.project + "/goals", `{"kind":"daily","target_words":500}`, true},
		"goal.delete":           {"/api/v1/projects/" + f.project + "/goals?kind=daily", "", false},
		"prefs.set":             {"/api/v1/me/prefs", `{"items":{"theme":"dark"}}`, true},
		"profile.update":        {"/api/v1/me", `{"display_name":"Somebody Else"}`, true},
		"revision.snapshot":     {"/api/v1/docs/" + f.doc + "/revisions", `{"label":"preview"}`, true},
		"revision.label":        {"/api/v1/docs/" + f.doc + "/revision", `{"revision_id":"` + f.revisionID + `","label":"relabelled"}`, true},
		"revision.restore":      {"/api/v1/docs/" + f.doc + "/revisions/restore", `{"revision_id":"` + f.revisionID + `","base_revision":` + rev(f.docRev) + `}`, false},
		"revision.prune":        {"/api/v1/projects/" + f.project + "/revisions/prune", `{}`, false},
		"session.revoke_others": {"/api/v1/me/sessions/revoke-others", "", false},
		"key.revoke":            {"/api/v1/keys/" + f.keyID + "/revoke", "", true},
		"doc.lease":             {"/api/v1/docs/" + f.doc + "/lease", `{"tab_id":"t1"}`, false},
	}
	for k, v := range f.compendiumFixtures(t) {
		base[k] = v
	}
	for k, v := range f.researchFixtures(t) {
		base[k] = v
	}
	for k, v := range f.compileFixtures(t) {
		base[k] = v
	}
	for k, v := range f.siteFixtures(t) {
		base[k] = v
	}
	for k, v := range f.aiFixtures(t) {
		base[k] = v
	}
	for k, v := range f.reviewFixtures(t) {
		base[k] = v
	}
	for k, v := range f.webhookDryFixtures(t) {
		base[k] = v
	}
	return base
}

// noPreview lists the ops that must refuse a dry run, with the reason.
var noPreview = map[string]string{
	"doc.lease":                 "in-memory lease",
	"mirror.rebuild":            "writes the mirror",
	"mirror.compendium.rebuild": "writes the mirror",
	"backup.create":             "writes an archive",
	"key.create":                "returns a secret",
	"share.create":              "returns a secret",
	"site.publish":              "writes a folder on the server",

	"ai.run":                     "spends money",
	"ai.chat.send":               "spends money",
	"ai.provider.test":           "connects to a provider",
	"ai.provider.models.refresh": "connects to a provider",

	"webhook.create":               "returns a secret",
	"webhook.rotate_secret":        "returns a secret",
	"webhook.test":                 "sends a request to the receiver",
	"mail.config.set":              "stores a sealed password and switches mail on or off",
	"ai.provider.create":           "takes a typed key: a secret input is never previewed",
	"ai.provider.update":           "takes a typed key: a secret input is never previewed",
	"share.update":                 "may take a typed passphrase: a secret input is never previewed",
	"mail.config.test":             "sends a message through the relay",
	"kindle.preview":               "compiles an edition and keeps a one-time token",
	"kindle.send":                  "queues a message that carries the manuscript out of the instance",
	"project.member.add":           "would say whose address it is",
	"compendium.member.add":        "would say whose address it is",
	"search.semantic":              "sends the question to the embedding endpoint (a GET: its handler must not run in a dry run)",
	"pandoc.config.set":            "runs the Pandoc program to check it",
	"pandoc.config.test":           "runs the Pandoc program",
	"project.attachment.upload":    "writes a file",
	"compendium.attachment.upload": "writes a file",
}

// TestDryRunSweep drives every op through the real REST route with a dry-run
// header and proves the preview is a preview: the database digest, the data
// directory and the event and audit tables are exactly what they were, and the
// response says dry_run. Ops without a fixture are still called with an empty
// request, so a new op cannot slip a write past the sweep.
func TestDryRunSweep(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	fx := f.fixtures(t)
	entries := s.Reg.Entries()
	if len(entries) == 0 {
		t.Fatal("no ops")
	}
	var unfixtured []string
	for _, e := range entries {
		e := e
		t.Run(e.Op.Name, func(t *testing.T) {
			who := f.owner
			if e.Op.Auth.Kind == ops.AuthAdmin {
				who = admin
			}
			path, body, would, driven := fillPath(e.Op.Path, f.project), bodyFor(e), false, false
			if d, ok := fx[e.Op.Name]; ok {
				path, body, would, driven = d.path, d.body, d.would, true
			} else if _, refused := noPreview[e.Op.Name]; !refused && e.Op.Method != http.MethodGet {
				unfixtured = append(unfixtured, e.Op.Name)
			}
			beforeDB := testutil.Dump(t, s.DB)
			beforeDir := testutil.TreeDigest(t, s.Cfg.DataDir)
			rec := s.doDry(who, e.Op.Method, path, body)
			if why, refused := noPreview[e.Op.Name]; refused {
				if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"dry_run_unsupported"`) {
					t.Errorf("%s (%s) must refuse a dry run: %d %s", e.Op.Name, why, rec.Code, rec.Body)
				}
			} else if driven {
				if rec.Code >= 300 {
					t.Fatalf("%s: %d %s", e.Op.Name, rec.Code, rec.Body)
				}
				m := decodeObj(t, rec)
				if m["dry_run"] != true {
					t.Errorf("response lacks dry_run: %s", rec.Body)
				}
				if _, ok := m["would_change"]; !ok {
					t.Errorf("response lacks would_change: %s", rec.Body)
				}
				if e.Op.Method != http.MethodGet && would && m["would_change"] != true {
					t.Errorf("a preview of %s should report a change: %s", e.Op.Name, rec.Body)
				}
				if rec.Header().Get("Preference-Applied") != "dry-run" {
					t.Errorf("missing Preference-Applied header")
				}
			}
			if got := testutil.Dump(t, s.DB); got != beforeDB {
				t.Errorf("%s: the dry run changed the database", e.Op.Name)
			}
			if got := testutil.TreeDigest(t, s.Cfg.DataDir); got != beforeDir {
				t.Errorf("%s: the dry run changed the data directory", e.Op.Name)
			}
		})
	}
	if len(unfixtured) > 0 {
		t.Logf("ops driven only with an empty request (add a fixture to cover them): %v", unfixtured)
	}
}

// TestDryRunReturnsWhatTheRealCallReturns: the preview of a create carries the
// same shape of result as the real call, read back through the same code path,
// and a real call afterwards works as if nothing had happened.
func TestDryRunReturnsWhatTheRealCallReturns(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	body := `{"title":"Chapter Dry","content":"Some words to count here."}`
	path := "/api/v1/projects/" + f.project + "/docs"
	dry := decodeObj(t, s.doDry(f.owner, "POST", path, body))
	item := dry["item"].(map[string]any)
	if item["title"] != "Chapter Dry" || item["word_count"].(float64) != 5 || item["id"] == "" {
		t.Errorf("preview result: %v", item)
	}
	if dry["changes"].(float64) < 2 { // the document and its search row at least
		t.Errorf("changes = %v", dry["changes"])
	}
	// The previewed document does not exist.
	var out struct{ Items []struct{ Title string } }
	rec := s.do(f.owner, "GET", path, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, d := range out.Items {
		if d.Title == "Chapter Dry" {
			t.Fatal("the preview created a document")
		}
	}
	real := s.do(f.owner, "POST", path, body)
	if real.Code != http.StatusCreated {
		t.Fatalf("real create after a preview: %d %s", real.Code, real.Body)
	}
	if got := decodeObj(t, real)["item"].(map[string]any)["id"]; got == item["id"] {
		t.Log("ids happened to match") // ids are random; this should not happen
	}
}

// TestDryRunRefusesAndAuthorizes: a preview needs the same authority as the
// real call, a stale revision is a 409 in the preview too, and an op that
// cannot be previewed says so with a stable code.
func TestDryRunRefusesAndAuthorizes(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	if rec := s.doDry(stranger, "POST", "/api/v1/docs/"+f.doc+"/trash", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger's preview: %d, want 404", rec.Code)
	}
	if rec := s.doDry(caller{name: "anon"}, "POST", "/api/v1/docs/"+f.doc+"/trash", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("an anonymous preview: %d, want 401", rec.Code)
	}
	rec := s.doDry(f.owner, "PATCH", "/api/v1/docs/"+f.doc, `{"base_revision":99,"content":"x"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"conflict"`) {
		t.Errorf("stale revision in a preview: %d %s", rec.Code, rec.Body)
	}
	rec = s.doDry(f.owner, "POST", "/api/v1/docs/"+f.doc+"/lease", `{"tab_id":"t1"}`)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"dry_run_unsupported"`) {
		t.Errorf("lease preview: %d %s", rec.Code, rec.Body)
	}
	// X-Scriobh-Dry-Run is an equal alternative to Prefer.
	req := httptest.NewRequest("POST", "/api/v1/docs/"+f.doc2+"/trash", nil)
	req.Header.Set("X-Scriobh-Dry-Run", "1")
	req.AddCookie(f.owner.cookie)
	r2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(r2, req)
	if decodeObj(t, r2)["dry_run"] != true {
		t.Errorf("X-Scriobh-Dry-Run not honoured: %s", r2.Body)
	}
	// A lease taken in a preview is not held: another tab can still take it.
	if rec := s.doDry(f.owner, "PATCH", "/api/v1/docs/"+f.doc, `{"base_revision":`+strconv.Itoa(f.docRev)+`,"tab_id":"tabA","content":"x y z"}`); rec.Code != 200 {
		t.Fatalf("preview with a tab: %d %s", rec.Code, rec.Body)
	}
	rec = s.do(f.owner, "POST", "/api/v1/docs/"+f.doc+"/lease", `{"tab_id":"tabB"}`)
	if !strings.Contains(rec.Body.String(), `"held":true`) {
		t.Errorf("a preview took the lease: %s", rec.Body)
	}
}

// Every op that declares NoDryRun is refused in a dry run over the real REST route: never answered as if it had run,
// never a server error, and, for a GET (whose handler a preview used to run as it is), exactly dry_run_unsupported. The
// declaration is what keeps a preview from running a handler that reaches outside the database. Ops listed in noPreview
// must declare it.
func TestEveryNoDryRunOpRefusesADryRunOverREST(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	n := 0
	for _, e := range s.Reg.Entries() {
		if !e.Op.NoDryRun {
			continue
		}
		n++
		who := f.owner
		if e.Op.Auth.Kind == ops.AuthAdmin {
			who = admin
		}
		rec := s.doDry(who, e.Op.Method, fillPath(e.Op.Path, f.project), bodyFor(e))
		if rec.Code < 300 || rec.Code >= 500 {
			t.Errorf("%s: a dry run answered %d %s", e.Op.Name, rec.Code, rec.Body)
		}
		if e.Op.Method == http.MethodGet && (rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "dry_run_unsupported")) {
			t.Errorf("%s is a GET marked NoDryRun and must be refused as unsupported: %d %s", e.Op.Name, rec.Code, rec.Body)
		}
	}
	if n < 10 {
		t.Errorf("only %d NoDryRun ops", n)
	}
	for name := range noPreview {
		if e, ok := s.Reg.Lookup(name); !ok || !e.Op.NoDryRun {
			t.Errorf("%s is in noPreview but does not declare NoDryRun", name)
		}
	}
}
