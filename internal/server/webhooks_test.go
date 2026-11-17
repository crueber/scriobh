package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/webhook"
)

// The webhooks, through the real server: the ops that happen, the events they
// write, the deliveries that leave, and what is in them.

type hookServer struct {
	*httptest.Server
	mu   sync.Mutex
	got  []hookRequest
	fail bool
}

type hookRequest struct {
	hdr  http.Header
	body []byte
}

func newHookServer(t *testing.T) *hookServer {
	h := &hookServer{}
	h.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.got = append(h.got, hookRequest{r.Header.Clone(), b})
		h.mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) requests() []hookRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookRequest(nil), h.got...)
}

func (h *hookServer) types() []string {
	var out []string
	for _, r := range h.requests() {
		out = append(out, r.hdr.Get(webhook.HeaderEvent))
	}
	return out
}

func (h *hookServer) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for len(h.requests()) < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := len(h.requests()); got < n {
		t.Fatalf("%d deliveries arrived, want %d: %v", got, n, h.types())
	}
}

type hookWorld struct {
	s       *Server
	owner   caller
	project string
	recv    *hookServer
	secret  string
	hookID  string
	doc     string
	docRev  int
	cancel  context.CancelFunc
}

func newHookWorld(t *testing.T, includeText bool) *hookWorld {
	t.Helper()
	s := newMCPServer(t, map[string]string{"SCRIOBH_WEBHOOK_ALLOW_PRIVATE": "1", "SCRIOBH_WEBHOOK_DEBOUNCE_MS": "0"})
	recv := newHookServer(t)
	s.Webhooks.SetSender(webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, Timeout: 5 * time.Second, TLS: recv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}))
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "The Hidden Title"})
	if err != nil {
		t.Fatal(err)
	}
	w := &hookWorld{s: s, owner: owner, project: out.(projects.Item).Item.ID, recv: recv}
	d, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: w.project, Title: "Chapter Secret Title", Content: "Manuscript words that must never leave."})
	if err != nil {
		t.Fatal(err)
	}
	w.doc, w.docRev = d.(docs.Item).Item.ID, d.(docs.Item).Item.Revision
	body := fmt.Sprintf(`{"name":"receiver","url":%q,"events":["document.updated","comment.created","suggestion.created","export.completed"],"include_text":%v}`, recv.URL+"/hook?x=1", includeText)
	rec := s.do(owner, "POST", "/api/v1/projects/"+w.project+"/webhooks", body)
	if rec.Code != 201 {
		t.Fatalf("webhook.create: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Item   struct{ ID string }
		Secret string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	w.secret, w.hookID = made.Secret, made.Item.ID
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	s.Webhooks.Start(ctx)
	t.Cleanup(func() { cancel(); s.Webhooks.Wait() })
	return w
}

func (w *hookWorld) rest(t *testing.T, method, path, body string) []byte {
	t.Helper()
	rec := w.s.do(w.owner, method, path, body)
	if rec.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

// doThings does one of each kind of thing a writer does in a project.
func (w *hookWorld) doThings(t *testing.T) {
	t.Helper()
	base := "/api/v1/projects/" + w.project
	w.rest(t, "PATCH", "/api/v1/docs/"+w.doc, fmt.Sprintf(`{"base_revision":%d,"content":"Manuscript words that must never leave, revised."}`, w.docRev))
	w.rest(t, "POST", base+"/comments", fmt.Sprintf(`{"document_id":%q,"body":"A comment whose words must not leave unless asked."}`, w.doc))
	var cur struct{ Item struct{ Revision int } }
	_ = json.Unmarshal(w.rest(t, "GET", "/api/v1/docs/"+w.doc, ""), &cur)
	w.rest(t, "POST", base+"/suggestions", fmt.Sprintf(`{"document_id":%q,"base_revision":%d,"changes":[{"quote":"Manuscript words","text":"Suggested words"}]}`, w.doc, cur.Item.Revision))
	w.rest(t, "POST", base+"/export", `{"mode":"single"}`)
	// Things that happen in a project and are not for webhooks.
	w.rest(t, "PATCH", base, `{"title":"A Renamed Title"}`)
	w.rest(t, "POST", base+"/docs", `{"title":"Another Chapter","content":"More."}`)
	w.rest(t, "POST", base+"/shares", `{"label":"friends","scope":"comment"}`)
	w.rest(t, "POST", "/api/v1/compendiums", `{"name":"A Private Bible"}`)
}

func TestWebhooksThroughTheRealServer(t *testing.T) {
	w := newHookWorld(t, false)
	w.doThings(t)
	w.recv.waitFor(t, 4)
	time.Sleep(600 * time.Millisecond) // anything else that was going to arrive
	types := w.recv.types()
	want := map[string]bool{"document.updated": true, "comment.created": true, "suggestion.created": true, "export.completed": true}
	if len(types) != 4 {
		for _, r := range w.recv.requests() {
			t.Logf("%s %s %s", r.hdr.Get(webhook.HeaderEvent), r.hdr.Get(webhook.HeaderID), r.body)
		}
		rows, _ := w.s.DB.Read().Query(`SELECT seq, type, COALESCE(document_id,''), payload_json FROM events ORDER BY seq`)
		for rows.Next() {
			var seq int
			var ty, d, p string
			_ = rows.Scan(&seq, &ty, &d, &p)
			t.Logf("event %d %s %s %s", seq, ty, d, p)
		}
		t.Fatalf("%d deliveries: %v", len(types), types)
	}
	for _, ty := range types {
		if !want[ty] {
			t.Errorf("an event nobody subscribed to: %s", ty)
		}
		delete(want, ty)
	}
	// Each is signed with the secret that create showed once, within the tolerance, and carries ids and counts.
	secrets := []string{"Manuscript words", "Hidden Title", "Renamed Title", "Chapter Secret", "A comment whose words", "Suggested words", "Another Chapter", "Private Bible", "friends"}
	for _, r := range w.recv.requests() {
		if !webhook.Verify(w.secret, r.hdr.Get(webhook.HeaderSignature), r.hdr.Get(webhook.HeaderTimestamp), r.body, time.Now(), 2*time.Minute) {
			t.Errorf("%s: the signature does not verify", r.hdr.Get(webhook.HeaderEvent))
		}
		for _, leak := range secrets {
			if strings.Contains(string(r.body), leak) {
				t.Errorf("%s carries %q: %s", r.hdr.Get(webhook.HeaderEvent), leak, r.body)
			}
		}
		var p struct {
			ProjectID string                 `json:"project_id"`
			Data      map[string]any         `json:"data"`
			Extra     map[string]interface{} `json:"-"`
		}
		_ = json.Unmarshal(r.body, &p)
		if p.ProjectID != w.project {
			t.Errorf("project id %q", p.ProjectID)
		}
		switch r.hdr.Get(webhook.HeaderEvent) {
		case "document.updated":
			if p.Data["document_id"] != w.doc || p.Data["words"] == nil || p.Data["revision"] == nil {
				t.Errorf("document.updated: %s", r.body)
			}
		case "comment.created":
			if p.Data["comment_id"] == nil || p.Data["text"] != nil {
				t.Errorf("comment.created: %s", r.body)
			}
		case "export.completed":
			if p.Data["export_id"] == nil || p.Data["bytes"] == nil || p.Data["documents"] == nil {
				t.Errorf("export.completed: %s", r.body)
			}
		}
	}
	// The listing shows what happened without a payload.
	var list struct {
		Items []struct {
			Delivered  int `json:"-"`
			Pending    int
			Dead       int
			LastStatus int `json:"last_status"`
		}
	}
	_ = json.Unmarshal(w.rest(t, "GET", "/api/v1/projects/"+w.project+"/webhooks", ""), &list)
	if len(list.Items) != 1 || list.Items[0].LastStatus != 200 || list.Items[0].Pending != 0 {
		t.Errorf("list: %+v", list)
	}
	var dl struct {
		Items []struct{ State, Event string }
	}
	_ = json.Unmarshal(w.rest(t, "GET", "/api/v1/projects/"+w.project+"/webhooks/"+w.hookID+"/deliveries?state=delivered", ""), &dl)
	if len(dl.Items) != 4 {
		t.Errorf("deliveries: %+v", dl)
	}
}

func TestCommentWordsLeaveOnlyWithTheOwnersSayAndAreNotStored(t *testing.T) {
	w := newHookWorld(t, true)
	base := "/api/v1/projects/" + w.project
	w.rest(t, "POST", base+"/comments", fmt.Sprintf(`{"document_id":%q,"body":"The owner asked for these WORDS."}`, w.doc))
	w.recv.waitFor(t, 1)
	r := w.recv.requests()[0]
	if !strings.Contains(string(r.body), "The owner asked for these WORDS.") {
		t.Fatalf("include_text=true but the words are not in the body: %s", r.body)
	}
	if !webhook.Verify(w.secret, r.hdr.Get(webhook.HeaderSignature), r.hdr.Get(webhook.HeaderTimestamp), r.body, time.Now(), 2*time.Minute) {
		t.Error("the signature does not cover the body with the words")
	}
	var stored string
	_ = w.s.DB.Read().QueryRow(`SELECT COALESCE(group_concat(payload_json), '') FROM webhook_deliveries`).Scan(&stored)
	if strings.Contains(stored, "WORDS") {
		t.Errorf("the outbox kept the words: %s", stored)
	}
	// Turning it off stops them at once.
	w.rest(t, "PATCH", base+"/webhooks/"+w.hookID, `{"include_text":false}`)
	w.rest(t, "POST", base+"/comments", fmt.Sprintf(`{"document_id":%q,"body":"A second SECRETWORDS comment."}`, w.doc))
	w.recv.waitFor(t, 2)
	if strings.Contains(string(w.recv.requests()[1].body), "SECRETWORDS") {
		t.Error("words left after include_text was turned off")
	}
}

// Nothing about a webhook reaches the mirror (so not a Git push either), an
// export, or the event stream, and a backup holds the configuration with the
// secret sealed.
func TestWebhookConfigurationStaysInTheDatabase(t *testing.T) {
	w := newHookWorld(t, false)
	w.doThings(t)
	w.recv.waitFor(t, 4)
	var sealed string
	if err := w.s.DB.Read().QueryRow(`SELECT secret_sealed FROM webhooks WHERE id = ?`, w.hookID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	var delivery string
	_ = w.s.DB.Read().QueryRow(`SELECT id FROM webhook_deliveries LIMIT 1`).Scan(&delivery)
	needles := []string{w.secret, strings.TrimPrefix(w.secret, "whsec_"), w.hookID, delivery, "/hook?x=1", w.recv.URL + "/hook", sealed, "receiver"}

	// The Markdown mirror (what Git pushes) is rebuilt and walked.
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.project+"/mirror/rebuild", "")
	if rec.Code != 200 {
		t.Logf("mirror rebuild: %d %s", rec.Code, rec.Body) // a server whose mirror is not running has nothing to leak; the walk below still runs
	}
	files := 0
	_ = filepath.WalkDir(filepath.Join(w.s.Cfg.DataDir, "mirror"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			files++
			for _, n := range needles {
				if strings.Contains(string(b), n) {
					t.Errorf("%s holds %q", p, n)
				}
			}
		}
		return nil
	})
	t.Logf("%d mirror files checked", files)

	// Exports: a single file and a bundle.
	for _, mode := range []string{"single", "bundle"} {
		rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.project+"/export", fmt.Sprintf(`{"mode":%q}`, mode))
		if rec.Code != 200 {
			t.Fatalf("export %s: %d %s", mode, rec.Code, rec.Body)
		}
		all := rec.Body.Bytes()
		if zr, err := zip.NewReader(bytes.NewReader(all), int64(len(all))); err == nil {
			var cat []byte
			for _, f := range zr.File {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				cat = append(cat, f.Name...)
				cat = append(cat, b...)
			}
			all = cat
		}
		for _, n := range needles {
			if strings.Contains(string(all), n) {
				t.Errorf("the %s export holds %q", mode, n)
			}
		}
	}

	// The backup holds the tables, with the secret sealed and no plaintext anywhere in the archive.
	res, err := (&backup.Service{DB: w.s.DB, DataDir: w.s.Cfg.DataDir, Version: "test"}).Create(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	sawDB := false
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == backup.DatabaseName {
			sawDB = true
			for _, must := range []string{"webhooks", sealed} {
				if !bytes.Contains(b, []byte(must)) {
					t.Errorf("the backup's database lacks %q (the configuration is part of a backup)", must)
				}
			}
		}
		for _, n := range []string{w.secret, strings.TrimPrefix(w.secret, "whsec_"), "/hook?x=1"} {
			if bytes.Contains(b, []byte(n)) {
				t.Errorf("%s in the backup holds the plaintext secret or the sealed address path", f.Name)
			}
		}
	}
	if !sawDB {
		t.Error("the backup has no database")
	}
	if _, err := os.Stat(filepath.Join(w.s.Cfg.DataDir, "secret.key")); err == nil {
		for _, f := range zr.File {
			if strings.Contains(f.Name, "secret.key") {
				t.Errorf("the backup holds the key file")
			}
		}
	}

	// The event stream never names a webhook, and an export event reaches only an editor's stream.
	var n int
	_ = w.s.DB.Read().QueryRow(`SELECT count(*) FROM events WHERE type LIKE 'webhook.%'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d webhook events were written", n)
	}
	_ = tls.VersionTLS12
}
