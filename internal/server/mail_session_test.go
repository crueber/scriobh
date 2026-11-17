package server

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// Mail decides what leaves the instance and to whom, so every mail.* and kindle.* op is for a person in a
// browser: an API key of any scope, pinned or not (and so the MCP endpoint, which speaks as a key), is refused, and
// none of them is a tool.
func TestMailAndKindleOpsRefuseKeysAndAreNoTools(t *testing.T) {
	w := newSiteWorld(t, nil)
	base := w.base()
	calls := []struct{ op, method, path, body string }{
		{"mail.config.get", "GET", "/api/v1/admin/mail", ""},
		{"mail.config.set", "PUT", "/api/v1/admin/mail", `{"enabled":false}`},
		{"mail.config.test", "POST", "/api/v1/admin/mail/test", `{}`},
		{"mail.outbox.status", "GET", "/api/v1/admin/mail/outbox", ""},
		{"mail.digest.get", "GET", "/api/v1/me/mail/digest", ""},
		{"mail.digest.set", "PUT", "/api/v1/me/mail/digest", `{"enabled":false}`},
		{"mail.progress.get", "GET", "/api/v1/me/mail/progress", ""},
		{"mail.progress.set", "PUT", "/api/v1/me/mail/progress", `{"enabled":false}`},
		{"kindle.address.get", "GET", "/api/v1/me/kindle", ""},
		{"kindle.address.set", "PUT", "/api/v1/me/kindle", `{"address":"a@kindle.com"}`},
		{"kindle.address.delete", "DELETE", "/api/v1/me/kindle", ""},
		{"kindle.preview", "POST", base + "/kindle/preview", `{}`},
		{"kindle.send", "POST", base + "/kindle/send", `{"token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`},
		{"kindle.log", "GET", base + "/kindle/log", ""},
	}
	registered := map[string]bool{}
	for _, e := range w.s.Reg.Entries() {
		if strings.HasPrefix(e.Op.Name, "mail.") || strings.HasPrefix(e.Op.Name, "kindle.") {
			registered[e.Op.Name] = true
		}
	}
	if len(registered) != len(calls) {
		t.Fatalf("%d mail and kindle ops are registered, %d are covered here", len(registered), len(calls))
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
			if !registered[o.op] {
				t.Errorf("%s is not registered", o.op)
			}
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
	// A person at the browser gets past authorization.
	if rec := w.s.do(w.admin, "GET", "/api/v1/admin/mail", ""); rec.Code != 200 {
		t.Errorf("the administrator's settings: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.owner, "GET", "/api/v1/admin/mail", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a writer reading the relay's settings: %d", rec.Code)
	}
	if rec := w.s.do(w.reader, "POST", base+"/kindle/preview", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("a reader previewing: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.stranger, "POST", base+"/kindle/preview", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger previewing: %d %s", rec.Code, rec.Body)
	}
}

// What goes to a Kindle is what a reader sees: no private notes, ghosted text, alternatives, notes or documents
// marked do not compile, and not the author's contact block. It is checked on the file that waits in the outbox,
// made by the real compile through the real ops.
func TestAKindleEditionHoldsNoNotesGhostsOrContactBlock(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.must(t, w.owner, "PATCH", w.base()+"/compile/contact", `{"name":"Máire","address":"CONTACT-ADDRESS-CANARY","email":"contact-canary@example.org","phone":"CONTACT-PHONE-CANARY"}`)
	alts := `<!-- scriobh:alts {"alt":"ALT-CANARY"} -->`
	w.doc(t, "Opening", "chapter", "", "The harbour woke. %%PRIVATE-CANARY%% It was <scriobh-ghost>GHOST-CANARY</scriobh-ghost>cold.\n\n"+alts+"\n", "done", "")
	w.doc(t, "Draft", "chapter", "", "NOCOMPILE-CANARY\n", "draft", `,"include_in_compile":false`)
	w.doc(t, "Ideas", "note", "", "NOTE-CANARY\n", "idea", "")
	w.must(t, w.admin, "PUT", "/api/v1/admin/mail", `{"host":"127.0.0.1","port":2525,"security":"none","allow_private":true,"from_address":"scriobh@example.org","enabled":true}`)
	w.must(t, w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor_kindle@kindle.com"}`)
	pv := w.must(t, w.editor, "POST", w.base()+"/kindle/preview", `{}`)
	var preview mail.KindlePreviewOut
	if err := json.Unmarshal(pv.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Recipient != "editor_kindle@kindle.com" || !strings.HasSuffix(preview.FileName, ".epub") || preview.Bytes < 1000 || preview.Token == "" {
		t.Fatalf("preview: %+v", preview)
	}
	sent := w.must(t, w.editor, "POST", w.base()+"/kindle/send", fmt.Sprintf(`{"token":%q}`, preview.Token))
	var out mail.KindleSendOut
	if err := json.Unmarshal(sent.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(w.s.Cfg.DataDir, "mail", "outbox", out.ID))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for name, data := range unzip(t, b) {
		sb.WriteString(name)
		sb.Write(data)
	}
	text := sb.String()
	if !strings.Contains(text, "harbour woke") {
		t.Fatal("the chapter is not in the edition")
	}
	for _, canary := range []string{"PRIVATE-CANARY", "GHOST-CANARY", "ALT-CANARY", "NOCOMPILE-CANARY", "NOTE-CANARY", "CONTACT-ADDRESS-CANARY", "CONTACT-PHONE-CANARY", "contact-canary"} {
		if strings.Contains(text, canary) {
			t.Errorf("the edition holds %s", canary)
		}
	}
	// A person who owns no such address gets nothing: the owner has not stored one.
	if rec := w.s.do(w.owner, "POST", w.base()+"/kindle/preview", `{}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_kindle_address") {
		t.Errorf("an owner with no address: %d %s", rec.Code, rec.Body)
	}
	// The log says who. The full address is for the project's owner (and administrators), sealed in the database;
	// the editor who sent it sees the domain only.
	lg := w.must(t, w.owner, "GET", w.base()+"/kindle/log", "")
	if !strings.Contains(lg.Body.String(), "editor_kindle@kindle.com") {
		t.Errorf("the owner's log: %s", lg.Body)
	}
	if el := w.must(t, w.editor, "GET", w.base()+"/kindle/log", ""); strings.Contains(el.Body.String(), "editor_kindle") || !strings.Contains(el.Body.String(), "kindle.com") {
		t.Errorf("the editor's log: %s", el.Body)
	}
	// The same compile, run with the options a person might set, shows the forcing is what keeps the block out.
	p := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "session"}
	raw, err := w.s.Reg.Call(t.Context(), p, "export.compile", compile.CompileIn{ProjectID: w.pid, Preset: "epub3", Options: publish.Options{Notes: "appendix", Ghosts: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	var loose strings.Builder
	for _, data := range unzip(t, raw.(compile.Out).Body) {
		loose.Write(data)
	}
	if !strings.Contains(loose.String(), "NOTE-CANARY") && !strings.Contains(loose.String(), "GHOST-CANARY") {
		t.Error("the options that the Kindle edition forces off change nothing in the compile, so this test would pass without them")
	}
}

// A digest holds a project's title and counts, and nothing a person wrote: not the manuscript, not a comment, not
// a document's title, not the name of an entity in the bible, not another person's name. Made with the real ops: a
// comment by the editor is what raises the owner's notification.
func TestADigestHoldsNoManuscriptCommentDocumentTitleOrEntityName(t *testing.T) {
	w := newSiteWorld(t, nil)
	p := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "session"}
	call := func(op string, in any) any {
		t.Helper()
		out, err := w.s.Reg.Call(t.Context(), p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	cid := call("compendium.create", compendiums.CreateIn{Name: "BIBLE-CANARY"}).(compendiums.Item).Item.ID
	call("entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "ENTITY-CANARY"})
	call("project.compendium.set", compendiums.SetIn{ID: w.pid, CompendiumID: cid})
	doc := w.doc(t, "DOCTITLE-CANARY", "chapter", "", "MANUSCRIPT-CANARY ENTITY-CANARY walked the quay.\n", "draft", "")
	w.must(t, w.editor, "POST", w.base()+"/comments", fmt.Sprintf(`{"document_id":%q,"body":"COMMENTTEXT-CANARY about ENTITY-CANARY"}`, doc))
	body, err := w.s.Mail.BuildDigest(t.Context(), w.owner.userID, "", "9999-12-31T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "The Salt Road") || !strings.Contains(body, "1 comment") {
		t.Fatalf("the digest does not say what is new:\n%s", body)
	}
	for _, canary := range []string{"MANUSCRIPT-CANARY", "COMMENTTEXT-CANARY", "DOCTITLE-CANARY", "ENTITY-CANARY", "BIBLE-CANARY", "editor@test.local", "editor"} {
		if strings.Contains(body, canary) {
			t.Errorf("the digest holds %q:\n%s", canary, body)
		}
	}
}

// The relay's password is not in the administrator's own views of the server either: the settings, the
// diagnostics and the diagnostics download.
func TestTheRelayPasswordIsNotInTheDiagnostics(t *testing.T) {
	w := newSiteWorld(t, nil)
	const pw = "RELAY-PASSWORD-CANARY-31337"
	rec := w.must(t, w.admin, "PUT", "/api/v1/admin/mail", fmt.Sprintf(`{"host":"mail.example.org","security":"starttls","username":"relayuser","password":%q,"from_address":"scriobh@example.org"}`, pw))
	hay := []string{rec.Body.String(), w.must(t, w.admin, "GET", "/api/v1/admin/mail", "").Body.String(), w.must(t, w.admin, "GET", "/api/v1/admin/diagnostics", "").Body.String()}
	dl := w.must(t, w.admin, "GET", "/api/v1/admin/diagnostics/download", "")
	hay = append(hay, dl.Body.String())
	if zr, err := zip.NewReader(bytes.NewReader(dl.Body.Bytes()), int64(dl.Body.Len())); err == nil {
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			hay = append(hay, string(b))
		}
	}
	for i, h := range hay {
		for _, form := range []string{pw, base64.StdEncoding.EncodeToString([]byte(pw)), base64.RawURLEncoding.EncodeToString([]byte(pw))} {
			if strings.Contains(h, form) {
				t.Errorf("the password is in view %d", i)
			}
		}
	}
	if !strings.Contains(hay[1], `"password_set":true`) {
		t.Errorf("the settings do not say a password is stored: %s", hay[1])
	}
}
