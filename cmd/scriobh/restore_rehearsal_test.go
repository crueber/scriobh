package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/webhook"
)

// The Phase 6 backup and restore rehearsal, through the real binary.
//
// A populated instance (two projects with documents, revisions, an
// alternative, an image, comments, share links, API keys, a Git remote with a
// token, an AI provider with a key, a webhook, site options and goals) is
// backed up with `scriobh backup` and restored with `scriobh restore` into an
// empty data directory, as on a clean machine, once with the same
// SCRIOBH_SECRET_KEY and once with a different one. With the same key every
// table, document, revision, file and the regenerated mirror match, and the
// sealed credentials are used for real (the AI key reaches a fake provider on
// loopback, a webhook is signed with the original secret and sent to its
// sealed path, and the Git token opens). With another key the documents are all
// there and every credential refuses with a plain message.
//
// scripts/restore-rehearsal.sh runs this against built binaries.

const (
	rehearsalKey1 = "rehearsal-key-one-0123456789abcdef0123456789abcdef"
	rehearsalKey2 = "rehearsal-key-two-fedcba9876543210fedcba9876543210"
	aiSecret      = "sk-rehearsal-ai-key-7f3a9c"
	gitSecret     = "glpat-rehearsal-git-token-55e1"
	mailSecret    = "relay-rehearsal-password-2c8d41"
	hookPathTok   = "receiver-token-b81d"
)

// fakeAI answers the model list like an OpenAI-compatible server and records
// the bearer keys it was sent.
type fakeAI struct {
	mu   sync.Mutex
	keys []string
}

