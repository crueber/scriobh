package gitmirror_test

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
)

// A fake code host: an httptest TLS server that speaks the small part of the GitHub (Enterprise Server paths,
// /api/v3 and /api/uploads) and Forgejo (/api/v1) REST APIs that Scríobh uses, and serves the repositories it
// "creates" over Git smart HTTP with git http-backend, so a push really lands in a bare repository. It was
// written from the providers' documentation for these tests. No real GitHub or Forgejo was used anywhere, and
// whatever it does is what the documentation was read to say, not what a real host was seen to do.

type fakeReq struct {
	Method, Path, Auth string
}

type fakeRepo struct {
	owner, name, desc, branch string
	private, archived         bool
	noPush                    bool
}

type fakeAsset struct {
	ID    int64
	Name  string
	State string
	Data  []byte
	Size  int64
}

type fakeRel struct {
	ID     int64
	Tag    string
	Body   string
	Target string
	Assets []*fakeAsset
}

type fakeHost struct {
	t      *testing.T
	flavor string
	srv    *httptest.Server
	root   string
	token  string
	login  string
	cgiH   http.Handler

	mu      sync.Mutex
	repos   map[string]*fakeRepo
	rels    map[string]*fakeRel
	reqs    []fakeReq
	nextID  int64
	orgs    map[string]bool
	uploads int

	// misbehaviour, set by a test
	mode        string
	redirectTo  string
	slow        time.Duration
	createFails int // answer a create with the name-taken error this many times, whatever the state
}

const fakeToken = "tok_Zq7Vn3xK9wLm2Rt5Yp8Bd4Hc6Jf1Ga0S"

func newFakeHost(t *testing.T, flavor string) *fakeHost {
	t.Helper()
	backend := backendPath(t)
	f := &fakeHost{t: t, flavor: flavor, root: t.TempDir(), token: fakeToken, login: "mairead", repos: map[string]*fakeRepo{}, rels: map[string]*fakeRel{}, orgs: map[string]bool{}, nextID: 100}
	f.cgiH = &cgi.Handler{Path: backend, Root: "/", InheritEnv: []string{"PATH", "HOME"},
		Env: []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"}}
	f.srv = httptest.NewTLSServer(f)
	t.Cleanup(f.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	gitmirror.SetRootCAsForTest(pool)
	gitmirror.SetProviderRootCAs(pool)
	t.Cleanup(func() {
		gitmirror.SetRootCAsForTest(nil)
		gitmirror.SetProviderRootCAs(nil)
		gitmirror.SetProviderTimeout(0)
	})
	return f
}

// host is the host:port an account is connected with (the Enterprise Server form for the github flavour).
func (f *fakeHost) host() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func (f *fakeHost) requests() []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReq{}, f.reqs...)
}

// writes counts the requests that change something at the host (an API call that is not a GET, and not Git).
func (f *fakeHost) writes() int {
	n := 0
	for _, r := range f.requests() {
		if r.Method != "GET" && !strings.Contains(r.Path, ".git/") {
			n++
		}
	}
	return n
}

func (f *fakeHost) repoDir(owner, name string) string {
	return filepath.Join(f.root, owner, name+".git")
}

func (f *fakeHost) addRepo(owner, name string, private bool) *fakeRepo {
	f.t.Helper()
	dir := f.repoDir(owner, name)
	if out, err := exec.Command("git", "init", "--bare", "--quiet", "--initial-branch=trunk", dir).CombinedOutput(); err != nil {
		f.t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "http.receivepack", "true").CombinedOutput(); err != nil {
		f.t.Fatalf("git config: %v\n%s", err, out)
	}
	r := &fakeRepo{owner: owner, name: name, private: private, branch: "trunk"}
	f.mu.Lock()
	f.repos[owner+"/"+name] = r
	f.mu.Unlock()
	return r
}

// head is the commit the bare repository's branch points at ("" when there is none).
func (f *fakeHost) head(owner, name, branch string) string {
	out, err := exec.Command("git", "-C", f.repoDir(owner, name), "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (f *fakeHost) gitOut(owner, name string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", f.repoDir(owner, name)}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fakeHost) isEmpty(owner, name string) bool {
	out, _ := exec.Command("git", "-C", f.repoDir(owner, name), "for-each-ref", "--count=1").Output()
	return len(strings.TrimSpace(string(out))) == 0
}

