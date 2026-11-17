package mirror_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
)

// An AI event names a project, so the mirror looks at the project; it must find
// nothing to write (and so Git sync nothing to push). The files are not touched:
// the same bytes at the same modification times.
func TestAIEventsDoNotRewriteTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("AI quiet")
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
	mode := "ask"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: pid, Mode: &mode})
	e.settleAll()
	if e.m.Cursor() == cursor {
		t.Fatal("the AI event did not reach the mirror's cursor; this test no longer proves anything")
	}
	after := snapshot()
	if len(before) == 0 || len(after) != len(before) {
		t.Fatalf("%d files before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s was rewritten by an AI event", p)
		}
	}
}
