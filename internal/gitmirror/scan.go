package gitmirror

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The pre-push check. Before anything leaves the server, every file that would
// be pushed, every document title (titles end up in commit messages) and every
// path is checked for something that looks like a credential. A match refuses
// the push and names the file and the kind, never the text.
//
// It is a safety net, not a verdict: manuscripts quote keys. A documented
// example key or an obvious placeholder is let through, a key header with no key
// after it is prose, and a real-looking match the owner has looked at can be
// acknowledged for that exact file content (see git.remote.secret_ack); a
// changed file is checked again.

// secretRule is one thing that must not be pushed.
type secretRule struct {
	name string
	re   *regexp.Regexp
}

var secretRules = []secretRule{
	// A header with a key body after it: base64 of at least 60 characters within
	// a few hundred characters, whatever indents or quotes the lines carry. A
	// header quoted on its own, in prose, is not a key.
	{"a private key", regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----.{0,400}?[A-Za-z0-9+/]{60,}`)},
	{"an API key (sk-…)", regexp.MustCompile(`\bsk-[A-Za-z0-9][A-Za-z0-9_-]{19,}`)},
	{"an API key (sk_…)", regexp.MustCompile(`\bsk_(?:live_|test_)?[A-Za-z0-9]{20,}`)},
	{"a bearer token", regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]{24,}`)},
	{"a GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`)},
	{"a Forgejo or Gitea token", regexp.MustCompile(`(?i)\b(?:token|authorization)[ \t]*[:=][ \t]*["']?[0-9a-f]{40}\b`)},
	{"an AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{20,}`)},
	{"a password in a URL", regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]{3,}@[A-Za-z0-9.-]+`)},
}

var (
	// A run of one filler character is a placeholder, not a secret.
	fillerRun = regexp.MustCompile(`(?i)(x{6,}|\*{4,}|\.{4,}|0{8,}|_{4,})`)
	// Words that make a match an example.
	exampleWord = regexp.MustCompile(`(?i)(example|sample|placeholder|dummy|your[-_ ]?(?:key|token|secret|password)|changeme|redacted|fake|not[-_]?a[-_]?real)`)
)

// isPlaceholder reports whether a match is a documented example or an obvious
// stand-in (AKIAIOSFODNN7EXAMPLE, sk-proj-xxxxxxxx, a URL with password
// "password"), which a book or a README quotes all the time.
func isPlaceholder(rule, m string) bool {
	if rule == "a password in a URL" {
		// Only the password says whether it is an example: not the host, which is
		// often example.org.
		i, j := strings.LastIndex(m, ":"), strings.LastIndex(m, "@")
		if i < 0 || j < i {
			return false
		}
		m = m[i+1 : j]
		switch strings.ToLower(m) {
		case "password", "pass", "passwd", "secret", "pwd", "hunter2", "user", "token":
			return true
		}
	}
	return exampleWord.MatchString(m) || fillerRun.MatchString(m)
}

// SecretFinding names something that looks like a secret.
type SecretFinding struct {
	Path string // the file, or "path (title)" for a document title
	Rule string
	SHA  string // sha256 of the content that matched, hex: what an acknowledgement covers
}

func (f SecretFinding) String() string { return f.Path + " looks like it holds " + f.Rule }

// SecretFindingView is a finding as an op returns it.
type SecretFindingView struct {
	Path string `json:"path" desc:"The file (or the document, for a title)."`
	Rule string `json:"rule" desc:"What it looks like."`
	SHA  string `json:"sha256" desc:"SHA-256 of the content that matched. git.remote.secret_ack covers exactly this file and content."`
}

func matchSecret(b []byte) (rule string, found bool) {
	for _, r := range secretRules {
		for _, loc := range r.re.FindAllIndex(b, -1) {
			if !isPlaceholder(r.name, string(b[loc[0]:loc[1]])) {
				return r.name, true
			}
		}
	}
	return "", false
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// scanForSecrets checks every file that would be pushed. It returns findings
// sorted by path, and never includes matched text. The published manifest is
// not content: it repeats every title and path, which are checked as what they
// are (scanMeta), and its hash changes on every save, so a finding in it could
// never be acknowledged.
func scanForSecrets(files map[string][]byte) []SecretFinding {
	var out []SecretFinding
	for path, b := range files {
		if path == PublishedManifest || strings.HasSuffix(path, "/"+PublishedManifest) {
			continue
		}
		if rule, ok := matchSecret(b); ok {
			out = append(out, SecretFinding{Path: path, Rule: rule, SHA: sha(b)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// scanMeta checks what is not file content but still ends up in the
// repository: document titles (they are in commit messages) and the paths.
func scanMeta(sn *snapshot) []SecretFinding {
	var out []SecretFinding
	for _, d := range sn.docs {
		if rule, ok := matchSecret([]byte(d.Title)); ok {
			out = append(out, SecretFinding{Path: d.Path + " (the title)", Rule: rule, SHA: sha([]byte(d.Title))})
		}
	}
	for _, t := range sn.project {
		if rule, ok := matchSecret([]byte(t)); ok {
			out = append(out, SecretFinding{Path: "the project's details (" + shortLabel(t) + ")", Rule: rule, SHA: sha([]byte(t))})
		}
	}
	for p := range sn.files {
		if rule, ok := matchSecret([]byte(p)); ok {
			out = append(out, SecretFinding{Path: p + " (the file name)", Rule: rule, SHA: sha([]byte(p))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// withoutAcknowledged drops the findings the owner has acknowledged: the same
// path with the same content hash.
func withoutAcknowledged(fs []SecretFinding, acks map[string]bool) []SecretFinding {
	var out []SecretFinding
	for _, f := range fs {
		if !acks[f.Path+"\x00"+f.SHA] {
			out = append(out, f)
		}
	}
	return out
}

// findingsSignature identifies a set of findings, so that an automatic push
// that finds the same ones again does not write another refusal.
func findingsSignature(fs []SecretFinding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.Path+"|"+f.SHA)
	}
	return strings.Join(parts, "\n")
}

// secretError is the refusal for a tree that looks like it holds a secret. It
// names the first few files so the writer knows where to look.
func secretError(fs []SecretFinding) *Error {
	var parts []string
	for i, f := range fs {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(fs)-3))
			break
		}
		parts = append(parts, f.String())
	}
	return gerr(CodeSecretInTree, "Nothing was pushed: %s. Change the text and the push goes ahead on the next save. If it is not a secret (a quoted example, a key in a story), the project's owner can acknowledge that exact file.", strings.Join(parts, "; "))
}

// shortLabel names a field in a finding without repeating the secret it holds.
func shortLabel(t string) string {
	r := []rune(t)
	if len(r) > 8 {
		return string(r[:8]) + "…"
	}
	return "…"
}