func (f *fakeHost) release(owner, name, tag string) *fakeRel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rels[owner+"/"+name+"/"+tag]
}

func (f *fakeHost) id() int64 {
	f.nextID++
	return f.nextID
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeHost) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if f.flavor == "github" {
		return h == "Bearer "+f.token
	}
	return h == "token "+f.token
}

func (f *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, fakeReq{r.Method, r.URL.Path, r.Header.Get("Authorization")})
	f.mu.Unlock()
	api := "/api/v3"
	if f.flavor == "forgejo" {
		api = "/api/v1"
	}
	switch {
	case strings.HasPrefix(r.URL.Path, api+"/"):
		f.serveAPI(w, r, strings.TrimPrefix(r.URL.Path, api))
	case f.flavor == "github" && strings.HasPrefix(r.URL.Path, "/api/uploads/"):
		f.serveUpload(w, r, strings.TrimPrefix(r.URL.Path, "/api/uploads"))
	default:
		f.serveGit(w, r)
	}
}

func (f *fakeHost) serveGit(w http.ResponseWriter, r *http.Request) {
	_, pw, ok := r.BasicAuth()
	if !ok || pw != f.token {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "authentication required for "+pw, http.StatusUnauthorized)
		return
	}
	f.cgiH.ServeHTTP(w, r)
}

// misbehave applies a test's chosen failure to an API request; it reports whether it answered.
func (f *fakeHost) misbehave(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	mode, to, slow := f.mode, f.redirectTo, f.slow
	f.mu.Unlock()
	switch mode {
	case "ratelimit":
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(120*time.Second).Unix(), 10))
		jsonOut(w, 403, map[string]any{"message": "API rate limit exceeded SECRET-RATE-WORDS"})
	case "ratelimit429":
		w.Header().Set("Retry-After", "7")
		jsonOut(w, 429, map[string]any{"message": "slow down SECRET-RATE-WORDS"})
	case "sso":
		w.Header().Set("X-GitHub-SSO", "required; url=https://127.0.0.1:1/sso/SSO-URL-CANARY")
		jsonOut(w, 403, map[string]any{"message": "Resource protected by organization SAML enforcement SECRET-SSO-WORDS"})
	case "huge":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		chunk := []byte(strings.Repeat(" ", 64<<10))
		for i := 0; i < 80; i++ { // 5 MiB of padding around nothing
			if _, err := w.Write(chunk); err != nil {
				return true
			}
		}
		_, _ = w.Write([]byte(`{"login":"x"}`))
	case "malformed":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"login": "mairead", "x": [`)
	case "slow":
		select {
		case <-time.After(slow):
		case <-r.Context().Done():
		}
		return false
	case "redirect":
		http.Redirect(w, r, to, http.StatusFound)
	case "5xx":
		http.Error(w, "<html>Bad gateway SECRET-5XX-WORDS</html>", 502)
	case "unauth":
		jsonOut(w, 401, map[string]any{"message": "Bad credentials SECRET-AUTH-WORDS"})
	default:
		return false
	}
	return true
}

func (f *fakeHost) repoJSON(rp *fakeRepo) map[string]any {
	m := map[string]any{"name": rp.name, "full_name": rp.owner + "/" + rp.name, "private": rp.private, "description": rp.desc, "default_branch": rp.branch,
		"archived": rp.archived, "fork": false, "owner": map[string]any{"login": rp.owner},
		"permissions": map[string]any{"push": !rp.noPush, "admin": !rp.noPush, "pull": true}}
	if f.flavor == "forgejo" {
		m["empty"] = f.isEmpty(rp.owner, rp.name)
	} else if f.isEmpty(rp.owner, rp.name) {
		m["size"] = 0
	} else {
		m["size"] = 12
	}
	return m
}

func (f *fakeHost) serveAPI(w http.ResponseWriter, r *http.Request, rest string) {
	if !f.authorized(r) {
		jsonOut(w, 401, map[string]any{"message": "Bad credentials"})
		return
	}
	if f.misbehave(w, r) {
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	switch {
	case r.Method == "GET" && rest == "/user":
		jsonOut(w, 200, map[string]any{"login": f.login})
	case r.Method == "GET" && rest == "/user/repos":
		f.mu.Lock()
		var out []map[string]any
		for _, rp := range f.repos {
			out = append(out, f.repoJSON(rp))
		}
		f.mu.Unlock()
		if out == nil {
			out = []map[string]any{}
		}
		jsonOut(w, 200, out)
	case r.Method == "POST" && (rest == "/user/repos" || (len(parts) == 3 && parts[0] == "orgs" && parts[2] == "repos")):
		f.createRepo(w, r, parts)
	case len(parts) == 3 && parts[0] == "repos" && (r.Method == "GET" || r.Method == "PATCH"):
		f.mu.Lock()
		rp := f.repos[parts[1]+"/"+parts[2]]
		f.mu.Unlock()
		if rp == nil {
			jsonOut(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		if r.Method == "PATCH" {
			var in struct {
				DefaultBranch string `json:"default_branch"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.DefaultBranch != "" && f.head(rp.owner, rp.name, in.DefaultBranch) == "" {
				jsonOut(w, 422, map[string]any{"message": "branch does not exist"})
				return
			}
			f.mu.Lock()
			rp.branch = in.DefaultBranch
			f.mu.Unlock()
		}
		jsonOut(w, 200, f.repoJSON(rp))
	case len(parts) >= 4 && parts[0] == "repos" && parts[3] == "releases":
		f.serveRelease(w, r, parts)
	default:
		jsonOut(w, 404, map[string]any{"message": "no such route " + rest})
	}
}

