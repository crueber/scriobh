package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
)

// TestSkillExamplesParse extracts every example command from the full skill
// and from each recipe and checks that it parses against the schema it was
// rendered from, with its required flags present. A skill that teaches a
// command the CLI would refuse fails here.
func TestSkillExamplesParse(t *testing.T) {
	schema := liveSchema(t)
	ix := cli.NewIndex(schema)
	texts := map[string]string{"full": cli.RenderSkill(schema, cli.SkillOptions{}), "compact": cli.RenderSkill(schema, cli.SkillOptions{Compact: true})}
	for _, n := range cli.RecipeNames {
		r, ok := cli.Recipe(schema, n)
		if !ok {
			t.Fatalf("no recipe %s", n)
		}
		texts["recipe "+n] = r
	}
	builtins := map[string]bool{"login": true, "logout": true, "config": true, "whoami": true, "ops": true, "schema": true, "skill": true, "completion": true, "help": true, "version": true, "mcp": true}
	total := 0
	for name, text := range texts {
		ex := cli.ExtractExamples(text)
		if len(ex) == 0 && name != "compact" && name != "recipe compendium" {
			t.Errorf("%s has no examples", name)
		}
		for _, line := range ex {
			total++
			args, err := cli.SplitCommand(line)
			if err != nil || len(args) < 2 || args[0] != "scriobh-cli" {
				t.Errorf("%s: cannot split %q: %v", name, line, err)
				continue
			}
			if builtins[args[1]] {
				continue
			}
			inv, err := ix.Parse(args[1:])
			if err != nil {
				t.Errorf("%s: %q does not parse: %v", name, line, err)
				continue
			}
			if miss := inv.Missing(nil); len(miss) > 0 {
				t.Errorf("%s: %q lacks required flags %v", name, line, miss)
			}
			if err := inv.Validate(); err != nil {
				t.Errorf("%s: %q does not validate: %v", name, line, err)
			}
			if err := inv.Preflight(); err != nil {
				t.Errorf("%s: %q would be refused before it is sent: %v", name, line, err)
			}
		}
	}
	if total < 40 {
		t.Errorf("only %d examples were checked", total)
	}
	// The reference documents every op, and the schema hash is stated.
	full := texts["full"]
	for _, o := range schema.Ops {
		if !strings.Contains(full, "#### `"+o.Command()+"`") {
			t.Errorf("the reference lacks %s", o.Command())
		}
	}
	if !strings.Contains(full, schema.Hash) {
		t.Error("the skill does not state the schema hash it was generated from")
	}
	// The skill's own prose says agent. The reference quotes each op's description, and
	// the AI ops (internal/domain/ai) name the feature they are, so those quotes are not checked.
	prose := full
	for _, o := range schema.Ops {
		prose = strings.ReplaceAll(prose, o.Desc, "")
	}
	var lines []string
	for _, l := range strings.Split(prose, "\n") {
		if !strings.HasPrefix(l, "scriobh-cli a"+"i ") { // the command group of the AI ops is named after the ops
			lines = append(lines, l)
		}
	}
	if strings.Contains(strings.ToLower(strings.Join(lines, "\n")), " a"+"i ") {
		t.Error("the skill should say agent, not the other word")
	}
}

