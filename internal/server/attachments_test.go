package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// pngBytes makes a valid PNG whose bytes depend on seed, so two seeds are two files.
func pngBytes(t testing.TB, w, h, seed int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x + seed), uint8(y), uint8(seed), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var tinyPDF = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")

type attFixture struct {
	s                                      *Server
	owner, editor, reader, stranger, admin caller
	pid, cid, other                        string
	readerKey, otherKey                    string
}

func newAttFixture(t *testing.T, env map[string]string) *attFixture {
	t.Helper()
	cfg := map[string]string{"SCRIOBH_RATE_LIMIT": "0"}
	for k, v := range env {
		cfg[k] = v
	}
	d := testutil.DB(t)
	s, err := New(Deps{Cfg: testutil.Config(t, cfg), DB: d})
	if err != nil {
		t.Fatal(err)
	}
	f := &attFixture{s: s}
	f.owner = s.newCaller(t, "owner", ops.RoleWriter)
	f.editor = s.newCaller(t, "editor", ops.RoleWriter)
	f.reader = s.newCaller(t, "reader", ops.RoleReader)
	f.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	f.admin = s.newCaller(t, "admin", ops.RoleAdmin)
	mk := func(path, body string) string {
		rec := s.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return idOf(t, rec.Body.Bytes())
	}
	f.pid = mk("/api/v1/projects", `{"title":"Attached Book"}`)
	f.other = mk("/api/v1/projects", `{"title":"Elsewhere"}`)
	f.cid = mk("/api/v1/compendiums", `{"name":"Attached Bible"}`)
	s.addMember(t, f.pid, f.editor.userID, ops.ProjectEditor)
	s.addMember(t, f.pid, f.reader.userID, ops.ProjectReader)
	return f
}

func (f *attFixture) upload(c caller, owner, query string, body []byte, ctype string) *httptest.ResponseRecorder {
	return f.uploadTo(c, "/api/v1/projects/"+owner+"/attachments", query, body, ctype)
}

func (f *attFixture) uploadTo(c caller, path, query string, body []byte, ctype string) *httptest.ResponseRecorder {
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return rec
}

type attOut struct {
	Item struct {
		ID                                     string
		OwnerKind                              string `json:"owner_kind"`
		OwnerID                                string `json:"owner_id"`
		Filename, Alt, Mime, Kind, SHA256, URL string
		Bytes                                  int64
		Width, Height, Refs                    int
	}
	Duplicate bool
}

func decodeAtt(t testing.TB, rec *httptest.ResponseRecorder) attOut {
	t.Helper()
	var o attOut
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return o
}

func (f *attFixture) get(c caller, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return rec
}

