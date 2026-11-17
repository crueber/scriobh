package server

import (
	"net/url"
	"strings"
	"testing"
)

const reviewCanary = "CANARY-TOKEN-ghp_abc123XYZ"

// A secret pasted into the address field is refused, and must not come back in the page the refusal is drawn on.
// (gitStoreValues keeps every posted field but `token` and `trigger_url`, so `url` is echoed into value="...".)
func TestReviewRefusedAddRemoteDoesNotEchoCredentialsInTheAddress(t *testing.T) {
	e := newWriteEnv(t)
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", url.Values{
		"name": {"x"}, "url": {"https://writer:" + reviewCanary + "@example.com/o/r.git"}, "audience": {"private"}, "push_mode": {"manual"},
	}, "", false)
	// a refusal is answered with a redirect to the page, which shows it (post, redirect, get)
	if rec.Code != 422 && rec.Code != 303 {
		t.Fatalf("status %d", rec.Code)
	}
	page := rec.Body.String()
	if rec.Code == 303 {
		page += followAs(t, e.s, e.owner, rec).Body.String() + rec.Header().Get("Location")
	}
	if strings.Contains(page, reviewCanary) {
		t.Error("the refused form came back with the secret from the address field in the page")
	}
}

// The same for the publish page's connect form: the host field.
func TestReviewRefusedConnectDoesNotEchoCredentialsInTheHost(t *testing.T) {
	e := newWriteEnv(t)
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/publish/connect", url.Values{
		"provider": {"forgejo"}, "host": {"https://u:" + reviewCanary + "@127.0.0.1:1"}, "token": {"t"},
	}, "", false)
	page := rec.Body.String()
	if rec.Code == 303 {
		page += followAs(t, e.s, e.owner, rec).Body.String() + rec.Header().Get("Location")
	}
	if strings.Contains(page, reviewCanary) {
		t.Error("the refused connect form came back with the secret from the host field in the page")
	}
}
