package cli_test

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crueber/scriobh/internal/testutil"
)

// The CLI sends every write with an Idempotency-Key and repeats it, with the same key, when an answer is lost (a 502, 503 or 504, or a broken
// connection). A private ("Only me") comment must come out of that as one comment, answered the second time by reading it again, with the
// words in no table but `comments` and not in what the server remembers of the request.
func TestAPrivateCommentRepeatedByTheCLIAfterALostAnswerIsOneComment(t *testing.T) {
	e := newEnv(t, nil)
	const canary = "PRIVATE-RETRY-CANARY"
	e.ok("project", "create", "--title", "Salt")
	e.ok("doc", "create", "Salt", "--title", "One", "--content", "The quiet house stood alone on the hill.")
	// a proxy that lets the first comment through, throws its answer away and says 503
	real := e.ts
	u, _ := url.Parse(real.URL)
	rp := httputil.NewSingleHostReverseProxy(u)
	var lost atomic.Int32
	e.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments") && lost.Add(1) == 1 {
			rp.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(e.ts.Close)
	r := e.ok("comment", "create", "Salt", "--document-id", "One", "--quote", "stood alone", "--body", canary+" note to self", "--visibility", "private")
	if lost.Load() < 2 {
		t.Fatalf("the CLI did not repeat the request (%d reached the proxy)", lost.Load())
	}
	if !strings.Contains(r.stdout, canary) || !strings.Contains(r.stdout, `"private"`) {
		t.Errorf("the repeated request was not answered with the comment: %s", r.stdout)
	}
	list := e.ok("comment", "list", "Salt", "--q", canary)
	if n := strings.Count(list.stdout, canary); n != 1 {
		t.Errorf("%d comments hold the words after a retry, want 1: %s", n, list.stdout)
	}
	if tb := testutil.TablesHolding(t, e.srv.DB, canary, "comments"); len(tb) != 0 {
		t.Errorf("the words are also in %v", tb)
	}
	// the same words sent under another key leave the same fingerprint: the server remembers nothing of the words, not even a hash of them
	e.ok("comment", "create", "Salt", "--document-id", "One", "--quote", "quiet house", "--body", "A DIFFERENT PRIVATE BODY", "--visibility", "private", "--idempotency-key", "other-key-1")
	rows, err := e.srv.DB.Read().Query(`SELECT request_hash FROM idempotency_keys WHERE op = 'comment.create'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hashes := map[string]bool{}
	for rows.Next() {
		var h string
		_ = rows.Scan(&h)
		hashes[h] = true
	}
	if len(hashes) != 1 {
		t.Errorf("the stored request fingerprints differ with the words (%d kinds): a hash of a private note is a guessing oracle", len(hashes))
	}
}
