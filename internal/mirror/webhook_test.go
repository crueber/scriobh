package mirror_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/export"
)

// Webhooks hold where a project's notifications go and what became of them: not a
// word of the manuscript, so none of their ops is something the projection shows.
// A new webhook op fails here until someone has said which it is.
func TestEveryWebhookOpIsClassifiedForTheMirror(t *testing.T) {
	e := newEnv(t)
	exempt := map[string]string{
		"webhook.create": "outbound configuration, not content", "webhook.update": "outbound configuration, not content",
		"webhook.delete": "outbound configuration, not content", "webhook.rotate_secret": "a sealed secret, never in the projection",
		"webhook.test": "a request to the receiver", "webhook.redeliver": "the outbox, not content",
	}
	n := 0
	for _, en := range e.reg.Entries() {
		name := en.Op.Name
		if !strings.HasPrefix(name, "webhook.") || en.Op.Method == "GET" {
			continue
		}
		n++
		if exempt[name] == "" {
			t.Errorf("%s changes data but is not classified for the mirror", name)
		}
	}
	if n != len(exempt) {
		t.Errorf("%d webhook ops change data, the table has %d", n, len(exempt))
	}
}

// A finished export is an event that names a project; the mirror must leave the
// project alone, and with it the Git push that follows the mirror.
func TestExportEventsDoNotRewriteTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Export quiet")
	e.doc(pid, "Chapter", "chapter", "Some text.\n")
	e.settleAll()
	snapshot := func() map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(filepath.Join(e.dir, "mirror"), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				fi, _ := d.Info()
				b, _ := os.ReadFile(p)
				out[p] = fi.ModTime().Format(time.RFC3339Nano) + string(b)
			}
			return nil
		})
		return out
	}
	before := snapshot()
	cursor := e.m.Cursor()
	e.call(e.owner, "export.markdown", export.MarkdownIn{ProjectID: pid, Mode: "single"})
	e.settleAll()
	if e.m.Cursor() == cursor {
		t.Fatal("the export event did not reach the mirror's cursor; this test no longer proves anything")
	}
	if st := e.m.Status(pid); st.Pending {
		t.Error("an export marked the project pending")
	}
	after := snapshot()
	if len(before) == 0 || len(after) != len(before) {
		t.Fatalf("%d files before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s was rewritten by an export", p)
		}
	}
}
