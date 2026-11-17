package server

import (
	"encoding/json"
	"testing"
)

// aiFixtures drives the AI ops that a preview can show through the dry-run
// sweep (and, run for real, through the event payload audit). The ones that
// spend money or dial out (ai.run, ai.provider.test, ai.provider.models.refresh)
// are in noPreview: they must refuse a preview.
func (f *dryFixture) aiFixtures(t *testing.T) map[string]drive {
	t.Helper()
	rec := f.srv.do(f.owner, "POST", "/api/v1/ai/providers", `{"name":"Fixture provider","kind":"openai_compatible","base_url":"https://api.example.com/v1","api_key":"sk-fixture-key-0123456789","default_model":"m"}`)
	if rec.Code >= 300 {
		t.Fatalf("fixture provider: %d %s", rec.Code, rec.Body)
	}
	var pv struct{ Item struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pv)
	pid := pv.Item.ID
	p := "/api/v1/projects/" + f.project + "/ai"
	return map[string]drive{
		"ai.provider.create":       {"/api/v1/ai/providers", `{"name":"Previewed","kind":"ollama","base_url":"https://ollama.example.com"}`, true},
		"ai.provider.update":       {"/api/v1/ai/providers/" + pid, `{"name":"Renamed","enabled":false}`, true},
		"ai.provider.delete":       {"/api/v1/ai/providers/" + pid, "", true},
		"ai.provider.model.set":    {"/api/v1/ai/providers/" + pid + "/models", `{"model":"custom","context_window":8192}`, true},
		"ai.provider.model.remove": {"/api/v1/ai/providers/" + pid + "/models?model=custom", "", false},
		"ai.price.set":             {"/api/v1/ai/prices", `{"model":"m","input_micros_per_mtok":1000,"output_micros_per_mtok":2000}`, true},
		"ai.price.delete":          {"/api/v1/ai/prices?model=m", "", false},
		"ai.budget.set":            {"/api/v1/ai/budget", `{"scope":"instance","max_tokens":100000}`, true},
		"ai.settings.set":          {"/api/v1/ai/settings", `{"currency":"EUR"}`, true},
		"ai.policy.set":            {p + "/policy", `{"mode":"ask","redaction":"entities","style_guide":"Short sentences."}`, true},
		"ai.exclude.set":           {p + "/exclude", `{"kind":"document","id":"` + f.doc + `","exclude":true}`, true},
		"ai.context.preview":       {p + "/preview", `{"recipe":"freeform","params":{"instruction":"x"}}`, false},
		"ai.run.cancel":            {p + "/run/cancel", `{"run_id":"air_00000000000000000000000000"}`, false},
		// the recipes' layer: what is kept, and what can be previewed without a provider
		"ai.style.set":    {p + "/style", `{"style_guide":"Short sentences."}`, true},
		"ai.defaults.set": {p + "/defaults", `{"scope":"user","class":"cheap","chain":[{"provider_id":"` + pid + `"}]}`, true},
		"ai.lab.analyse":  {p + "/lab/analyse", `{"document_id":"` + f.doc + `"}`, false},
		"ai.chat.create":  {p + "/chats", `{"title":"A chat"}`, true},
	}
}
