package ai_test

import (
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// Switching AI on for a project must not let a collaborator point it at a host they
// control: a personal provider needs the project owner's say-so.
func TestAPersonalProviderNeedsTheOwnersSayso(t *testing.T) {
	e := newEnv(t, nil)
	instance := newFake(t, nil)
	iid := e.addProvider(instance, "Instance", false)
	theirs := newFake(t, nil)
	mine := e.call(e.editor, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: lib.KindOpenAICompatible, BaseURL: theirs.srv.URL + "/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	e.enable("ask", nil) // the owner turns AI on; nothing else
	in := e.freeform("Hello")
	// By chain order the editor's own provider comes after the instance's, so name it.
	in.Provider = mine
	pv := e.preview(e.editor, in)
	if pv.Decision.Allowed || len(pv.Attempts) != 0 {
		t.Fatalf("a personal provider was usable without the owner's say-so: %+v", pv.Decision)
	}
	if len(pv.SkippedProviders) != 1 || pv.SkippedProviders[0].Reason != "not_allowed" || !strings.Contains(pv.SkippedProviders[0].Message, "personal") {
		t.Errorf("%+v", pv.SkippedProviders)
	}
	// With no provider named, the instance's is used, never theirs.
	pv = e.preview(e.editor, e.freeform("Hello"))
	if !pv.Decision.Allowed || pv.Attempts[0].ProviderID != iid {
		t.Fatalf("%+v %+v", pv.Decision, pv.Attempts)
	}
	for _, a := range pv.Attempts {
		if a.ProviderID == mine {
			t.Error("the personal provider is in the chain")
		}
	}
	// Even a chain that names it (an owner cannot, a stale one could) does not use it.
	if len(theirs.requests()) != 0 {
		t.Fatal("text reached a personal provider")
	}
	// The owner allows personal providers: now it is usable.
	yes := true
	e.enable("ask", func(p *aicore.PolicySetIn) { p.AllowPersonal = &yes })
	if pv := e.preview(e.editor, in); !pv.Decision.Allowed || pv.Attempts[0].ProviderID != mine {
		t.Fatalf("%+v", pv.Decision)
	}
	// Or the owner names one of their own, without turning the flag on.
	e.enable("ask", func(p *aicore.PolicySetIn) { no := false; p.AllowPersonal = &no })
	own := e.call(e.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "Owner's", Kind: lib.KindOpenAICompatible, BaseURL: theirs.srv.URL + "/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	e.enable("ask", func(p *aicore.PolicySetIn) { l := []string{own}; p.Providers = &l })
	in.Provider = own
	if pv := e.preview(e.owner, in); !pv.Decision.Allowed {
		t.Errorf("a provider the owner named: %+v", pv.Decision)
	}
	if e.call(e.owner, "ai.policy.get", aicore.PolicyGetIn{ProjectID: e.project}).(aicore.PolicyOut).Item.AllowPersonal {
		t.Error("the flag should be off")
	}
	_, err := e.try(e.editor, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, AllowPersonal: &yes})
	wantCode(t, err, ops.CodeForbidden)
}