// TestCompendiumRecipeFollowsTheSchema: with no compendium ops it says so; with
// ops the CLI has never heard of, it lists them with runnable examples.
func TestCompendiumRecipeFollowsTheSchema(t *testing.T) {
	plain := liveSchema(t)
	plain.Ops = withoutCompendiumOps(plain.Ops)
	text, _ := cli.Recipe(plain, "compendium")
	if !strings.Contains(text, "offers no Compendium commands") {
		t.Errorf("without compendium ops: %s", text)
	}
	str := map[string]any{"type": "string"}
	withOps := plain
	withOps.Ops = append(append([]cli.Op(nil), plain.Ops...),
		cli.Op{Name: "compendium.create", Method: "POST", Path: "/api/v1/compendiums", Scope: "write", Status: 201, Desc: "Create a compendium. It may outlive any project.",
			Input:  map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{"name": str}},
			Output: map[string]any{"type": "object"}},
		cli.Op{Name: "compendium.list", Method: "GET", Path: "/api/v1/compendiums", Scope: "read", Status: 200, Desc: "List compendiums.",
			Input: map[string]any{"type": "object", "properties": map[string]any{}}, Output: map[string]any{"type": "object"}},
		cli.Op{Name: "entity.create", Method: "POST", Path: "/api/v1/compendiums/{id}/entities", Scope: "write", Status: 201, Desc: "Add an entity.",
			Input: map[string]any{"type": "object", "required": []any{"id", "kind", "name"}, "properties": map[string]any{
				"id": map[string]any{"type": "string", "in": "path"}, "name": str,
				"kind": map[string]any{"type": "string", "enum": []any{"character", "place"}}}},
			Output: map[string]any{"type": "object"}},
	)
	text, _ = cli.Recipe(withOps, "compendium")
	for _, want := range []string{"scriobh-cli compendium create --name", "scriobh-cli entity create \"The Salt Road\" --kind character --name", "scriobh-cli compendium list"} {
		if !strings.Contains(text, want) {
			t.Errorf("recipe lacks %q:\n%s", want, text)
		}
	}
	ix := cli.NewIndex(withOps)
	for _, line := range cli.ExtractExamples(text) {
		args, _ := cli.SplitCommand(line)
		if inv, err := ix.Parse(args[1:]); err != nil || len(inv.Missing(nil)) > 0 {
			t.Errorf("%q: %v", line, err)
		}
	}
	// The kind inferred from the path lets an entity be addressed through its compendium.
	o, _ := ix.ByName("entity.create")
	for _, f := range o.Fields() {
		if f.Name == "id" && f.Ref != "compendium" {
			t.Errorf("entity.create's id refers to %q, want compendium", f.Ref)
		}
	}
}

