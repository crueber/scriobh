package gitmirror

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/crueber/scriobh/internal/secrets"
)

// Error codes. They are stable: the screens switch on them, and they are stored
// in git_remotes.last_error_code and git_pushes.error_code.
const (
	CodeBadURL              = "bad_url"
	CodeNoKey               = "no_secret_key"
	CodeCredentialsMissing  = "credentials_missing"
	CodeCredentialsBad      = "credentials_unreadable"
	CodeHostKeyUnpinned     = "host_key_unpinned"
	CodeHostKeyChanged      = "host_key_changed"
	CodeAuthFailed          = "auth_failed"
	CodeRepoNotFound        = "repo_not_found"
	CodeUnreachable         = "unreachable"
	CodeAddressBlocked      = "address_blocked"
	CodeTimeout             = "timeout"
	CodeRemoteAhead         = "remote_ahead"
	CodeRejectedByRemote    = "rejected_by_remote"
	CodeSecretInTree        = "secret_in_tree"
	CodeNoMirror            = "no_mirror"
	CodeMirrorOff           = "mirror_off"
	CodeBundleFailed        = "bundle_failed"
	CodeTooLarge            = "too_large"
	CodeInterrupted         = "interrupted"
	CodeRemoteChanged       = "remote_changed"
	CodeInternal            = "internal"
	CodeNothingToPush       = "up_to_date"
	CodePausedByRestore     = "paused_by_restore"
	codeScanDone            = "scan_done"
	maxServerMessageRunes   = 300
	maxSingleErrorMessageSz = 600
)

// Error is a failure of the Git side, typed so that a screen can say what to do
// about it. Message is written for a person and never contains a credential.
type Error struct {
	Code    string
	Message string
	// Host and KeyName say where and with what: the host the remote points at
	// and the name of the remote whose key or token was used.
	Host    string
	KeyName string
	// Retry reports whether trying again later can help without anyone acting.
	Retry bool
	// RetryAfter is the wait a host asked for, in seconds (a rate limit).
	RetryAfter int
	cause      error
}

func (e *Error) Error() string { return e.Message }

// Unwrap exposes the cause for errors.Is; the cause is never printed.
func (e *Error) Unwrap() error { return e.cause }

func gerr(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// HostKeyError is returned by the host key check during the SSH handshake.
type HostKeyError struct {
	Changed     bool   // a key was pinned and this is a different one
	Host        string // host:port
	Presented   string // SHA256 fingerprint the server showed
	Pinned      string // SHA256 fingerprint that was pinned; empty if none
	PresentedBy string // the key type
}

func (e *HostKeyError) Error() string {
	if e.Changed {
		return "the host key of " + e.Host + " changed"
	}
	return "the host key of " + e.Host + " has not been accepted"
}

// classify turns whatever a transport call returned into a typed Error. rem
// supplies the host and the key name; secret values are scrubbed from any
// server text that is kept.
func classify(err error, host, keyName string, scrub ...string) *Error {
	if err == nil {
		return nil
	}
	var ge *Error
	if errors.As(err, &ge) {
		return ge
	}
	mk := func(code, format string, a ...any) *Error {
		return &Error{Code: code, Message: fmt.Sprintf(format, a...), Host: host, KeyName: keyName, cause: err}
	}
	var hk *HostKeyError
	switch {
	case errors.As(err, &hk):
		if hk.Changed {
			return mk(CodeHostKeyChanged, "The host key of %s has changed since you accepted it (it was %s, it is now %s). This can mean the server was reinstalled, or that something is intercepting the connection. Nothing was sent. If you are sure the change is expected, check the new fingerprint with the host's owner and accept it again.", hk.Host, hk.Pinned, hk.Presented)
		}
		return mk(CodeHostKeyUnpinned, "The host key of %s has not been accepted yet. Scan it, compare the fingerprint with the one the host publishes, and accept it.", hk.Host)
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeTimeout, Message: fmt.Sprintf("%s did not answer in time.", host), Host: host, KeyName: keyName, Retry: true, cause: err}
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeInterrupted, Message: "The operation was cancelled.", Host: host, KeyName: keyName, cause: err}
	case errors.Is(err, secrets.ErrNoKey):
		return mk(CodeNoKey, "%s", secrets.ErrNoKey.Error())
	case errors.Is(err, secrets.ErrCannotOpen):
		return mk(CodeCredentialsBad, "The credential for %q cannot be read with this installation's secret key. Enter it again (generate a new deploy key, or paste the token again).", keyName)
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return mk(CodeAuthFailed, "%s refused the credentials of %q. Check that the deploy key is added to the repository with write access, or that the token is valid and may push.", host, keyName)
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return mk(CodeRepoNotFound, "%s says the repository does not exist, or that %q has no access to it. Check the address and that the key can write to it.", host, keyName)
	case errors.Is(err, git.ErrNonFastForwardUpdate):
		return mk(CodeRemoteAhead, "The remote has commits this project did not make, so the push was refused.")
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.Is(err, errBlockedAddr):
		logTransportDetail(CodeAddressBlocked, err, scrub...)
		return &Error{Code: CodeAddressBlocked, Message: fmt.Sprintf("%s could not be reached: its address is not one this server may connect to. If it is a machine on your own network, an administrator can allow that with SCRIOBH_GIT_ALLOW_PRIVATE=1.", host), Host: host, KeyName: keyName, cause: err}
	case strings.Contains(low, "unable to authenticate"), strings.Contains(low, "no supported methods remain"), strings.Contains(low, "permission denied (publickey"):
		return mk(CodeAuthFailed, "%s refused the deploy key of %q. Check that its public key is added to the repository with write access.", host, keyName)
	case errors.Is(err, errBodyTooLarge):
		logTransportDetail(CodeTooLarge, err, scrub...)
		return &Error{Code: CodeTooLarge, Message: fmt.Sprintf("%s sent more data than a repository may hold here (SCRIOBH_GIT_MAX_REPO_MB).", host), Host: host, KeyName: keyName, cause: err}
	case errors.As(err, &dns), errors.As(err, &op), strings.Contains(low, "connection refused"), strings.Contains(low, "connection reset"), strings.Contains(low, "no route to host"),
		strings.Contains(low, "network is unreachable"), strings.Contains(low, "i/o timeout"), strings.Contains(low, "eof"), strings.Contains(low, "no address for"):
		// Generic on purpose: what failed, where, and on which port is a map of the
		// server's network for whoever configured the address.
		logTransportDetail(CodeUnreachable, err, scrub...)
		return &Error{Code: CodeUnreachable, Message: fmt.Sprintf("%s could not be reached. The commit is kept locally and will be tried again.", host), Host: host, KeyName: keyName, Retry: true, cause: err}
	case strings.Contains(low, "declined"), strings.Contains(low, "protected branch"), strings.Contains(low, "pre-receive"), strings.Contains(low, "remote rejected"), strings.Contains(low, "hook"):
		return mk(CodeRejectedByRemote, "%s refused the push: %s", host, shorten(secrets.Redact(msg, scrub...)))
	}
	logTransportDetail(CodeInternal, err, scrub...)
	return mk(CodeInternal, "The Git operation with %s failed. Details are in the server log.", host)
}

// shorten makes text from the network safe to store and show: one line, no
// control characters, bounded.
func shorten(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
		if n >= maxServerMessageRunes {
			b.WriteString("…")
			break
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
