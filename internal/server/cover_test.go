package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func TestProjectCoverRoute(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create",
		projects.CreateIn{Title: `<script>alert(1)</script> & "Tide"`, Subtitle: "A <b>sub</b>", AuthorName: "Máire", Kind: "novel", CoverColor: "#7A4B2A"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	path := "/api/v1/projects/" + pid + "/cover"

	rec := s.do(owner, "GET", path, "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline;") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", h)
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
		t.Errorf("the image has no policy of its own: %q", csp)
	}
	if h.Get("Cache-Control") != "private, no-cache" || h.Get("ETag") == "" {
		t.Errorf("caching: %q %q", h.Get("Cache-Control"), h.Get("ETag"))
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "<svg ") || !strings.Contains(body, `fill="#7A4B2A"`) || strings.Contains(body, "<script") || strings.Contains(body, "<b>") {
		t.Errorf("the cover:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the hostile title was not escaped:\n%s", body)
	}

	// A matching ETag is a 304 with no body; a changed title is a new ETag.
	etag := h.Get("ETag")
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(owner.cookie)
	req.Header.Set("If-None-Match", etag)
	r2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(r2, req)
	if r2.Code != http.StatusNotModified || r2.Body.Len() != 0 || r2.Header().Get("ETag") != etag {
		t.Errorf("revalidation: %d, %d body bytes", r2.Code, r2.Body.Len())
	}
	if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.update",
		projects.UpdateIn{ID: pid, Title: ptr("A new title")}); err != nil {
		t.Fatal(err)
	}
	r3 := s.do(owner, "GET", path, "")
	if r3.Header().Get("ETag") == etag || !strings.Contains(r3.Body.String(), "A new title") {
		t.Error("the cover did not follow the title")
	}
	// A reader may see it; an outsider and a stranger get the usual answers.
	if r := s.do(reader, "GET", path, ""); r.Code != 200 {
		t.Errorf("reader: %d", r.Code)
	}
	if r := s.do(outsider, "GET", path, ""); r.Code != 404 {
		t.Errorf("outsider: %d", r.Code)
	}
	if r := s.do(caller{name: "anon"}, "GET", path, ""); r.Code != 401 {
		t.Errorf("anonymous: %d", r.Code)
	}
}

// The export dialog posts format=html to the same UI route; the file that comes
// back is the HTML export.
func TestExportDialogHTML(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Dialog"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	csrf := s.Sess.CSRFToken(sessionOf(t, s, owner))
	post := func(vals url.Values) *httptest.ResponseRecorder {
		vals.Set("_csrf", csrf)
		req := httptest.NewRequest("POST", "/ui/projects/"+pid+"/export", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(owner.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	rec := post(url.Values{"format": {"html"}, "mode": {"single"}})
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Header().Get("Content-Disposition"), "dialog.html") ||
		!strings.Contains(rec.Body.String(), "Content-Security-Policy") {
		t.Fatalf("html export: %d %v", rec.Code, rec.Header())
	}
	if rec := post(url.Values{"format": {"markdown"}, "mode": {"single"}}); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") {
		t.Errorf("markdown export: %v", rec.Header())
	}
	if rec := post(url.Values{"mode": {"bundle"}}); rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("default is Markdown: %v", rec.Header())
	}
	// The dialog shows the choice.
	form := s.do(owner, "GET", "/ui/projects/"+pid+"/export", "")
	if form.Code != 200 || !strings.Contains(form.Body.String(), `name="format"`) || !strings.Contains(form.Body.String(), `name="private_notes"`) {
		t.Errorf("the dialog: %d", form.Code)
	}
}

func sessionOf(t *testing.T, s *Server, c caller) string {
	t.Helper()
	_, info, err := s.Sess.Lookup(context.Background(), c.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}
