package gitmirror

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Remote states (git_remotes.state).
const (
	StateNew      = "new"
	StateSynced   = "synced"
	StateBehind   = "behind"
	StateRejected = "rejected"
	StateBlocked  = "blocked"
)

// Auth kinds.
const (
	AuthSSH   = "ssh_deploy_key"
	AuthHTTPS = "https_token"
)

// Audiences (git_remotes.audience): what a remote may carry.
const (
	// AudiencePrivate is the full mirror: manuscript, notes (if switched on), alternatives, ghosted text, the
	// outline's details. For a backup in a repository only the owner can read.
	AudiencePrivate = "private"
	// AudiencePublic is what a reader of the book sees and nothing else (see audience.go). It is the default
	// of a remote made now: the strictest.
	AudiencePublic = "public"
)

// Push modes.
const (
	ModeOnSave   = "on_save"
	ModeInterval = "interval"
	ModeManual   = "manual"
)

// Push triggers (git_pushes.trigger).
const (
	TriggerSave     = "save"
	TriggerInterval = "interval"
	TriggerManual   = "manual"
	TriggerRetry    = "retry"
	TriggerForce    = "force"
)

// Limits.
const (
	maxRemotesPerProject = 5 // F-362: a private backup, a public mirror, and room for a few more
	maxNameRunes         = 80
	maxURLBytes          = 500
	maxPrefixBytes       = 120
	maxBranchBytes       = 100
	minIntervalMinutes   = 5
	maxIntervalMinutes   = 10080
	defaultInterval      = 60
	maxTokenBytes        = 1024
)

// remoteRow is a git_remotes row, secrets included. It never leaves this
// package: views are built from it.
type remoteRow struct {
	ID, ProjectID, Name, Preset, URL, Branch, PathPrefix string
	AuthKind, Username, SecretCipher, PublicKey          string
	KnownHosts                                           string
	PushMode                                             string
	IntervalMinutes                                      int
	IncludeNotes, Enabled                                bool
	AuthorName, AuthorEmail                              string
	Audience, CreatedVia                                 string
	OnlyDone, SignCommits, RestorePaused                 bool

	State                                    string
	LastPushAt, LastAttemptAt, NextAttemptAt string
	LastCommitSHA, LocalSHA, RemoteSHA       string
	MirrorHash                               string
	LastError, LastErrorCode                 string
	FailCount                                int
	CreatedAt, UpdatedAt                     string
}

// endpointInfo is a remote address taken apart.
type endpointInfo struct {
	Proto string // ssh, https or file
	User  string
	Host  string
	Port  int
	Path  string
}

// HostPort is the host and port as pinned and shown ("github.com:22").
func (e endpointInfo) HostPort() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$|^\[[0-9A-Fa-f:.]+\]$`)

// parseURL checks and takes apart a remote address. Only ssh://, scp-style
// (git@host:owner/repo.git) and https:// are accepted, with no credentials in
// the address. file:// and local paths are accepted only when the service was
// built to allow them (tests); otherwise any editor could point a remote at a
// directory of the server.
func parseURL(raw string, allowFile bool) (endpointInfo, *Error) {
	var zero endpointInfo
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return zero, gerr(CodeBadURL, "Enter the address of the repository.")
	case len(raw) > maxURLBytes:
		return zero, gerr(CodeBadURL, "That address is too long.")
	case strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return zero, gerr(CodeBadURL, "An address has no spaces or control characters.")
	}
	if strings.ContainsAny(raw, "?#") {
		return zero, gerr(CodeBadURL, "A repository address has no query string or fragment. Credentials do not belong in it: enter a token in its own field.")
	}
	ep, err := transport.NewEndpoint(raw)
	if err != nil {
		return zero, gerr(CodeBadURL, "That is not an address Git can use. Use ssh://git@host/owner/repo.git or https://host/owner/repo.git.")
	}
	out := endpointInfo{Proto: ep.Protocol, User: ep.User, Host: strings.Trim(ep.Host, "[]"), Port: ep.Port, Path: ep.Path}
	switch ep.Protocol {
	case "ssh":
		if out.Port == 0 {
			out.Port = 22
		}
		if out.User == "" {
			out.User = "git"
		}
		if ep.Password != "" {
			return zero, gerr(CodeBadURL, "An SSH address does not carry a password. Use a deploy key.")
		}
	case "https":
		if out.Port == 0 {
			out.Port = 443
		}
		if ep.User != "" || ep.Password != "" {
			return zero, gerr(CodeBadURL, "Do not put a user name or token in the address. Enter them in their own fields; the token is stored encrypted.")
		}
	case "http":
		return zero, gerr(CodeBadURL, "Plain http:// would send the token in the clear. Use https://.")
	case "file":
		if !allowFile {
			return zero, gerr(CodeBadURL, "Only ssh:// and https:// addresses are supported.")
		}
		return out, nil
	default:
		return zero, gerr(CodeBadURL, "Only ssh:// and https:// addresses are supported.")
	}
	if (net.ParseIP(out.Host) == nil && !hostRe.MatchString(out.Host)) || len(out.Host) > 253 {
		return zero, gerr(CodeBadURL, "The host name in that address is not valid.")
	}
	p := strings.Trim(out.Path, "/")
	if p == "" || strings.Contains(out.Path, "..") {
		return zero, gerr(CodeBadURL, "The address must name a repository, like owner/repo.git.")
	}
	return out, nil
}

// cleanBranch validates a branch name.
func cleanBranch(b string) (string, *Error) {
	b = strings.TrimSpace(b)
	if b == "" {
		return "main", nil
	}
	if len(b) > maxBranchBytes || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") ||
		strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{") || strings.ContainsAny(b, " ~^:?*[\\") ||
		strings.IndexFunc(b, unicode.IsControl) >= 0 {
		return "", gerr(CodeBadURL, "That is not a valid branch name.")
	}
	if err := plumbing.NewBranchReferenceName(b).Validate(); err != nil {
		return "", gerr(CodeBadURL, "That is not a valid branch name.")
	}
	return b, nil
}

var prefixSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._ -]*$`)

// cleanPrefix validates the optional subdirectory the files are placed in.
func cleanPrefix(p string) (string, *Error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if len(p) > maxPrefixBytes || !utf8.ValidString(p) {
		return "", gerr(CodeBadURL, "The folder is too long.")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") || !prefixSegment.MatchString(seg) || strings.EqualFold(seg, ".git") {
			return "", gerr(CodeBadURL, "The folder may hold letters, digits, spaces, dots, dashes and underscores, separated by slashes, and may not start with a dot.")
		}
	}
	return p, nil
}

// cleanName validates a remote's name.
func cleanName(n string) (string, *Error) {
	n = strings.TrimSpace(n)
	if n == "" {
		return "", gerr(CodeBadURL, "Give the remote a name, like Backup.")
	}
	if utf8.RuneCountInString(n) > maxNameRunes || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "", gerr(CodeBadURL, "The name is too long or has control characters.")
	}
	return n, nil
}

// cleanAuthor validates the commit identity. Both or neither.
func cleanAuthor(name, email string) (string, string, *Error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" && email == "" {
		return "", "", nil
	}
	if name == "" || email == "" {
		return "", "", gerr(CodeBadURL, "Set both the author name and the author email, or neither.")
	}
	if utf8.RuneCountInString(name) > 100 || len(email) > 200 || strings.ContainsAny(name+email, "<>\n\r\x00") ||
		strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") || strings.ContainsAny(email, " ") {
		return "", "", gerr(CodeBadURL, "That author name or email is not valid.")
	}
	return name, email, nil
}
