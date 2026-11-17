package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

type fakeDownload struct{ name, typ string }

func (f fakeDownload) Download() (string, string, []byte) { return f.name, f.typ, []byte("body") }

func TestDownloadHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDownload(rec, fakeDownload{name: "../../x\"y\r\nSet-Cookie: pwned=1.md", typ: "text/markdown; charset=utf-8"})
	h := rec.Header()
	cd := h.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment; filename=") || !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition %q", cd)
	}
	if strings.ContainsAny(cd, "\r\n/\\") || strings.Contains(cd, "..") {
		t.Errorf("unsafe Content-Disposition %q", cd)
	}
	if h.Get("Set-Cookie") != "" {
		t.Error("header injection through the file name")
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Errorf("headers: %v", h)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "body" || h.Get("Content-Length") != "4" {
		t.Errorf("%d %q %s", rec.Code, rec.Body.String(), h.Get("Content-Length"))
	}
}

// The export op over the real handler chain: the file arrives as an
// attachment, not JSON, and the access rules hold.
func TestExportOverREST(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: `He said "hi"; ../x`})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: "Body of one."}); err != nil {
		t.Fatal(err)
	}
	s.addMember(t, pid, reader.userID, ops.ProjectReader)

	rec := s.do(owner, "POST", "/api/v1/projects/"+pid+"/export", "{}")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="he-said-hi-x.md"; filename*=UTF-8''he-said-hi-x.md` {
		t.Errorf("Content-Disposition %q", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "## One\n\nBody of one.\n") || !strings.HasPrefix(rec.Body.String(), "# He said") {
		t.Errorf("body %q", rec.Body)
	}

	rec = s.do(owner, "POST", "/api/v1/projects/"+pid+"/export", `{"mode":"bundle","notes":true}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || !strings.HasSuffix(rec.Header().Get("Content-Disposition"), "he-said-hi-x.zip") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); err != nil {
		t.Errorf("not a zip: %v", err)
	}

	if rec := s.do(reader, "POST", "/api/v1/projects/"+pid+"/export", "{}"); rec.Code != http.StatusForbidden {
		t.Errorf("a reader: %d, want 403", rec.Code)
	}
	if rec := s.do(outsider, "POST", "/api/v1/projects/"+pid+"/export", "{}"); rec.Code != http.StatusNotFound {
		t.Errorf("an outsider: %d, want 404", rec.Code)
	}
	if rec := s.do(caller{name: "anon"}, "POST", "/api/v1/projects/"+pid+"/export", "{}"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", rec.Code)
	}
	if rec := s.do(owner, "POST", "/api/v1/projects/"+pid+"/export", `{"mode":"pdf"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("a bad mode: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSearchOverREST(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Searchable"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Harbour", Content: "The lighthouse keeper."}); err != nil {
		t.Fatal(err)
	}
	rec := s.do(owner, "GET", "/api/v1/projects/"+pid+"/search?q=lighth&limit=5", "")
	var got struct {
		Total int
		Items []struct {
			SnippetHTML string `json:"snippet_html"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 || got.Total != 1 || len(got.Items) != 1 || !strings.Contains(got.Items[0].SnippetHTML, "<mark>lighthouse</mark>") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "GET", "/api/v1/projects/"+pid+"/search?q=%22%20*%20%28%29", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("punctuation only: %d", rec.Code)
	}
	if rec := s.do(owner, "GET", "/api/v1/projects/"+pid+"/search?q=x&limit=abc", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a non-number limit: %d", rec.Code)
	}
	if rec := s.do(outsider, "GET", "/api/v1/projects/"+pid+"/search?q=lighthouse", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an outsider: %d", rec.Code)
	}
}
