package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Phase 2 definition of done for backups, as a test through the real
// binary: a mirror appears while the server runs; `scriobh backup` works beside
// the running server; `scriobh restore` refuses while that server holds the
// data directory, refuses a non-empty directory without --force, and brings the
// project back into an empty one, where the mirror is current again.

// cli executes the binary as a CLI against a data directory.
func cli(t *testing.T, bin, data string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_DATA_DIR=" + data}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func readZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestBackupAndRestoreThroughTheBinary(t *testing.T) {
	bin := buildBinary(t, "")
	data := t.TempDir()
	p := start(t, bin, data, "SCRIOBH_MIRROR_DEBOUNCE_MS=50")
	c := client()
	signIn(t, c, p.base, "dev-writer")
	resp, _ := c.Get(p.base + "/")
	lib := body(t, resp)
	csrf := reCSRF.FindStringSubmatch(lib)
	if csrf == nil {
		t.Fatal("no csrf token")
	}
	resp, err := c.PostForm(p.base+"/ui/projects", url.Values{"title": {"The Salt Road"}, "kind": {"novel"}, "_csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	var pid string
	{
		resp, _ := c.Get(p.base + "/api/v1/projects")
		var out struct{ Items []struct{ ID string } }
		if err := json.Unmarshal([]byte(body(t, resp)), &out); err != nil || len(out.Items) != 1 {
			t.Fatalf("projects: %v", err)
		}
		pid = out.Items[0].ID
	}
	req, _ := http.NewRequest("POST", p.base+"/api/v1/projects/"+pid+"/docs", strings.NewReader(`{"title":"Harbour","kind":"chapter","content":"The quiet harbour.\n"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = c.Do(req)
	if b := body(t, resp); resp.StatusCode != 201 {
		t.Fatalf("create doc: %d %s", resp.StatusCode, b)
	}

	// The mirror follows, with no help.
	var mirrored string
	waitFor(t, "the mirror", func() bool {
		m, _ := filepath.Glob(filepath.Join(data, "mirror", "the-salt-road--*", "manuscript", "*-harbour.md"))
		if len(m) == 1 {
			mirrored = m[0]
			return true
		}
		return false
	})
	if b, _ := os.ReadFile(mirrored); string(b) != "The quiet harbour.\n" {
		t.Errorf("mirrored text = %q", b)
	}
	// And the status op says so.
	resp, _ = c.Get(p.base + "/api/v1/projects/" + pid + "/mirror")
	if s := body(t, resp); !strings.Contains(s, `"in_sync":true`) {
		waitFor(t, "an in-sync status", func() bool {
			resp, _ := c.Get(p.base + "/api/v1/projects/" + pid + "/mirror")
			return strings.Contains(body(t, resp), `"in_sync":true`)
		})
	}

	// A backup beside the running server.
	out, errOut, code := cli(t, bin, data, "backup")
	if code != 0 {
		t.Fatalf("backup beside a running server: %d\n%s\n%s", code, out, errOut)
	}
	zips, _ := filepath.Glob(filepath.Join(data, "backups", "*.zip"))
	if len(zips) != 1 {
		t.Fatalf("backups = %v\n%s", zips, out)
	}
	entries := readZip(t, zips[0])
	key, _ := os.ReadFile(filepath.Join(data, "secret.key"))
	if len(bytes.TrimSpace(key)) < 32 {
		t.Fatal("the server did not write its key file; the test cannot check the exclusion")
	}
	for name, b := range entries {
		if strings.Contains(name, "secret.key") || bytes.Contains(b, bytes.TrimSpace(key)) {
			t.Errorf("the secret key reached the backup (%s)", name)
		}
	}
	for _, want := range []string{"manifest.json", "scriobh.db"} {
		if entries[want] == nil {
			t.Errorf("the backup has no %s", want)
		}
	}

	// Restore refuses while the server holds the data directory.
	_, errOut, code = cli(t, bin, data, "restore", zips[0], "--force")
	if code == 0 || !strings.Contains(errOut, "another") {
		t.Errorf("restore into a running server's directory: code %d, stderr %q", code, errOut)
	}
	p.stop(t)

	// A second, explicit backup path, taken offline.
	explicit := filepath.Join(t.TempDir(), "offline.zip")
	if _, errOut, code := cli(t, bin, data, "backup", "--out", explicit); code != 0 {
		t.Fatalf("offline backup: %d %s", code, errOut)
	}
	if _, errOut, code := cli(t, bin, data, "backup", "--out", explicit); code == 0 || !strings.Contains(errOut, "already exists") {
		t.Errorf("a backup must not overwrite a file: %d %s", code, errOut)
	}

	// Restore into the directory that still holds data: refused without --force.
	if _, errOut, code := cli(t, bin, data, "restore", explicit); code == 0 || !strings.Contains(errOut, "--force") {
		t.Errorf("restore over existing data without --force: %d %s", code, errOut)
	}
	// Restore into an empty directory.
	data2 := t.TempDir()
	out, errOut, code = cli(t, bin, data2, "restore", explicit)
	if code != 0 {
		t.Fatalf("restore: %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "1 projects") || !strings.Contains(out, "secret key") {
		t.Errorf("restore summary: %s", out)
	}
	if _, err := os.Stat(filepath.Join(data2, "secret.key")); err == nil {
		t.Error("restore created a key; it must never invent or copy one")
	}

	// The restored server has the project and a current mirror.
	p2 := start(t, bin, data2, "SCRIOBH_MIRROR_DEBOUNCE_MS=50")
	c2 := client()
	signIn(t, c2, p2.base, "dev-writer")
	resp, _ = c2.Get(p2.base + "/api/v1/projects")
	var list struct{ Items []struct{ ID, Title string } }
	if err := json.Unmarshal([]byte(body(t, resp)), &list); err != nil || len(list.Items) != 1 || list.Items[0].Title != "The Salt Road" {
		t.Fatalf("restored projects: %v %+v\n%s", err, list, p2.logs)
	}
	resp, _ = c2.Get(p2.base + "/api/v1/projects/" + list.Items[0].ID + "/docs")
	if s := body(t, resp); !strings.Contains(s, "Harbour") {
		t.Errorf("the restored project lost its chapter: %s", s)
	}
	waitFor(t, "the restored mirror", func() bool {
		resp, _ := c2.Get(p2.base + "/api/v1/projects/" + list.Items[0].ID + "/mirror")
		return strings.Contains(body(t, resp), `"in_sync":true`)
	})
	m, _ := filepath.Glob(filepath.Join(data2, "mirror", "the-salt-road--*", "manuscript", "*-harbour.md"))
	if len(m) != 1 {
		t.Errorf("restored mirror = %v", m)
	}
	p2.stop(t)
}

// The off switch: no mirror directory, and the ops say why.
func TestMirrorSwitchedOff(t *testing.T) {
	bin := buildBinary(t, "")
	data := t.TempDir()
	p := start(t, bin, data, "SCRIOBH_MIRROR=off", "SCRIOBH_MIRROR_DEBOUNCE_MS=20")
	c := client()
	signIn(t, c, p.base, "dev-writer")
	resp, _ := c.Get(p.base + "/")
	csrf := reCSRF.FindStringSubmatch(body(t, resp))
	resp, _ = c.PostForm(p.base+"/ui/projects", url.Values{"title": {"Quiet"}, "_csrf": {csrf[1]}})
	body(t, resp)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(data, "mirror")); !os.IsNotExist(err) {
		t.Errorf("the mirror wrote with SCRIOBH_MIRROR=off: %v", err)
	}
	resp, _ = c.Get(p.base + "/api/v1/projects")
	var out struct{ Items []struct{ ID string } }
	json.Unmarshal([]byte(body(t, resp)), &out)
	resp, _ = c.Get(p.base + "/api/v1/projects/" + out.Items[0].ID + "/mirror")
	if s := body(t, resp); !strings.Contains(s, `"enabled":false`) {
		t.Errorf("status with the mirror off: %s", s)
	}
}