func TestSkillCommandPrintsAndRecipes(t *testing.T) {
	e := newEnv(t, nil)
	full := e.ok("--skill")
	if !strings.HasPrefix(full.stdout, "---\nname: scriobh") || !strings.Contains(full.stdout, "## Command reference") {
		t.Errorf("--skill: %.200s", full.stdout)
	}
	for _, n := range cli.RecipeNames {
		r := e.ok("--skill", n)
		if !strings.HasPrefix(r.stdout, "# Recipe: "+n) {
			t.Errorf("recipe %s: %.100s", n, r.stdout)
		}
		if r2 := e.ok("skill", "show", n); r2.stdout != r.stdout {
			t.Errorf("skill show %s differs from --skill %s", n, n)
		}
	}
	// The skill printed by the CLI describes the live server, which is what the
	// committed skill is generated from.
	if !strings.Contains(full.stdout, e.srv.Reg.Schema().Hash) {
		t.Error("the printed skill does not carry the server's schema hash")
	}
	if r := e.run("--skill", "nonsense"); r.code != cli.ExitUsage {
		t.Errorf("an unknown recipe: exit %d", r.code)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillInstallClaudeFileAndUninstall(t *testing.T) {
	e := newEnv(t, nil)
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Detected by its folder.
	r := e.ok("skill", "install", "--dir", proj)
	inst := r.data(t)["installs"].([]any)
	if len(inst) != 1 || inst[0].(map[string]any)["agent"] != "claude" || inst[0].(map[string]any)["action"] != "created" {
		t.Fatalf("install: %s", r.stdout)
	}
	path := filepath.Join(proj, ".claude/skills/scriobh/SKILL.md")
	body := readFile(t, path)
	if !strings.HasPrefix(body, "---\nname: scriobh") || !strings.Contains(body, "scriobh:skill:file sha256=") || !strings.Contains(body, "#### `doc update`") {
		t.Errorf("the skill file: %.300s", body)
	}
	// Reinstall is a no-op; a dry run changes nothing and says what it would do.
	if r := e.ok("skill", "install", "--dir", proj); r.data(t)["installs"].([]any)[0].(map[string]any)["action"] != "unchanged" {
		t.Errorf("reinstall: %s", r.stdout)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(body, "Ground rules", "Ground rules (stale)", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hand edit is refused, not overwritten.
	edited := readFile(t, path)
	r = e.run("skill", "install", "--dir", proj)
	if r.code != cli.ExitConflict || readFile(t, path) != edited {
		t.Fatalf("a hand edit must be refused and kept: exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "edited by hand") {
		t.Errorf("the reason should be stated: %s", r.stdout)
	}
	// --dry-run with --force reports and writes nothing.
	r = e.ok("skill", "install", "--dir", proj, "--force", "--dry-run")
	if got := r.data(t)["installs"].([]any)[0].(map[string]any)["action"]; got != "would_be_updated" || readFile(t, path) != edited {
		t.Errorf("dry run: %v", got)
	}
	e.ok("skill", "install", "--dir", proj, "--force")
	if readFile(t, path) != body {
		t.Error("--force should restore the generated text")
	}
	// Uninstall refuses edits, then removes the file and its folder.
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := e.run("skill", "uninstall", "--dir", proj); r.code != cli.ExitConflict {
		t.Errorf("uninstall of an edited file: exit %d", r.code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("an edited file must survive uninstall")
	}
	e.ok("skill", "uninstall", "--dir", proj, "--force")
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the skill folder should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude")); err != nil {
		t.Error("uninstall must leave the user's own folders")
	}
}

func TestSkillInstallBlockKeepsTheUsersText(t *testing.T) {
	e := newEnv(t, nil)
	proj := t.TempDir()
	agents := filepath.Join(proj, "AGENTS.md")
	original := "# My project\n\nUse tabs.\n\nDo not touch vendor/.\n"
	if err := os.WriteFile(agents, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	r := e.ok("skill", "install", "--dir", proj)
	got := r.data(t)["installs"].([]any)[0].(map[string]any)
	if got["agent"] != "agents" || got["action"] != "appended" {
		t.Fatalf("%s", r.stdout)
	}
	body := readFile(t, agents)
	if !strings.HasPrefix(body, original) || !strings.Contains(body, "<!-- scriobh:skill:begin sha256=") || !strings.Contains(body, "<!-- scriobh:skill:end -->") {
		t.Errorf("the user's text must come first and stay: %.200s", body)
	}
	if st, _ := os.Stat(agents); st.Mode().Perm() != 0o640 {
		t.Errorf("the file's mode changed to %o", st.Mode().Perm())
	}
	if strings.Contains(body, "#### `doc update`") {
		t.Error("an instruction file gets the compact skill, not the whole reference")
	}
	// Reinstall replaces the block in place; text after it survives.
	if err := os.WriteFile(agents, []byte(body+"\nTail the user added.\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if r := e.ok("skill", "install", "--dir", proj); r.data(t)["installs"].([]any)[0].(map[string]any)["action"] != "unchanged" {
		t.Errorf("reinstall: %s", r.stdout)
	}
	// Edit inside the block: refused. Edit outside it: fine.
	cur := readFile(t, agents)
	_ = os.WriteFile(agents, []byte(strings.Replace(cur, "Ground rules", "Ground rules (mine)", 1)), 0o640)
	if r := e.run("skill", "install", "--dir", proj); r.code != cli.ExitConflict {
		t.Errorf("an edit inside the block: exit %d", r.code)
	}
	_ = os.WriteFile(agents, []byte(cur), 0o640)
	e.ok("skill", "uninstall", "--dir", proj)
	after := readFile(t, agents)
	if after != original+"\nTail the user added.\n" {
		t.Errorf("uninstall should leave exactly the user's text:\n%q", after)
	}
	// A file that holds only our block goes away entirely.
	solo := t.TempDir()
	e.ok("skill", "install", "--dir", solo, "--agent", "gemini")
	if _, err := os.Stat(filepath.Join(solo, "GEMINI.md")); err != nil {
		t.Fatal(err)
	}
	e.ok("skill", "uninstall", "--dir", solo, "--agent", "gemini")
	if _, err := os.Stat(filepath.Join(solo, "GEMINI.md")); !os.IsNotExist(err) {
		t.Error("a file we created and emptied should be removed")
	}
}

func TestSkillInstallStaysInsideTheDirectory(t *testing.T) {
	e := newEnv(t, nil)
	proj := t.TempDir()
	outside := t.TempDir()
	// No agent found and none named: an error, nothing written.
	if r := e.run("skill", "install", "--dir", proj); r.code != cli.ExitUsage {
		t.Errorf("no agent: exit %d", r.code)
	}
	// --file may not leave the directory, absolute or relative.
	for _, f := range []string{filepath.Join(outside, "x.md"), "../escape.md", "a/../../escape.md"} {
		if r := e.run("skill", "install", "--dir", proj, "--agent", "codex", "--file", f); r.code != cli.ExitUsage {
			t.Errorf("--file %s: exit %d, want 2: %s", f, r.code, r.stderr)
		}
	}
	// A symbolic link in the path is not followed.
	if err := os.Symlink(outside, filepath.Join(proj, ".claude")); err != nil {
		t.Skip("no symlinks here")
	}
	if r := e.run("skill", "install", "--dir", proj, "--agent", "claude"); r.code != cli.ExitUsage {
		t.Errorf("through a symlink: exit %d", r.code)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written outside the directory: %v", entries)
	}
	// A foreign file at the skill path is not replaced without --force.
	proj2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj2, ".cursor/rules"), 0o755)
	foreign := filepath.Join(proj2, ".cursor/rules/scriobh.mdc")
	_ = os.WriteFile(foreign, []byte("mine, not yours\n"), 0o644)
	if r := e.run("skill", "install", "--dir", proj2); r.code != cli.ExitConflict || readFile(t, foreign) != "mine, not yours\n" {
		t.Errorf("a foreign file: exit %d", r.code)
	}
}

func TestSkillGlobalUsesTheHomeDirectory(t *testing.T) {
	e := newEnv(t, nil)
	_ = os.MkdirAll(filepath.Join(e.home, ".claude"), 0o755)
	r := e.ok("skill", "install", "--global", "--agent", "claude")
	if _, err := os.Stat(filepath.Join(e.home, ".claude/skills/scriobh/SKILL.md")); err != nil {
		t.Fatalf("%v: %s", err, r.stdout)
	}
	if r := e.run("skill", "install", "--global", "--agent", "cursor"); r.code != cli.ExitUsage {
		t.Errorf("cursor has no global file: exit %d", r.code)
	}
}

func TestSkillCheckReportsDrift(t *testing.T) {
	e := newEnv(t, nil)
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)
	// Installed from the live server's schema: current, as far as the server goes.
	e.ok("skill", "install", "--dir", proj)
	r := e.run("skill", "check", "--dir", proj)
	d := r.data(t)
	installs := d["installs"].([]any)
	if len(installs) != 1 || installs[0].(map[string]any)["current"] != true || installs[0].(map[string]any)["hand_edited"] != false {
		t.Fatalf("check: %s", r.stdout)
	}
	if d["server_schema"] != e.srv.Reg.Schema().Hash {
		t.Errorf("server_schema = %v", d["server_schema"])
	}
	// The CLI's embedded snapshot is compared too; it matches only when `make skill` is current.
	snapOK := d["cli_snapshot_ok"] == true
	if snapOK && r.code != 0 {
		t.Errorf("everything current but exit %d", r.code)
	}
	if !snapOK && r.code != cli.ExitDrift {
		t.Errorf("a stale snapshot must be drift (exit 10), got %d", r.code)
	}
	// The server's schema changes under an installed copy: drift.
	path := filepath.Join(proj, ".claude/skills/scriobh/SKILL.md")
	cur := readFile(t, path)
	stale := strings.NewReplacer(e.srv.Reg.Schema().Hash, "sha256:"+strings.Repeat("0", 64)).Replace(cur)
	_ = os.WriteFile(path, []byte(stale), 0o644)
	r = e.run("skill", "check", "--dir", proj)
	if r.code != cli.ExitDrift {
		t.Errorf("an out-of-date install: exit %d", r.code)
	}
	if got := r.data(t)["installs"].([]any)[0].(map[string]any); got["current"] != false {
		t.Errorf("%v", got)
	}
	// A hand edit is drift as well.
	_ = os.WriteFile(path, []byte(strings.Replace(cur, "Ground rules", "Ground rules!", 1)), 0o644)
	if r := e.run("skill", "check", "--dir", proj); r.code != cli.ExitDrift || r.data(t)["installs"].([]any)[0].(map[string]any)["hand_edited"] != true {
		t.Errorf("hand edit: exit %d", r.code)
	}
	// Nothing installed is not drift.
	if r := e.run("skill", "check", "--dir", t.TempDir()); r.code != 0 && r.data(t)["cli_snapshot_ok"] == true {
		t.Errorf("no install: exit %d", r.code)
	}
}

func TestCompletionScripts(t *testing.T) {
	e := newEnv(t, nil)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		r := e.ok("completion", shell)
		for _, want := range []string{"project", "doc", "update", "base-revision", "skill", "dry-run"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%s completion lacks %q", shell, want)
			}
		}
	}
	// The scripts are valid for the shells that exist here.
	for shell, flag := range map[string]string{"bash": "-n", "zsh": "-n"} {
		bin, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		path := filepath.Join(t.TempDir(), "c."+shell)
		_ = os.WriteFile(path, []byte(e.ok("completion", shell).stdout), 0o644)
		if out, err := exec.Command(bin, flag, path).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", shell, err, out)
		}
	}
	if r := e.run("completion", "tcsh"); r.code != cli.ExitUsage {
		t.Errorf("unknown shell: exit %d", r.code)
	}
}

// withoutCompendiumOps drops the ops that make up the story bible, to stand in
// for a server that has none.
func withoutCompendiumOps(all []cli.Op) []cli.Op {
	var out []cli.Op
	for _, o := range all {
		if cli.IsCompendiumOp(o) {
			continue
		}
		out = append(out, o)
	}
	return out
}
