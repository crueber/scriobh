package ai_test

import (
	"net/http"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// The hash is the approval, so it covers where the text goes: an administrator who
// moves a provider between the preview and the run does not get the text sent to the
// new address on the strength of the old approval.
func TestTheApprovalCoversTheDestination(t *testing.T) {
	e := newEnv(t, nil)
	approved, elsewhere := newFake(t, nil), newFake(t, nil)
	pid := e.addProvider(approved, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	moved := elsewhere.srv.URL + "/v1"
	key := planted
	e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, BaseURL: &moved, APIKey: &key})
	_, err := e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeConflict)
	if len(approved.requests())+len(elsewhere.requests()) != 0 {
		t.Fatal("the approved text was sent after the destination changed")
	}
	// The same preview taken after the move is what runs.
	pv = e.preview(e.editor, e.freeform("Hello"))
	in2 := e.freeform("Hello")
	in2.Nonce, in2.PayloadHash = pv.Nonce, pv.PayloadHash
	if _, err := e.try(e.editor, "ai.run", in2); err != nil {
		t.Fatal(err)
	}
	if len(elsewhere.requests()) != 1 {
		t.Error("the new address did not get the request")
	}
	// Declaring the provider local (or not) is a change of destination too.
	h1 := e.preview(e.editor, e.freeform("Hello")).PayloadHash
	e.loopbackLocal.Store(true)
	yes := true
	e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, Local: &yes})
	if h2 := e.preview(e.editor, e.freeform("Hello")).PayloadHash; h2 == h1 {
		t.Error("making a provider local did not change the hash")
	}
}

// A provider declared local is dialled as a local one only: if its name now leads to a
// public address the connection is refused in every project, not just local-only ones.
// Otherwise its key and the text would go, in the clear if it is an http address, to
// a place shown as local and charged as free.
func TestALocalProviderNeverReachesAPublicAddress(t *testing.T) {
	e := newEnv(t, nil)
	srv := newFake(t, nil)
	e.addProvider(srv, "Claims to be local", true) // declared local; the guard sees a public address
	e.enable("ask", nil)                           // not a local-only project
	_, err := e.run(e.editor, e.freeform("Hello"))
	wantCode(t, err, ops.CodeProvider)
	if err.Data["provider_class"] != lib.ClassBlocked || len(srv.requests()) != 0 {
		t.Fatalf("%v, %d requests", err.Data, len(srv.requests()))
	}
	if n := e.count(`SELECT count(*) FROM ai_usage_daily WHERE cost_micros <> 0`); n != 0 {
		t.Error("charged for a connection that was refused")
	}
}

// What an editor learns about an instance provider: that it exists, what it is called, its kind
// and models. Not how it is configured.
func TestInstanceProviderConfigurationIsForItsAdministrators(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	item := e.call(e.admin, "ai.provider.create", aicore.ProviderCreateIn{Name: "Shared", Kind: lib.KindOpenCode, Scope: "instance", BaseURL: f.srv.URL,
		APIKey: planted, Username: "svc-account", KeepAlive: "30m", DefaultModel: "m",
		Headers: []lib.Header{{Name: "OpenAI-Organization", Value: "org-internal-123"}}, Local: false}).(aicore.ProviderItem).Item
	for _, p := range []ops.Principal{e.editor, e.owner} {
		got := e.call(p, "ai.provider.get", aicore.ProviderIn{ID: item.ID}).(aicore.ProviderItem).Item
		if got.Headers != nil || got.Username != "" || got.KeepAlive != "" || got.BaseURL != "" || got.KeyHint != "" || !got.HasKey {
			t.Errorf("%s sees how it is configured: %+v", p.Name, got)
		}
	}
	got := e.call(e.admin, "ai.provider.get", aicore.ProviderIn{ID: item.ID}).(aicore.ProviderItem).Item
	if len(got.Headers) != 1 || got.Username != "svc-account" || got.KeepAlive != "30m" || got.BaseURL == "" || got.KeyHint != "…cdef" {
		t.Errorf("the administrator's view: %+v", got)
	}
	// A short key gets no hint at all; four characters of eight is half of it.
	short := e.call(e.admin, "ai.provider.create", aicore.ProviderCreateIn{Name: "Short", Kind: lib.KindOpenAICompatible, Scope: "instance", BaseURL: f.srv.URL + "/v1", APIKey: "sk-12345"}).(aicore.ProviderItem).Item
	if !short.HasKey || short.KeyHint != "" {
		t.Errorf("%+v", short)
	}
	// A person's own provider is shown to them whole.
	mine := e.call(e.editor, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: lib.KindOpenAICompatible, BaseURL: f.srv.URL + "/v1", APIKey: planted, Username: "me"}).(aicore.ProviderItem).Item
	if mine.Username != "me" || mine.BaseURL == "" || mine.KeyHint != "…cdef" {
		t.Errorf("%+v", mine)
	}
}

