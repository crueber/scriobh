package gitmirror

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

// The code host's REST API (F-368, F-369): GitHub, and Forgejo or Gitea. It is a second client beside the Git
// transport and is held to the same rules, with its own limits, because a JSON API is not a repository:
//
//   - every connection goes through the network guard, the address that is connected to being the one that is
//     checked; no proxy from the environment;
//   - a redirect is never followed (the token would go where a server said); the only hosts a request may name are
//     the two this client derives from the host the owner configured (the API and, for GitHub, its upload host).
//     No URL taken from a response (clone_url, upload_url, browser_download_url, a Link header) is ever requested;
//   - a body is limited (1 MiB of JSON), pages are limited, every request has a deadline;
//   - what the host says in its error messages is never kept or shown: an error is a fixed sentence for a status.
//     The token is in no URL, no log line and no error.

// Providers.
const (
	ProviderGitHub  = "github"
	ProviderForgejo = "forgejo"
)

// Limits of the API client.
const (
	apiBodyLimit   = 1 << 20
	apiTimeout     = 30 * time.Second
	uploadTimeout  = 10 * time.Minute
	repoPageSize   = 50
	maxRetryWait   = time.Hour
	maxAssetsSeen  = 200
	githubVersion  = "2022-11-28"
	userAgent      = "Scriobh-Git-Publish"
	maxRepoNameLen = 100
)

// Codes of the API client.
const (
	CodeNameTaken            = "name_taken"
	CodeProviderRateLimited  = "provider_rate_limited"
	CodeSSORequired          = "sso_required"
	CodeProviderRejected     = "provider_rejected"
	CodeProviderDown         = "provider_unavailable"
	CodeProviderBad          = "provider_bad_response"
	CodeTokenScope           = "token_cannot_write"
	CodeRepoNotEmpty         = "repo_not_empty"
	CodePlanChanged          = "plan_changed"
	CodeTagConflict          = "tag_conflict"
	CodePublishIncomplete    = "publish_incomplete"
	CodeConfirmationRequired = "confirmation_required"
)

// providerTarget is a host and where its API is.
type providerTarget struct {
	kind    string // github or forgejo
	host    string // the web host as the owner configured it: github.com, or host[:port]
	api     string // base address of the REST API, no trailing slash
	uploads string // base address of the asset upload endpoint (GitHub); empty means the API
}

func (t providerTarget) apiHost() string     { return hostOf(t.api) }
func (t providerTarget) uploadsHost() string { return hostOf(firstNonEmpty(t.uploads, t.api)) }

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// newTarget validates the host the owner typed and derives the API. For GitHub the host is github.com or a
// GitHub Enterprise Server (the API under /api/v3, uploads under /api/uploads); for Forgejo it is the host
// (the API under /api/v1). The host goes through the same address checks as a remote's.
func newTarget(kind, host string) (providerTarget, *Error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	host = strings.ToLower(strings.TrimSpace(host))
	switch kind {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderForgejo:
		if host == "" {
			return providerTarget{}, gerr(CodeBadURL, "Enter the host of your Forgejo or Gitea, like git.example.org.")
		}
	default:
		return providerTarget{}, gerr(CodeBadURL, "The provider is github or forgejo.")
	}
	ep, e := parseURL("https://"+host+"/owner/repo.git", false)
	if e != nil || strings.ContainsAny(host, "/@?#") {
		return providerTarget{}, gerr(CodeBadURL, "That is not a host name. Write it without https:// or a path, like git.example.org or git.example.org:3000.")
	}
	web := ep.Host
	if ep.Port != 443 {
		web = net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))
	}
	t := providerTarget{kind: kind, host: web}
	switch {
	case kind == ProviderGitHub && web == "github.com":
		t.api, t.uploads = "https://api.github.com", "https://uploads.github.com"
	case kind == ProviderGitHub:
		t.api, t.uploads = "https://"+web+"/api/v3", "https://"+web+"/api/uploads"
	default:
		t.api = "https://" + web + "/api/v1"
	}
	return t, nil
}

// repoURL is the address of a repository's Git endpoint: built here, never taken from a response.
func (t providerTarget) repoURL(owner, name string) string {
	return "https://" + t.host + "/" + owner + "/" + name + ".git"
}

// providerRootCAs is a test seam: the roots the API client trusts besides the system's.
var providerRootCAs atomic.Pointer[x509.CertPool]

// providerTimeout is a test seam for the per-request deadline.
var providerTimeout atomic.Int64

func apiDeadline() time.Duration {
	if d := providerTimeout.Load(); d > 0 {
		return time.Duration(d)
	}
	return apiTimeout
}