func (f *attFixture) dataFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	root := filepath.Join(f.s.Cfg.DataDir, "files")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.Contains(p, ".tmp") {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func TestUploadStoresDeduplicatesAndCounts(t *testing.T) {
	f := newAttFixture(t, nil)
	img := pngBytes(t, 40, 30, 1)
	rec := f.upload(f.editor, f.pid, "filename=cover%20art.png&alt=A%20map", img, "image/png")
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	a := decodeAtt(t, rec)
	if a.Item.Mime != "image/png" || a.Item.Kind != "image" || a.Item.Width != 40 || a.Item.Height != 30 || a.Item.Alt != "A map" ||
		a.Item.Filename != "cover art.png" || a.Item.URL != "/files/"+a.Item.ID || a.Item.Bytes != int64(len(img)) || a.Duplicate {
		t.Errorf("item: %+v", a)
	}
	files := f.dataFiles(t)
	if len(files) != 1 || !strings.HasPrefix(files[0], filepath.Join("projects", f.pid, "attachments", a.Item.SHA256)) || !strings.HasSuffix(files[0], ".png") {
		t.Fatalf("files on disk: %v", files)
	}
	// The same bytes again, under another name, are the same attachment.
	rec = f.upload(f.owner, f.pid, "filename=other-name.png", img, "")
	b := decodeAtt(t, rec)
	if rec.Code != 201 || !b.Duplicate || b.Item.ID != a.Item.ID || b.Item.Filename != "cover art.png" {
		t.Errorf("duplicate: %d %+v", rec.Code, b)
	}
	if n := len(f.dataFiles(t)); n != 1 {
		t.Errorf("%d files after a duplicate", n)
	}
	// The same bytes in another project are another attachment, with its own file.
	rec = f.upload(f.owner, f.other, "filename=x.png", img, "")
	if c := decodeAtt(t, rec); rec.Code != 201 || c.Duplicate || c.Item.ID == a.Item.ID {
		t.Errorf("another project: %d %+v", rec.Code, c)
	}
	if n := len(f.dataFiles(t)); n != 2 {
		t.Errorf("%d files, want 2", n)
	}
	// The counters follow: project, person and instance.
	var bytesUsed int64
	_ = f.s.DB.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = 'owner:' || ?`, f.pid).Scan(&bytesUsed)
	if bytesUsed != int64(len(img)) {
		t.Errorf("project usage %d, want %d", bytesUsed, len(img))
	}
	_ = f.s.DB.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = 'instance'`).Scan(&bytesUsed)
	if bytesUsed != int64(2*len(img)) {
		t.Errorf("instance usage %d, want %d", bytesUsed, 2*len(img))
	}
	// list and get
	rec = f.get(f.reader, "/api/v1/projects/"+f.pid+"/attachments")
	var l struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if rec.Code != 200 || len(l.Items) != 1 || l.Items[0].ID != a.Item.ID {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(f.reader, "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID); rec.Code != 200 {
		t.Errorf("get: %d", rec.Code)
	}
	// An attachment of one project is not found through another.
	if rec := f.get(f.owner, "/api/v1/projects/"+f.other+"/attachments/"+a.Item.ID); rec.Code != 404 {
		t.Errorf("another project's id: %d, want 404", rec.Code)
	}
}

func TestUploadTypePolicy(t *testing.T) {
	f := newAttFixture(t, nil)
	png1 := pngBytes(t, 8, 8, 2)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<!DOCTYPE html><html><script>alert(document.cookie)</script></html>`)
	elf := append([]byte("\x7fELF\x02\x01\x01\x00"), make([]byte, 64)...)
	polyglot := append([]byte("GIF89a"), []byte(`<script>alert(1)</script>`)...) // a GIF header on a page
	cases := []struct {
		name, filename, ctype string
		body                  []byte
		want                  int
		why                   string
	}{
		{"svg by name", "logo.svg", "image/svg+xml", svg, 415, "not kept"},
		{"svg named png", "logo.png", "image/png", svg, 415, "SVG"},
		{"svg without a name", "", "", svg, 415, "SVG"},
		{"svg as text", "figure.txt", "text/plain", svg, 415, "SVG"},
		{"xml", "data.xml", "", []byte(`<?xml version="1.0"?><a/>`), 415, "not kept"},
		{"html by name", "page.html", "text/html", html, 415, "not kept"},
		{"html named png", "page.png", "image/png", html, 415, "contents do not match"},
		{"elf named txt", "tool.txt", "", elf, 415, "binary"},
		{"elf named exe", "tool.exe", "", elf, 415, "not kept"},
		{"elf named png", "tool.png", "", elf, 415, "binary"},
		{"script", "run.txt", "", []byte("#!/bin/sh\nrm -rf /\n"), 415, "script"},
		{"script named sh", "run.sh", "", []byte("echo hi\n"), 415, "not kept"},
		{"polyglot gif named png", "x.png", "", polyglot, 415, "contents do not match"},
		{"png named jpg", "photo.jpg", "", png1, 415, "contents do not match"},
		{"png named txt", "notes.txt", "", png1, 415, "contents do not match"},
		{"declared type contradicts bytes", "a.png", "image/jpeg", png1, 415, "declared type"},
		{"pdf named png", "a.png", "", tinyPDF, 415, "contents do not match"},
		{"double extension", "invoice.pdf.exe", "", tinyPDF, 415, "not kept"},
		{"empty", "a.txt", "", nil, 422, "empty"},
		{"not utf-8", "a.txt", "", []byte{0xff, 0xfe, 0xfd, 'a', 'b'}, 415, "UTF-8"},
		{"truncated png", "a.png", "", png1[:20], 415, "not kept"},
		{"fine png, no name", "", "image/png", png1, 201, ""},
		{"fine png, octet-stream", "a.png", "application/octet-stream", pngBytes(t, 9, 9, 22), 201, ""},
		{"fine pdf", "paper.PDF", "application/pdf", tinyPDF, 201, ""},
		{"fine text", "notes.txt", "text/plain", []byte("Notes\nmore notes\n"), 201, ""},
		{"fine markdown", "notes.md", "", []byte("# Title\n\ntext\n"), 201, ""},
		{"html in a txt is inert text", "snippet.txt", "", []byte("<p>just text about <b>html</b></p>"), 201, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := ""
			if tc.filename != "" {
				q = "filename=" + strings.ReplaceAll(tc.filename, " ", "%20")
			}
			rec := f.upload(f.owner, f.pid, q, tc.body, tc.ctype)
			if rec.Code != tc.want {
				t.Fatalf("%d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want != 201 && !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(tc.why)) {
				t.Errorf("message should mention %q: %s", tc.why, rec.Body)
			}
		})
	}
	// Nothing refused left anything behind: the files on disk are exactly the accepted ones.
	var accepted int
	for _, tc := range cases {
		if tc.want == 201 {
			accepted++
		}
	}
	if got := len(f.dataFiles(t)); got != accepted {
		t.Errorf("%d files on disk, want %d: %v", got, accepted, f.dataFiles(t))
	}
	if entries, _ := os.ReadDir(filepath.Join(f.s.Cfg.DataDir, "files", ".tmp")); len(entries) != 0 {
		t.Errorf("%d temp files left behind", len(entries))
	}
}

func TestUploadNamesNeverBecomePaths(t *testing.T) {
	f := newAttFixture(t, nil)
	img := pngBytes(t, 8, 8, 3)
	for i, name := range []string{"../../../etc/passwd.png", `..\..\evil.png`, "/abs/path/x.png", "a/b/c.png", "x\x00.png", "%2e%2e%2f%2e%2e%2fx.png", "con..png", "..png"} {
		rec := f.upload(f.owner, f.pid, "filename="+strings.NewReplacer("\x00", "%00", "\\", "%5C", "/", "%2F").Replace(name), pngBytes(t, 8, 8, 10+i), "")
		if rec.Code != 201 {
			t.Errorf("%q: %d %s", name, rec.Code, rec.Body)
			continue
		}
		a := decodeAtt(t, rec)
		if strings.ContainsAny(a.Item.Filename, `/\`) || strings.Contains(a.Item.Filename, "..") && strings.HasPrefix(a.Item.Filename, ".") || a.Item.Filename == "" {
			t.Errorf("%q became %q", name, a.Item.Filename)
		}
	}
	_ = img
	root := filepath.Join(f.s.Cfg.DataDir, "files", "projects", f.pid, "attachments")
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		name := e.Name()
		if len(name) != 64+len(".png") || !strings.HasSuffix(name, ".png") {
			t.Errorf("a stored file is named %q: not <sha256>.<ext>", name)
		}
	}
	if _, err := os.Stat(filepath.Join(f.s.Cfg.DataDir, "etc")); err == nil {
		t.Error("a traversal name wrote outside the owner's folder")
	}
}

func TestUploadSizeLimitsAndQuotas(t *testing.T) {
	f := newAttFixture(t, map[string]string{"SCRIOBH_UPLOAD_MAX_MB": "1", "SCRIOBH_QUOTA_PROJECT_MB": "3", "SCRIOBH_QUOTA_USER_MB": "4", "SCRIOBH_QUOTA_INSTANCE_MB": "5"})
	text := func(n int, seed byte) []byte { return append(bytes.Repeat([]byte{'a'}, n-1), seed) }
	if rec := f.upload(f.owner, f.pid, "filename=big.txt", text(1<<20+1, 'x'), ""); rec.Code != 413 || !strings.Contains(rec.Body.String(), "too_large") {
		t.Errorf("over the file limit: %d %s", rec.Code, rec.Body)
	}
	// A body whose length is not declared is limited as well.
	req := httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments?filename=big.txt", io_NopNoLen{bytes.NewReader(text(1<<20+5, 'y'))})
	req.ContentLength = -1
	req.AddCookie(f.owner.cookie)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Errorf("an undeclared length over the limit: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 0 {
		t.Errorf("%d files stored after refusals", n)
	}
	put := func(c caller, owner string, seed byte) int {
		return f.upload(c, owner, "filename=f.txt", text(1<<20, seed), "").Code
	}
	for i := 0; i < 3; i++ {
		if code := put(f.owner, f.pid, byte('a'+i)); code != 201 {
			t.Fatalf("upload %d: %d", i, code)
		}
	}
	// Three MB in the project: a fourth is over the project quota. (The early check
	// cannot tell a duplicate from a new file before reading it, so even a repeat is
	// refused at the limit; the file is already there to link to.)
	if rec := f.upload(f.owner, f.pid, "filename=f.txt", text(1<<20, 'd'), ""); rec.Code != 413 || !strings.Contains(rec.Body.String(), "quota_exceeded") {
		t.Errorf("project quota: %d %s", rec.Code, rec.Body)
	}
	if code := put(f.owner, f.pid, 'a'); code != 413 {
		t.Errorf("at the limit even a repeat is refused early: %d", code)
	}
	// The owner has used 3 of their 4 MB: one more elsewhere fits, the next does not.
	if code := put(f.owner, f.other, 'e'); code != 201 {
		t.Errorf("within the person's quota: %d", code)
	}
	if rec := f.upload(f.owner, f.other, "filename=f.txt", text(1<<20, 'f'), ""); rec.Code != 413 || !strings.Contains(rec.Body.String(), "storage limit") {
		t.Errorf("person quota: %d %s", rec.Code, rec.Body)
	}
	// Someone else is not held to it, until the instance fills (5 MB, 4 used).
	f.s.addMember(t, f.other, f.editor.userID, ops.ProjectEditor)
	if code := put(f.editor, f.other, 'g'); code != 201 {
		t.Errorf("another person: %d", code)
	}
	if rec := f.upload(f.editor, f.other, "filename=f.txt", text(1<<20, 'h'), ""); rec.Code != 413 || !strings.Contains(rec.Body.String(), "this server") {
		t.Errorf("instance quota: %d %s", rec.Code, rec.Body)
	}
	// Deleting gives the space back, in every counter.
	rec = f.get(f.owner, "/api/v1/projects/"+f.pid+"/attachments")
	var l struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if rec := f.s.do(f.owner, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+l.Items[0].ID, ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if code := put(f.editor, f.other, 'i'); code != 201 {
		t.Errorf("after a delete: %d", code)
	}
	// usage op: the numbers are what the counters hold.
	rec = f.get(f.admin, "/api/v1/projects/"+f.pid+"/attachment-usage")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"instance"`) {
		t.Errorf("usage: %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(f.owner, "/api/v1/projects/"+f.pid+"/attachment-usage"); strings.Contains(rec.Body.String(), `"instance"`) {
		t.Errorf("the instance total is for administrators: %s", rec.Body)
	}
}

// io_NopNoLen hides a reader's length from the request.
type io_NopNoLen struct{ r *bytes.Reader }

func (n io_NopNoLen) Read(p []byte) (int, error) { return n.r.Read(p) }

func TestServedHeaders(t *testing.T) {
	f := newAttFixture(t, nil)
	img := decodeAtt(t, f.upload(f.owner, f.pid, "filename=map.png", pngBytes(t, 10, 10, 4), ""))
	pdf := decodeAtt(t, f.upload(f.owner, f.pid, "filename=paper.pdf", tinyPDF, ""))
	txt := decodeAtt(t, f.upload(f.owner, f.pid, "filename=notes.md", []byte("<script>alert(1)</script> # notes"), ""))

	rec := f.get(f.reader, img.Item.URL)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline;") {
		t.Fatalf("image: %d %v", rec.Code, rec.Header())
	}
	for _, h := range []struct{ k, want string }{
		{"X-Content-Type-Options", "nosniff"},
		{"Cache-Control", "private, no-cache"},
		{"Content-Security-Policy", "sandbox"},
		{"Cross-Origin-Resource-Policy", "same-origin"},
	} {
		if !strings.Contains(rec.Header().Get(h.k), h.want) {
			t.Errorf("image %s = %q, want %q", h.k, rec.Header().Get(h.k), h.want)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), pngBytes(t, 10, 10, 4)) {
		t.Error("the served bytes differ from the uploaded ones")
	}
	etag := rec.Header().Get("ETag")
	if etag != `"`+img.Item.SHA256+`"` {
		t.Errorf("etag %q", etag)
	}
	if rec := f.get(f.reader, img.Item.URL, "If-None-Match", etag); rec.Code != 304 {
		t.Errorf("revalidation: %d", rec.Code)
	}
	if rec := f.get(f.reader, img.Item.URL, "Range", "bytes=0-7"); rec.Code != 206 || rec.Body.Len() != 8 {
		t.Errorf("range: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := f.get(f.reader, img.Item.URL+"?download=1"); !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("forced download: %q", rec.Header().Get("Content-Disposition"))
	}
	rec = f.get(f.reader, pdf.Item.URL)
	if rec.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline;") ||
		strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") ||
		rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("pdf headers: %v", rec.Header())
	}
	rec = f.get(f.reader, txt.Item.URL)
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("text is served as an inert download: %v", rec.Header())
	}
	// Who may read.
	if rec := f.get(caller{name: "anonymous"}, img.Item.URL); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	stranger := f.get(f.stranger, img.Item.URL)
	missing := f.get(f.stranger, "/files/att_00000000000000000000000000")
	if stranger.Code != 404 || missing.Code != 404 || stranger.Body.String() != missing.Body.String() {
		t.Errorf("a stranger and a missing file must look the same: %d %q / %d %q", stranger.Code, stranger.Body, missing.Code, missing.Body)
	}
	if rec := f.get(f.admin, img.Item.URL); rec.Code != 404 { // an administrator who is not a member is a stranger on the pages
		t.Errorf("admin: %d", rec.Code)
	}
	for _, bad := range []string{"/files/..%2f..%2fscriobh.db", "/files/" + img.Item.ID + "/x", "/files/", "/files/%00", "/files/att_x"} {
		if rec := f.get(f.owner, bad); rec.Code == 200 {
			t.Errorf("%s answered 200", bad)
		}
	}
	// An API key does not read files through the browser route (keys are for /api/).
	key, _ := f.s.mintKey(t, f.owner, `{"name":"k"}`)
	if rec := f.s.doKey(key, "GET", img.Item.URL, ""); rec.Code == 200 {
		t.Errorf("a key read through /files: %d", rec.Code)
	}
	// But it can download through the op, and a pinned key only from its own project.
	if rec := f.s.doKey(key, "GET", "/api/v1/projects/"+f.pid+"/attachments/"+img.Item.ID+"/download", ""); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes(t, 10, 10, 4)) ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("download op: %d %v", rec.Code, rec.Header())
	}
	pin, _ := f.s.mintKey(t, f.owner, `{"name":"pinned","project_id":"`+f.other+`"}`)
	if rec := f.s.doKey(pin, "GET", "/api/v1/projects/"+f.pid+"/attachments/"+img.Item.ID+"/download", ""); rec.Code != 404 {
		t.Errorf("a key pinned elsewhere: %d", rec.Code)
	}
}

