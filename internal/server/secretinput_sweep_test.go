package server

import (
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/idempotency"
)

// Every op whose input has a field that holds something a person typed to keep secret (a password, a token, a key, a
// passphrase, a PEM) must declare SecretInput: its request is then never fingerprinted or stored for idempotent replay
// (a stored hash of the body is a guessing oracle for a weak secret) and never previewed. A field that only looks like a
// secret is listed here with the reason.
var secretFieldName = regexp.MustCompile(`(?i)(token|password|secret|passphrase|api_key|pem|credential)`)

var notTypedSecrets = map[string]string{
	"import.preview.token":                  "not an input: the preview's own random one-time token comes back in the answer",
	"import.apply.token":                    "the random one-time token the preview issued, high entropy and single use",
	"import.append.token":                   "the random one-time token the preview issued, high entropy and single use",
	"import.git_apply.token":                "the random one-time token the preview issued",
	"kindle.send.token":                     "the random one-time token the preview issued, bound to person, project and address",
	"search.semantic.enable.consent_token":  "a random consent token made by the preview",
	"repo.publish.plan_token":               "a random token made by the plan",
	"mail.config.set.tls_ca_pem":            "a public certificate authority, not a secret",
	"git.trigger.set.rotate_secret":         "a boolean: rotate the trigger secret",
	"ai.policy.set.budget_tokens":           "a count of model tokens",
	"ai.run.max_tokens":                     "a count of model tokens",
	"ai.chat.send.max_tokens":               "a count of model tokens",
	"ai.chat.preview.max_tokens":            "a count of model tokens",
	"ai.context.preview.max_tokens":         "a count of model tokens",
	"ai.budget.set.max_tokens":              "a count of model tokens",
	"entity.field.set.key":                  "the name of a field, not a key",
	"entity.field.remove.key":               "the name of a field, not a key",
	"project.passphrase.set.allow_keys":     "a boolean about API keys",
	"project.passphrase.set.approve_keys":   "ids of API keys",
	"project.passphrase.set.unapprove_keys": "ids of API keys",
}

func TestEveryOpWithATypedSecretDeclaresSecretInput(t *testing.T) {
	s := newAuthzServer(t)
	checked := 0
	for _, e := range s.Reg.Entries() {
		if e.Op.SecretInput {
			continue
		}
		var walk func(rt reflect.Type, depth int)
		walk = func(rt reflect.Type, depth int) {
			for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
				rt = rt.Elem()
			}
			if rt.Kind() != reflect.Struct || depth > 3 {
				return
			}
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "" || name == "-" {
					name = strings.ToLower(f.Name)
				}
				if f.Tag.Get("path") != "" || f.Tag.Get("in") == "path" {
					continue
				}
				if secretFieldName.MatchString(name) {
					checked++
					if _, ok := notTypedSecrets[e.Op.Name+"."+name]; !ok {
						t.Errorf("%s takes %q but does not declare SecretInput: add it, or list the field in notTypedSecrets with the reason it is not a typed secret", e.Op.Name, name)
					}
				}
				walk(f.Type, depth+1)
			}
		}
		walk(e.In, 0)
	}
	if checked < 10 {
		t.Errorf("the sweep looked at only %d fields; it is not seeing the inputs", checked)
	}
	// An allow-list entry that no longer names a real op and field is stale.
	for key := range notTypedSecrets {
		i := strings.LastIndex(key, ".")
		op, field := key[:i], key[i+1:]
		e, ok := s.Reg.Lookup(op)
		if !ok {
			t.Errorf("notTypedSecrets names %s, which is not an op", op)
			continue
		}
		found := false
		var walk func(rt reflect.Type, depth int)
		walk = func(rt reflect.Type, depth int) {
			for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
				rt = rt.Elem()
			}
			if rt.Kind() != reflect.Struct || depth > 3 {
				return
			}
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				n := strings.Split(f.Tag.Get("json"), ",")[0]
				if n == "" {
					n = strings.ToLower(f.Name)
				}
				if n == field {
					found = true
				}
				walk(f.Type, depth+1)
			}
		}
		walk(e.In, 0)
		if !found && !strings.HasSuffix(key, "import.preview.token") {
			t.Errorf("notTypedSecrets lists %s, but the op has no such field", key)
		}
	}
}

// share.create takes a passphrase and answers with a token. A repeated Idempotency-Key is a conflict (a retry never makes a
// second link), and what is kept of the request does not let anyone guess the passphrase from the database.
func TestShareCreateKeepsNoGuessableHashOfItsPassphrase(t *testing.T) {
	w := newShareWorld(t, nil)
	base := "/api/v1/projects/" + w.pid + "/shares"
	body := `{"label":"locked","passphrase":"correct horse battery"}`
	post := func() int {
		req := httptest.NewRequest("POST", base, strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "pp-1")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(w.owner.cookie)
		rec := httptest.NewRecorder()
		w.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if c := post(); c != 201 {
		t.Fatalf("share.create: %d", c)
	}
	if c := post(); c == 201 {
		t.Error("a repeated key made a second link")
	}
	var hash string
	if err := w.s.DB.Read().QueryRow(`SELECT request_hash FROM idempotency_keys WHERE key = 'pp-1'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	for _, guess := range []string{"correct horse battery", "letmein"} {
		g := strings.Replace(body, "correct horse battery", guess, 1)
		if idempotency.Fingerprint("share.create", "POST", base, nil, []byte(g)) == hash {
			t.Errorf("the stored request hash is the hash of a body with the passphrase %q", guess)
		}
	}
}
