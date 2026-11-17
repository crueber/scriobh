package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/registry"
)

// liveSchema is the registry's schema as the server would publish it, so these
// tests do not depend on the committed snapshot being regenerated.
func liveSchema(t testing.TB) cli.Schema {
	t.Helper()
	b, err := json.Marshal(registry.Build(nil, nil, "").Schema())
	if err != nil {
		t.Fatal(err)
	}
	s, err := cli.ParseSchema(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSchemaToCommandMapping pins how ops become commands: the words, the
// positionals, the flags and the usage line, for a few representative ops.
func TestSchemaToCommandMapping(t *testing.T) {
	ix := cli.NewIndex(liveSchema(t))
	golden := map[string]struct{ command, usage, example string }{
		"doc.update": {"doc update",
			"scriobh-cli doc update <doc> --base-revision N [flags]",
			"scriobh-cli doc update \"The Salt Road/Chapter 1\" --base-revision 1"},
		"doc.resolve_link": {"doc resolve-link",
			"scriobh-cli doc resolve-link <project> --title TEXT [flags]",
			"scriobh-cli doc resolve-link \"The Salt Road\" --title \"Chapter 1\""},
		"project.create": {"project create",
			"scriobh-cli project create --title TEXT [flags]",
			"scriobh-cli project create --title \"Chapter 1\""},
		"key.revoke": {"key revoke",
			"scriobh-cli key revoke <key>",
			"scriobh-cli key revoke \"agent one\" --yes"},
		"export.markdown": {"export markdown",
			"scriobh-cli export markdown <project> [flags]",
			"scriobh-cli export markdown \"The Salt Road\" --output result.out"},
	}
	for name, want := range golden {
		o, ok := ix.ByName(name)
		if !ok {
			t.Errorf("%s is not in the schema", name)
			continue
		}
		if o.Command() != want.command {
			t.Errorf("%s command = %q, want %q", name, o.Command(), want.command)
		}
		if got := cli.UsageLine(o); got != want.usage {
			t.Errorf("%s usage = %q, want %q", name, got, want.usage)
		}
		got := cli.Example(o)
		if got != want.example {
			t.Errorf("%s example = %q, want %q", name, got, want.example)
		}
	}
	// Flags come from the input fields, hyphenated, with the description as help.
	o, _ := ix.ByName("doc.update")
	var flags []string
	for _, f := range o.Fields() {
		flags = append(flags, f.Loc+":"+f.Flag())
	}
	for _, w := range []string{"path:id", "body:base-revision", "body:content", "body:story-date", "body:include-in-compile"} {
		if !contains(flags, w) {
			t.Errorf("doc update lacks flag %s: %v", w, flags)
		}
	}
	for _, f := range o.Fields() {
		if f.Name == "base_revision" && (!f.Required || f.Desc == "" || f.Type != "integer") {
			t.Errorf("base_revision field = %+v", f)
		}
		if f.Name == "id" && f.Ref != "doc" {
			t.Errorf("the id of doc.update refers to a %q, want doc", f.Ref)
		}
	}
	// Every op is reachable by exactly one command.
	seen := map[string]string{}
	for _, o := range ix.Schema.Ops {
		if prev, dup := seen[o.Command()]; dup {
			t.Errorf("%s and %s are both %q", prev, o.Name, o.Command())
		}
		seen[o.Command()] = o.Name
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestParseFlags(t *testing.T) {
	ix := cli.NewIndex(liveSchema(t))
	parse := func(args ...string) (*cli.Invocation, error) { return ix.Parse(args) }

	inv, err := parse("--server", "http://x", "doc", "update", "The Salt Road/Ch 1", "--base-revision=3", "--title", "T", "--include-in-compile", "--json", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Op.Name != "doc.update" || inv.Values["id"][0] != "The Salt Road/Ch 1" || inv.Values["base_revision"][0] != "3" || inv.Values["include_in_compile"][0] != "true" {
		t.Errorf("values: %v", inv.Values)
	}
	if inv.Global.Server != "http://x" || !inv.Global.JSON || !inv.Global.DryRun {
		t.Errorf("globals: %+v", inv.Global)
	}
	// The kind's name is an alias for the id flag; both spellings of a field work.
	for _, a := range [][]string{{"doc", "get", "--doc", "X"}, {"doc", "get", "--id", "X"}} {
		if inv, err := parse(a...); err != nil || inv.Values["id"][0] != "X" {
			t.Errorf("%v: %v %v", a, inv, err)
		}
	}
	if inv, err := parse("doc", "update", "X", "--base_revision", "2"); err != nil || inv.Values["base_revision"][0] != "2" {
		t.Errorf("underscore spelling: %v %v", inv, err)
	}
	// A context flag with no field of that kind is a hint for name lookups.
	if inv, err := parse("doc", "get", "Epilogue", "--project", "The Salt Road"); err != nil || inv.Hints["project"] != "The Salt Road" {
		t.Errorf("hint: %v %v", inv, err)
	}
	// An op's own flag wins over a global of the same name (key list --all).
	if inv, err := parse("key", "list", "--all"); err != nil || inv.Values["all"][0] != "true" || inv.Global.All {
		t.Errorf("key list --all: %+v %v", inv, err)
	}
	// Errors are usage errors, with a hint.
	for name, args := range map[string][]string{
		"unknown command": {"docs", "nope"},
		"unknown flag":    {"doc", "get", "X", "--bogus"},
		"extra argument":  {"doc", "get", "X", "Y"},
		"missing value":   {"doc", "get", "X", "--project"},
		"empty":           {},
	} {
		_, err := parse(args...)
		if _, ok := err.(*cli.UsageError); !ok {
			t.Errorf("%s: %T %v, want a usage error", name, err, err)
		}
	}
	_, err = parse("doc", "nope")
	if ue, ok := err.(*cli.UsageError); !ok || !strings.Contains(ue.Hint, "doc get") {
		t.Errorf("a near miss should suggest commands: %v", err)
	}
	// Two dashes end the flags.
	if inv, err := parse("project", "create", "--", "--title"); err != nil || inv.Values["title"][0] != "--title" {
		t.Errorf("--: %v %v", inv, err)
	}
}

func TestBind(t *testing.T) {
	ix := cli.NewIndex(liveSchema(t))
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bind := func(stdin string, args ...string) (*cli.Request, error) {
		inv, err := ix.Parse(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return cli.Bind(inv, strings.NewReader(stdin), nil)
	}
	body := func(r *cli.Request) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatalf("body %q: %v", r.Body, err)
		}
		return m
	}

	r, err := bind("", "doc", "update", "doc_01234567890123456789012345", "--base-revision", "4", "--content", "@"+write("a.md", "From a file.\n"), "--target-words", "900", "--include-in-compile=false")
	if err != nil {
		t.Fatal(err)
	}
	b := body(r)
	if r.Method != "PATCH" || r.Path != "/api/v1/docs/doc_01234567890123456789012345" ||
		b["base_revision"].(float64) != 4 || b["content"] != "From a file.\n" || b["target_words"].(float64) != 900 || b["include_in_compile"] != false {
		t.Errorf("%s %s %v", r.Method, r.Path, b)
	}
	// Standard input, an escaped at-sign, --<field>-file, and --data underneath flags.
	if r, err := bind("typed\n", "doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--content", "-"); err != nil || body(r)["content"] != "typed\n" {
		t.Errorf("stdin: %v %v", r, err)
	}
	if r, err := bind("", "doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--title", "@@home"); err != nil || body(r)["title"] != "@home" {
		t.Errorf("@@: %v %v", r, err)
	}
	if r, err := bind("", "doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--content-file", write("b.md", "B")); err != nil || body(r)["content"] != "B" {
		t.Errorf("--content-file: %v %v", r, err)
	}
	r, err = bind("", "doc", "update", "doc_01234567890123456789012345", "--data", `{"base_revision":7,"title":"from data","status":"draft"}`, "--title", "flag wins")
	if err != nil || body(r)["title"] != "flag wins" || body(r)["status"] != "draft" || body(r)["base_revision"].(float64) != 7 {
		t.Errorf("--data: %v %v", r, err)
	}
	// Query parameters stay out of the body; no body for a GET.
	if r, err := bind("", "doc", "list", "prj_01234567890123456789012345", "--trash"); err != nil || r.Query.Get("trash") != "true" || r.Body != nil {
		t.Errorf("query: %+v %v", r, err)
	}
	// Errors: missing file, wrong type, bad enum, two stdin readers.
	for name, args := range map[string][]string{
		"missing file": {"doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--content", "@/no/such/file"},
		"bad int":      {"doc", "update", "doc_01234567890123456789012345", "--base-revision", "x"},
		"bad enum":     {"doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--status", "nearly"},
		"missing flag": {"doc", "update", "doc_01234567890123456789012345"},
	} {
		if _, err := bind("", args...); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if _, ok := err.(*cli.UsageError); !ok {
			t.Errorf("%s: %T, want a usage error", name, err)
		}
	}
	if _, err := bind("x", "doc", "update", "doc_01234567890123456789012345", "--base-revision", "1", "--content", "-", "--title", "-"); err == nil {
		t.Error("standard input can only feed one value")
	}
	// Uploads: a folder becomes the file list, hidden files are left out.
	root := filepath.Join(dir, "book")
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "01.md"), []byte("One"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "sub", "02.md"), []byte("Two"), 0o600)
	_ = os.WriteFile(filepath.Join(root, ".hidden"), []byte("no"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0xff, 0xfe, 0x00}, 0o600)
	r, err = bind("", "import", "preview", "--files", "@"+root)
	if err != nil {
		t.Fatal(err)
	}
	files := body(r)["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("files: %v", files)
	}
	first := files[0].(map[string]any)
	if first["path"] != "01.md" || first["content"] != "One" || files[1].(map[string]any)["path"] != "bin.dat" || files[1].(map[string]any)["data"] == nil {
		t.Errorf("files: %v", files)
	}
	if files[2].(map[string]any)["path"] != "sub/02.md" {
		t.Errorf("a nested file keeps its relative path: %v", files[2])
	}
}
