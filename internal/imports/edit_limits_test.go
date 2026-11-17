package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

func TestEditAndReorderListsAreBounded(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{"a.md": "A.\n"})
	edits := make([]imports.Edit, imports.MaxEdits+1)
	for i := range edits {
		edits[i] = imports.Edit{Key: "a.md"}
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Files: f, Edits: edits}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("too many edits: %v", err)
	}
	keys := make([]string, imports.MaxReorderKeys+1)
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Files: f, Reorder: []imports.Reorder{{Keys: keys}}}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("too many reorder keys: %v", err)
	}
	pv := e.preview(f, "")
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, Edits: edits}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("apply with too many edits: %v", err)
	}
}
