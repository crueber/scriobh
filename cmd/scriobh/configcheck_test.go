package main

import (
	"bytes"
	"strings"
	"testing"
)

func runConfigCheck(t *testing.T, env map[string]string) (string, string, int) {
	t.Helper()
	// Start from a clean slate for what the identity line reads.
	for _, k := range []string{"SCRIOBH_OIDC_ISSUER", "SCRIOBH_OIDC_CLIENT_ID", "SCRIOBH_OIDC_CLIENT_SECRET", "SCRIOBH_OIDC_STUB"} {
		t.Setenv(k, "")
	}
	t.Setenv("SCRIOBH_DATA_DIR", t.TempDir())
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code := run([]string{"config", "check"}, &out, &errb)
	return out.String(), errb.String(), code
}

func identityOf(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "identity") {
			return strings.TrimSpace(strings.TrimPrefix(l, "identity"))
		}
	}
	t.Fatalf("no identity line in %q", out)
	return ""
}

// With no issuer set the identity line says so; it used to print nothing after the label.
func TestConfigCheckIdentityWhenNothingIsConfigured(t *testing.T) {
	out, errs, code := runConfigCheck(t, nil)
	got := identityOf(t, out)
	if !strings.Contains(got, "not configured") || !strings.Contains(got, "SCRIOBH_OIDC_ISSUER") {
		t.Errorf("identity line = %q, want it to say the issuer is not configured", got)
	}
	if code == 0 || !strings.Contains(errs, "SCRIOBH_OIDC_ISSUER is required") {
		t.Errorf("exit %d, stderr %q: the check should still fail and say why", code, errs)
	}
}

func TestConfigCheckIdentityShowsTheIssuerAndTheStub(t *testing.T) {
	out, _, _ := runConfigCheck(t, map[string]string{"SCRIOBH_OIDC_ISSUER": "https://id.example.com/realm", "SCRIOBH_OIDC_CLIENT_ID": "a", "SCRIOBH_OIDC_CLIENT_SECRET": "b"})
	if got := identityOf(t, out); got != "https://id.example.com/realm" {
		t.Errorf("identity line = %q", got)
	}
	out, _, _ = runConfigCheck(t, map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_LISTEN": "127.0.0.1:0"})
	if got := identityOf(t, out); !strings.Contains(got, "development issuer") {
		t.Errorf("identity line = %q", got)
	}
}

// typo is a name one letter-pair away from SCRIOBH_TRUST_PROXY, spelled in two pieces so that the test that every name in
// the repository is a known one does not read it as a setting.
const typo = "SCRIOBH_TRUST_" + "PROXIES"

// A mistyped yes/no setting fails the check and names the variable and what it accepts.
func TestConfigCheckRefusesAMistypedBoolean(t *testing.T) {
	_, errs, code := runConfigCheck(t, map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_LISTEN": "127.0.0.1:0", "SCRIOBH_TRUST_PROXY": "ture"})
	if code == 0 || !strings.Contains(errs, "SCRIOBH_TRUST_PROXY") || !strings.Contains(errs, `"ture"`) || !strings.Contains(errs, "1, true, yes, on, 0, false, no, off") {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
}

// An unknown SCRIOBH_ name is a warning in the check (exit 0 when all else is fine), with the nearest setting as a hint
// and never its value; names the server or the tooling read are not warned about.
func TestConfigCheckWarnsAboutAnUnknownName(t *testing.T) {
	out, errs, code := runConfigCheck(t, map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_LISTEN": "127.0.0.1:0",
		typo: "very-private-value", "SCRIOBH_URL": "http://localhost:1", "SCRIOBH_KEEP": "/tmp/x"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "warning: "+typo+" is not a setting this version reads") || !strings.Contains(out, "did you mean SCRIOBH_TRUST_PROXY?") {
		t.Errorf("no warning with a hint in:\n%s", out)
	}
	if strings.Contains(out, "very-private-value") || strings.Contains(out, "SCRIOBH_URL") || strings.Contains(out, "SCRIOBH_KEEP") {
		t.Errorf("a value, or a name that is known, was warned about:\n%s", out)
	}
	if !strings.Contains(out, "configuration is valid") {
		t.Errorf("the check should still end valid:\n%s", out)
	}
}
