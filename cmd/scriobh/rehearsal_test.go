package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Helpers shared by the restore and upgrade rehearsals.

// rehearsalBinary is the binary a rehearsal drives: SCRIOBH_REHEARSAL_BIN when
// set (scripts/*-rehearsal.sh builds one with make's flags and passes it), or a
// development build of this tree.
func rehearsalBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("SCRIOBH_REHEARSAL_BIN"); b != "" {
		abs, err := filepath.Abs(b)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	return buildBinary(t, "")
}

// runEnv runs the binary as a CLI with exactly the given environment (plus PATH).
func runEnv(t *testing.T, bin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

// apiClient is a signed-in HTTP client for one server, with JSON helpers that
// fail the test on an unexpected status.
type apiClient struct {
	t    *testing.T
	base string
	c    *http.Client
	auth string // a bearer token instead of the cookie jar, when set
}

func newAPI(t *testing.T, base, sub string) *apiClient {
	c := client()
	signIn(t, c, base, sub)
	return &apiClient{t: t, base: base, c: c}
}

// withJar returns a client for base that reuses an existing cookie jar (a
// session minted on another server).
func withJar(t *testing.T, base string, jar http.CookieJar) *apiClient {
	return &apiClient{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

func (a *apiClient) do(method, path string, in any, ctype string) (int, []byte) {
	a.t.Helper()
	var rd io.Reader
	switch v := in.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
		ctype = "application/json"
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if a.auth != "" {
		req.Header.Set("Authorization", "Bearer "+a.auth)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, b
}

// call sends in as JSON and decodes the answer into out, failing unless the
// status is want.
func (a *apiClient) call(method, path string, in, out any, want int) []byte {
	a.t.Helper()
	code, b := a.do(method, path, in, "")
	if code != want {
		a.t.Fatalf("%s %s = %d, want %d: %.600s", method, path, code, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			a.t.Fatalf("%s %s: %v in %.300s", method, path, err, b)
		}
	}
	return b
}

// get returns the raw JSON body of a GET that must answer 200.
func (a *apiClient) get(path string) []byte { a.t.Helper(); return a.call("GET", path, nil, nil, 200) }

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// treeHashes maps every regular file under root (slash paths) to its SHA-256,
// leaving out the top-level names in skip.
func treeHashes(t *testing.T, root string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == root {
				return filepath.SkipAll
			}
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, s := range skip {
			if rel == s || strings.HasPrefix(rel, s+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = sha(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffMaps reports the keys whose values differ, are missing or are extra.
func diffMaps(want, got map[string]string) []string {
	var out []string
	for k, w := range want {
		if g, ok := got[k]; !ok {
			out = append(out, "missing "+k)
		} else if g != w {
			out = append(out, "differs "+k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			out = append(out, "extra "+k)
		}
	}
	sort.Strings(out)
	return out
}

// newJar returns an empty cookie jar.
func newJar() http.CookieJar { j, _ := cookiejar.New(nil); return j }

// report prints a line a rehearsal script shows: the numbers it checked.
func report(t *testing.T, f string, a ...any) {
	t.Helper()
	t.Logf("REHEARSAL "+f, a...)
}
