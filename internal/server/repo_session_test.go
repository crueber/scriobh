package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// Publishing to a repository puts the manuscript where others can read it and uses a person's token, so every
// repo.* op is for a person in a browser: an API key of any scope, pinned or not (and so the MCP endpoint, which
// speaks as a key), is refused, and none of them is a tool.
func TestRepoOpsRefuseKeysAndAreNoTools(t *testing.T) {
	w := newSiteWorld(t, nil)
	base := w.base()
	calls := []struct{ op, method, path, body string }{
		{"repo.account.list", "GET", "/api/v1/git/accounts", ""},
		{"repo.account.connect", "POST", "/api/v1/git/accounts", `{"provider":"github","token":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`},
		{"repo.account.disconnect", "DELETE", "/api/v1/git/accounts?account_id=gac_x", ""},
		{"repo.list", "POST", base + "/repo/list", `{"account_id":"gac_x"}`},
		{"repo.plan", "POST", base + "/repo/plan", `{"account_id":"gac_x","name":"book"}`},
		{"repo.publish", "POST", base + "/repo/publish", `{"account_id":"gac_x","name":"book","plan_token":"x"}`},
		{"repo.release", "POST", base + "/repo/release", `{"tag":"v1","format":"epub"}`},
		{"repo.release_get", "GET", base + "/repo/release?release_id=grl_x", ""},
		{"repo.release_list", "GET", base + "/repo/releases", ""},
	}
	for _, c := range []struct{ who, scope, pin string }{
		{"owner", "read", ""}, {"owner", "write", ""}, {"owner", "write", w.pid},
		{"editor", "write", ""}, {"admin", "admin", ""}, {"admin", "admin", w.pid},
	} {
		caller := map[string]caller{"owner": w.owner, "editor": w.editor, "admin": w.admin}[c.who]
		body := fmt.Sprintf(`{"name":"%s %s","scope":%q`, c.who, c.scope, c.scope)
		if c.pin != "" {
			body += fmt.Sprintf(`,"project_id":%q`, c.pin)
		}
		key, _ := w.s.mintKey(t, caller, body+"}")
		for _, o := range calls {
			if rec := w.s.doKey(key, o.method, o.path, o.body); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
				t.Errorf("%s %s key, %s: %d %s", c.who, c.scope, o.op, rec.Code, rec.Body)
			}
		}
		names := w.s.allToolNames(t, key, "")
		for _, o := range calls {
			if has(names, strings.ReplaceAll(o.op, ".", "_")) {
				t.Errorf("a key is offered the tool %s", o.op)
			}
			r := w.s.modernCall(t, key, "tools/call", map[string]any{"name": strings.ReplaceAll(o.op, ".", "_"), "arguments": map[string]any{"id": w.pid}})
			if r.msg.Error == nil && !strings.Contains(string(r.raw), `"isError":true`) {
				t.Errorf("a key called the tool %s over MCP: %s", o.op, r.raw)
			}
		}
	}
	// A person at the browser gets past authorization (and is then told what is missing).
	if rec := w.s.do(w.owner, "GET", "/api/v1/git/accounts", ""); rec.Code != 200 {
		t.Errorf("the owner's account list: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.owner, "POST", base+"/repo/plan", `{"account_id":"gac_x","name":"book"}`); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "account") {
		t.Errorf("the owner planning with an account that is not there: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.editor, "POST", base+"/repo/plan", `{"account_id":"gac_x","name":"book"}`); rec.Code != http.StatusForbidden {
		t.Errorf("an editor at the browser: %d %s", rec.Code, rec.Body)
	}
}

// What a release carries is what a reader sees, whatever the project holds beside it: no private notes, ghosted
// text, alternatives, notes or documents marked do not compile, and not the author's contact block.
func TestAReleasesEditionHoldsWhatAReaderSeesAndNoContactBlock(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.must(t, w.owner, "PATCH", w.base()+"/compile/contact", `{"name":"Máire","address":"CONTACT-ADDRESS-CANARY","email":"contact-canary@example.org","phone":"CONTACT-PHONE-CANARY"}`)
	alts := `<!-- scriobh:alts {"alt":"ALT-CANARY"} -->`
	w.doc(t, "Opening", "chapter", "", "The harbour woke. %%PRIVATE-CANARY%% It was <scriobh-ghost>GHOST-CANARY</scriobh-ghost>cold.\n\n"+alts+"\n", "done", "")
	w.doc(t, "Draft", "chapter", "", "NOCOMPILE-CANARY\n", "draft", `,"include_in_compile":false`)
	w.doc(t, "Ideas", "note", "", "NOTE-CANARY\n", "idea", "")
	p := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "session"}
	canaries := []string{"PRIVATE-CANARY", "GHOST-CANARY", "ALT-CANARY", "NOCOMPILE-CANARY", "NOTE-CANARY", "CONTACT-ADDRESS-CANARY", "CONTACT-PHONE-CANARY", "contact-canary"}
	compile := w.s.gitCompiler()
	for _, c := range []struct{ format, preset string }{{"epub", "epub3"}, {"docx", "styled-docx"}, {"md", "plain-markdown"}, {"pdf", "print-6x9"}} {
		t.Run(c.format, func(t *testing.T) {
			ed, err := compile(t.Context(), p, editionRequest(w.pid, c.format, c.preset))
			if err != nil {
				t.Fatal(err)
			}
			if len(ed.Body) == 0 || ed.Filename == "" || ed.ContentType == "" {
				t.Fatalf("edition = %+v", ed)
			}
			if len(ed.InputHash) != 64 {
				t.Errorf("no manifest hash: %q", ed.InputHash)
			}
			text := string(ed.Body)
			if c.format == "epub" || c.format == "docx" {
				zr, err := zip.NewReader(bytes.NewReader(ed.Body), int64(len(ed.Body)))
				if err != nil {
					t.Fatal(err)
				}
				var sb strings.Builder
				for _, f := range zr.File {
					rc, _ := f.Open()
					b, _ := io.ReadAll(rc)
					_ = rc.Close()
					sb.WriteString(f.Name)
					sb.Write(b)
				}
				text = sb.String()
				if !strings.Contains(text, "harbour woke") {
					t.Errorf("the chapter is not in the %s", c.format)
				}
			}
			if c.format != "pdf" { // a PDF's text is compressed; the other formats are searched
				for _, canary := range canaries {
					if strings.Contains(text, canary) {
						t.Errorf("the %s holds %s", c.format, canary)
					}
				}
			}
		})
	}
	// Asked as someone who may not edit, it is refused by the export ops themselves.
	if _, err := compile(t.Context(), ops.Principal{UserID: w.reader.userID, Role: ops.RoleReader, Via: "session"}, editionRequest(w.pid, "epub", "epub3")); err == nil {
		t.Error("a reader built an edition")
	}
}

