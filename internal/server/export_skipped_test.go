package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
)

// The Export dialog leaves out documents marked do not compile, and says how
// many in a header (the screen can show it).
func TestExportDialogSaysWhatItLeftOut(t *testing.T) {
	e := newWriteEnv(t)
	no := false
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: 1, Title: sp("Chapter one"), Content: sp("It was a dark night, nightmark.\n"), IncludeInCompile: &no})
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", url.Values{"mode": {"single"}}, "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "nightmark") {
		t.Fatalf("export: %d, the excluded chapter is in the file", rec.Code)
	}
	if rec.Header().Get("X-Export-Skipped") != "1" {
		t.Errorf("X-Export-Skipped = %q", rec.Header().Get("X-Export-Skipped"))
	}
}