func (f *fakeAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.keys = append(f.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"object":"list","data":[{"id":"fake-model","object":"model"}]}`)
}

func (f *fakeAI) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) == 0 {
		return ""
	}
	return f.keys[len(f.keys)-1]
}

// hookSink records webhook deliveries.
type hookSink struct {
	mu  sync.Mutex
	got []hookHit
}

type hookHit struct {
	path, ts, sig string
	body          []byte
}

func (h *hookSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.got = append(h.got, hookHit{path: r.URL.Path, ts: r.Header.Get(webhook.HeaderTimestamp), sig: r.Header.Get(webhook.HeaderSignature), body: b})
	h.mu.Unlock()
	w.WriteHeader(204)
}

func (h *hookSink) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.got) }

// verified reports whether delivery i went to the sealed path and carries a
// valid signature under secret.
func (h *hookSink) verified(i int, secret string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.got) {
		return fmt.Errorf("no delivery %d", i)
	}
	d := h.got[i]
	if d.path != "/hooks/"+hookPathTok {
		return fmt.Errorf("delivered to %q, not the sealed path", d.path)
	}
	ts, err := strconv.ParseInt(d.ts, 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp %q", d.ts)
	}
	if want := "v1=" + webhook.Sign(secret, ts, d.body); !strings.Contains(d.sig, want) {
		return fmt.Errorf("signature %q does not match the original secret", d.sig)
	}
	return nil
}

// coverPNG is a picture of w by h pixels for a cover; seed changes the pattern, so two covers are different bytes.
func coverPNG(t *testing.T, w, h, seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x*seed/2 + 20), uint8(y/3 + 40*seed), uint8(90 + 50*seed), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))
	for x := 0; x < 12; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{uint8(20 * x), uint8(30 * y), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// tableState is each table's row count and a hash of its rows.
type tableState struct {
	Rows int
	Hash string
}

// dbState opens a stopped data directory's database and describes every table.
// It also runs the integrity and foreign-key checks.
func dbState(t *testing.T, path string) map[string]tableState {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	var check string
	if err := d.Read().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Errorf("%s: integrity_check = %q %v", path, check, err)
	}
	fk, err := d.Read().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for fk.Next() {
		n++
	}
	fk.Close()
	if n != 0 {
		t.Errorf("%s: foreign_key_check found %d rows", path, n)
	}
	rows, err := d.Read().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		names = append(names, s)
	}
	rows.Close()
	out := map[string]tableState{}
	for _, name := range names {
		r, err := d.Read().QueryContext(ctx, `SELECT * FROM "`+name+`"`)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cols, _ := r.Columns()
		var lines []string
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for _, v := range vals {
				fmt.Fprintf(&sb, "%T:%v\x1f", v, v)
			}
			lines = append(lines, sb.String())
		}
		r.Close()
		sort.Strings(lines)
		out[name] = tableState{Rows: len(lines), Hash: sha([]byte(strings.Join(lines, "\x1e")))}
	}
	return out
}

// snapshot is what the API says about the instance, captured to compare.
type snapshot map[string]string

func capture(t *testing.T, a *apiClient, projects []string, query string) snapshot {
	t.Helper()
	s := snapshot{}
	put := func(k string, b []byte) { s[k] = string(b) }
	put("project.list", a.get("/api/v1/projects"))
	put("key.list", stripVolatile(a.get("/api/v1/keys")))
	for _, pid := range projects {
		put(pid+" project.get", a.get("/api/v1/projects/"+pid))
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		b := a.get("/api/v1/projects/" + pid + "/docs")
		put(pid+" doc.list", b)
		json.Unmarshal(b, &list)
		for _, d := range list.Items {
			put(d.ID+" doc.get", a.get("/api/v1/docs/"+d.ID))
			put(d.ID+" revision.list", a.get("/api/v1/docs/"+d.ID+"/revisions"))
			put(d.ID+" alternative.list", a.get("/api/v1/docs/"+d.ID+"/alternatives"))
		}
		put(pid+" comment.list", a.get("/api/v1/projects/"+pid+"/comments"))
		put(pid+" share.list", stripVolatile(a.get("/api/v1/projects/"+pid+"/shares")))
		put(pid+" attachment.list", a.get("/api/v1/projects/"+pid+"/attachments"))
		put(pid+" cover_layout.get", a.get("/api/v1/projects/"+pid+"/cover-layout"))
		put(pid+" search", a.get("/api/v1/projects/"+pid+"/search?q="+query))
		put(pid+" site.options", a.get("/api/v1/projects/"+pid+"/site/options"))
		put(pid+" goal.list", stripVolatile(a.get("/api/v1/projects/"+pid+"/goals")))
		put(pid+" webhook.list", stripVolatile(a.get("/api/v1/projects/"+pid+"/webhooks")))
		put(pid+" git.remote.list", stripVolatile(a.get("/api/v1/projects/"+pid+"/git/remotes")))
	}
	put("ai.provider.list", stripVolatile(a.get("/api/v1/ai/providers")))
	return s
}

// stripVolatile removes fields that change by being used (last use times, use
// counts, "today" on a goal) so two snapshots compare on what was stored.
func stripVolatile(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	var walk func(any) any
	walk = func(x any) any {
		switch m := x.(type) {
		case map[string]any:
			for k := range m {
				if strings.HasPrefix(k, "last_used") || strings.HasPrefix(k, "last_seen") || k == "uses" || k == "use_count" || k == "views" || strings.HasPrefix(k, "last_view") ||
					k == "today" || k == "progress" || k == "next_attempt_at" || k == "updated_at" || k == "last_error" || k == "last_error_code" || k == "state" || k == "fail_count" || k == "last_attempt_at" {
					delete(m, k)
					continue
				}
				m[k] = walk(m[k])
			}
		case []any:
			for i := range m {
				m[i] = walk(m[i])
			}
		}
		return x
	}
	out, _ := json.Marshal(walk(v))
	return out
}

func TestRestoreRehearsal(t *testing.T) {
	if testing.Short() {
		t.Skip("the restore rehearsal builds and runs the binary")
	}
	bin := rehearsalBinary(t)
	ai := &fakeAI{}
	aiSrv := httptest.NewServer(ai)
	defer aiSrv.Close()
	sink := &hookSink{}
	hookSrv := httptest.NewServer(sink)
	defer hookSrv.Close()

	common := []string{"SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1", "SCRIOBH_GIT_ALLOW_PRIVATE=1", "SCRIOBH_MIRROR_DEBOUNCE_MS=50",
		"SCRIOBH_BACKUP_INTERVAL_HOURS=0", "SCRIOBH_WEBHOOK_DEBOUNCE_MS=50", "SCRIOBH_RATE_LIMIT=0"}
	env := func(dir, key string) []string {
		return append([]string{"SCRIOBH_DATA_DIR=" + dir, "SCRIOBH_SECRET_KEY=" + key}, common...)
	}
	serve := func(dir, key string) *proc {
		return start(t, bin, dir, append([]string{"SCRIOBH_SECRET_KEY=" + key}, common...)...)
	}

	// ---- populate ------------------------------------------------------------
	data1 := t.TempDir()
	p := serve(data1, rehearsalKey1)
	a := newAPI(t, p.base, "dev-admin")
	revoked := newAPI(t, p.base, "dev-admin") // a second session, revoked below

	type idOut struct {
		Item struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		} `json:"item"`
	}
	project := func(title string) string {
		var o idOut
		a.call("POST", "/api/v1/projects", map[string]any{"title": title, "kind": "novel"}, &o, 201)
		return o.Item.ID
	}
	p1, p2 := project("The Salt Road"), project("Lantern Hours")
	doc := func(pid, parent, kind, title, content string) (string, int) {
		var o idOut
		a.call("POST", "/api/v1/projects/"+pid+"/docs", map[string]any{"title": title, "kind": kind, "parent_id": parent, "content": content}, &o, 201)
		return o.Item.ID, o.Item.Revision
	}
	update := func(id string, rev int, content string) int {
		var o idOut
		a.call("PATCH", "/api/v1/docs/"+id, map[string]any{"base_revision": rev, "content": content}, &o, 200)
		return o.Item.Revision
	}
	part, _ := doc(p1, "", "part", "Part One", "")
	ch1, r1 := doc(p1, part, "chapter", "Harbour", "The quiet harbour at dusk.\n\nNóra counted the boats: *seven*, then eight.  \n")
	ch2, r2 := doc(p1, part, "chapter", "The Road", "A road of salt and gorse, and a lantern swinging.\n")
	doc(p1, "", "note", "Research", "Tide tables for 1911.\n")
	q1, _ := doc(p2, "", "chapter", "Lamplighter", "The lamplighter walked the harbour wall each evening.\n")
	_ = q1
	r1 = update(ch1, r1, "The quiet harbour at dusk.\n\nNóra counted the boats: *seven*, then eight, then nine.  \n")
	a.call("POST", "/api/v1/docs/"+ch1+"/revisions", map[string]any{"label": "Draft 1"}, nil, 201)
	r1 = update(ch1, r1, "The quiet harbour at dusk.\n\nNóra counted the boats: *seven*, then eight, then nine.  \n\nThe tide turned.\n")

	// An image, linked from the chapter.
	code, b := a.do("POST", "/api/v1/projects/"+p1+"/attachments?filename=map.png&alt=A+map", pngBytes(t), "image/png")
	if code != 201 {
		t.Fatalf("upload: %d %s", code, b)
	}
	var att idOut
	json.Unmarshal(b, &att)
	r1 = update(ch1, r1, "The quiet harbour at dusk.\n\n![A map](/files/"+att.Item.ID+")\n\nNóra counted the boats: *seven*, then eight, then nine.  \n\nThe tide turned.\n")

	// The book's own cover (docs/COVERS.md): a front picture and a back picture, cleaned by the cover upload and
	// named in a layout of sides; the front and the thumbnail are made when the layout is saved. The pictures are
	// attachments and travel in the backup, the layout is a row; what was made from them (files/projects/<id>/covers)
	// is not archived and must be made again on the restored server.
	var coverFront, coverBack struct {
		AttachmentID string `json:"attachment_id"`
	}
	a.call("POST", "/api/v1/projects/"+p1+"/cover-layout/images?part=front&filename=front.png", coverPNG(t, 400, 600, 1), &coverFront, 201)
	a.call("POST", "/api/v1/projects/"+p1+"/cover-layout/images?part=back&filename=back.png", coverPNG(t, 400, 600, 2), &coverBack, 201)
	a.call("PUT", "/api/v1/projects/"+p1+"/cover-layout", map[string]any{"revision": 0, "shape": "sides", "trim": "6x9",
		"front": map[string]any{"mode": "image", "attachment": coverFront.AttachmentID},
		"back":  map[string]any{"mode": "image", "attachment": coverBack.AttachmentID},
		"alt":   "A lantern on a quay at dusk"}, nil, 200)
	coverURLs := []string{"/api/v1/projects/" + p1 + "/cover", "/api/v1/projects/" + p1 + "/cover?size=thumb", "/api/v1/projects/" + p1 + "/cover?part=back"}
	coverBefore := map[string]string{}
	for _, u := range coverURLs {
		code, b := a.do("GET", u, nil, "")
		if code != 200 || len(b) < 100 || b[0] != 0xFF || b[1] != 0xD8 {
			t.Fatalf("the cover before the backup, %s: %d, %d bytes (want a JPEG)", u, code, len(b))
		}
		coverBefore[u] = sha(b)
	}

	// An alternative and a comment.
	a.call("POST", "/api/v1/docs/"+ch2+"/alternatives", map[string]any{"base_revision": r2, "scope": "word", "quote": "gorse", "variants": []string{"heather"}}, nil, 201)
	a.call("POST", "/api/v1/projects/"+p1+"/comments", map[string]any{"document_id": ch1, "quote": "The tide turned.", "body": "Too abrupt?"}, nil, 201)
	a.call("POST", "/api/v1/projects/"+p1+"/comments", map[string]any{"body": "Overall: lovely."}, nil, 201)

	// Share links: one kept, one revoked.
	var sh1, sh2 struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Path string `json:"path"`
	}
	a.call("POST", "/api/v1/projects/"+p1+"/shares", map[string]any{"label": "Editor"}, &sh1, 201)
	a.call("POST", "/api/v1/projects/"+p1+"/shares", map[string]any{"label": "Old reader"}, &sh2, 201)
	a.call("POST", "/api/v1/projects/"+p1+"/shares/"+sh2.Item.ID+"/revoke", map[string]any{}, nil, 200)

	// API keys: one kept, one revoked.
	var k1, k2 struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Token string `json:"secret"`
	}
	a.call("POST", "/api/v1/keys", map[string]any{"name": "drafting agent"}, &k1, 201)
	a.call("POST", "/api/v1/keys", map[string]any{"name": "old agent"}, &k2, 201)
	a.call("POST", "/api/v1/keys/"+k2.Item.ID+"/revoke", map[string]any{}, nil, 200)

	// A Git remote with a token; nothing listens at the address, which is fine:
	// the token is what is checked.
	a.call("POST", "/api/v1/projects/"+p1+"/git/remotes", map[string]any{"name": "Backup", "url": "https://127.0.0.1:9/owner/salt-road.git",
		"auth_kind": "https_token", "token": gitSecret, "push_mode": "manual"}, nil, 201)

	// An instance AI provider with a key, on loopback.
	var prov idOut
	a.call("POST", "/api/v1/ai/providers", map[string]any{"name": "Fake", "kind": "openai_compatible", "base_url": aiSrv.URL + "/v1",
		"api_key": aiSecret, "local": true, "default_model": "fake-model"}, &prov, 201)

	// A webhook with a sealed path.
	var hook struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Secret string `json:"secret"`
	}
	a.call("POST", "/api/v1/projects/"+p1+"/webhooks", map[string]any{"name": "Notify", "url": hookSrv.URL + "/hooks/" + hookPathTok,
		"events": []string{"comment.created"}}, &hook, 201)
	if !strings.HasPrefix(hook.Secret, "whsec_") {
		t.Fatalf("webhook secret %q", hook.Secret)
	}

	// Mail, switched on with a relay password and a Kindle edition waiting in the outbox. The relay is on loopback
	// where nothing listens, so the dispatcher's tries are refused and the message stays waiting.
	a.call("PUT", "/api/v1/admin/mail", map[string]any{"host": "127.0.0.1", "port": 2525, "security": "starttls", "allow_private": true, "username": "relay-user", "password": mailSecret,
		"from_address": "scriobh@example.org", "from_name": "Scriobh", "enabled": true}, nil, 200)
	a.call("PUT", "/api/v1/me/kindle", map[string]any{"address": "rehearsal@kindle.com"}, nil, 200)
	var kpv struct {
		Token string `json:"token"`
	}
	a.call("POST", "/api/v1/projects/"+p1+"/kindle/preview", map[string]any{}, &kpv, 200)
	a.call("POST", "/api/v1/projects/"+p1+"/kindle/send", map[string]any{"token": kpv.Token}, nil, 200)

	// Site options and goals.
	a.call("PATCH", "/api/v1/projects/"+p1+"/site/options", map[string]any{"options": map[string]any{"footer_text": "Written by the sea."}}, nil, 200)
	a.call("PUT", "/api/v1/projects/"+p1+"/goals", map[string]any{"kind": "daily", "target_words": 500}, nil, 200)

	// Revoke the second session.
	var sess struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	a.call("GET", "/api/v1/me/sessions", nil, &sess, 200)
	revokedOne := false
	for _, s := range sess.Items {
		if !s.Current && !revokedOne {
			a.call("POST", "/api/v1/me/sessions/"+s.ID+"/revoke", map[string]any{}, nil, 200)
			revokedOne = true
		}
	}
	if !revokedOne {
		t.Fatalf("no second session to revoke: %+v", sess)
	}
	if code, _ := revoked.do("GET", "/api/v1/projects", nil, ""); code != 401 {
		t.Fatalf("a revoked session still works: %d", code)
	}

	// The credentials work before the backup.
	checkCredentials := func(a *apiClient, sinkBefore int) {
		t.Helper()
		var tr struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		a.call("POST", "/api/v1/ai/providers/"+prov.Item.ID+"/test", map[string]any{}, &tr, 200)
		if !tr.OK || ai.last() != aiSecret {
			t.Errorf("AI provider test: ok=%v %q; the fake provider was sent %q", tr.OK, tr.Message, ai.last())
		}
		a.call("POST", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID+"/test", map[string]any{}, nil, 200)
		waitFor(t, "a webhook delivery", func() bool { return sink.count() > sinkBefore })
		if err := sink.verified(sinkBefore, hook.Secret); err != nil {
			t.Errorf("webhook delivery: %v", err)
		}
		var gt struct {
			OK      bool   `json:"ok"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		a.call("POST", "/api/v1/projects/"+p1+"/git/remote/test", map[string]any{}, &gt, 200)
		if gt.Code == "credentials_bad" || strings.Contains(gt.Message, "secret key") {
			t.Errorf("git remote test could not open the token: %+v", gt)
		}
	}
	checkCredentials(a, 0)

	// The mirror catches up.
	for _, pid := range []string{p1, p2} {
		waitFor(t, "the mirror of "+pid, func() bool {
			code, b := a.do("GET", "/api/v1/projects/"+pid+"/mirror", nil, "")
			return code == 200 && strings.Contains(string(b), `"in_sync":true`)
		})
	}
	before := capture(t, a, []string{p1, p2}, "harbour")

	// A backup beside the running server works (the scheduled path does the same).
	if out, errOut, code := runEnv(t, bin, env(data1, rehearsalKey1), "backup"); code != 0 {
		t.Fatalf("online backup: %d %s %s", code, out, errOut)
	}
	p.stop(t)
	logs := p.logs.String()

	// The archive used below is taken with the server stopped, so the database
	// it holds is exactly the one compared against.
	archive := filepath.Join(t.TempDir(), "rehearsal.zip")
	t0 := time.Now()
	out, errOut, code := runEnv(t, bin, env(data1, rehearsalKey1), "backup", "--out", archive)
	if code != 0 {
		t.Fatalf("backup: %d %s %s", code, out, errOut)
	}
	fi, _ := os.Stat(archive)
	report(t, "backup: %s (%d bytes) in %s", filepath.Base(archive), fi.Size(), time.Since(t0).Round(time.Millisecond))

	// No secret in the clear anywhere it should not be.
	entries := readZip(t, archive)
	for _, s := range []string{aiSecret, gitSecret, mailSecret, hook.Secret, hookPathTok, rehearsalKey1, k1.Token} {
		for name, b := range entries {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("a secret (%.6s...) is in the archive's %s", s, name)
			}
		}
		if strings.Contains(logs, s) || strings.Contains(out, s) {
			t.Errorf("a secret (%.6s...) reached the server log or the backup output", s)
		}
	}

	srcDB := dbState(t, filepath.Join(data1, "scriobh.db"))
	srcFiles := treeHashes(t, filepath.Join(data1, "files"), ".tmp")
	// What was made from the cover's pictures is not in the archive (internal/backup leaves files/projects/<id>/covers
	// out), so it is not compared with the restored files; it must be there to leave out, and it is made again below.
	srcCovers := 0
	for name := range srcFiles {
		if strings.Contains(name, "/covers/") {
			delete(srcFiles, name)
			srcCovers++
		}
	}
	if srcCovers < 3 {
		t.Fatalf("the source holds %d derived cover pictures, want the front, the thumbnail and the back", srcCovers)
	}
	for _, name := range []string{"cover_layouts", "attachments", "attachment_refs"} {
		if srcDB[name].Rows == 0 {
			t.Fatalf("the source has no rows in %s: the cover was not written", name)
		}
	}
	srcMirror := treeHashes(t, filepath.Join(data1, "mirror"), ".state", ".tmp")
	if len(srcFiles) == 0 || len(srcMirror) == 0 {
		t.Fatalf("nothing to compare: %d files, %d mirror files", len(srcFiles), len(srcMirror))
	}
	rowTotal := 0
	for _, s := range srcDB {
		rowTotal += s.Rows
	}
	report(t, "source: %d tables, %d rows, %d stored files, %d mirror files", len(srcDB), rowTotal, len(srcFiles), len(srcMirror))

	// ---- restore with the same key into an empty directory --------------------
	data2 := filepath.Join(t.TempDir(), "elsewhere", "data") // does not exist yet, as on a clean machine
	t0 = time.Now()
	out, errOut, code = runEnv(t, bin, env(data2, rehearsalKey1), "restore", archive)
	if code != 0 {
		t.Fatalf("restore: %d\n%s\n%s", code, out, errOut)
	}
	report(t, "restore (same key): %s", time.Since(t0).Round(time.Millisecond))
	if !strings.Contains(out, "restored 2 projects and 7 documents") {
		t.Errorf("restore summary: %s", out)
	}
	if strings.Contains(out, "cannot be read") {
		t.Errorf("with the same key nothing should be unreadable:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(data2, "secret.key")); err == nil {
		t.Error("restore wrote a key file")
	}
	gotDB := dbState(t, filepath.Join(data2, "scriobh.db"))
	for name, w := range srcDB {
		g := gotDB[name]
		switch {
		case name == "audit_log":
			if g.Rows != w.Rows+1 {
				t.Errorf("audit_log: %d rows, want %d plus the restore line", g.Rows, w.Rows)
			}
		case name == "mail_outbox":
			// A restore empties what waited: the message the source had is not in the copy.
			if w.Rows < 1 || g.Rows != 0 {
				t.Errorf("mail_outbox: %d rows after restore, %d before (a waiting message must be gone, and there must have been one)", g.Rows, w.Rows)
			}
		case name == "kindle_previews" && g.Rows == 0:
		case name == "webhooks" || name == "webhook_state" || name == "git_remotes" || name == "mail_config":
			// A restore pauses webhooks (switched off, with a reason), parks the feeder's cursor and switches mail off; the rows are
			// checked below, through the API and the table itself.
			if g.Rows != w.Rows {
				t.Errorf("table %s: %d rows after restore, %d before", name, g.Rows, w.Rows)
			}
		case g != w:
			t.Errorf("table %s: %d rows (hash %.8s) after restore, %d (hash %.8s) before", name, g.Rows, g.Hash, w.Rows, w.Hash)
		}
	}
	// Mail's settings survive the restore column for column (the sealed password included); only the switch,
	// the reason and the pause differ, and the marker the restore wrote is gone.
	mailCols := func(path string) string {
		d, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		var h, u, pw, sec, from, name, doms string
		var port, mb, on, paused int
		if err := d.Read().QueryRow(`SELECT host, port, security, username, password_sealed, from_address, from_name, max_attachment_mb, kindle_domains, enabled, restore_paused FROM mail_config`).Scan(&h, &port, &sec, &u, &pw, &from, &name, &mb, &doms, &on, &paused); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(h, port, sec, u, pw, from, name, mb, doms) + fmt.Sprintf("|on=%d paused=%d", on, paused)
	}
	srcMail, gotMail := mailCols(filepath.Join(data1, "scriobh.db")), mailCols(filepath.Join(data2, "scriobh.db"))
	if !strings.HasSuffix(srcMail, "|on=1 paused=0") || !strings.HasSuffix(gotMail, "|on=0 paused=1") || strings.TrimSuffix(srcMail, "|on=1 paused=0") != strings.TrimSuffix(gotMail, "|on=0 paused=1") {
		t.Errorf("mail_config differs after restore beyond the pause:\n  before %s\n  after  %s", srcMail, gotMail)
	}
	if _, err := os.Stat(filepath.Join(data2, "mail.restore-pause")); err == nil {
		t.Error("the restore left its mail marker behind after pausing mail")
	}
	for name := range gotDB {
		if _, ok := srcDB[name]; !ok {
			t.Errorf("table %s exists only after the restore", name)
		}
	}
	gotFiles := treeHashes(t, filepath.Join(data2, "files"), ".tmp")
	for name := range gotFiles {
		if strings.Contains(name, "/covers/") {
			t.Errorf("the restored directory holds a derived cover picture the archive should not have carried: %s", name)
		}
	}
	if d := diffMaps(srcFiles, gotFiles); len(d) > 0 {
		t.Errorf("stored files differ after restore: %v", d)
	}
	if d, err := db.Open(filepath.Join(data2, "scriobh.db")); err != nil {
		t.Fatal(err)
	} else {
		var on, parked int
		d.Read().QueryRow(`SELECT count(*) FROM webhooks WHERE enabled = 1`).Scan(&on)
		d.Read().QueryRow(`SELECT count(*) FROM webhook_state WHERE cursor < 0`).Scan(&parked)
		d.Close()
		if on != 0 || parked != 1 {
			t.Errorf("after a restore %d webhook(s) are on and the feeder is parked in %d state row(s); want none on, parked", on, parked)
		}
	}
	report(t, "restored database: %d tables match row for row (audit_log +1, the restore line; webhooks paused and the feeder parked); integrity_check ok; foreign_key_check clean", len(srcDB))

	// The mirror must regenerate on its own: remove the copy the archive carried.
	if err := os.RemoveAll(filepath.Join(data2, "mirror")); err != nil {
		t.Fatal(err)
	}
	s2 := serve(data2, rehearsalKey1)

	// Sessions are kept by a restore (documented); a revoked one stays revoked.
	old := withJar(t, s2.base, a.c.Jar)
	if code, b := old.do("GET", "/api/v1/projects", nil, ""); code != 200 {
		t.Errorf("a session from before the backup: %d %s (OPERATIONS.md says sessions survive a restore)", code, b)
	}
	if code, _ := withJar(t, s2.base, revoked.c.Jar).do("GET", "/api/v1/projects", nil, ""); code != 401 {
		t.Errorf("a session revoked before the backup came back: %d", code)
	}
	// API keys and share links likewise.
	key := &apiClient{t: t, base: s2.base, c: &http.Client{Timeout: 20 * time.Second}, auth: k1.Token}
	if code, b := key.do("GET", "/api/v1/projects", nil, ""); code != 200 {
		t.Errorf("an API key from before the backup: %d %s", code, b)
	}
	key.auth = k2.Token
	if code, _ := key.do("GET", "/api/v1/projects", nil, ""); code != 401 {
		t.Errorf("a revoked API key came back: %d", code)
	}
	anon := &apiClient{t: t, base: s2.base, c: &http.Client{Timeout: 20 * time.Second}}
	if code, _ := anon.do("GET", sh1.Path, nil, ""); code != 200 {
		t.Errorf("a share link from before the backup: %d", code)
	}
	if code, _ := anon.do("GET", sh2.Path, nil, ""); code == 200 {
		t.Errorf("a revoked share link came back: %d", code)
	}

	a2 := newAPI(t, s2.base, "dev-admin")
	// The restored copy sends nothing until a person says so: the webhook is paused, with a plain reason.
	var paused struct {
		Item struct {
			Enabled        bool   `json:"enabled"`
			DisabledReason string `json:"disabled_reason"`
		} `json:"item"`
	}
	a2.call("GET", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID, nil, &paused, 200)
	if paused.Item.Enabled || !strings.Contains(paused.Item.DisabledReason, "restored from a backup") {
		t.Errorf("a webhook after a restore: %+v", paused.Item)
	}
	if !strings.Contains(out, "paused") {
		t.Errorf("the restore output does not say webhooks were paused:\n%s", out)
	}
	a2.call("PATCH", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID, map[string]any{"enabled": true}, nil, 200)
	// Mail is paused too: off, with the restore's reason, until an administrator switches it on.
	var mailCfg struct {
		Enabled         bool   `json:"enabled"`
		PausedByRestore bool   `json:"paused_by_restore"`
		DisabledReason  string `json:"disabled_reason"`
	}
	a2.call("GET", "/api/v1/admin/mail", nil, &mailCfg, 200)
	if mailCfg.Enabled || !mailCfg.PausedByRestore || !strings.Contains(mailCfg.DisabledReason, "restored from a backup") {
		t.Errorf("mail after a restore: %+v", mailCfg)
	}
	if !strings.Contains(out, "Mail:") {
		t.Errorf("the restore output does not say mail was paused:\n%s", out)
	}
	// So is the Git remote: it pushes nothing (a public mirror included) until its owner switches it on, and says why.
	var pausedRem struct {
		Item struct {
			Enabled bool `json:"enabled"`
			Status  struct {
				LastErrorCode string `json:"last_error_code"`
			} `json:"status"`
		} `json:"item"`
	}
	a2.call("GET", "/api/v1/projects/"+p1+"/git/remote", nil, &pausedRem, 200)
	if pausedRem.Item.Enabled || pausedRem.Item.Status.LastErrorCode != "paused_by_restore" {
		t.Errorf("a git remote after a restore: %+v", pausedRem.Item)
	}
	a2.call("PATCH", "/api/v1/projects/"+p1+"/git/remote", map[string]any{"enabled": true}, nil, 200)
	after := capture(t, a2, []string{p1, p2}, "harbour")
	var diffs []string
	for k, v := range before {
		if after[k] != v {
			diffs = append(diffs, k)
		}
	}
	sort.Strings(diffs)
	for _, k := range diffs {
		t.Errorf("API answer differs after restore: %s\n  before: %.400s\n  after:  %.400s", k, before[k], after[k])
	}
	report(t, "API: %d answers (documents, revisions, alternatives, comments, shares, keys, attachments, search, site, goals, webhooks and remotes (once switched on again), providers) identical", len(before))
	// Full-text search answers from the restored index, not a fallback.
	var hits struct {
		Items    []json.RawMessage `json:"items"`
		Degraded bool              `json:"degraded"`
	}
	json.Unmarshal([]byte(after[p1+" search"]), &hits)
	if len(hits.Items) == 0 || hits.Degraded {
		t.Errorf("search after restore: %d hits, degraded %v", len(hits.Items), hits.Degraded)
	}
	report(t, "search: %q finds %d documents from the FTS index (not degraded)", "harbour", len(hits.Items))

	// The cover: the layout (compared above with the rest of the answers) and its pictures came back, and what is
	// shown of it is made again, the same bytes as before the backup.
	for _, u := range coverURLs {
		code, b := a2.do("GET", u, nil, "")
		if code != 200 || len(b) < 100 || b[0] != 0xFF || b[1] != 0xD8 {
			t.Errorf("the cover after the restore, %s: %d, %d bytes (want a JPEG)", u, code, len(b))
		} else if sha(b) != coverBefore[u] {
			t.Errorf("the cover after the restore, %s: sha %.12s, before the backup %.12s", u, sha(b), coverBefore[u])
		}
	}
	remade := 0
	for name := range treeHashes(t, filepath.Join(data2, "files"), ".tmp") {
		if strings.Contains(name, "/covers/") {
			remade++
		}
	}
	if remade < 3 {
		t.Errorf("after the restore the covers folder holds %d derived pictures; the server should have made the front, the thumbnail and the back again", remade)
	}
	for _, id := range []string{coverFront.AttachmentID, coverBack.AttachmentID} {
		code, b := a2.do("GET", "/api/v1/projects/"+p1+"/attachments/"+id+"/download", nil, "")
		if code != 200 || len(b) == 0 {
			t.Errorf("a cover picture after the restore: %d, %d bytes", code, len(b))
		}
	}
	report(t, "cover: the layout (sides, front and back pictures, alt text, hash) restored identically; its %d derived pictures were left out of the archive and made again on the restored server with the same bytes", remade)

	// Attachments: the bytes served are the bytes uploaded.
	code, b = a2.do("GET", "/api/v1/projects/"+p1+"/attachments/"+att.Item.ID+"/download", nil, "")
	if code != 200 || sha(b) != sha(pngBytes(t)) {
		t.Errorf("attachment after restore: %d, sha %s", code, sha(b))
	}
	report(t, "attachment: sha256 %s served unchanged", sha(b)[:16])

	// The sealed credentials open with the same key, for real.
	checkCredentials(a2, sink.count())
	var rem struct {
		Item struct {
			Enabled   bool `json:"enabled"`
			HasSecret bool `json:"has_secret"`
		} `json:"item"`
	}
	a2.call("GET", "/api/v1/projects/"+p1+"/git/remote", nil, &rem, 200)
	if !rem.Item.Enabled || !rem.Item.HasSecret {
		t.Errorf("git remote after a same-key restore: %+v", rem.Item)
	}
	report(t, "secrets (same key): AI key reached the provider, webhook signed with the original secret at its sealed path, Git token opened")

	// The mirror is rebuilt byte for byte.
	for _, pid := range []string{p1, p2} {
		waitFor(t, "the regenerated mirror of "+pid, func() bool {
			code, b := a2.do("GET", "/api/v1/projects/"+pid+"/mirror", nil, "")
			return code == 200 && strings.Contains(string(b), `"in_sync":true`)
		})
	}
	s2.stop(t)
	if d := diffMaps(srcMirror, treeHashes(t, filepath.Join(data2, "mirror"), ".state", ".tmp")); len(d) > 0 {
		t.Errorf("the regenerated mirror differs: %v", d)
	}
	report(t, "mirror: %d files regenerated identically from an empty mirror/", len(srcMirror))
	if s := s2.logs.String(); strings.Contains(s, aiSecret) || strings.Contains(s, gitSecret) || strings.Contains(s, hook.Secret) {
		t.Error("a secret reached the restored server's log")
	}

	// ---- restore with a different key ------------------------------------------
	data3 := filepath.Join(t.TempDir(), "other-key")
	out, errOut, code = runEnv(t, bin, env(data3, rehearsalKey2), "restore", archive)
	if code != 0 {
		t.Fatalf("restore with another key: %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"restored 2 projects and 7 documents", "Git remotes: the credentials of 1 remote(s) cannot be read",
		"AI providers: the keys of 1 provider(s) cannot be read", "Webhooks: 1 webhook(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore with another key should say %q:\n%s", want, out)
		}
	}
	report(t, "restore (other key): the output names what cannot be read:\n%s", indent(out))
	s3 := serve(data3, rehearsalKey2)
	// Sessions are stored as plain hashes, so they do not depend on the key.
	if code, _ := withJar(t, s3.base, a.c.Jar).do("GET", "/api/v1/projects", nil, ""); code != 200 {
		t.Errorf("a session from before the backup, restored with another key: %d", code)
	}
	if code, _ := withJar(t, s3.base, revoked.c.Jar).do("GET", "/api/v1/projects", nil, ""); code != 401 {
		t.Errorf("a revoked session came back with another key: %d", code)
	}
	a3 := newAPI(t, s3.base, "dev-admin")
	// The writing is all there.
	var docs struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	a3.call("GET", "/api/v1/projects/"+p1+"/docs", nil, &docs, 200)
	if len(docs.Items) != 5 || string(a3.get("/api/v1/docs/"+ch1)) != before[ch1+" doc.get"] {
		t.Errorf("documents after a restore with another key: %d", len(docs.Items))
	}
	// Each credential refuses, plainly.
	code, b = a3.do("POST", "/api/v1/ai/providers/"+prov.Item.ID+"/test", map[string]any{}, "")
	if !strings.Contains(string(b), "cannot be read with this installation's secret key") {
		t.Errorf("AI provider test with another key: %d %s", code, b)
	}
	var wh struct {
		Item struct {
			Enabled        bool   `json:"enabled"`
			DisabledReason string `json:"disabled_reason"`
		} `json:"item"`
	}
	a3.call("GET", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID, nil, &wh, 200)
	n := sink.count()
	a3.do("POST", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID+"/test", map[string]any{}, "")
	a3.call("GET", "/api/v1/projects/"+p1+"/webhooks/"+hook.Item.ID, nil, &wh, 200)
	if wh.Item.Enabled || !strings.Contains(wh.Item.DisabledReason, "secret key") {
		t.Errorf("webhook with another key: %+v", wh.Item)
	}
	time.Sleep(300 * time.Millisecond)
	if sink.count() != n {
		t.Error("a webhook was delivered although its secret cannot be read")
	}
	var rem3 struct {
		Item struct {
			Enabled   bool `json:"enabled"`
			HasSecret bool `json:"has_secret"`
		} `json:"item"`
	}
	raw := string(a3.call("GET", "/api/v1/projects/"+p1+"/git/remote", nil, &rem3, 200))
	if rem3.Item.Enabled || rem3.Item.HasSecret || !strings.Contains(raw, `"last_error_code":"credentials_unreadable"`) ||
		!strings.Contains(raw, "cannot be read with this installation's secret key") {
		t.Errorf("git remote after a restore with another key: %+v %s", rem3.Item, raw)
	}
	report(t, "secrets (other key): AI test and Git remote say %q; webhook switched off: %q", "cannot be read with this installation's secret key", wh.Item.DisabledReason)
	s3.stop(t)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}