// providerClient makes the requests of one account against one host.
type providerClient struct {
	t     providerTarget
	token string
	hc    *http.Client
	up    *http.Client
}

func newProviderClient(t providerTarget, token string) *providerClient {
	mk := func(timeout time.Duration) *http.Client {
		tr := &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				mode := netguard.PublicOnly
				if allowPrivate.Load() {
					mode = netguard.AllowLocal
				}
				return netguard.Dialer(netguard.Options{Mode: mode, DialTimeout: dialTimeout})(ctx, network, addr)
			},
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: providerRootCAs.Load()},
			TLSHandshakeTimeout:    15 * time.Second,
			ResponseHeaderTimeout:  timeout,
			MaxResponseHeaderBytes: 64 << 10,
			DisableKeepAlives:      true,
			ForceAttemptHTTP2:      false,
		}
		return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &providerClient{t: t, token: token, hc: mk(apiDeadline()), up: mk(uploadTimeout)}
}

// do makes one request. out, when given, is decoded from a 2xx answer. The error is typed and is a fixed
// sentence: nothing the host said is kept.
func (c *providerClient) do(ctx context.Context, hc *http.Client, method, rawURL string, body io.Reader, ctype string, out any) (int, *Error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return 0, gerr(CodeInternal, "The request to %s could not be made.", c.t.host)
	}
	if h := strings.ToLower(u.Host); h != c.t.apiHost() && h != c.t.uploadsHost() {
		return 0, gerr(CodeInternal, "The request to %s could not be made.", c.t.host)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return 0, gerr(CodeInternal, "The request to %s could not be made.", c.t.host)
	}
	req.Header.Set("User-Agent", userAgent)
	switch c.t.kind {
	case ProviderGitHub:
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", githubVersion)
	default:
		req.Header.Set("Authorization", "token "+c.token)
		req.Header.Set("Accept", "application/json")
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, c.transportError(err)
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, apiBodyLimit+1))
	if len(data) > apiBodyLimit {
		return resp.StatusCode, gerr(CodeProviderBad, "%s sent a longer answer than this server accepts, so it was not read.", c.t.host)
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, c.statusError(resp, method, data)
	}
	if rerr != nil {
		return resp.StatusCode, c.transportError(rerr)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, gerr(CodeProviderBad, "%s answered with something this server cannot read.", c.t.host)
		}
	}
	return resp.StatusCode, nil
}

func (c *providerClient) transportError(err error) *Error {
	switch {
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeInterrupted, Message: "The request was cancelled.", Host: c.t.host}
	case errors.Is(err, errBlockedAddr), errors.Is(err, netguard.ErrBlocked):
		return &Error{Code: CodeAddressBlocked, Message: fmt.Sprintf("%s could not be reached: its address is not one this server may connect to. If it is a machine on your own network, an administrator can allow that with SCRIOBH_GIT_ALLOW_PRIVATE=1.", c.t.host), Host: c.t.host, cause: err}
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return &Error{Code: CodeTimeout, Message: fmt.Sprintf("%s did not answer in time.", c.t.host), Host: c.t.host, Retry: true, cause: err}
	}
	logTransportDetail(CodeUnreachable, err, c.token)
	return &Error{Code: CodeUnreachable, Message: fmt.Sprintf("%s could not be reached.", c.t.host), Host: c.t.host, Retry: true, cause: err}
}

// statusError is the fixed sentence for a status. What is read of the body is the structured part only (the
// name of the field a validation error is about), never a message.
func (c *providerClient) statusError(resp *http.Response, method string, body []byte) *Error {
	h := c.t.host
	mk := func(code, f string, a ...any) *Error {
		return &Error{Code: code, Message: fmt.Sprintf(f, a...), Host: h}
	}
	switch s := resp.StatusCode; {
	case s == http.StatusUnauthorized:
		return mk(CodeAuthFailed, "%s refused the token. It may have expired or been revoked: connect the account again with a new one.", h)
	case s == http.StatusTooManyRequests, s == http.StatusForbidden && rateLimited(resp):
		e := mk(CodeProviderRateLimited, "%s says there have been too many requests. Wait a little and try again.", h)
		e.RetryAfter, e.Retry = retryAfter(resp), true
		return e
	case s == http.StatusForbidden && resp.Header.Get("X-GitHub-SSO") != "":
		// The header carries a URL to authorize the token; it is never followed or shown.
		return mk(CodeSSORequired, "%s will not let this token reach that organization until the token is authorized for its single sign-on. Do that in the token's settings at the host, then try again.", h)
	case s == http.StatusForbidden:
		return mk(CodeTokenScope, "%s says this token may not do that. It needs permission to create repositories and to write contents (for a release, also releases).", h)
	case s == http.StatusNotFound:
		return mk(CodeRepoNotFound, "%s says that does not exist, or that the token has no access to it.", h)
	case s == http.StatusConflict, s == http.StatusUnprocessableEntity && c.nameField(body):
		if method == http.MethodPost {
			return mk(CodeNameTaken, "%s says a repository or release with that name already exists.", h)
		}
		return mk(CodeProviderRejected, "%s did not accept the request.", h)
	case s == http.StatusUnprocessableEntity, s >= 400 && s < 500:
		return mk(CodeProviderRejected, "%s did not accept the request.", h)
	case s >= 300 && s < 400:
		return mk(CodeProviderBad, "%s answered with a redirect, which is not followed.", h)
	}
	return &Error{Code: CodeProviderDown, Message: fmt.Sprintf("%s had a problem answering. Try again later.", h), Host: h, Retry: true}
}

