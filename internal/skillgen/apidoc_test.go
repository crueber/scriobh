package skillgen

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

func TestSpliceAPIDoc(t *testing.T) {
	s := ops.Schema{Hash: "sha256:0123456789abcdef", Ops: []ops.OpSchema{
		{Name: "doc.get", Method: "GET", Path: "/api/v1/docs/{id}", Desc: "Get one document. More words.", Auth: ops.RequireProject(ops.ProjectReader), Scope: "read", Status: 200},
		{Name: "key.create", Method: "POST", Path: "/api/v1/keys", Desc: "Make a key | with a pipe.", Auth: ops.Writer(), Scope: "admin", Status: 201, NoDryRun: true},
	}}
	doc := "# API\n\nprose before\n\n" + APIBegin + "\nold\n" + APIEnd + "\n\nprose after\n"
	out, err := SpliceAPIDoc([]byte(doc), s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"prose before", "prose after", "| `doc.get` | `GET /api/v1/docs/{id}` | project reader | read |  | Get one document. More words. |", "no dry run, 201 on success", "with a pipe", "\\|"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nold\n") {
		t.Error("the old table was kept")
	}
	again, err := SpliceAPIDoc(out, s)
	if err != nil || string(again) != got {
		t.Errorf("splicing twice changed the file (err %v)", err)
	}
}

func TestSpliceAPIDocNeedsBothMarkersOnce(t *testing.T) {
	s := ops.Schema{Hash: "sha256:0123456789abcdef"}
	for name, doc := range map[string]string{
		"none":    "no markers",
		"no end":  APIBegin + "\n",
		"twice":   APIBegin + APIEnd + APIBegin + APIEnd,
		"reverse": APIEnd + APIBegin,
	} {
		if _, err := SpliceAPIDoc([]byte(doc), s); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