func TestCompendiumAttachmentsAreMembersOnly(t *testing.T) {
	f := newAttFixture(t, nil)
	// The reader is on the project, not on the Compendium. The project's primary is the Compendium.
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/compendium", `{"compendium_id":"`+f.cid+`"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := f.uploadTo(f.owner, "/api/v1/compendiums/"+f.cid+"/attachments", "filename=portrait.png", pngBytes(t, 12, 12, 5), "")
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	a := decodeAtt(t, rec)
	if a.Item.OwnerKind != "compendium" || a.Item.OwnerID != f.cid {
		t.Errorf("%+v", a)
	}
	if files := f.dataFiles(t); len(files) != 1 || !strings.HasPrefix(files[0], filepath.Join("compendiums", f.cid)) {
		t.Errorf("files: %v", files)
	}
	if rec := f.get(f.owner, a.Item.URL); rec.Code != 200 {
		t.Errorf("owner: %d", rec.Code)
	}
	if rec := f.get(f.reader, a.Item.URL); rec.Code != 404 {
		t.Errorf("a project reader who is not on the Compendium: %d, want 404", rec.Code)
	}
	if rec := f.get(f.reader, "/api/v1/compendiums/"+f.cid+"/attachments"); rec.Code != 404 {
		t.Errorf("list: %d, want 404", rec.Code)
	}
	if rec := f.uploadTo(f.reader, "/api/v1/compendiums/"+f.cid+"/attachments", "", pngBytes(t, 8, 8, 6), ""); rec.Code != 404 {
		t.Errorf("upload by a non-member: %d, want 404", rec.Code)
	}
	f.s.addCompendiumMember(t, f.cid, f.reader.userID, ops.ProjectReader)
	if rec := f.get(f.reader, a.Item.URL); rec.Code != 200 {
		t.Errorf("a member: %d", rec.Code)
	}
	if rec := f.uploadTo(f.reader, "/api/v1/compendiums/"+f.cid+"/attachments", "", pngBytes(t, 8, 8, 6), ""); rec.Code != 403 {
		t.Errorf("a reader cannot upload: %d, want 403", rec.Code)
	}
	// A project's attachment is not reachable through the Compendium's routes, or the reverse.
	pa := decodeAtt(t, f.upload(f.owner, f.pid, "", pngBytes(t, 8, 8, 7), ""))
	if rec := f.get(f.owner, "/api/v1/compendiums/"+f.cid+"/attachments/"+pa.Item.ID); rec.Code != 404 {
		t.Errorf("project attachment through the Compendium: %d", rec.Code)
	}
}

