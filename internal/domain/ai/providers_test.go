package ai_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

func TestAKeyIsSealedAndNeverReturnedOrLogged(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	pid := e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	// Used, so every code path that opens the key has run.
	if _, err := e.run(e.owner, e.freeform("Hello")); err != nil {
		t.Fatal(err)
	}
	e.call(e.admin, "ai.provider.test", aicore.ProviderIn{ID: pid})

	// Not in any op result.
	for _, p := range []ops.Principal{e.admin, e.owner} {
		raw, _ := json.Marshal(e.call(p, "ai.provider.list", struct{}{}))
		if strings.Contains(string(raw), planted) || strings.Contains(string(raw), "api_key_cipher") {
			t.Fatalf("the list holds the key: %s", raw)
		}
		raw, _ = json.Marshal(e.call(p, "ai.provider.get", aicore.ProviderIn{ID: pid}))
		if strings.Contains(string(raw), planted) {
			t.Fatalf("get holds the key: %s", raw)
		}
	}
	list := e.call(e.admin, "ai.provider.list", struct{}{}).(aicore.ProviderListOut)
	if !list.Items[0].HasKey || list.Items[0].KeyHint != "…cdef" {
		t.Errorf("provider %+v", list.Items[0])
	}
	// Not in the database, the audit log, the events, the log, or the data directory.
	if strings.Contains(e.dump(), planted) {
		t.Error("the plain key is in the database")
	}
	var sealed string
	_ = e.d.Read().QueryRow(`SELECT api_key_cipher FROM ai_providers`).Scan(&sealed)
	if !strings.HasPrefix(sealed, "v1.") {
		t.Errorf("not sealed: %q", sealed)
	}
	if strings.Contains(e.log.String(), planted) {
		t.Error("the plain key is in the log")
	}
	_ = filepath.WalkDir(e.dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), planted) {
				t.Errorf("the key is in %s", path)
			}
		}
		return nil
	})
	// The provider got it once per request, in the header only; never in a body.
	for _, r := range f.requests() {
		if strings.Contains(string(r.Body), planted) {
			t.Error("the key is in a request body")
		}
	}
}

func TestAKeyBelongsToOneAddress(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	pid := e.addProvider(f, "Main", false)
	other := newFake(t, nil)
	url := other.srv.URL + "/v1"
	out := e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, BaseURL: &url}).(aicore.ProviderItem).Item
	if out.HasKey {
		t.Fatal("moving a provider to another address must drop its key")
	}
	var sealed string
	_ = e.d.Read().QueryRow(`SELECT api_key_cipher FROM ai_providers`).Scan(&sealed)
	if sealed != "" {
		t.Fatal("the old ciphertext was kept")
	}
	// A key sent with the move is sealed for the new address.
	third := newFake(t, nil)
	url3, key := third.srv.URL+"/v1", planted
	out = e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, BaseURL: &url3, APIKey: &key}).(aicore.ProviderItem).Item
	if !out.HasKey {
		t.Fatal("the new key was not stored")
	}
	// A ciphertext copied to another row, or a row re-pointed in the database, does not open.
	e.enable("ask", nil)
	e.addProvider(newFake(t, nil), "Second", false)
	var cipher string
	_ = e.d.Read().QueryRow(`SELECT api_key_cipher FROM ai_providers WHERE name = 'Main'`).Scan(&cipher)
	_, _ = e.d.Writer().Exec(`UPDATE ai_providers SET api_key_cipher = ? WHERE name = 'Second'`, cipher)
	_, err := e.try(e.owner, "ai.context.preview", aicore.RunIn{ProjectID: e.project, Recipe: "freeform", Provider: secondID(t, e), Params: map[string]string{"instruction": "x"}})
	if err != nil {
		t.Fatal(err) // a preview never opens the key
	}
	in := e.freeform("x")
	in.Provider = secondID(t, e)
	_, rerr := e.run(e.owner, in)
	if rerr == nil || rerr.Code != ops.CodeConflict || !strings.Contains(rerr.Message, "cannot be read") {
		t.Fatalf("a copied ciphertext must not open: %v", rerr)
	}
	if n := len(third.requests()) + len(other.requests()); n != 0 {
		t.Fatalf("a provider got %d requests with a key that should not open", n)
	}
}

