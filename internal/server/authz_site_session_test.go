package server

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Publishing makes text public, which is the act share.create is for, so the ops
// that decide or perform what becomes public on the site are for a person at a
// browser: an API key of any scope, pinned or not (and so the MCP endpoint, which
// speaks as a key), is refused. Reading what would be published stays open.
func TestSitePublicationOpsRefuseKeys(t *testing.T) {
	w := newSiteWorld(t, nil)
	doc := w.doc(t, "Low water", "chapter", "", "A done chapter that must not leave without its owner's say.", "done", "")
	sessionOnly := []struct{ op, method, path, body string }{
		{"site.options.set", "PATCH", w.base() + "/site/options", `{"options":{"tagline":"set by a key"}}`},
		{"site.doc.set", "PATCH", w.base() + "/site/documents/" + doc, `{"tags":["by-key"]}`},
		{"site.publish", "POST", w.base() + "/site/publish", ""},
	}
	readable := []struct{ op, method, path, body string }{
		{"site.status", "GET", w.base() + "/site", ""},
		{"site.options.get", "GET", w.base() + "/site/options", ""},
		{"site.doc.list", "GET", w.base() + "/site/documents", ""},
		{"site.preview", "POST", w.base() + "/site/preview", ""},
	}
	keys := map[string]string{}
	for _, c := range []struct{ who, scope, pin string }{
		{"owner", "read", ""}, {"owner", "write", ""}, {"owner", "read", w.pid}, {"owner", "write", w.pid},
		{"editor", "write", ""}, {"editor", "write", w.pid}, {"editor", "read", w.pid},
		{"admin", "admin", ""}, {"admin", "admin", w.pid}, {"admin", "write", ""},
	} {
		caller := map[string]caller{"owner": w.owner, "editor": w.editor, "admin": w.admin}[c.who]
		body := fmt.Sprintf(`{"name":"%s %s","scope":%q`, c.who, c.scope, c.scope)
		if c.pin != "" {
			body += fmt.Sprintf(`,"project_id":%q`, c.pin)
		}
		secret, _ := w.s.mintKey(t, caller, body+"}")
		keys[fmt.Sprintf("%s %s pin=%v", c.who, c.scope, c.pin != "")] = secret
	}
	for who, key := range keys {
		for _, o := range sessionOnly {
			rec := w.s.doKey(key, o.method, o.path, o.body)
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
				t.Errorf("%s key, %s: %d %s", who, o.op, rec.Code, rec.Body)
			}
		}
		// Whatever the keys tried, nothing was published and nothing changed.
		if ents, _ := os.ReadDir(w.root); len(ents) != 0 {
			t.Errorf("%s key published to the site folder: %v", who, ents)
		}
		// The tool list does not offer them, and calling one by name is refused.
		names := w.s.allToolNames(t, key, "")
		for _, o := range sessionOnly {
			if has(names, strings.ReplaceAll(o.op, ".", "_")) {
				t.Errorf("%s key is offered the tool %s", who, o.op)
			}
		}
		// Calling one by name over /mcp, the way the reported exploit did, is refused too.
		for _, o := range sessionOnly {
			args := map[string]any{"id": w.pid}
			if o.op == "site.options.set" {
				args["options"] = map[string]any{"tagline": "set by a key"}
			}
			if o.op == "site.doc.set" {
				args["document_id"], args["tags"] = doc, []string{"by-key"}
			}
			r := w.s.modernCall(t, key, "tools/call", map[string]any{"name": strings.ReplaceAll(o.op, ".", "_"), "arguments": args})
			if r.msg.Error == nil && !strings.Contains(string(r.raw), `"isError":true`) {
				t.Errorf("%s key called the tool %s over MCP: %s", who, o.op, r.raw)
			}
		}
		for _, o := range readable[:3] { // the read ops stay open to a key that may read the project
			if rec := w.s.doKey(key, o.method, o.path, o.body); rec.Code != 200 {
				t.Errorf("%s key, %s: %d %s", who, o.op, rec.Code, rec.Body)
			}
		}
	}
	if opts := w.s.do(w.owner, "GET", w.base()+"/site/options", ""); strings.Contains(opts.Body.String(), "set by a key") {
		t.Errorf("a key changed the site options: %s", opts.Body)
	}
	// The preview and status tools stay on the list.
	names := w.s.allToolNames(t, keys["owner write pin=true"], "")
	for _, n := range []string{"site_status", "site_preview", "site_options_get", "site_doc_list"} {
		if !has(names, n) {
			t.Errorf("the read tool %s is gone from the list", n)
		}
	}
	// A person with a session still can.
	for _, o := range sessionOnly {
		if rec := w.s.do(w.owner, o.method, o.path, o.body); rec.Code != 200 {
			t.Errorf("the owner, %s: %d %s", o.op, rec.Code, rec.Body)
		}
	}
	if ents, _ := os.ReadDir(w.root); len(ents) == 0 {
		t.Error("the owner's publish wrote nothing")
	}
	// An editor at the browser too.
	if rec := w.s.do(w.editor, "PATCH", w.base()+"/site/options", `{"options":{"tagline":"by an editor"}}`); rec.Code != 200 {
		t.Errorf("an editor with a session: %d %s", rec.Code, rec.Body)
	}
	for _, o := range readable {
		if rec := w.s.do(w.reader, o.method, o.path, o.body); rec.Code == 200 {
			t.Errorf("a reader got %s", o.op)
		}
	}
}