// ai.provider.test and the model refresh answer a person who is not an administrator with
// "it failed", never which of blocked, refused or unreachable it was: that would turn them
// into a scanner of the server's network.
func TestProviderTestIsNoOracleForNonAdministrators(t *testing.T) {
	e := newEnv(t, nil)
	e.loopbackLocal.Store(true) // the real classifier's view: an address on this machine is not public
	hit := newFake(t, nil)
	mine := e.call(e.editor, "ai.provider.create", aicore.ProviderCreateIn{Name: "Probe", Kind: lib.KindOpenAICompatible, BaseURL: "https://example.org/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	// Re-pointed (as the database could be) at the server's own network, and at a closed port, and a 401.
	for name, target := range map[string]string{"loopback": hit.srv.URL + "/v1", "closed": "https://127.0.0.1:1/v1"} {
		if _, err := e.d.Writer().Exec(`UPDATE ai_providers SET base_url = ?, api_key_cipher = '' WHERE id = ?`, target, mine); err != nil {
			t.Fatal(err)
		}
		out := e.call(e.editor, "ai.provider.test", aicore.ProviderIn{ID: mine}).(aicore.TestOut)
		if out.OK || out.Class != "" || out.Status != 0 || !strings.Contains(out.Message, "could not be used") {
			t.Errorf("%s: %+v", name, out)
		}
		_, err := e.try(e.editor, "ai.provider.models.refresh", aicore.ProviderIn{ID: mine})
		wantCode(t, err, ops.CodeProvider)
		if err.Data != nil || strings.Contains(err.Message, "address") && !strings.Contains(err.Message, "Check its address") {
			t.Errorf("%s: refresh said %+v %q", name, err.Data, err.Message)
		}
	}
	if len(hit.requests()) != 0 {
		t.Fatal("a non-local provider reached the server's own address")
	}
	// An administrator, testing their own, is told the class.
	adm := e.call(e.admin, "ai.provider.create", aicore.ProviderCreateIn{Name: "Admin", Kind: lib.KindOpenAICompatible, BaseURL: "https://example.org/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	_, _ = e.d.Writer().Exec(`UPDATE ai_providers SET base_url = ? WHERE id = ?`, hit.srv.URL+"/v1", adm)
	if out := e.call(e.admin, "ai.provider.test", aicore.ProviderIn{ID: adm}).(aicore.TestOut); out.Class != lib.ClassBlocked {
		t.Errorf("%+v", out)
	}
}

func TestTestAndRefreshAreRateLimitedPerPerson(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) { oaiReply(w, r, b, "x") })
	pid := e.addProvider(f, "Main", false)
	limited := 0
	for i := 0; i < 40; i++ {
		var err *ops.Error
		if i%2 == 0 {
			_, err = e.try(e.admin, "ai.provider.models.refresh", aicore.ProviderIn{ID: pid})
		} else {
			_, err = e.try(e.admin, "ai.provider.test", aicore.ProviderIn{ID: pid})
		}
		if err != nil {
			if err.Code != ops.CodeRateLimited {
				t.Fatalf("%v", err)
			}
			limited++
		}
	}
	if limited < 20 {
		t.Fatalf("only %d of 40 were refused", limited)
	}
	if n := len(f.requests()); n > 20 {
		t.Errorf("%d requests reached the provider", n)
	}
}
