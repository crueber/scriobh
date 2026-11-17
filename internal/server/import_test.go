package server

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The import ops declare their own body limit; every other route keeps the
// default, and the caller is authorized before the body is read.
func TestImportOpsHaveTheirOwnBodyLimit(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "w", ops.RoleWriter)

	// 3 MiB is over the default 2 MiB but inside the import limit: not a 413.
	big := `{"files":[{"path":"a.md","content":"` + strings.Repeat("a", 3<<20) + `"},{"path":"b.md","content":"Hello there."}]}`
	rec := s.do(w, "POST", "/api/v1/imports/preview", big)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"skipped_total":1`) {
		t.Fatalf("a 3 MiB import preview: %d %.200s", rec.Code, rec.Body)
	}
	// A real zip upload arrives base64-encoded in JSON.
	z := base64.StdEncoding.EncodeToString(zipBytes(t))
	rec = s.do(w, "POST", "/api/v1/imports/preview", `{"zip":"`+z+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"documents":1`) {
		t.Errorf("a zip preview: %d %.300s", rec.Code, rec.Body)
	}
	// Past the import limit it is a 413.
	over := `{"name":"` + strings.Repeat("a", imports.MaxBody+1) + `"}`
	if rec := s.do(w, "POST", "/api/v1/imports/preview", over); rec.Code != 413 {
		t.Errorf("over the import limit: %d", rec.Code)
	}
	// Other routes keep the 2 MiB default.
	if rec := s.do(w, "POST", "/api/v1/projects", `{"title":"`+strings.Repeat("a", 3<<20)+`"}`); rec.Code != 413 {
		t.Errorf("project.create with 3 MiB: %d, want 413", rec.Code)
	}
	// An anonymous caller is refused without the body being needed.
	if rec := s.do(caller{name: "anonymous"}, "POST", "/api/v1/imports/preview", big); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// An instance reader may not import.
	r := s.newCaller(t, "r", ops.RoleReader)
	if rec := s.do(r, "POST", "/api/v1/imports/preview", big); rec.Code != 403 {
		t.Errorf("reader: %d", rec.Code)
	}
}

func zipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("book/01-one.md")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("One.\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
