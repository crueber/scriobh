// Command standin is a stand-in for the Scríobh server in the desktop shell's tests. It follows the launch
// protocol of docs/p8-notes/desktop-contract.md closely enough for the shell to be exercised without the real
// binary: it refuses to start without the desktop variables, takes a lock on its data directory (a second start
// prints ALREADY_RUNNING and exits 75), prints LISTEN, serves /healthz and /desktop/start, and exits 0 on SIGTERM.
//
// It follows the contract as the Go side now has it (docs/p8-notes/desktop-contract.md, "Changes and clarifications
// made by the Go side"): a request whose Host is not exactly 127.0.0.1:<port> gets 421 on every route (change 10);
// /desktop/start has no lockout, accepts the token once, and refuses a cross-site request without spending it
// (change 7); the server stops, as on SIGTERM, when its standard input was a pipe at start and reaches its end
// (change 23); and it binds the port it bound last time, kept in desktop/port, when that is free (change 24).
//
// It also serves a few pages the real app does not have, for checking what the shell does with a hostile link, a
// download and a page script that tries to reach the shell (see page.go), and it remembers the paths it was
// asked for so a test can see where the shell sent the webview.
//
// /healthz answers the shell's challenge as the real server does (Go change 30): the shell sends X-Scriobh-Challenge,
// a 64-character lower-case hex nonce, and the answer carries X-Scriobh-Proof, an HMAC of it under a key derived from
// the launch token (launch.rs and internal/desktopidp/launch.go hold the same construction and the same vector).
//
// STANDIN_MODE selects a failure for the tests: no-listen, unhealthy, die-early, die-once, leak, ignore-term,
// die-after, no-proof (answers 200 and no proof, as a program that is not the server would), wrong-proof (a proof
// made without the token), proof-once (right for the first challenge, wrong after: a program that took the port
// between the shell's two looks).
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var hexToken = regexp.MustCompile(`^[0-9a-f]{32,}$`)

var proofWanted = regexp.MustCompile(`^[0-9a-f]{64}$`)

// answered counts the challenges the proof-once mode has answered.
var answered atomic.Int64

