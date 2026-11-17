package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Phase 3 exit criterion "a script can create a project and a chapter
// through the API alone", as a test: build the real server and the real slim
// CLI, mint an API key through a signed-in session (the one step that needs a
// person), and from then on use only the CLI.

func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "scriobh-cli")
	if out, err := exec.Command("go", "build", "-o", bin, "../scriobh-cli").CombinedOutput(); err != nil {
		t.Fatalf("build scriobh-cli: %v\n%s", err, out)
	}
	return bin
}

type cliRun struct {
	bin, url, key, cfg, cache string
}

func (c cliRun) run(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + c.cfg, "SCRIOBH_URL=" + c.url, "SCRIOBH_API_KEY=" + c.key,
		"SCRIOBH_CONFIG_DIR=" + c.cfg, "SCRIOBH_CACHE_DIR=" + c.cache}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return code, out.String(), errb.String()
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func TestAgentCLIEndToEnd(t *testing.T) {
	server := buildBinary(t, "")
	cliBin := buildCLI(t)
	p := start(t, server, t.TempDir())

	// A person signs in and issues a key; nothing else needs a session.
	c := client()
	signIn(t, c, p.base, "dev-writer")
	req, _ := http.NewRequest("POST", p.base+"/api/v1/keys", strings.NewReader(`{"name":"e2e agent","scope":"write","expires_in_days":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw := body(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("key.create: %d %s", resp.StatusCode, raw)
	}
	secret, _ := jsonOf(t, raw)["secret"].(string)
	if !strings.HasPrefix(secret, "scr_") {
		t.Fatalf("no secret in %s", raw)
	}

	cli := cliRun{bin: cliBin, url: p.base, key: secret, cfg: t.TempDir(), cache: t.TempDir()}

	// whoami, with the key pinned in the environment only.
	code, out, errs := cli.run(t, "", "whoami")
	if code != 0 || jsonOf(t, out)["data"].(map[string]any)["via"] != "apikey" {
		t.Fatalf("whoami: %d\n%s\n%s", code, out, errs)
	}
	if strings.Contains(out+errs, secret) {
		t.Fatal("the CLI printed the key")
	}

	// A project and a chapter, through the CLI alone.
	code, out, errs = cli.run(t, "", "project", "create", "--title", "The Salt Road", "--kind", "novel", "--json")
	if code != 0 {
		t.Fatalf("project create: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = cli.run(t, "The tide had gone out and taken the light with it.\n", "doc", "create", "The Salt Road", "--title", "The Harbour at Low Tide", "--kind", "chapter", "--content", "-")
	if code != 0 {
		t.Fatalf("doc create: %d\n%s\n%s", code, out, errs)
	}
	created := jsonOf(t, out)["data"].(map[string]any)["item"].(map[string]any)

	// Read it back, by path.
	code, out, errs = cli.run(t, "", "doc", "get", "The Salt Road/The Harbour at Low Tide")
	if code != 0 {
		t.Fatalf("doc get: %d\n%s\n%s", code, out, errs)
	}
	got := jsonOf(t, out)["data"].(map[string]any)["item"].(map[string]any)
	if got["id"] != created["id"] || !strings.Contains(got["content"].(string), "taken the light") {
		t.Fatalf("read back: %v", got)
	}

	// Update with the right revision, then with the stale one: exit 4.
	code, out, errs = cli.run(t, "", "doc", "update", "The Salt Road/The Harbour at Low Tide", "--base-revision", "1", "--content", "A second draft.")
	if code != 0 {
		t.Fatalf("update: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = cli.run(t, "", "doc", "update", "The Salt Road/The Harbour at Low Tide", "--base-revision", "1", "--content", "A stale draft.")
	if code != 4 || out != "" {
		t.Fatalf("a stale revision: exit %d, want 4\n%s\n%s", code, out, errs)
	}
	fail := jsonOf(t, errs)
	if fail["error"].(map[string]any)["code"] != "conflict" || fail["exit"].(float64) != 4 {
		t.Errorf("error object: %s", errs)
	}

	// A dry-run trash reports a change and changes nothing.
	code, out, errs = cli.run(t, "", "doc", "trash", "The Salt Road/The Harbour at Low Tide", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run trash: %d\n%s\n%s", code, out, errs)
	}
	meta := jsonOf(t, out)["meta"].(map[string]any)
	if meta["dry_run"] != true || meta["would_change"] != true {
		t.Errorf("meta: %v", meta)
	}
	code, out, _ = cli.run(t, "", "doc", "get", created["id"].(string))
	if code != 0 || jsonOf(t, out)["data"].(map[string]any)["item"].(map[string]any)["trashed"] != false {
		t.Fatalf("the dry run trashed the document: %s", out)
	}
	// Without --yes a real trash is refused; with it, it happens.
	if code, _, _ = cli.run(t, "", "doc", "trash", created["id"].(string)); code != 2 {
		t.Errorf("trash without --yes: exit %d, want 2", code)
	}

	// The skill and its check, against this very server.
	code, out, _ = cli.run(t, "", "--skill", "drafting")
	if code != 0 || !strings.Contains(out, "# Recipe: drafting") {
		t.Errorf("--skill drafting: %d %.100s", code, out)
	}
	proj := t.TempDir()
	if code, out, errs = cli.run(t, "", "skill", "install", "--dir", proj, "--agent", "claude"); code != 0 {
		t.Fatalf("skill install: %d\n%s\n%s", code, out, errs)
	}
	code, out, errs = cli.run(t, "", "skill", "check", "--dir", proj)
	d := jsonOf(t, out)["data"].(map[string]any)
	if d["server_schema"] == "" || d["installs"].([]any)[0].(map[string]any)["current"] != true {
		t.Errorf("skill check: %d\n%s\n%s", code, out, errs)
	}
	// Exit 10 means drift; the committed snapshot is regenerated by `make skill`,
	// so it is allowed to differ here only through cli_snapshot_ok.
	if code != 0 && (code != 10 || d["cli_snapshot_ok"] != false) {
		t.Errorf("skill check exit %d with cli_snapshot_ok=%v", code, d["cli_snapshot_ok"])
	}

	// The key is revoked, and the CLI says exit 5.
	code, out, errs = cli.run(t, "", "key", "list")
	_ = out
	if code != 5 { // a write-scope key may not manage keys
		t.Errorf("key list with a write key: exit %d, want 5\n%s", code, errs)
	}
	resp, _ = c.Get(p.base + "/api/v1/keys")
	var keys struct {
		Items []struct{ ID string }
	}
	_ = json.Unmarshal([]byte(body(t, resp)), &keys)
	if len(keys.Items) != 1 {
		t.Fatalf("keys: %v", keys)
	}
	req, _ = http.NewRequest("POST", p.base+"/api/v1/keys/"+keys.Items[0].ID+"/revoke", nil)
	if resp, err = c.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("revoke: %v %v", err, resp)
	}
	body(t, resp)
	if code, _, _ = cli.run(t, "", "whoami"); code != 5 {
		t.Errorf("after revoke: exit %d, want 5", code)
	}
}