func secondID(t *testing.T, e *env) string {
	var id string
	if err := e.d.Read().QueryRow(`SELECT id FROM ai_providers WHERE name = 'Second'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProviderAuthority(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	// A writer cannot add an instance provider, or mark anything local.
	_, err := e.try(e.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "x", Kind: lib.KindOpenAICompatible, Scope: "instance", BaseURL: f.srv.URL + "/v1"})
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(e.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "x", Kind: lib.KindOllama, BaseURL: f.srv.URL, Local: true})
	wantCode(t, err, ops.CodeForbidden)
	// A writer's own provider is invisible to everyone else, administrators included.
	mine := e.call(e.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: lib.KindOpenAICompatible, BaseURL: f.srv.URL + "/v1", APIKey: planted}).(aicore.ProviderItem).Item
	if mine.Scope != "user" || !mine.Editable {
		t.Fatalf("%+v", mine)
	}
	for _, p := range []ops.Principal{e.admin, e.editor} {
		l := e.call(p, "ai.provider.list", struct{}{}).(aicore.ProviderListOut)
		for _, it := range l.Items {
			if it.ID == mine.ID {
				t.Fatalf("%s sees another person's provider", p.Name)
			}
		}
		_, err = e.try(p, "ai.provider.get", aicore.ProviderIn{ID: mine.ID})
		wantCode(t, err, ops.CodeNotFound)
		_, err = e.try(p, "ai.provider.delete", aicore.ProviderIn{ID: mine.ID})
		wantCode(t, err, ops.CodeNotFound)
	}
	// An instance provider: everyone sees it, nobody but an administrator changes it, and an
	// editor does not see its address.
	pid := e.addProvider(f, "Shared", false)
	got := e.call(e.editor, "ai.provider.get", aicore.ProviderIn{ID: pid}).(aicore.ProviderItem).Item
	if got.BaseURL != "" || got.Editable || got.HasKey != true {
		t.Fatalf("editor's view %+v", got)
	}
	name := "Renamed"
	_, err = e.try(e.editor, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, Name: &name})
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(e.editor, "ai.provider.delete", aicore.ProviderIn{ID: pid})
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(e.editor, "ai.provider.test", aicore.ProviderIn{ID: pid})
	wantCode(t, err, ops.CodeForbidden)
	if a := e.call(e.admin, "ai.provider.get", aicore.ProviderIn{ID: pid}).(aicore.ProviderItem).Item; a.BaseURL == "" || !a.Editable {
		t.Fatalf("admin's view %+v", a)
	}
	// An editor can name an instance provider in a policy only if the owner does; a
	// stranger's personal one is refused there.
	mode := "ask"
	ids := []string{mine.ID}
	_, err = e.try(e.editor, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode, Providers: &ids})
	wantCode(t, err, ops.CodeForbidden) // the owner only
	_, err = e.try(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode, Providers: &ids})
	if err != nil {
		t.Fatal("the owner may name their own provider:", err)
	}
	ids = []string{"aip_00000000000000000000000000"}
	_, err = e.try(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Providers: &ids})
	wantCode(t, err, ops.CodeInvalid)
	// Personal providers can be switched off for the server.
	e2 := newEnv(t, func(o *aicore.Options) { o.Config.NoUserProviders = true })
	_, err = e2.try(e2.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: lib.KindOpenAICompatible, BaseURL: f.srv.URL + "/v1"})
	wantCode(t, err, ops.CodeConflict)
}

// The address rules, at creation.
func TestProviderAddressesAreChecked(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.AllowPlainHTTP = false })
	e.loopbackLocal.Store(true) // the real classifier's view: loopback is local
	create := func(p ops.Principal, url string, local bool) *ops.Error {
		_, err := e.try(p, "ai.provider.create", aicore.ProviderCreateIn{Name: "p" + url, Kind: lib.KindOpenAICompatible, BaseURL: url, Local: local})
		return err
	}
	for _, c := range []struct {
		url  string
		why  string
		code ops.Code
	}{
		{"https://user:pass@api.example.com/v1", "credentials in the address", ops.CodeInvalid},
		{"https://api.example.com/v1?key=abc", "a query string", ops.CodeInvalid},
		{"ftp://api.example.com", "another scheme", ops.CodeInvalid},
		{"http://api.example.com/v1", "plain http to a public host", ops.CodeInvalid},
		{"https://169.254.169.254/latest", "the cloud metadata address", ops.CodeInvalid},
		{"https://[fd00:ec2::254]/", "the IPv6 metadata address", ops.CodeInvalid},
		{"https://127.0.0.1/v1", "loopback without local", ops.CodeInvalid},
		{"https://localhost/v1", "localhost without local", ops.CodeInvalid},
		{"https://10.0.0.5/v1", "a private address without local", ops.CodeInvalid},
		{"https://192.168.1.20:11434", "a LAN address without local", ops.CodeInvalid},
		{"https://100.64.0.9", "carrier-grade NAT without local", ops.CodeInvalid},
		{"https://[::1]/", "IPv6 loopback without local", ops.CodeInvalid},
		{"", "empty", ops.CodeInvalid},
	} {
		if err := create(e.admin, c.url, false); err == nil || err.Code != c.code {
			t.Errorf("%s (%s) was not refused: %v", c.url, c.why, err)
		}
	}
	// An administrator may declare a local endpoint; the metadata address is never allowed, local or not.
	if err := create(e.admin, "http://192.168.1.20:11434", true); err != nil {
		t.Errorf("a declared LAN provider: %v", err)
	}
	if err := create(e.admin, "http://127.0.0.1:4096", true); err != nil {
		t.Errorf("a declared loopback provider: %v", err)
	}
	if err := create(e.admin, "http://169.254.169.254", true); err == nil {
		t.Error("the metadata address must never be allowed, even as local")
	}
	if err := create(e.owner, "https://api.example.com/v1", false); err != nil {
		t.Errorf("a writer's public https provider: %v", err)
	}
}

func TestTestingAProviderSaysWhatWentWrongInPlainWords(t *testing.T) {
	e := newEnv(t, nil)
	cases := map[string]struct {
		status int
		class  string
	}{"401": {401, lib.ClassAuth}, "402": {402, lib.ClassCredit}, "429": {429, lib.ClassRate}, "503": {503, lib.ClassUnavailable}, "400": {400, lib.ClassRejected}}
	for name, c := range cases {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte, n int) {
			http.Error(w, `{"error":{"message":"detail with `+planted+` and http://10.1.2.3/secret"}}`, c.status)
		})
		pid := e.addProvider(f, "p"+name, false)
		out := e.call(e.admin, "ai.provider.test", aicore.ProviderIn{ID: pid}).(aicore.TestOut)
		if out.OK || out.Class != c.class || out.Message == "" || out.Status != c.status {
			t.Errorf("%s: %+v", name, out)
		}
		if strings.Contains(out.Message, "10.1.2.3") || strings.Contains(out.Message, planted) || strings.Contains(out.Message, f.srv.URL) {
			t.Errorf("%s: the message echoes provider text or an address: %q", name, out.Message)
		}
	}
	// Unreachable: a closed port.
	dead := newFake(t, nil)
	pid := e.addProvider(dead, "dead", false)
	dead.srv.Close()
	out := e.call(e.admin, "ai.provider.test", aicore.ProviderIn{ID: pid}).(aicore.TestOut)
	if out.OK || out.Class != lib.ClassUnreachable || strings.Contains(out.Message, "127.0.0.1") {
		t.Errorf("unreachable: %+v", out)
	}
	// The good case lists models, and they can be fetched and kept with manual ones.
	f := newFake(t, nil)
	gid := e.addProvider(f, "good", false)
	out = e.call(e.admin, "ai.provider.test", aicore.ProviderIn{ID: gid}).(aicore.TestOut)
	if !out.OK || out.Models != 2 {
		t.Errorf("good: %+v", out)
	}
	e.call(e.admin, "ai.provider.model.set", aicore.ModelSetIn{ID: gid, Model: "custom-7b", ContextWindow: 8192})
	ms := e.call(e.admin, "ai.provider.models.refresh", aicore.ProviderIn{ID: gid}).(aicore.ModelsOut)
	byID := map[string]aicore.ModelInfo{}
	for _, m := range ms.Items {
		byID[m.ID] = m
	}
	if !byID["custom-7b"].Manual || byID["model-a"].ContextWindow != 32768 || byID["model-a"].InputMicros == nil || *byID["model-a"].InputMicros != 1_000_000 {
		t.Errorf("models %+v", ms.Items)
	}
	if e.count(`SELECT count(*) FROM ai_providers WHERE models_fetched_at <> ''`) != 1 {
		t.Error("the list was not cached")
	}
	cached := e.call(e.editor, "ai.provider.models", aicore.ProviderIn{ID: gid}).(aicore.ModelsOut)
	if len(cached.Items) != 3 {
		t.Errorf("cached %+v", cached.Items)
	}
}
