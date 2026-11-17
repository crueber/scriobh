package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// What leaves the server is decided by people, in a browser. An API key, at any
// scope, pinned or not, owned by the project's owner or by an administrator, cannot
// turn AI on, let agents in, lift a never-send flag, raise a budget or change a
// price: a prompt-injected agent must not be able to open the door it then walks through.
func TestAConfigurationOpIsRefusedToEveryKey(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	op := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(t.Context(), op, "project.create", projects.CreateIn{Title: "Guarded"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	d, err := s.Reg.Call(t.Context(), op, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: "Words."})
	if err != nil {
		t.Fatal(err)
	}
	did := d.(docs.Item).Item.ID
	base := "/api/v1/projects/" + pid + "/ai"
	calls := []struct{ who, method, path, body string }{
		{"owner", "PUT", base + "/policy", `{"mode":"ask","agents_may_use_ai":true}`},
		{"owner", "PUT", base + "/exclude", fmt.Sprintf(`{"kind":"document","id":%q,"exclude":false}`, did)},
		{"admin", "PUT", "/api/v1/ai/budget", `{"scope":"instance","max_tokens":1}`},
		{"admin", "PUT", "/api/v1/ai/settings", `{"enabled":true}`},
		{"admin", "PUT", "/api/v1/ai/prices", `{"model":"m","input_micros_per_mtok":1,"output_micros_per_mtok":1}`},
		{"admin", "DELETE", "/api/v1/ai/prices?model=m", ""},
	}
	// Flag the document and switch AI on from the browser, as the person would.
	if rec := s.do(owner, "PUT", base+"/exclude", fmt.Sprintf(`{"kind":"document","id":%q,"exclude":true}`, did)); rec.Code != 200 {
		t.Fatalf("session exclude: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "PUT", base+"/policy", `{"mode":"off"}`); rec.Code != 200 {
		t.Fatalf("session policy: %d %s", rec.Code, rec.Body)
	}
	for _, owned := range []struct {
		name string
		by   caller
	}{{"owner", owner}, {"admin", admin}} {
		for _, scope := range []string{"read", "write", "admin"} {
			if scope == "admin" && owned.name != "admin" {
				continue // a writer cannot mint an admin-scope key
			}
			for _, pin := range []string{"", pid} {
				body := fmt.Sprintf(`{"name":"k","scope":%q}`, scope)
				if pin != "" {
					body = fmt.Sprintf(`{"name":"k","scope":%q,"project_id":%q}`, scope, pin)
				}
				secret, _ := s.mintKey(t, owned.by, body)
				for _, c := range calls {
					rec := s.doKey(secret, c.method, c.path, c.body)
					if rec.Code != http.StatusForbidden {
						t.Errorf("a %s-scope key (pinned %v) of the %s: %s %s = %d %s", scope, pin != "", owned.name, c.method, c.path, rec.Code, rec.Body)
					}
				}
			}
		}
	}
	// Nothing was changed by the refused calls: AI is still off and the flag still stands.
	var mode string
	_ = s.DB.Read().QueryRow(`SELECT mode FROM ai_policies WHERE project_id = ?`, pid).Scan(&mode)
	var excluded int
	_ = s.DB.Read().QueryRow(`SELECT ai_exclude FROM documents WHERE id = ?`, did).Scan(&excluded)
	if mode != "off" || excluded != 1 {
		t.Errorf("mode %q, ai_exclude %d", mode, excluded)
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM ai_budgets`).Scan(&n)
	if n != 0 {
		t.Error("a key set a budget")
	}
	// The browser still can, and the schema says which ops are for browsers.
	for _, name := range []string{"ai.policy.set", "ai.exclude.set", "ai.budget.set", "ai.settings.set", "ai.price.set", "ai.price.delete",
		"ai.provider.create", "ai.provider.update", "ai.provider.delete", "ai.provider.test", "ai.provider.models.refresh", "ai.provider.model.set", "ai.provider.model.remove"} {
		e, ok := s.Reg.Lookup(name)
		if !ok || !e.Op.Auth.SessionOnly {
			t.Errorf("%s is not session-only", name)
		}
	}
	// Running and previewing stay open to a key, but only when the owner (in a session) allowed agents.
	for _, name := range []string{"ai.run", "ai.context.preview"} {
		if e, _ := s.Reg.Lookup(name); e.Op.Auth.SessionOnly {
			t.Errorf("%s must stay open to keys the owner allowed", name)
		}
	}
}