func TestDeleteIsRefcountAwareAndRemovesTheFile(t *testing.T) {
	f := newAttFixture(t, nil)
	a := decodeAtt(t, f.upload(f.owner, f.pid, "filename=fig.png", pngBytes(t, 9, 9, 8), ""))
	doc := idOf(t, f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/docs", `{"title":"Uses the figure","content":"Look: ![a figure]`+"("+a.Item.URL+`)"}`).Body.Bytes())
	var refs struct {
		Items []struct{ Kind, ID, Title string }
	}
	rec := f.get(f.owner, "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID+"/refs")
	_ = json.Unmarshal(rec.Body.Bytes(), &refs)
	if rec.Code != 200 || len(refs.Items) != 1 || refs.Items[0].ID != doc || refs.Items[0].Kind != "doc" || refs.Items[0].Title != "Uses the figure" {
		t.Fatalf("refs: %d %s", rec.Code, rec.Body)
	}
	if rec := f.s.do(f.owner, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID, ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"refs":1`) {
		t.Errorf("a referenced attachment: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 1 {
		t.Fatalf("%d files", n)
	}
	// Saving the document without the link releases it.
	save := func(content string, rev int) {
		body, _ := json.Marshal(map[string]any{"base_revision": rev, "content": content})
		if rec := f.s.do(f.owner, "PATCH", "/api/v1/docs/"+doc, string(body)); rec.Code != 200 {
			t.Fatalf("save: %d %s", rec.Code, rec.Body)
		}
	}
	save("No figure now.", 1)
	rec = f.get(f.owner, "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID+"/refs")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("refs after removing the link: %s", rec.Body)
	}
	// A link to an attachment of another project records nothing and reveals nothing.
	save("![x]("+a.Item.URL+") and ![y](/files/att_00000000000000000000000000)", 2)
	other := idOf(t, f.s.do(f.owner, "POST", "/api/v1/projects/"+f.other+"/docs", `{"title":"Elsewhere","content":"![x](`+a.Item.URL+`)"}`).Body.Bytes())
	_ = other
	rec = f.get(f.owner, "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID+"/refs")
	var r2 struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &r2)
	if len(r2.Items) != 1 || r2.Items[0].ID != doc {
		t.Errorf("refs: %s", rec.Body)
	}
	rec = f.s.do(f.owner, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID+"?force=true", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("forced delete: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 0 {
		t.Errorf("the file was not removed: %v", f.dataFiles(t))
	}
	// A reader cannot delete.
	b := decodeAtt(t, f.upload(f.owner, f.pid, "", pngBytes(t, 9, 9, 9), ""))
	if rec := f.s.do(f.reader, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+b.Item.ID, ""); rec.Code != 403 {
		t.Errorf("reader delete: %d", rec.Code)
	}
	// An unreferenced one goes without force, file and all.
	if rec := f.s.do(f.editor, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+b.Item.ID, ""); rec.Code != 200 {
		t.Errorf("delete: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 0 {
		t.Errorf("files left: %v", f.dataFiles(t))
	}
}

func TestPurgeRemovesAttachmentFiles(t *testing.T) {
	f := newAttFixture(t, nil)
	a := decodeAtt(t, f.upload(f.owner, f.other, "", pngBytes(t, 9, 9, 11), ""))
	c := decodeAtt(t, f.uploadTo(f.owner, "/api/v1/compendiums/"+f.cid+"/attachments", "", pngBytes(t, 9, 9, 12), ""))
	_ = a
	_ = c
	if n := len(f.dataFiles(t)); n != 2 {
		t.Fatalf("%d files", n)
	}
	if rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.other+"/trash", ""); rec.Code != 200 {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	if rec := f.s.do(f.owner, "DELETE", "/api/v1/projects/"+f.other, `{"confirm_title":"Elsewhere"}`); rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 1 {
		t.Errorf("the project's files remain: %v", f.dataFiles(t))
	}
	var rows, used int
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM attachments WHERE project_id = ?`, f.other).Scan(&rows)
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM file_usage WHERE scope = 'owner:' || ?`, f.other).Scan(&used)
	if rows != 0 || used != 0 {
		t.Errorf("rows %d, counters %d after a purge", rows, used)
	}
	var instance int64
	_ = f.s.DB.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = 'instance'`).Scan(&instance)
	if instance != c.Item.Bytes {
		t.Errorf("the instance counter is %d, want %d: the purge's cascade must be counted", instance, c.Item.Bytes)
	}
	if rec := f.s.do(f.owner, "POST", "/api/v1/compendiums/"+f.cid+"/trash", ""); rec.Code != 200 {
		t.Fatalf("trash compendium: %d %s", rec.Code, rec.Body)
	}
	if rec := f.s.do(f.owner, "DELETE", "/api/v1/compendiums/"+f.cid, `{"confirm_name":"Attached Bible"}`); rec.Code >= 300 {
		t.Fatalf("purge compendium: %d %s", rec.Code, rec.Body)
	}
	if n := len(f.dataFiles(t)); n != 0 {
		t.Errorf("the Compendium's files remain: %v", f.dataFiles(t))
	}
}

