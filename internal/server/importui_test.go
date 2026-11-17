package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The import pages: who may open them, and what they say.
func TestImportPages(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	reader := s.newCaller(t, "reader", ops.RoleReader)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	viewer := s.newCaller(t, "viewer", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	s.addMember(t, e.pid, viewer.userID, ops.ProjectReader)

	rec := s.do(e.owner, "GET", "/import", "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `webkitdirectory`) || !strings.Contains(body, `id="imp-zip"`) || !strings.Contains(body, "js/import.") {
		t.Errorf("import page: %d", rec.Code)
	}
	if strings.Contains(body, "data-into") {
		t.Error("a new-project import names a target project")
	}
	if rec := s.do(reader, "GET", "/import", ""); rec.Code != 403 {
		t.Errorf("import page for an instance reader: %d, want 403", rec.Code)
	}
	if rec := s.do(caller{name: "anon"}, "GET", "/import", ""); rec.Code != 303 {
		t.Errorf("import page signed out: %d", rec.Code)
	}

	into := "/p/" + e.slug + "/import"
	if rec := s.do(e.owner, "GET", into, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `data-into="`+e.pid+`"`) {
		t.Errorf("import into, owner: %d", rec.Code)
	}
	if rec := s.do(editor, "GET", into, ""); rec.Code != 200 {
		t.Errorf("import into, editor: %d", rec.Code)
	}
	if rec := s.do(viewer, "GET", into, ""); rec.Code != 403 {
		t.Errorf("import into, project reader: %d, want 403", rec.Code)
	}
	if rec := s.do(stranger, "GET", into, ""); rec.Code != 404 {
		t.Errorf("import into, stranger: %d, want 404 (the project must not leak)", rec.Code)
	}

	// The project menu offers it to people who can edit, and the library to people who can create.
	if m := s.do(editor, "GET", "/ui/projects/"+e.pid+"/menu", "").Body.String(); !strings.Contains(m, into) {
		t.Error("the project menu does not offer adding files to an editor")
	}
	if m := s.do(viewer, "GET", "/ui/projects/"+e.pid+"/menu", "").Body.String(); strings.Contains(m, into) || strings.Contains(m, "Rebuild the copy") {
		t.Error("the project menu offers editing actions to a reader")
	}
	if lib := s.do(e.owner, "GET", "/", "").Body.String(); !strings.Contains(lib, `href="/import"`) {
		t.Error("the library has no Import link")
	}
}

func TestMirrorLineAndRebuild(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	viewer := s.newCaller(t, "viewer", ops.RoleWriter)
	s.addMember(t, e.pid, viewer.userID, ops.ProjectReader)

	menu := s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/menu", "").Body.String()
	if !strings.Contains(menu, `class="mirror-line"`) || !strings.Contains(menu, "Rebuild the copy") {
		t.Errorf("the owner's menu has no mirror line or no Rebuild")
	}
	// A reader sees the state, not the button, and cannot post the rebuild.
	if m := s.do(viewer, "GET", "/ui/projects/"+e.pid+"/menu", "").Body.String(); !strings.Contains(m, "Markdown copy") || strings.Contains(m, "Rebuild the copy") {
		t.Errorf("the reader's menu: mirror line missing or Rebuild offered")
	}
	if rec := s.post(t, viewer, "/ui/projects/"+e.pid+"/mirror/rebuild", url.Values{}, "", false); rec.Code != 403 {
		t.Errorf("rebuild by a reader: %d, want 403", rec.Code)
	}
	if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/mirror/rebuild", url.Values{}, "-", false); rec.Code != 403 {
		t.Errorf("rebuild without a token: %d, want 403", rec.Code)
	}
	rec := s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/mirror/rebuild", url.Values{"view": {"active"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "The Markdown copy is up to date.") {
		t.Errorf("rebuild by the owner: %d %.300s", rec.Code, rec.Body)
	}
}

func TestMirrorSummaryWords(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Dublin")
	cases := []struct {
		name   string
		st     mirror.Status
		edit   bool
		state  string
		line   string
		detail string
		button bool
	}{
		{"off", mirror.Status{Enabled: false}, true, "off", "switched off", "", false},
		{"fine", mirror.Status{Enabled: true, InSync: true, Files: 14, LastWriteAt: "2026-07-01T10:30:00Z"}, true, "ok", "up to date", "14 files, last changed 1 Jul 2026, 11:30.", true},
		{"one file", mirror.Status{Enabled: true, InSync: true, Files: 1}, false, "ok", "up to date", "1 file.", false},
		{"waiting", mirror.Status{Enabled: true, Pending: true, Files: 3}, true, "pending", "being brought up to date", "3 files so far.", true},
		{"failing", mirror.Status{Enabled: true, LastError: "no space left on device", NextRetryAt: "2026-07-01T10:35:00Z"}, true, "error", "could not be written", "no space left on device. Trying again at 1 Jul 2026, 11:35.", true},
		{"never written", mirror.Status{Enabled: true}, true, "none", "has not been written yet", "It is written a few seconds after a change.", true},
	}
	for _, c := range cases {
		v := mirrorSummary(c.st, loc, c.edit)
		if v.State != c.state || !strings.Contains(v.Line, c.line) || v.Detail != c.detail || v.CanRebuild != c.button {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

func TestBackupsSectionIsForAdministrators(t *testing.T) {
	s := newAuthzServer(t)
	writer := s.newCaller(t, "writer", ops.RoleWriter)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: writer.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Anything"}); err != nil {
		t.Fatal(err)
	}

	if b := s.do(writer, "GET", "/settings", "").Body.String(); strings.Contains(b, `id="backups"`) || strings.Contains(b, "scriobh restore") {
		t.Error("a writer sees the Backups section")
	}
	b := s.do(admin, "GET", "/settings", "").Body.String()
	for _, want := range []string{`id="backups"`, "Back up now", "scriobh restore", "secret.key", "There are no backups yet."} {
		if !strings.Contains(b, want) {
			t.Errorf("the administrator's settings lack %q", want)
		}
	}
	if rec := s.post(t, writer, "/ui/settings/backups", url.Values{}, "", false); rec.Code != 403 {
		t.Errorf("a writer took a backup: %d, want 403", rec.Code)
	}
	if rec := s.post(t, admin, "/ui/settings/backups", url.Values{}, "-", false); rec.Code != 403 {
		t.Errorf("backup without a token: %d, want 403", rec.Code)
	}
	if rec := s.post(t, admin, "/ui/settings/backups", url.Values{}, "", false); rec.Code != 303 {
		t.Fatalf("backup by an administrator: %d %s", rec.Code, rec.Body)
	}
	b = s.do(admin, "GET", "/settings?saved=backup", "").Body.String()
	if !strings.Contains(b, "Manual backup") || !strings.Contains(b, "/api/v1/admin/backups/") || !strings.Contains(b, "Backup taken.") {
		t.Errorf("the new backup is not listed with a download link")
	}
	// The download link is the REST route, which only an administrator can use.
	if rec := s.do(writer, "GET", "/api/v1/admin/backups", ""); rec.Code != 403 {
		t.Errorf("backup list for a writer: %d", rec.Code)
	}
}
