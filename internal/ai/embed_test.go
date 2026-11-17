package ai_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/testutil"
)

func vecJSON(n int, v string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = v
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func okVec(n int) string { return vecJSON(n, "0.5") }

func TestEmbedShapes(t *testing.T) {
	t.Run("openai shape, answered out of order", func(t *testing.T) {
		c, sn := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":[{"index":1,"embedding":%s},{"index":0,"embedding":%s}],"usage":{"prompt_tokens":7}}`, vecJSON(8, "2"), vecJSON(8, "1"))
		}, nil)
		res, err := c.Embed(context.Background(), "m", []string{"a", "b"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Dim != 8 || len(res.Vectors) != 2 || res.Vectors[0][0] != 1 || res.Vectors[1][0] != 2 || res.InputTokens != 7 || !res.Reported {
			t.Fatalf("%+v", res)
		}
		if sn.reqs[0].Path != "/embeddings" || !strings.Contains(string(sn.reqs[0].Body), `"encoding_format":"float"`) {
			t.Fatalf("%s %s", sn.reqs[0].Path, sn.reqs[0].Body)
		}
		if sn.reqs[0].Header.Get("Authorization") != "Bearer sk-secret-key-123456" {
			t.Fatal("the key goes in the Authorization header")
		}
	})
	t.Run("ollama /api/embed", func(t *testing.T) {
		c, sn := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"model":"m","embeddings":[%s,%s],"prompt_eval_count":5}`, okVec(8), okVec(8))
		}, nil)
		res, err := c.Embed(context.Background(), "m", []string{"a", "b"})
		if err != nil || res.Dim != 8 || res.InputTokens != 5 {
			t.Fatalf("%v %+v", err, res)
		}
		if sn.reqs[0].Path != "/api/embed" {
			t.Fatal(sn.reqs[0].Path)
		}
	})
	t.Run("ollama falls back to /api/embeddings on a 404", func(t *testing.T) {
		c, sn := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/embed" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"embedding":%s}`, okVec(8))
		}, nil)
		res, err := c.Embed(context.Background(), "m", []string{"a", "b", "c"})
		if err != nil || len(res.Vectors) != 3 {
			t.Fatalf("%v %+v", err, res)
		}
		if len(sn.reqs) != 4 || sn.reqs[1].Path != "/api/embeddings" {
			t.Fatalf("%d requests", len(sn.reqs))
		}
	})
	t.Run("a model that does not exist is not_found, not a fallback loop", func(t *testing.T) {
		c, _ := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, nil)
		_, err := c.Embed(context.Background(), "m", []string{"a"})
		if pe, ok := ai.AsProviderError(err); !ok || pe.Class != ai.ClassNotFound {
			t.Fatalf("%v", err)
		}
	})
	t.Run("opencode has no embedding route", func(t *testing.T) {
		c, sn := client(t, ai.KindOpenCode, func(w http.ResponseWriter, r *http.Request) {}, nil)
		_, err := c.Embed(context.Background(), "m", []string{"a"})
		if err == nil || len(sn.reqs) != 0 {
			t.Fatal("OpenCode was called")
		}
	})
}

