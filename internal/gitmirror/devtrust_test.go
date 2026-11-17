//go:build !prod

package gitmirror_test

import (
	"encoding/pem"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
)

// The development trust seam (what the browser tests use through SCRIOBH_DEV_TRUST_PEM) makes the host API client trust a
// self-signed certificate, and refuses a file that holds none.
func TestTheDevelopmentTrustSeamLetsTheClientReachAFakeHost(t *testing.T) {
	h := newHarness(t)
	f := newFakeHost(t, "forgejo")
	gitmirror.SetProviderRootCAs(nil)
	gitmirror.SetRootCAsForTest(nil)
	if _, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: f.host(), Token: f.token}); err == nil {
		t.Fatal("a self-signed host was reached with nothing trusted")
	}
	if err := gitmirror.TrustForDevelopment([]byte("not a certificate")); err == nil {
		t.Error("a file with no certificate was accepted")
	}
	if err := gitmirror.TrustForDevelopment(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gitmirror.SetProviderRootCAs(nil); gitmirror.SetRootCAsForTest(nil) })
	if _, err := h.call(h.owner, "repo.account.connect", gitmirror.AccountConnectIn{Provider: "forgejo", Host: f.host(), Token: f.token}); err != nil {
		t.Fatalf("a trusted host was not reached: %v", err)
	}
}
