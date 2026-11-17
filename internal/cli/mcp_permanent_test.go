package cli_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/cli"
)

// --permanent, --groups and --read-only become the connection's query, nothing else does.
func TestStdioProxyAsksForPermanentToolsOnlyWhenTold(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer ts.Close()
	run := func(args ...string) {
		var o bytes.Buffer
		vars := map[string]string{"SCRIOBH_URL": ts.URL, "SCRIOBH_API_KEY": "scr_x", "HOME": t.TempDir(), "SCRIOBH_CONFIG_DIR": t.TempDir()}
		rt := &cli.Runtime{Stdin: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"), Stdout: &o, Stderr: io.Discard,
			Getenv: func(k string) string { return vars[k] }, IsTTY: func(io.Writer) bool { return false }, Now: time.Now}
		if code := cli.Main(rt, append([]string{"mcp"}, args...)); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
	run()
	run("--permanent", "--read-only", "--groups", "doc,stash")
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[0] != "" || queries[1] != "groups=doc%2Cstash&permanent=1&readonly=1" {
		t.Errorf("queries: %q", queries)
	}
}
