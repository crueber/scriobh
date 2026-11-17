package desktop

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

// modelServer is a stand-in for Ollama on a loopback port.
func modelServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

func TestDetectListsTheModelsOfAServerOnThisComputer(t *testing.T) {
	var hits atomic.Int32
	var path, method string
	addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path, method = r.URL.Path, r.Method
		fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2019393189,"details":{"parameter_size":"3.2B","family":"llama"}},{"name":"nomic-embed-text:latest","size":274302450}]}`)
	})
	var dials []string
	stop := netguard.Observe(func(_, a string) { dials = append(dials, a) })
	defer stop()
	models, err := detectAt(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "llama3.2:latest" || models[0].Size != 2019393189 || models[0].Parameters != "3.2B" || models[1].Parameters != "" {
		t.Errorf("models: %+v", models)
	}
	if hits.Load() != 1 || path != "/api/tags" || method != "GET" {
		t.Errorf("the look made %d requests, the last %s %s; want one GET /api/tags", hits.Load(), method, path)
	}
	if len(dials) != 1 {
		t.Errorf("the look was observed %d times, want once: %v", len(dials), dials)
	}
}

// TestDetectTreatsTheAnswerAsHostile: each limit, with an answer made to pass it.
func TestDetectTreatsTheAnswerAsHostile(t *testing.T) {
	t.Run("an answer past the size limit is refused", func(t *testing.T) {
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"models":[{"name":"`+strings.Repeat("a", detectMaxBytes)+`"}]}`)
		})
		if _, err := detectAt(context.Background(), addr); err == nil {
			t.Error("an oversized answer was accepted")
		}
	})
	t.Run("an endless answer is cut off", func(t *testing.T) {
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"models":[`)
			chunk := []byte(`{"name":"m"},`)
			for i := 0; i < (4<<20)/len(chunk); i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		})
		if _, err := detectAt(context.Background(), addr); err == nil {
			t.Error("an endless answer was accepted")
		}
	})
	t.Run("more models than the limit are not kept", func(t *testing.T) {
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			var b strings.Builder
			b.WriteString(`{"models":[`)
			for i := 0; i < detectMaxItems+50; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"name":"m%d"}`, i)
			}
			b.WriteString(`]}`)
			fmt.Fprint(w, b.String())
		})
		models, err := detectAt(context.Background(), addr)
		if err != nil || len(models) != detectMaxItems {
			t.Errorf("%d models kept (%v), want %d", len(models), err, detectMaxItems)
		}
	})
	t.Run("names are cut and stripped", func(t *testing.T) {
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"models":[{"name":"ok\u0000\u001b[31m\nred","size":-5,"details":{"parameter_size":"`+strings.Repeat("9", 100)+`"}},{"name":"`+strings.Repeat("n", detectNameMax+40)+`"},{"name":"\u0007"},{"name":""}]}`)
		})
		models, err := detectAt(context.Background(), addr)
		if err != nil || len(models) != 2 {
			t.Fatalf("models: %+v %v", models, err)
		}
		if models[0].Name != "ok[31mred" || models[0].Size != 0 || len(models[0].Parameters) != 16 {
			t.Errorf("first model: %+v", models[0])
		}
		if len(models[1].Name) != detectNameMax {
			t.Errorf("a long name was kept at %d characters", len(models[1].Name))
		}
	})
	t.Run("a redirect is not followed", func(t *testing.T) {
		var elsewhere atomic.Int32
		other := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			elsewhere.Add(1)
			fmt.Fprint(w, `{"models":[{"name":"stolen"}]}`)
		})
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://"+other+"/api/tags", http.StatusFound)
		})
		if models, err := detectAt(context.Background(), addr); err == nil || elsewhere.Load() != 0 {
			t.Errorf("a redirect was followed: %v, %d requests elsewhere", models, elsewhere.Load())
		}
	})
	t.Run("something that is not a model server", func(t *testing.T) {
		for _, body := range []string{`<html>hello</html>`, `{}`, `{"models":"x"}`, `[]`, ``} {
			addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if models, err := detectAt(context.Background(), addr); err == nil {
				t.Errorf("%q was read as a model server: %v", body, models)
			}
		}
		addr := modelServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusInternalServerError) })
		if _, err := detectAt(context.Background(), addr); err == nil {
			t.Error("a 500 was read as a model server")
		}
	})
	t.Run("a server that never answers is given up on", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		release := make(chan struct{})
		defer close(release)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { <-release; c.Close() }()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := detectAt(ctx, ln.Addr().String()); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("a silent server was read as a model server")
			}
		case <-time.After(detectTimeout + 5*time.Second):
			t.Fatal("the look did not give up on a silent server")
		}
	})
	t.Run("nothing listening", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		if _, err := detectAt(context.Background(), addr); err == nil {
			t.Error("a closed port was read as a model server")
		}
	})
	t.Run("only this computer", func(t *testing.T) {
		for _, addr := range []string{"192.0.2.1:11434", "example.com:11434", "0.0.0.0:11434"} {
			var dialled bool
			stop := netguard.Observe(func(_, _ string) { dialled = true })
			_, err := detectAt(context.Background(), addr)
			stop()
			if err == nil || dialled {
				t.Errorf("%s was asked (dialled=%v)", addr, dialled)
			}
		}
	})
	if OllamaAddress != "127.0.0.1:11434" || !netguard.IsLoopbackAddress(OllamaAddress) {
		t.Errorf("the address asked is %q", OllamaAddress)
	}
}
