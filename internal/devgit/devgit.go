//go:build !prod

// Package devgit is a development-only Git server for end-to-end tests: an in-process SSH server that hands
// git-upload-pack and git-receive-pack to the system git against bare repositories under a root directory,
// plus a loopback HTTP control API to create repositories, authorise keys, make commits from outside, rotate
// the host key and inspect results. It is never part of a production build.
package devgit

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Options configure a server.
type Options struct {
	Root         string // directory holding the bare repositories
	Listen       string // SSH listen address; default 127.0.0.1:0
	AllowAnyUser bool   // accept any SSH user name (the key still decides)
	RSA          bool   // also offer an RSA host key
}

// Server is a running dev Git server.
type Server struct {
	o       Options
	mu      sync.Mutex
	keys    []ssh.Signer
	orig    []ssh.Signer
	allowed map[string]ssh.PublicKey // by SHA256 fingerprint
	sshLn   net.Listener
	ctlLn   net.Listener
	ctl     *http.Server
	wg      sync.WaitGroup
	conns   map[net.Conn]bool
	done    chan struct{}
	once    sync.Once
}

// ErrNoGit is returned by Start when the system git program is missing.
var ErrNoGit = errors.New("the system git program is not available; the dev Git server uses it as the server side")

func newSigner(kind string) (ssh.Signer, error) {
	var key any
	switch kind {
	case "ed25519", "":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		key = k
	case "ecdsa":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		key = k
	case "rsa":
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		key = k
	default:
		return nil, fmt.Errorf("unknown key type %q (ed25519, ecdsa or rsa)", kind)
	}
	return ssh.NewSignerFromKey(key)
}

// Start brings the SSH listener and the control listener up.
func Start(o Options) (*Server, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, ErrNoGit
	}
	if o.Root == "" {
		return nil, errors.New("a root directory is required")
	}
	if err := os.MkdirAll(o.Root, 0o755); err != nil {
		return nil, err
	}
	if o.Listen == "" {
		o.Listen = "127.0.0.1:0"
	}
	s := &Server{o: o, allowed: map[string]ssh.PublicKey{}, conns: map[net.Conn]bool{}, done: make(chan struct{})}
	kinds := []string{"ed25519"}
	if o.RSA {
		kinds = append(kinds, "rsa")
	}
	for _, k := range kinds {
		sg, err := newSigner(k)
		if err != nil {
			return nil, err
		}
		s.keys = append(s.keys, sg)
	}
	s.orig = append([]ssh.Signer(nil), s.keys...)
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return nil, err
	}
	s.sshLn = ln
	cl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	s.ctlLn = cl
	s.ctl = &http.Server{Handler: s.routes()}
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.acceptLoop() }()
	go func() { defer s.wg.Done(); _ = s.ctl.Serve(cl) }()
	return s, nil
}

// SSHAddr is host:port of the SSH listener.
func (s *Server) SSHAddr() string { return s.sshLn.Addr().String() }

// ControlURL is the base URL of the control API.
func (s *Server) ControlURL() string { return "http://" + s.ctlLn.Addr().String() }

// Done is closed after Stop or POST /shutdown.
func (s *Server) Done() <-chan struct{} { return s.done }

// Stop shuts both listeners and open connections.
func (s *Server) Stop() {
	s.once.Do(func() {
		_ = s.sshLn.Close()
		_ = s.ctl.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		close(s.done)
	})
}

// Authorize adds a public key (an authorized_keys line) and returns its fingerprint.
func (s *Server) Authorize(line string) (string, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", err
	}
	fp := ssh.FingerprintSHA256(k)
	s.mu.Lock()
	s.allowed[fp] = k
	s.mu.Unlock()
	return fp, nil
}

func (s *Server) acceptLoop() {
	for {
		c, err := s.sshLn.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

func (s *Server) serve(c net.Conn) {
	s.mu.Lock()
	keys := append([]ssh.Signer(nil), s.keys...)
	s.mu.Unlock()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !s.o.AllowAnyUser && meta.User() != "git" {
			return nil, errors.New("only the user git may connect")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, a := range s.allowed {
			if bytes.Equal(a.Marshal(), key.Marshal()) {
				return nil, nil
			}
		}
		return nil, errors.New("unknown key")
	}}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, creqs)
	}
}

