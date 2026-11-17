package server

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// A finished export is for the people who can run one: an editor hears it (ids
// and counts), a reader on the same stream hears the text change that follows
// and nothing of the export before it.
func TestExportEventsReachEditorsAndNotReaders(t *testing.T) {
	f := newSSEFixture(t, nil)
	editor := f.s.newCaller(t, "ed", ops.RoleReader)
	f.s.addMember(t, f.pid, editor.userID, ops.ProjectEditor)
	f.save(t, "Some text to export.", 1)
	ec, code := openStream(t, f.ts.URL, editor, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	rd, code := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	ec.nextEvent() // hello
	rd.nextEvent()
	if rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/export", `{"mode":"single"}`); rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	e := ec.nextEvent()
	if e.Event != "export.completed" || !strings.Contains(e.Data, `"export_id"`) || strings.Contains(e.Data, "Some text") || strings.Contains(e.Data, ".md") {
		t.Fatalf("what an editor hears: %+v", e)
	}
	f.save(t, "Some text to export, changed.", 2)
	time.Sleep(20 * time.Millisecond)
	if got := rd.nextEvent(); got.Event != "doc.updated" {
		t.Errorf("a reader heard %q (%s) where the first thing should be the text change", got.Event, got.Data)
	}
}