func TestUploadAuthorizationAndPreviews(t *testing.T) {
	f := newAttFixture(t, nil)
	img := pngBytes(t, 8, 8, 13)
	for _, tc := range []struct {
		who  caller
		want int
	}{{f.stranger, 404}, {f.reader, 403}, {f.editor, 201}, {caller{name: "anonymous"}, 401}} {
		if rec := f.upload(tc.who, f.pid, "", img, ""); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d: %s", tc.who.name, rec.Code, tc.want, rec.Body)
		}
	}
	// A refused caller's body is not read: the rejection comes first.
	rd := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{'x'}, 1<<20))}
	req := httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments", rd)
	req.AddCookie(f.stranger.cookie)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 || rd.n != 0 {
		t.Errorf("a stranger's upload: %d, %d bytes read", rec.Code, rd.n)
	}
	// A key: a read-scope key cannot upload; a write key pinned to the project can.
	readKey, _ := f.s.mintKey(t, f.owner, `{"name":"r","scope":"read"}`)
	writeKey, _ := f.s.mintKey(t, f.owner, `{"name":"w","scope":"write","project_id":"`+f.pid+`"}`)
	otherKey, _ := f.s.mintKey(t, f.owner, `{"name":"o","scope":"write","project_id":"`+f.other+`"}`)
	post := func(key, owner string, body []byte) int {
		req := httptest.NewRequest("POST", "/api/v1/projects/"+owner+"/attachments?filename=k.png", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post(readKey, f.pid, pngBytes(t, 8, 8, 14)); c != 403 {
		t.Errorf("read key: %d", c)
	}
	if c := post(writeKey, f.pid, pngBytes(t, 8, 8, 15)); c != 201 {
		t.Errorf("write key: %d", c)
	}
	if c := post(otherKey, f.pid, pngBytes(t, 8, 8, 16)); c != 404 {
		t.Errorf("key pinned elsewhere: %d", c)
	}
	// A preview is refused before the body is read, and writes nothing.
	before := len(f.dataFiles(t))
	rd = &countingReader{r: bytes.NewReader(img)}
	req = httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments", rd)
	req.Header.Set("Prefer", "dry-run")
	req.AddCookie(f.owner.cookie)
	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "dry_run_unsupported") || rd.n != 0 {
		t.Errorf("dry run: %d %s (read %d bytes)", rec.Code, rec.Body, rd.n)
	}
	if after := len(f.dataFiles(t)); after != before {
		t.Errorf("a preview left files behind: %d -> %d", before, after)
	}
	// An Idempotency-Key does not make the raw route replay or hide anything.
	req = httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/attachments", bytes.NewReader(pngBytes(t, 8, 8, 17)))
	req.Header.Set("Idempotency-Key", "abc")
	req.AddCookie(f.owner.cookie)
	rec = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Errorf("with an idempotency key: %d %s", rec.Code, rec.Body)
	}
	// Update changes the name and the alt text, never the bytes.
	a := decodeAtt(t, f.upload(f.owner, f.pid, "filename=a.png", pngBytes(t, 8, 8, 18), ""))
	rec = f.s.do(f.reader, "PATCH", "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID, `{"alt":"x"}`)
	if rec.Code != 403 {
		t.Errorf("reader update: %d", rec.Code)
	}
	rec = f.s.do(f.editor, "PATCH", "/api/v1/projects/"+f.pid+"/attachments/"+a.Item.ID, `{"alt":"A harbour","filename":"../harbour"}`)
	u := decodeAtt(t, rec)
	if rec.Code != 200 || u.Item.Alt != "A harbour" || u.Item.Filename != "harbour.png" || u.Item.SHA256 != a.Item.SHA256 {
		t.Errorf("update: %d %+v", rec.Code, u)
	}
}