func (f *fakeHost) createRepo(w http.ResponseWriter, r *http.Request, parts []string) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
		AutoInit    bool   `json:"auto_init"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		jsonOut(w, 422, map[string]any{"message": "bad body"})
		return
	}
	owner := f.login
	if parts[0] == "orgs" {
		owner = parts[1]
		f.mu.Lock()
		ok := f.orgs[owner]
		f.mu.Unlock()
		if !ok {
			jsonOut(w, 404, map[string]any{"message": "Not Found"})
			return
		}
	}
	f.mu.Lock()
	_, exists := f.repos[owner+"/"+in.Name]
	if f.createFails > 0 {
		f.createFails--
		exists = true
	}
	f.mu.Unlock()
	if in.AutoInit {
		f.t.Errorf("a repository was created with auto_init: the first push is meant to be ours")
	}
	if exists {
		if f.flavor == "github" {
			jsonOut(w, 422, map[string]any{"message": "Repository creation failed.", "errors": []map[string]any{{"resource": "Repository", "code": "custom", "field": "name", "message": "name already exists on this account"}}})
		} else {
			jsonOut(w, 409, map[string]any{"message": "The repository with the same name already exists."})
		}
		return
	}
	rp := f.addRepo(owner, in.Name, in.Private)
	rp.desc = in.Description
	jsonOut(w, 201, f.repoJSON(rp))
}

func (f *fakeHost) relJSON(owner, name string, rel *fakeRel) map[string]any {
	var assets []map[string]any
	for _, a := range rel.Assets {
		m := map[string]any{"id": a.ID, "name": a.Name, "size": a.Size}
		if f.flavor == "github" {
			m["state"] = a.State
		}
		assets = append(assets, m)
	}
	if assets == nil {
		assets = []map[string]any{}
	}
	return map[string]any{"id": rel.ID, "tag_name": rel.Tag, "body": rel.Body, "draft": false, "assets": assets,
		"html_url": "https://127.0.0.1:1/never-followed/" + owner + "/" + name, "upload_url": "https://127.0.0.1:1/never-followed/upload{?name,label}"}
}

func (f *fakeHost) serveRelease(w http.ResponseWriter, r *http.Request, parts []string) {
	owner, name := parts[1], parts[2]
	key := func(tag string) string { return owner + "/" + name + "/" + tag }
	switch {
	case r.Method == "POST" && len(parts) == 4:
		var in struct {
			Tag    string `json:"tag_name"`
			Target string `json:"target_commitish"`
			Body   string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		if _, ok := f.repos[owner+"/"+name]; !ok {
			f.mu.Unlock()
			jsonOut(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		if f.rels[key(in.Tag)] != nil {
			f.mu.Unlock()
			jsonOut(w, 422, map[string]any{"message": "already_exists", "errors": []map[string]any{{"resource": "Release", "code": "already_exists", "field": "tag_name"}}})
			return
		}
		rel := &fakeRel{ID: f.id(), Tag: in.Tag, Body: in.Body, Target: in.Target}
		f.rels[key(in.Tag)] = rel
		f.mu.Unlock()
		jsonOut(w, 201, f.relJSON(owner, name, rel))
	case r.Method == "GET" && len(parts) == 6 && parts[4] == "tags":
		rel := f.release(owner, name, parts[5])
		if rel == nil {
			jsonOut(w, 404, map[string]any{"message": "Not Found"})
			return
		}
		f.mu.Lock()
		out := f.relJSON(owner, name, rel)
		f.mu.Unlock()
		jsonOut(w, 200, out)
	case r.Method == "POST" && len(parts) == 6 && parts[5] == "assets" && f.flavor == "forgejo":
		f.addAsset(w, r, owner, name, parts[4])
	case r.Method == "DELETE" && f.flavor == "github" && len(parts) == 6 && parts[4] == "assets":
		f.deleteAsset(w, owner, name, parts[5])
	case r.Method == "DELETE" && f.flavor == "forgejo" && len(parts) == 7 && parts[5] == "assets":
		f.deleteAsset(w, owner, name, parts[6])
	default:
		jsonOut(w, 404, map[string]any{"message": "no such release route"})
	}
}

func (f *fakeHost) relByID(owner, name, id string) *fakeRel {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, rel := range f.rels {
		if strings.HasPrefix(k, owner+"/"+name+"/") && strconv.FormatInt(rel.ID, 10) == id {
			return rel
		}
	}
	return nil
}

func (f *fakeHost) addAsset(w http.ResponseWriter, r *http.Request, owner, name, relID string) {
	rel := f.relByID(owner, name, relID)
	if rel == nil {
		jsonOut(w, 404, map[string]any{"message": "Not Found"})
		return
	}
	assetName := r.URL.Query().Get("name")
	var data []byte
	if f.flavor == "forgejo" {
		file, _, err := r.FormFile("attachment")
		if err != nil {
			jsonOut(w, 422, map[string]any{"message": "no attachment"})
			return
		}
		data, _ = io.ReadAll(file)
	} else {
		data, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	for _, a := range rel.Assets {
		if a.Name == assetName {
			f.mu.Unlock()
			jsonOut(w, 422, map[string]any{"message": "already_exists", "errors": []map[string]any{{"resource": "ReleaseAsset", "code": "already_exists", "field": "name"}}})
			return
		}
	}
	a := &fakeAsset{ID: f.id(), Name: assetName, State: "uploaded", Data: data, Size: int64(len(data))}
	rel.Assets = append(rel.Assets, a)
	f.uploads++
	f.mu.Unlock()
	jsonOut(w, 201, map[string]any{"id": a.ID, "name": a.Name, "size": a.Size})
}

func (f *fakeHost) serveUpload(w http.ResponseWriter, r *http.Request, rest string) {
	if !f.authorized(r) {
		jsonOut(w, 401, map[string]any{"message": "Bad credentials"})
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/") // repos/o/r/releases/id/assets
	if len(parts) != 6 || parts[0] != "repos" || parts[3] != "releases" || parts[5] != "assets" || r.Method != "POST" {
		jsonOut(w, 404, map[string]any{"message": "no such upload route"})
		return
	}
	f.addAsset(w, r, parts[1], parts[2], parts[4])
}

func (f *fakeHost) deleteAsset(w http.ResponseWriter, owner, name, assetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, rel := range f.rels {
		if !strings.HasPrefix(k, owner+"/"+name+"/") {
			continue
		}
		for i, a := range rel.Assets {
			if strconv.FormatInt(a.ID, 10) == assetID {
				rel.Assets = append(rel.Assets[:i], rel.Assets[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
	}
	jsonOut(w, 404, map[string]any{"message": "Not Found"})
}