func editionRequest(project, format, preset string) gitmirror.EditionRequest {
	return gitmirror.EditionRequest{ProjectID: project, Format: format, Preset: preset}
}

// An owner's key cannot widen what reaches a public remote, over REST and over MCP (where the Git group is not
// offered and a call by name is refused).
func TestAKeyCannotWidenAPublicRemoteOverRESTAndMCP(t *testing.T) {
	w := newSiteWorld(t, nil)
	base := w.base()
	rec := w.must(t, w.owner, "POST", base+"/git/remotes", `{"name":"Public mirror","url":"https://forge.example.org/mairead/salt-road.git","token":"tok_abcdefghijklmnopqrstuvwxyz","push_mode":"manual"}`)
	rid := idOf(t, rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"audience":"public"`) {
		t.Fatalf("a remote made without saying is not public: %s", rec.Body)
	}
	key, _ := w.s.mintKey(t, w.owner, fmt.Sprintf(`{"name":"owner write","scope":"write","project_id":%q}`, w.pid))
	unpinned, _ := w.s.mintKey(t, w.owner, `{"name":"owner write all","scope":"write"}`)
	for _, k := range []string{key, unpinned} {
		for name, body := range map[string]string{
			"audience": fmt.Sprintf(`{"remote_id":%q,"audience":"private"}`, rid),
			"notes":    fmt.Sprintf(`{"remote_id":%q,"include_notes":true}`, rid),
			"only":     fmt.Sprintf(`{"remote_id":%q,"only_done":false}`, rid),
			"address":  fmt.Sprintf(`{"remote_id":%q,"url":"https://forge.example.org/mairead/other.git"}`, rid),
		} {
			if rec := w.s.doKey(k, "PATCH", base+"/git/remote", body); rec.Code != http.StatusForbidden {
				t.Errorf("%s by a key: %d %s", name, rec.Code, rec.Body)
			}
		}
		if rec := w.s.doKey(k, "POST", base+"/git/push/force", fmt.Sprintf(`{"remote_id":%q,"confirm":"Public mirror"}`, rid)); rec.Code != http.StatusForbidden {
			t.Errorf("force push by a key: %d %s", rec.Code, rec.Body)
		}
		names := w.s.allToolNames(t, k, "")
		for _, n := range []string{"git_remote_update", "git_push_force", "git_remote_create"} {
			if has(names, n) {
				t.Errorf("a key is offered %s", n)
			}
			r := w.s.modernCall(t, k, "tools/call", map[string]any{"name": n, "arguments": map[string]any{"id": w.pid, "remote_id": rid, "audience": "private"}})
			if r.msg.Error == nil && !strings.Contains(string(r.raw), `"isError":true`) {
				t.Errorf("a key called %s over MCP: %s", n, r.raw)
			}
		}
	}
	// A person at the browser can.
	// (and, since the confirmation is the op's, only with the remote's name typed)
	if rec := w.s.do(w.owner, "PATCH", base+"/git/remote", fmt.Sprintf(`{"remote_id":%q,"audience":"private"}`, rid)); rec.Code != http.StatusConflict {
		t.Errorf("the owner's own change without the name: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.owner, "PATCH", base+"/git/remote", fmt.Sprintf(`{"remote_id":%q,"audience":"private","confirm_name":"Public mirror"}`, rid)); rec.Code != 200 {
		t.Errorf("the owner's own change: %d %s", rec.Code, rec.Body)
	}
}

// A ghost in a table cell is not in an edition, whatever the preset.
func TestAGhostInATableCellIsNotInAnEdition(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.doc(t, "Opening", "chapter", "", "Before.\n\n| a | b |\n|---|---|\n| x | <scriobh-ghost>CELL-GHOST-CANARY</scriobh-ghost> |\n| y | %%CELL-PRIVATE-CANARY%% |\n\nAfter.\n", "done", "")
	p := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "session"}
	compile := w.s.gitCompiler()
	for _, c := range []struct{ format, preset string }{{"md", "plain-markdown"}, {"epub", "epub3"}, {"docx", "styled-docx"}} {
		ed, err := compile(t.Context(), p, editionRequest(w.pid, c.format, c.preset))
		if err != nil {
			t.Fatal(err)
		}
		text := string(ed.Body)
		if c.format != "md" {
			zr, _ := zip.NewReader(bytes.NewReader(ed.Body), int64(len(ed.Body)))
			var sb strings.Builder
			for _, f := range zr.File {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				sb.Write(b)
			}
			text = sb.String()
		}
		for _, canary := range []string{"CELL-GHOST-CANARY", "CELL-PRIVATE-CANARY"} {
			if strings.Contains(text, canary) {
				t.Errorf("the %s holds %s", c.format, canary)
			}
		}
		if !strings.Contains(text, "Before.") {
			t.Errorf("the %s lost the chapter", c.format)
		}
	}
}
