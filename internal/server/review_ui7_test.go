package server

import "testing"

// REVIEW: safeNext promises "a page of this project", but the prefix test does not look at dot segments, so
// next=/p/{slug}/../../settings is accepted and http.Redirect cleans it to /settings (same origin, not external).
func TestReviewSafeNextDotSegments(t *testing.T) {
	for _, next := range []string{"/p/book/../../settings", "/p/book/../other/d/x", "/p/book/%2e%2e/%2e%2e/admin"} {
		got := safeNext(next, "book")
		if got != "/p/book" {
			t.Errorf("safeNext(%q) = %q, want the project's home", next, got)
		}
	}
}