type countingReader struct {
	r *bytes.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// Concurrent uploads of the same bytes make one attachment and one file;
// uploads racing a delete of the same bytes never leave a row without its file.
func TestConcurrentUploadsAndDeletes(t *testing.T) {
	f := newAttFixture(t, nil)
	img := pngBytes(t, 20, 20, 19)
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := f.upload(f.owner, f.pid, "filename=same.png", img, ""); rec.Code == 201 {
				ids <- decodeAtt(t, rec).Item.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d attachments from one file: %v", len(seen), seen)
	}
	var id string
	for k := range seen {
		id = k
	}
	// Delete and re-upload in a loop, from several goroutines: at the end, a row
	// exists exactly when its file does.
	for round := 0; round < 12; round++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			f.s.do(f.owner, "DELETE", "/api/v1/projects/"+f.pid+"/attachments/"+id, "")
		}()
		go func() {
			defer wg.Done()
			f.upload(f.owner, f.pid, "filename=same.png", img, "")
		}()
		wg.Wait()
		var n int
		var cur string
		_ = f.s.DB.Read().QueryRow(`SELECT count(*), COALESCE(max(id), '') FROM attachments WHERE project_id = ?`, f.pid).Scan(&n, &cur)
		files := len(f.dataFiles(t))
		if n != files {
			t.Fatalf("round %d: %d rows, %d files", round, n, files)
		}
		if cur != "" {
			id = cur
		} else if rec := f.upload(f.owner, f.pid, "filename=same.png", img, ""); rec.Code == 201 {
			id = decodeAtt(t, rec).Item.ID
		}
	}
}
