package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is set by the build (-ldflags "-X .../internal/cli.Version=...").
var Version = "dev"

// Config is the saved login. The file is written 0600 and the key is never
// printed back.
type Config struct {
	Server string `json:"server"`
	Key    string `json:"key"`
}

// Runtime is everything the CLI touches outside itself, so tests can stand in
// for it.
type Runtime struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// IsTTY says whether a stream is a terminal (human output) or not.
	IsTTY func(io.Writer) bool
	// HTTP is the client; its redirect policy is replaced.
	HTTP *http.Client
	Now  func() time.Time
}

// DefaultRuntime wires the real process.
func DefaultRuntime() *Runtime {
	return &Runtime{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv, IsTTY: isTerminal, Now: time.Now}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// home is the user's home directory: $HOME as the runtime sees it, else the
// system's answer.
func (rt *Runtime) home() string {
	if h := rt.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// configDir is where the login lives: SCRIOBH_CONFIG_DIR, or the user's config
// directory.
func (rt *Runtime) configDir() (string, error) {
	if d := rt.Getenv("SCRIOBH_CONFIG_DIR"); d != "" {
		return d, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "scriobh"), nil
}

func (rt *Runtime) cacheDir() (string, error) {
	if d := rt.Getenv("SCRIOBH_CACHE_DIR"); d != "" {
		return d, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "scriobh"), nil
}

func (rt *Runtime) configPath(g Global) (string, error) {
	if g.Config != "" {
		return g.Config, nil
	}
	d, err := rt.configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// LoadConfig reads the saved login; a missing file is an empty config.
func (rt *Runtime) LoadConfig(g Global) (Config, error) {
	p, err := rt.configPath(g)
	if err != nil {
		return Config{}, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	return c, nil
}

// SaveConfig writes the login with mode 0600, through a temporary file so a
// crash cannot leave half a key behind.
func (rt *Runtime) SaveConfig(g Global, c Config) (string, error) {
	p, err := rt.configPath(g)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return p, os.Rename(tmp.Name(), p)
}

// Credentials resolves the server and key: flag, then environment, then the
// saved login.
func (rt *Runtime) Credentials(g Global) (server, key string, err error) {
	cfg, err := rt.LoadConfig(g)
	if err != nil {
		return "", "", err
	}
	server = firstNonEmpty(g.Server, rt.Getenv("SCRIOBH_URL"), cfg.Server)
	key = firstNonEmpty(g.Key, rt.Getenv("SCRIOBH_API_KEY"), rt.Getenv("SCRIOBH_TOKEN"), cfg.Key)
	return strings.TrimSpace(server), strings.TrimSpace(key), nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// NormalizeServer checks and cleans a server URL.
func NormalizeServer(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, &UsageError{Msg: fmt.Sprintf("%q is not a server URL", s), Hint: "Use something like https://write.example.com or http://127.0.0.1:8080"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

// isLoopback reports whether a URL points at this machine.
func isLoopback(u *url.URL) bool {
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Client talks to one server with one key.
type Client struct {
	Base    *url.URL
	Key     string
	HTTP    *http.Client
	Timeout time.Duration
	Retries int
	sleep   func(time.Duration)
}

// Response is an HTTP response read into memory.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// NewClient builds a client; the HTTP client's redirect policy is set so a key
// is never sent anywhere the URL did not name.
func (rt *Runtime) NewClient(g Global) (*Client, error) {
	server, key, err := rt.Credentials(g)
	if err != nil {
		return nil, err
	}
	if server == "" {
		return nil, &Fail{Code: "no_server", Message: "No server is configured.", Hint: "Run: scriobh-cli login --url https://write.example.com --key scr_…   (or set SCRIOBH_URL and SCRIOBH_API_KEY)", Exit: ExitUsage}
	}
	base, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	hc := rt.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	hc2 := *hc
	hc2.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	timeout := 60 * time.Second
	if g.Timeout != "" {
		d, err := time.ParseDuration(g.Timeout)
		if err != nil || d <= 0 {
			return nil, usagef("--timeout takes a duration such as 30s")
		}
		timeout = d
	}
	retries := 2
	if g.retriesSet {
		retries = g.Retries
	}
	return &Client{Base: base, Key: key, HTTP: &hc2, Timeout: timeout, Retries: retries, sleep: time.Sleep}, nil
}

// Do sends one request. Network failures and 502/503/504 are retried a few
// times when the request is safe to repeat (a read, or a write that carries an
// Idempotency-Key); the exit is 8 when the server never answers.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body []byte, hdr map[string]string) (*Response, error) {
	u := *c.Base
	u.Path = c.Base.Path + path
	u.RawPath = ""
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	_, hasKey := hdr["Idempotency-Key"]
	safe := method == "GET" || method == "HEAD" || hasKey
	for attempt := 0; ; attempt++ {
		resp, err := c.once(ctx, method, u.String(), body, hdr)
		retryable := err != nil || resp.Status == 502 || resp.Status == 503 || resp.Status == 504
		if err == nil && !retryable {
			return resp, nil
		}
		if !retryable || !safe || attempt >= c.Retries || ctx.Err() != nil {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
		c.sleep(time.Duration(300*(1<<attempt)) * time.Millisecond)
	}
}

func (c *Client) once(ctx context.Context, method, rawURL string, body []byte, hdr map[string]string) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, &Fail{Code: "bad_request", Message: err.Error(), Exit: ExitUsage}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "scriobh-cli/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &Fail{Code: "unreachable", Message: "Could not reach " + c.Base.Host + ": " + scrub(err, c.Key), Exit: ExitUnreachable,
			Hint: "Check the server URL and your network.", Cause: err}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 512<<20))
	if err != nil {
		return nil, &Fail{Code: "unreachable", Message: "The connection to " + c.Base.Host + " broke: " + scrub(err, c.Key), Exit: ExitUnreachable, Cause: err}
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, &Fail{Code: "redirect", Message: fmt.Sprintf("The server redirected the request (%d) to %s. Redirects are not followed, so the key stays where you sent it.", res.StatusCode, res.Header.Get("Location")),
			Hint: "Set --server to the final address (for example the https:// one).", Exit: ExitUnreachable}
	}
	return &Response{Status: res.StatusCode, Header: res.Header, Body: b}, nil
}

// scrub removes the key from an error text, in case a transport error echoes
// a header.
func scrub(err error, key string) string {
	s := err.Error()
	if key != "" {
		s = strings.ReplaceAll(s, key, "[key]")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		s = ue.Err.Error()
	}
	return s
}

// newIdempotencyKey is a fresh random key for one invocation.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "cli-" + hex.EncodeToString(b)
}

// ---- the schema --------------------------------------------------------------

// SchemaSource says where a schema came from.
type SchemaSource string

// Where the schema came from.
const (
	SourceLive     SchemaSource = "server"   // fetched just now
	SourceCache    SchemaSource = "cache"    // the saved copy, still fresh or revalidated
	SourceStale    SchemaSource = "stale"    // the saved copy; the server could not be asked
	SourceEmbedded SchemaSource = "embedded" // the snapshot built into this binary
)

// cacheTTL is how long a saved schema is used without asking the server.
const cacheTTL = 10 * time.Minute

type cacheFile struct {
	Server    string          `json:"server"`
	ETag      string          `json:"etag"`
	FetchedAt time.Time       `json:"fetched_at"`
	Schema    json.RawMessage `json:"schema"`
}

func (rt *Runtime) cachePath(server string) (string, error) {
	d, err := rt.cacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(server))
	return filepath.Join(d, "schema-"+hex.EncodeToString(sum[:6])+".json"), nil
}

// LoadSchema returns the schema for the configured server: a fresh cached copy,
// else the server's (revalidated by ETag, so an unchanged schema costs a 304),
// else a stale copy, else the snapshot built into this binary. force skips the
// freshness check. The server's own schema hash names the version everywhere.
func (rt *Runtime) LoadSchema(ctx context.Context, g Global, force bool) (Schema, SchemaSource) {
	server, key, _ := rt.Credentials(g)
	if server == "" || key == "" {
		return Embedded(), SourceEmbedded
	}
	base, err := NormalizeServer(server)
	if err != nil {
		return Embedded(), SourceEmbedded
	}
	cp, _ := rt.cachePath(base.String())
	var cached cacheFile
	haveCache := false
	if b, err := os.ReadFile(cp); err == nil && json.Unmarshal(b, &cached) == nil && cached.Server == base.String() {
		if _, err := ParseSchema(cached.Schema); err == nil {
			haveCache = true
		}
	}
	if haveCache && !force && !g.NoCache && rt.now().Sub(cached.FetchedAt) < cacheTTL {
		s, _ := ParseSchema(cached.Schema)
		return s, SourceCache
	}
	cl, err := rt.NewClient(g)
	if err != nil {
		return fallback(cached, haveCache)
	}
	cl.Retries = 0
	cl.Timeout = 8 * time.Second
	hdr := map[string]string{}
	if haveCache && !g.NoCache {
		hdr["If-None-Match"] = cached.ETag
	}
	resp, err := cl.Do(ctx, "GET", "/api/v1/_ops", nil, nil, hdr)
	if err != nil || (resp.Status != 200 && resp.Status != 304) {
		return fallback(cached, haveCache)
	}
	if resp.Status == 304 && haveCache {
		cached.FetchedAt = rt.now()
		rt.writeCache(cp, cached)
		s, _ := ParseSchema(cached.Schema)
		return s, SourceCache
	}
	s, err := ParseSchema(resp.Body)
	if err != nil {
		return fallback(cached, haveCache)
	}
	rt.writeCache(cp, cacheFile{Server: base.String(), ETag: resp.Header.Get("ETag"), FetchedAt: rt.now(), Schema: json.RawMessage(resp.Body)})
	return s, SourceLive
}

func fallback(c cacheFile, have bool) (Schema, SchemaSource) {
	if have {
		if s, err := ParseSchema(c.Schema); err == nil {
			return s, SourceStale
		}
	}
	return Embedded(), SourceEmbedded
}

func (rt *Runtime) now() time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

func (rt *Runtime) writeCache(path string, c cacheFile) {
	b, err := json.Marshal(c)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".schema-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() == nil {
		_ = os.Rename(tmp.Name(), path)
	}
}
