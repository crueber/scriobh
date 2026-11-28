package cli

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeServerRejectsUserinfo(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		forbidden string
	}{
		{
			name:      "username and password",
			raw:       "https://alice:topsecret@write.example.com",
			forbidden: "topsecret",
		},
		{
			name: "username only",
			raw:  "https://alice@write.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := NormalizeServer(tc.raw)
			if err == nil {
				t.Fatalf("NormalizeServer(%q) = %v, want error", tc.raw, u)
			}
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %T (%v), want *UsageError", err, err)
			}
			if tc.forbidden != "" && strings.Contains(ue.Msg, tc.forbidden) {
				t.Fatalf("error message leaked secret %q: %s", tc.forbidden, ue.Msg)
			}
			if !strings.Contains(ue.Hint, "--key") || !strings.Contains(ue.Hint, "SCRIOBH_API_KEY") {
				t.Fatalf("hint %q should explain API key authentication", ue.Hint)
			}
		})
	}
}

func TestScrubRedactsKeysAndEmbeddedURLs(t *testing.T) {
	const apiKey = "scr_secret_key_value_12345"
	err := errors.New("request to https://user:secret@write.example.com/api/v1/projects failed with bearer " + apiKey)
	got := scrub(err, apiKey)
	if strings.Contains(got, apiKey) {
		t.Fatalf("scrub leaked API key: %s", got)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("scrub leaked URL password: %s", got)
	}
	if !strings.Contains(got, "[key]") {
		t.Fatalf("scrub should replace key with [key]: %s", got)
	}
	if !strings.Contains(got, "https://user:xxxxx@write.example.com/api/v1/projects") {
		t.Fatalf("scrub should redact URL password with xxxxx: %s", got)
	}

	uErr := &url.Error{
		Op:  "Get",
		URL: "https://user:secret@write.example.com/api/v1/projects",
		Err: errors.New("dial https://user:secret@write.example.com:443 failed with " + apiKey),
	}
	gotUE := scrub(uErr, apiKey)
	if strings.Contains(gotUE, apiKey) || strings.Contains(gotUE, "secret") {
		t.Fatalf("scrub(*url.Error) leaked secret: %s", gotUE)
	}
}