// proof is what the real server answers to nonce: HMAC-SHA-256 under k, of the nonce's characters, where k is
// HMAC-SHA-256 under the token's characters of "scriobh desktop healthz key v1".
func proof(token, nonce string) string {
	k := hmac.New(sha256.New, []byte(token))
	k.Write([]byte("scriobh desktop healthz key v1"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(nonce))
	return hex.EncodeToString(m.Sum(nil))
}

type state struct {
	mu    sync.Mutex
	used  bool
	seen  []string
	fetch []string
	probe json.RawMessage
}

func main() {
	mode := os.Getenv("STANDIN_MODE")
	token := os.Getenv("SCRIOBH_LAUNCH_TOKEN")
	if os.Getenv("SCRIOBH_DESKTOP") != "1" || !hexToken.MatchString(token) {
		fmt.Fprintln(os.Stderr, "standin: desktop mode needs SCRIOBH_DESKTOP=1 and a launch token of at least 32 hex characters")
		os.Exit(2)
	}
	if os.Getenv("SCRIOBH_LISTEN") != "127.0.0.1:0" {
		fmt.Fprintln(os.Stderr, "standin: desktop mode listens on 127.0.0.1:0 only")
		os.Exit(2)
	}
	dataDir := os.Getenv("SCRIOBH_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "scriobh-standin-default")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "standin:", err)
		os.Exit(1)
	}
	lock := filepath.Join(dataDir, "standin.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Println("ALREADY_RUNNING")
		os.Exit(75)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	unlock := func() { os.Remove(lock) }
	// A mode that ignores the request to stop ignores the end of standard input too.
	if mode != "ignore-term" {
		stopWhenStdinCloses(os.Stdin, func() {
			// The lock goes first: a shell that was killed has closed our standard error as well, and writing to
			// it ends a Go program with SIGPIPE (it ended the real server too, until desktop mode began to survive it), before anything after it runs.
			unlock()
			fmt.Fprintln(os.Stderr, "standin: standard input closed, so the shell is gone; stopping")
			os.Exit(0)
		})
	}

	if mode == "die-once" {
		// Fails the first time it is started on a data directory and works after that: a restart that succeeds.
		marker := filepath.Join(dataDir, "died-once")
		if _, err := os.Stat(marker); err != nil {
			os.WriteFile(marker, nil, 0o600)
			mode = "die-early"
		}
	}
	switch mode {
	case "die-early":
		fmt.Fprintln(os.Stderr, "standin: migration 0042 failed")
		fmt.Fprintln(os.Stderr, "standin: the database is from a newer version")
		unlock()
		os.Exit(3)
	case "leak":
		fmt.Fprintf(os.Stderr, "standin: starting with token %s and key %s\n", token, os.Getenv("SCRIOBH_SECRET_KEY"))
		fmt.Fprintln(os.Stderr, "standin: \x1b[31mcoloured\x1b[0m and a bell\a")
		unlock()
		os.Exit(3)
	case "no-listen":
		fmt.Fprintln(os.Stderr, "standin: waiting for something that never comes")
		waitForSignal(unlock, false)
		return
	}

	ln, err := listen(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin:", err)
		unlock()
		os.Exit(1)
	}
	hostWanted := ln.Addr().String() // 127.0.0.1:<port>, the only Host the server answers to
	st := &state{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unhealthy" {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		// Exactly one challenge header, as the real server insists: a doubled one is not answered.
		if vals := r.Header.Values("X-Scriobh-Challenge"); len(vals) == 1 && proofWanted.MatchString(vals[0]) {
			nonce := vals[0]
			switch {
			case mode == "no-proof":
			case mode == "wrong-proof" || (mode == "proof-once" && answered.Add(1) > 1):
				w.Header().Set("X-Scriobh-Proof", proof("not the launch token", nonce))
			default:
				w.Header().Set("X-Scriobh-Proof", proof(token, nonce))
			}
		}
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /desktop/start", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		ok := subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(token)) == 1
		// No lockout: wrong guesses are counted by nobody. A request from another site is refused before the token
		// is looked at, so it cannot spend it.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !ok || st.used {
			http.NotFound(w, r)
			return
		}
		st.used = true
		http.SetCookie(w, &http.Cookie{Name: "scriobh_desktop_launch", Value: "standin", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	// What the shell passed, for the launch test: never the values, only their shape.
	mux.HandleFunc("GET /standin/report", func(w http.ResponseWriter, r *http.Request) {
		var names []string
		for _, kv := range os.Environ() {
			if n, _, _ := strings.Cut(kv, "="); strings.HasPrefix(n, "SCRIOBH_") {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		argv := strings.Join(os.Args[1:], " ")
		writeJSON(w, map[string]any{
			"argc":            len(os.Args) - 1,
			"token_in_argv":   strings.Contains(argv, token),
			"key_in_argv":     os.Getenv("SCRIOBH_SECRET_KEY") != "" && strings.Contains(argv, os.Getenv("SCRIOBH_SECRET_KEY")),
			"key_len":         len(os.Getenv("SCRIOBH_SECRET_KEY")),
			"key_fingerprint": fingerprint(os.Getenv("SCRIOBH_SECRET_KEY")),
			"scriobh_names":   names,
			"stdin_is_a_pipe": stdinIsPipe(),
		})
	})
	mux.HandleFunc("GET /standin/seen", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		writeJSON(w, map[string]any{"seen": st.seen, "fetch": st.fetch, "probe": st.probe})
	})
	mux.HandleFunc("POST /standin/probe", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		st.mu.Lock()
		if json.Valid(b) {
			st.probe = b
		}
		st.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	pages(mux)

	srv := &http.Server{Handler: hostCheck(hostWanted, record(st, mux)), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	fmt.Printf("LISTEN http://%s\n", ln.Addr().String())

	if mode == "die-after" {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			fmt.Fprintln(os.Stderr, "standin: fatal: out of disk space")
			unlock()
			os.Exit(1)
		}()
	}
	waitForSignal(unlock, mode == "ignore-term")
}

// listen binds the port that was bound last time (desktop/port under the data directory) when it is free, and any
// free port otherwise, and remembers the one it got.
func listen(dataDir string) (net.Listener, error) {
	path := filepath.Join(dataDir, "desktop", "port")
	var ln net.Listener
	var err error
	if raw, rerr := os.ReadFile(path); rerr == nil && len(raw) < 16 {
		if n, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && n >= 1024 && n <= 65535 {
			ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
		}
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	if _, bound, serr := net.SplitHostPort(ln.Addr().String()); serr == nil {
		if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
			_ = os.WriteFile(path, []byte(bound+"\n"), 0o600)
		}
	}
	return ln, err
}

// hostCheck answers 421 to any request whose Host is not exactly the bound address, before anything else runs.
// `localhost:<port>` is not accepted: it can resolve to ::1, where nothing here listens.
func hostCheck(want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != want {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stopWhenStdinCloses stops the process when its standard input reaches its end, if it was a pipe (or a socket) at
// start. A terminal, /dev/null or a closed descriptor never stops it.
func stopWhenStdinCloses(in *os.File, stop func()) {
	if !stdinIsPipe() {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, in)
		stop()
	}()
}

func waitForSignal(unlock func(), ignoreTerm bool) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	for range ch {
		if ignoreTerm {
			fmt.Fprintln(os.Stderr, "standin: ignoring the request to stop")
			continue
		}
		unlock()
		os.Exit(0)
	}
}

// fingerprint lets a test see that two launches were given the same key without seeing the key.
func fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("standin fingerprint\x00" + key))
	return hex.EncodeToString(sum[:6])
}

func stdinIsPipe() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&(os.ModeNamedPipe|os.ModeSocket) != 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// record remembers the path of every page request, without its query.
func record(st *state, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/standin/") && r.URL.Path != "/healthz" {
			st.mu.Lock()
			if len(st.seen) < 500 {
				st.seen = append(st.seen, r.Method+" "+r.URL.Path)
				// How the webview described the request, for the checks that depend on it.
				st.fetch = append(st.fetch, fmt.Sprintf("%s site=%s mode=%s dest=%s origin=%s", r.URL.Path,
					r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Sec-Fetch-Mode"), r.Header.Get("Sec-Fetch-Dest"), r.Header.Get("Origin")))
			}
			st.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}
