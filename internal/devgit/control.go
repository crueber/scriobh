//go:build !prod

package devgit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /info", s.hInfo)
	m.HandleFunc("POST /repos", s.hRepos)
	m.HandleFunc("POST /authorize", s.hAuthorize)
	m.HandleFunc("POST /deauthorize", s.hDeauthorize)
	m.HandleFunc("POST /foreign-commit", s.hForeign)
	m.HandleFunc("GET /head", s.hHead)
	m.HandleFunc("GET /log", s.hLog)
	m.HandleFunc("POST /rotate-host-key", s.hRotate)
	m.HandleFunc("POST /restore-host-key", s.hRestore)
	m.HandleFunc("POST /verify-bundle", s.hVerifyBundle)
	m.HandleFunc("POST /shutdown", s.hShutdown)
	return m
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	reply(w, code, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	d := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := d.Decode(v); err != nil && err.Error() != "EOF" {
		return err
	}
	return nil
}

type hostKeyInfo struct {
	Type              string `json:"type"`
	Fingerprint       string `json:"fingerprint"`
	AuthorizedKeyLine string `json:"authorized_keys_line"`
}

func (s *Server) hostKeyInfos() []hostKeyInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []hostKeyInfo{}
	for _, k := range s.keys {
		pk := k.PublicKey()
		out = append(out, hostKeyInfo{Type: pk.Type(), Fingerprint: ssh.FingerprintSHA256(pk), AuthorizedKeyLine: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))})
	}
	return out
}

func (s *Server) hInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	auth := []string{}
	for fp := range s.allowed {
		auth = append(auth, fp)
	}
	s.mu.Unlock()
	sort.Strings(auth)
	reply(w, 200, map[string]any{"ssh_addr": s.SSHAddr(), "host_keys": s.hostKeyInfos(), "repos": s.repoNames(), "authorized": auth})
}

func (s *Server) hRepos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	dir, err := s.repoDir(in.Name)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if _, err := os.Stat(dir); err == nil {
		fail(w, 409, errors.New("that repository exists"))
		return
	}
	if _, err := git(s.o.Root, gitEnv(s.o.Root), "init", "-q", "--bare", "-b", "main", dir); err != nil {
		fail(w, 500, err)
		return
	}
	reply(w, 201, map[string]string{"name": strings.TrimSuffix(in.Name, ".git"), "path": "/" + strings.TrimSuffix(in.Name, ".git") + ".git"})
}

func (s *Server) hAuthorize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicKey string `json:"public_key"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	fp, err := s.Authorize(in.PublicKey)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]string{"fingerprint": fp})
}

func (s *Server) hDeauthorize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	s.mu.Lock()
	_, had := s.allowed[in.Fingerprint]
	delete(s.allowed, in.Fingerprint)
	s.mu.Unlock()
	reply(w, 200, map[string]bool{"removed": had})
}

func (s *Server) hForeign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo    string            `json:"repo"`
		Branch  string            `json:"branch"`
		Message string            `json:"message"`
		Files   map[string]string `json:"files"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Branch == "" {
		in.Branch = "main"
	}
	sha, err := s.foreignCommit(in.Repo, in.Branch, in.Message, in.Files)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 201, map[string]string{"sha": sha})
}

func (s *Server) hHead(w http.ResponseWriter, r *http.Request) {
	dir, err := s.repoDir(r.URL.Query().Get("repo"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	b := r.URL.Query().Get("branch")
	if b == "" {
		b = "main"
	}
	if !branchRe.MatchString(b) {
		fail(w, 400, errors.New("bad branch name"))
		return
	}
	sha, err := git(dir, gitEnv(s.o.Root), "rev-parse", "--verify", "-q", "refs/heads/"+b)
	if err != nil || sha == "" {
		fail(w, 404, errors.New("no such branch"))
		return
	}
	reply(w, 200, map[string]string{"sha": sha})
}

func (s *Server) hLog(w http.ResponseWriter, r *http.Request) {
	dir, err := s.repoDir(r.URL.Query().Get("repo"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	b := r.URL.Query().Get("branch")
	if b == "" {
		b = "main"
	}
	if !branchRe.MatchString(b) {
		fail(w, 400, errors.New("bad branch name"))
		return
	}
	out, err := git(dir, gitEnv(s.o.Root), "log", "--format=%H%x1f%s%x1f%an%x1f%ae", "refs/heads/"+b)
	if err != nil {
		fail(w, 404, errors.New("no such branch"))
		return
	}
	type c struct {
		SHA     string `json:"sha"`
		Subject string `json:"subject"`
		Author  string `json:"author"`
		Email   string `json:"email"`
	}
	items := []c{}
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(l, "\x1f")
		if len(p) == 4 {
			items = append(items, c{p[0], p[1], p[2], p[3]})
		}
	}
	reply(w, 200, map[string]any{"items": items})
}

func (s *Server) hRotate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type string `json:"type"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Type == "" {
		s.mu.Lock()
		if len(s.keys) > 0 {
			in.Type = s.keys[0].PublicKey().Type()
		}
		s.mu.Unlock()
		switch in.Type {
		case "ssh-ed25519":
			in.Type = "ed25519"
		case "ssh-rsa":
			in.Type = "rsa"
		default:
			in.Type = "ed25519"
		}
	}
	sg, err := newSigner(in.Type)
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.mu.Lock()
	s.keys = []ssh.Signer{sg}
	s.mu.Unlock()
	reply(w, 200, map[string]any{"host_keys": s.hostKeyInfos()})
}

func (s *Server) hRestore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.keys = append([]ssh.Signer(nil), s.orig...)
	s.mu.Unlock()
	reply(w, 200, map[string]any{"host_keys": s.hostKeyInfos()})
}

func (s *Server) hVerifyBundle(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if _, err := os.Stat(in.Path); err != nil {
		fail(w, 404, errors.New("no such file"))
		return
	}
	scratch, err := os.MkdirTemp("", "devgit-bundle-")
	if err != nil {
		fail(w, 500, err)
		return
	}
	defer os.RemoveAll(scratch)
	env := gitEnv(scratch)
	if _, err := git(scratch, env, "init", "-q"); err != nil {
		fail(w, 500, err)
		return
	}
	vcmd := exec.Command("git", "bundle", "verify", in.Path)
	vcmd.Dir, vcmd.Env = scratch, env
	var vo bytes.Buffer
	vcmd.Stdout, vcmd.Stderr = &vo, &vo
	verr := vcmd.Run()
	type head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	}
	heads := []head{}
	lcmd := exec.Command("git", "bundle", "list-heads", in.Path)
	lcmd.Dir, lcmd.Env = scratch, env
	lo, lerr := lcmd.Output()
	if lerr == nil {
		sc := bufio.NewScanner(bytes.NewReader(lo))
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) == 2 {
				heads = append(heads, head{f[0], f[1]})
			}
		}
	}
	reply(w, 200, map[string]any{"ok": verr == nil && lerr == nil, "verify": strings.TrimSpace(vo.String()), "heads": heads})
}

func (s *Server) hShutdown(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, map[string]bool{"stopping": true})
	// Flush before Stop closes the listener and every connection: under the
	// race detector the goroutine could otherwise cut the response off and the
	// client saw EOF instead of the 200 (internal/devgit's end-to-end test).
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.Stop()
}