// repoPath maps the path of an exec request to a directory under the root, or "" when it escapes it.
func (s *Server) repoPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "'\"")
	p = filepath.Clean("/" + strings.TrimPrefix(p, "/"))
	full := filepath.Join(s.o.Root, p)
	rel, err := filepath.Rel(s.o.Root, full)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return full
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		if len(req.Payload) < 4 {
			_ = req.Reply(false, nil)
			return
		}
		n := binary.BigEndian.Uint32(req.Payload)
		if int(n) > len(req.Payload)-4 {
			_ = req.Reply(false, nil)
			return
		}
		cmdline := string(req.Payload[4 : 4+n])
		_ = req.Reply(true, nil)
		name, rest, _ := strings.Cut(cmdline, " ")
		var sub string
		switch name {
		case "git-upload-pack":
			sub = "upload-pack"
		case "git-receive-pack":
			sub = "receive-pack"
		default:
			_, _ = io.WriteString(ch.Stderr(), "no such command\n")
			_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 127})
			return
		}
		dir := s.repoPath(rest)
		if dir == "" {
			_, _ = io.WriteString(ch.Stderr(), "no such repository\n")
			_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 128})
			return
		}
		cmd := exec.Command("git", sub, dir)
		cmd.Stdin = ch
		cmd.Stdout = ch
		cmd.Stderr = ch.Stderr()
		cmd.Env = gitEnv(s.o.Root)
		code := byte(0)
		if err := cmd.Run(); err != nil {
			code = 1
		}
		_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, code})
		return
	}
}

func gitEnv(home string) []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home, "GIT_TERMINAL_PROMPT=0")
}

// ---- helpers shared with the control API ------------------------------------------------------

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,60}$`)
var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,100}$`)

func (s *Server) repoDir(name string) (string, error) {
	name = strings.TrimSuffix(name, ".git")
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("a repository name is letters, digits, dot, dash and underscore")
	}
	return filepath.Join(s.o.Root, name+".git"), nil
}

func git(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (s *Server) repoNames() []string {
	ents, _ := os.ReadDir(s.o.Root)
	out := []string{}
	for _, e := range ents {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
			out = append(out, strings.TrimSuffix(e.Name(), ".git"))
		}
	}
	sort.Strings(out)
	return out
}

// foreignCommit makes a commit on branch from outside Scríobh and pushes it to the bare repository.
func (s *Server) foreignCommit(repo, branch, message string, files map[string]string) (string, error) {
	dir, err := s.repoDir(repo)
	if err != nil {
		return "", err
	}
	if !branchRe.MatchString(branch) {
		return "", errors.New("bad branch name")
	}
	if _, err := os.Stat(dir); err != nil {
		return "", errors.New("no such repository")
	}
	tmp, err := os.MkdirTemp("", "devgit-foreign-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := append(gitEnv(tmp),
		"GIT_AUTHOR_NAME=Someone Else", "GIT_AUTHOR_EMAIL=else@example.com",
		"GIT_COMMITTER_NAME=Someone Else", "GIT_COMMITTER_EMAIL=else@example.com")
	if _, err := git(tmp, env, "init", "-q", "-b", "tmp-work"); err != nil {
		return "", err
	}
	if _, err := git(tmp, env, "remote", "add", "origin", dir); err != nil {
		return "", err
	}
	if _, err := git(dir, env, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		if _, err := git(tmp, env, "fetch", "-q", "origin", branch); err != nil {
			return "", err
		}
		if _, err := git(tmp, env, "checkout", "-q", "-B", branch, "FETCH_HEAD"); err != nil {
			return "", err
		}
	} else if _, err := git(tmp, env, "checkout", "-q", "--orphan", branch); err != nil {
		return "", err
	}
	if len(files) == 0 {
		files = map[string]string{"outside.txt": message + "\n"}
	}
	for p, c := range files {
		clean := filepath.Clean(p)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return "", errors.New("bad file path " + p)
		}
		full := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return "", err
		}
	}
	if _, err := git(tmp, env, "add", "-A"); err != nil {
		return "", err
	}
	if message == "" {
		message = "A commit from outside"
	}
	if _, err := git(tmp, env, "commit", "-q", "--allow-empty", "-m", message); err != nil {
		return "", err
	}
	sha, err := git(tmp, env, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := git(tmp, env, "push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", err
	}
	return sha, nil
}