// nameField reports whether a GitHub validation error is about the repository's name already existing: the
// structured field and code are read, never the message.
func (c *providerClient) nameField(body []byte) bool {
	var v struct {
		Errors []struct {
			Resource string `json:"resource"`
			Field    string `json:"field"`
			Code     string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, e := range v.Errors {
		if e.Field == "name" || e.Code == "already_exists" {
			return true
		}
	}
	return false
}

func rateLimited(resp *http.Response) bool {
	return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
}

// retryAfter reads the wait a host asks for, in seconds, bounded.
func retryAfter(resp *http.Response) int {
	wait := time.Duration(0)
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			wait = time.Duration(n) * time.Second
		}
	} else if v := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			wait = time.Until(time.Unix(n, 0))
		}
	}
	switch {
	case wait <= 0:
		wait = 60 * time.Second
	case wait > maxRetryWait:
		wait = maxRetryWait
	}
	return int(wait.Seconds())
}

// ---- the calls -----------------------------------------------------------------

// repoInfo is a repository as the host reports it (the fields used).
type repoInfo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Empty         bool   `json:"empty"`
	Size          int64  `json:"size"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Push  bool `json:"push"`
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func (c *providerClient) path(parts ...string) string {
	return c.t.api + "/" + strings.Join(parts, "/")
}

func pe(s string) string { return url.PathEscape(s) }

// user returns the login the token belongs to.
func (c *providerClient) user(ctx context.Context) (string, *Error) {
	var u struct {
		Login string `json:"login"`
	}
	if _, e := c.do(ctx, c.hc, http.MethodGet, c.path("user"), nil, "", &u); e != nil {
		return "", e
	}
	if !validOwner(u.Login) {
		return "", gerr(CodeProviderBad, "%s answered with something this server cannot read.", c.t.host)
	}
	return u.Login, nil
}

// listRepos returns one page of the repositories the token can push to.
func (c *providerClient) listRepos(ctx context.Context, page int) (repos []repoInfo, more bool, e *Error) {
	if page < 1 {
		page = 1
	}
	var q string
	switch c.t.kind {
	case ProviderGitHub:
		q = fmt.Sprintf("user/repos?per_page=%d&page=%d&sort=full_name&affiliation=owner,collaborator,organization_member", repoPageSize, page)
	default:
		q = fmt.Sprintf("user/repos?limit=%d&page=%d", repoPageSize, page)
	}
	var raw []repoInfo
	if _, e := c.do(ctx, c.hc, http.MethodGet, c.t.api+"/"+q, nil, "", &raw); e != nil {
		return nil, false, e
	}
	more = len(raw) >= repoPageSize
	for _, r := range raw {
		if !validOwner(r.Owner.Login) || !validRepoName(r.Name) || !(r.Permissions.Push || r.Permissions.Admin) {
			continue
		}
		repos = append(repos, r)
	}
	return repos, more, nil
}

// repo reads one repository; exists is false for a 404.
func (c *providerClient) repo(ctx context.Context, owner, name string) (*repoInfo, bool, *Error) {
	var r repoInfo
	st, e := c.do(ctx, c.hc, http.MethodGet, c.path("repos", pe(owner), pe(name)), nil, "", &r)
	if st == http.StatusNotFound {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	return &r, true, nil
}

// createRepo creates an empty repository (no README, no licence: the first push is ours). The owner is the
// account's own login (the user endpoint) or an organization the token can create in.
func (c *providerClient) createRepo(ctx context.Context, login, owner, name, description string, private bool) (*repoInfo, *Error) {
	body := map[string]any{"name": name, "description": description, "private": private, "auto_init": false}
	if c.t.kind == ProviderForgejo {
		body["default_branch"] = "main"
	}
	b, _ := json.Marshal(body)
	p := c.path("user", "repos")
	if !strings.EqualFold(owner, login) {
		p = c.path("orgs", pe(owner), "repos")
	}
	var r repoInfo
	if _, e := c.do(ctx, c.hc, http.MethodPost, p, bytes.NewReader(b), "application/json", &r); e != nil {
		return nil, e
	}
	return &r, nil
}

// setDefaultBranch points the repository's default branch at one that now exists.
func (c *providerClient) setDefaultBranch(ctx context.Context, owner, name, branch string) *Error {
	b, _ := json.Marshal(map[string]any{"default_branch": branch})
	_, e := c.do(ctx, c.hc, http.MethodPatch, c.path("repos", pe(owner), pe(name)), bytes.NewReader(b), "application/json", nil)
	return e
}

type assetInfo struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	State string `json:"state"`
}

type releaseInfo struct {
	ID      int64       `json:"id"`
	Tag     string      `json:"tag_name"`
	Body    string      `json:"body"`
	HTMLURL string      `json:"html_url"`
	Draft   bool        `json:"draft"`
	Assets  []assetInfo `json:"assets"`
}

func (c *providerClient) releaseByTag(ctx context.Context, owner, name, tag string) (*releaseInfo, bool, *Error) {
	var r releaseInfo
	st, e := c.do(ctx, c.hc, http.MethodGet, c.path("repos", pe(owner), pe(name), "releases", "tags", pe(tag)), nil, "", &r)
	if st == http.StatusNotFound {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	if len(r.Assets) > maxAssetsSeen {
		r.Assets = r.Assets[:maxAssetsSeen]
	}
	return &r, true, nil
}

func (c *providerClient) createRelease(ctx context.Context, owner, name, tag, target, title, notes string) (*releaseInfo, *Error) {
	b, _ := json.Marshal(map[string]any{"tag_name": tag, "target_commitish": target, "name": title, "body": notes, "draft": false, "prerelease": false})
	var r releaseInfo
	if _, e := c.do(ctx, c.hc, http.MethodPost, c.path("repos", pe(owner), pe(name), "releases"), bytes.NewReader(b), "application/json", &r); e != nil {
		return nil, e
	}
	return &r, nil
}

// uploadAsset adds a file to a release. GitHub takes the raw body at its upload host (derived, never the
// upload_url of a response); Forgejo takes a multipart form at the API.
func (c *providerClient) uploadAsset(ctx context.Context, owner, name string, releaseID int64, asset, ctype string, data []byte) *Error {
	id := strconv.FormatInt(releaseID, 10)
	switch c.t.kind {
	case ProviderGitHub:
		base := firstNonEmpty(c.t.uploads, c.t.api)
		u := base + "/repos/" + pe(owner) + "/" + pe(name) + "/releases/" + id + "/assets?name=" + url.QueryEscape(asset)
		_, e := c.do(ctx, c.up, http.MethodPost, u, bytes.NewReader(data), firstNonEmpty(ctype, "application/octet-stream"), nil)
		return e
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachment"; filename=%q`, asset))
	h.Set("Content-Type", firstNonEmpty(ctype, "application/octet-stream"))
	part, err := mw.CreatePart(h)
	if err != nil {
		return gerr(CodeInternal, "The upload could not be prepared.")
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	u := c.path("repos", pe(owner), pe(name), "releases", id, "assets") + "?name=" + url.QueryEscape(asset)
	_, e := c.do(ctx, c.up, http.MethodPost, u, &buf, mw.FormDataContentType(), nil)
	return e
}

func (c *providerClient) deleteAsset(ctx context.Context, owner, name string, releaseID, assetID int64) *Error {
	var p string
	switch c.t.kind {
	case ProviderGitHub:
		p = c.path("repos", pe(owner), pe(name), "releases", "assets", strconv.FormatInt(assetID, 10))
	default:
		p = c.path("repos", pe(owner), pe(name), "releases", strconv.FormatInt(releaseID, 10), "assets", strconv.FormatInt(assetID, 10))
	}
	_, e := c.do(ctx, c.hc, http.MethodDelete, p, nil, "", nil)
	return e
}

// ---- names -----------------------------------------------------------------------

// validOwner accepts the logins hosts allow: letters, digits, dots, dashes and underscores. Anything else in a
// response is not used (it would go into an address).
func validOwner(s string) bool {
	if s == "" || len(s) > 100 || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// validRepoName is the same rule for a repository name, which may not be "." or "..", end in .git or end in a dot.
func validRepoName(s string) bool {
	return validOwner(s) && len(s) <= maxRepoNameLen && s != "." && s != ".." && !strings.HasSuffix(s, ".git") && !strings.HasSuffix(s, ".")
}
