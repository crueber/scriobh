package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Exit codes. They are part of the CLI's contract: scripts and agents branch
// on them, and the skill documents them. A code is never renumbered.
const (
	ExitOK          = 0  // success (a dry run that would change something is 0 unless --fail-on-change)
	ExitGeneric     = 1  // anything else: a server error, an unreadable response
	ExitUsage       = 2  // the command was mistyped, or a name was ambiguous
	ExitNotFound    = 3  // not_found, or a name that matches nothing
	ExitConflict    = 4  // conflict: a stale revision, or a repeat that cannot be replayed
	ExitAuth        = 5  // unauthenticated or forbidden, or no credentials
	ExitValidation  = 6  // invalid, too_large, dry_run_unsupported, idempotency_key_reuse
	ExitRateLimited = 7  // rate_limited: wait for retry_after
	ExitUnreachable = 8  // the server could not be reached or answered with something that is not Scríobh
	ExitWouldChange = 9  // --dry-run --fail-on-change, and the command would change something
	ExitDrift       = 10 // skill check found an installed skill or snapshot out of date
)

// ExitTable documents the codes, for the skill and the help.
var ExitTable = []struct {
	Code    int
	Name    string
	Meaning string
	Action  string
}{
	{ExitOK, "ok", "Success. A dry run is also 0, with dry_run true in the envelope.", "Continue."},
	{ExitGeneric, "error", "A server error or an unreadable response.", "Retry once later; if it persists, stop and report it."},
	{ExitUsage, "usage", "The command was mistyped, a required flag is missing, or a name matched more than one thing (error code ambiguous).", "Fix the command; for an ambiguous name, use an id from the candidates."},
	{ExitNotFound, "not_found", "Nothing by that id or name, or you have no access to it.", "Re-read the tree; the name may have changed."},
	{ExitConflict, "conflict", "The document changed since you read it (stale base_revision), or a retried request cannot be replayed.", "Re-read, merge, and retry with the current revision."},
	{ExitAuth, "auth", "The key is missing, expired, revoked or not allowed to do this.", "Stop. Do not retry; ask the person who issued the key."},
	{ExitValidation, "validation", "The server refused the input (invalid, too_large, dry_run_unsupported, idempotency_key_reuse).", "Fix the input; do not retry unchanged."},
	{ExitRateLimited, "rate_limited", "Too many requests.", "Wait for retry_after seconds; do not loop."},
	{ExitUnreachable, "unreachable", "The server could not be reached.", "Check the URL and the network; retry later."},
	{ExitWouldChange, "would_change", "With --dry-run --fail-on-change: the command would change something.", "Review the preview, then run it without --dry-run."},
	{ExitDrift, "drift", "skill check found an installed skill or the CLI snapshot out of date.", "Run: scriobh-cli skill install"},
}

// Fail is every error the CLI reports. It prints as JSON on stderr and sets
// the exit code.
type Fail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"status,omitempty"`
	Field   string         `json:"field,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// RetryAfter is seconds, for rate_limited.
	RetryAfter int `json:"retry_after,omitempty"`
	// Candidates lists what an ambiguous or missing name might have meant.
	Candidates []Candidate `json:"candidates,omitempty"`
	Exit       int         `json:"-"`
	Cause      error       `json:"-"`
}

// Candidate is one possible meaning of a name.
type Candidate struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path,omitempty"`
}

func (f *Fail) Error() string { return f.Code + ": " + f.Message }

func (f *Fail) Unwrap() error { return f.Cause }

// ExitFor maps a server error code, or failing that an HTTP status, to an exit
// code. A code this CLI has never heard of (an op from a newer server) still
// lands in the right bucket by its status.
func ExitFor(code string, status int) int {
	switch code {
	case "unauthenticated", "forbidden":
		return ExitAuth
	case "not_found":
		return ExitNotFound
	case "conflict":
		return ExitConflict
	case "invalid", "too_large", "dry_run_unsupported", "idempotency_key_reuse", "method_not_allowed":
		return ExitValidation
	case "rate_limited":
		return ExitRateLimited
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ExitAuth
	case status == http.StatusNotFound:
		return ExitNotFound
	case status == http.StatusConflict:
		return ExitConflict
	case status == http.StatusTooManyRequests:
		return ExitRateLimited
	case status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity || status == http.StatusBadRequest || status == http.StatusMethodNotAllowed:
		return ExitValidation
	}
	return ExitGeneric
}

// fromServer builds a Fail from an error response.
func fromServer(status int, header http.Header, body []byte) *Fail {
	var env struct {
		Error struct {
			Code       string         `json:"code"`
			Message    string         `json:"message"`
			Field      string         `json:"field"`
			Data       map[string]any `json:"data"`
			RetryAfter int            `json:"retry_after"`
		} `json:"error"`
	}
	f := &Fail{Status: status}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		f.Code, f.Message, f.Field, f.Data, f.RetryAfter = env.Error.Code, env.Error.Message, env.Error.Field, env.Error.Data, env.Error.RetryAfter
	} else {
		f.Code = "http_" + fmt.Sprint(status)
		f.Message = fmt.Sprintf("The server answered %d %s, and not in Scríobh's error format.", status, http.StatusText(status))
		if status < 500 && status != 401 && status != 403 && status != 404 {
			f.Hint = "Is --server the address of a Scríobh server (not a login page or a proxy)?"
		}
	}
	if f.RetryAfter == 0 && status == http.StatusTooManyRequests {
		var n int
		if _, err := fmt.Sscanf(header.Get("Retry-After"), "%d", &n); err == nil {
			f.RetryAfter = n
		}
	}
	f.Exit = ExitFor(f.Code, status)
	return f
}

func usageFail(e *UsageError) *Fail {
	return &Fail{Code: "usage", Message: e.Msg, Hint: e.Hint, Exit: ExitUsage}
}
