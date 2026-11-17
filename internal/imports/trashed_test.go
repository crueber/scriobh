package imports_test

import (
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// A project in the trash is read-only, so adding files to it is refused (409, data.reason "project_trashed") before
// anything is read. An import that was already running when the owner threw the project away is refused too, by the
// transaction that would have written into it: the registry's check is made once, at the start of the call, so the
// test makes it see a live project, as it would have a moment earlier, and trashes the project underneath.
func TestAppendIntoATrashedProjectIsRefused(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Existing"}).(projects.Item).Item.ID
	count := func() int {
		return len(e.must(e.w, "outline.get", docs.OutlineIn{ProjectID: pid}).(docs.OutlineOut).Items)
	}
	before := count()
	f := files(map[string]string{"manuscript/01-a.md": "A scene.\n"})
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f}).(imports.PreviewOut)
	e.must(e.w, "project.trash", projects.StateIn{ID: pid})

	refused := func(what string, err error) {
		t.Helper()
		if oe := ops.AsError(err); err == nil || oe.Code != ops.CodeConflict || oe.Data["reason"] != ops.ReasonProjectTrashed {
			t.Errorf("%s: %v, want a 409 naming the trash", what, err)
		}
	}
	_, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Files: f})
	refused("through the registry", err)
	_, err = e.call(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Files: f})
	refused("the preview", err)

	// In flight: the registry still believes the project is live.
	real := e.reg.Env().ProjectTrashed
	e.reg.Env().ProjectTrashed = func(_ context.Context, _ string) bool { return false }
	t.Cleanup(func() { e.reg.Env().ProjectTrashed = real })
	_, err = e.call(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Files: f})
	refused("in flight", err)
	e.reg.Env().ProjectTrashed = real

	e.must(e.w, "project.restore", projects.StateIn{ID: pid})
	if n := count(); n != before {
		t.Errorf("a refused import left %d documents behind", n-before)
	}
	e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Files: f})
	if n := count(); n <= before {
		t.Errorf("after the restore the import should add its documents: %d new", n-before)
	}
}