func TestEmbedHostileAnswers(t *testing.T) {
	big := okVec(8)
	cases := []struct {
		name  string
		kind  string
		reply func(w http.ResponseWriter, r *http.Request)
		class string
	}{
		{"not json", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }, ai.ClassBadResponse},
		{"empty body", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {}, ai.ClassBadResponse},
		{"fewer vectors than texts", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":[{"index":0,"embedding":%s}]}`, big)
		}, ai.ClassBadResponse},
		{"more vectors than texts", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s,%s]}`, big, big, big)
		}, ai.ClassBadResponse},
		{"two vectors of the same index", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":[{"index":0,"embedding":%s},{"index":0,"embedding":%s}]}`, big, big)
		}, ai.ClassBadResponse},
		{"an index out of range", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"data":[{"index":0,"embedding":%s},{"index":9,"embedding":%s}]}`, big, big)
		}, ai.ClassBadResponse},
		{"vectors of different lengths", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, okVec(8), okVec(9))
		}, ai.ClassBadResponse},
		{"a vector too short", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, okVec(3), okVec(3))
		}, ai.ClassBadResponse},
		{"a vector too long", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, okVec(ai.MaxEmbedDim+1), okVec(ai.MaxEmbedDim+1))
		}, ai.ClassBadResponse},
		{"a number that overflows float32", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, vecJSON(8, "1e39"), okVec(8))
		}, ai.ClassBadResponse},
		{"a number that overflows float64", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, vecJSON(8, "1e999"), okVec(8))
		}, ai.ClassBadResponse},
		{"NaN written as a bare word", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[[NaN,1,1,1,1,1,1,1],%s]}`, okVec(8))
		}, ai.ClassBadResponse},
		{"a vector with no direction", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, vecJSON(8, "0"), okVec(8))
		}, ai.ClassBadResponse},
		{"squares that overflow", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"embeddings":[%s,%s]}`, vecJSON(8, "1e200"), okVec(8))
		}, ai.ClassBadResponse},
		{"strings for numbers", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"embeddings":[["a","b","c","d","e","f","g","h"],["a","b","c","d","e","f","g","h"]]}`)
		}, ai.ClassBadResponse},
		{"an error field with a 200", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"error":"SECRET_WORDS the model said"}`)
		}, ai.ClassRejected},
		{"a redirect is not followed", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/internal", http.StatusFound)
		}, ai.ClassBadResponse},
		{"401", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"error":{"message":"bad key"}}`, 401) }, ai.ClassAuth},
		{"429", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow down", 429) }, ai.ClassRate},
		{"500", ai.KindOllama, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }, ai.ClassUnavailable},
		{"503", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }, ai.ClassUnavailable},
		{"400", ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "too long", 400) }, ai.ClassRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := client(t, tc.kind, tc.reply, nil)
			res, err := c.Embed(context.Background(), "m", []string{"a", "b"})
			pe, ok := ai.AsProviderError(err)
			if !ok || pe.Class != tc.class {
				t.Fatalf("want %s, got %v (%+v)", tc.class, err, res)
			}
			if strings.Contains(pe.Message()+pe.Detail+err.Error(), "SECRET_WORDS") {
				t.Error("the endpoint's own words reached the error")
			}
		})
	}
}

func TestEmbedBodyOverTheCap(t *testing.T) {
	c, _ := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) { // the helper's cap is 64 KiB
		w.Write([]byte(`{"embeddings":[`))
		for i := 0; i < 400; i++ {
			w.Write([]byte(vecJSON(256, "0.123456789") + ","))
		}
		w.Write([]byte(okVec(8) + "]}"))
	}, nil)
	_, err := c.Embed(context.Background(), "m", []string{"a"})
	if pe, ok := ai.AsProviderError(err); !ok || pe.Class != ai.ClassTooLarge {
		t.Fatalf("%v", err)
	}
}

// clientWith is a client with its own limits.
func clientWith(t *testing.T, kind string, h http.HandlerFunc, lim ai.Limits) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := ai.NewClient(ai.Endpoint{Kind: kind, BaseURL: srv.URL, Mode: netguard.PublicOnly}, ai.ClientOptions{Guard: netguard.Options{Classify: publicLoopback}, Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestEmbedTimeoutAndLimits(t *testing.T) {
	slow := clientWith(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}, ai.Limits{Request: 300 * time.Millisecond, FirstByte: 300 * time.Millisecond})
	start := time.Now()
	_, err := slow.Embed(context.Background(), "m", []string{"a"})
	took := time.Since(start)
	if pe, ok := ai.AsProviderError(err); !ok || pe.Class != ai.ClassTimeout {
		t.Fatalf("%v after %v", err, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 2*time.Second {
			t.Errorf("a slow embedding was cut off after %v", took)
		}
	})
	c, sn := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {}, nil)
	if _, err := c.Embed(context.Background(), "m", nil); err == nil {
		t.Error("no texts")
	}
	if _, err := c.Embed(context.Background(), "m", make([]string, ai.MaxEmbedBatch+1)); err == nil {
		t.Error("too many texts")
	}
	if _, err := c.Embed(context.Background(), "m", []string{strings.Repeat("x", ai.MaxEmbedInputBytes+1)}); err == nil {
		t.Error("a text over the limit")
	}
	if _, err := c.Embed(context.Background(), "bad model name!", []string{"a"}); err == nil {
		t.Error("a bad model name")
	}
	if len(sn.reqs) != 0 {
		t.Error("a request that was refused locally was sent")
	}
}
