package ops

import "context"

// The project passphrase gate (F-252).
//
// A project's owner may protect it with a passphrase. While it is set, a person must unlock the project in
// their session before anything of it is served: not the manuscript, not a title of a document, a search hit,
// a comment, a revision or a count. It is a lock for the screen (a shared laptop, a family member with the
// session), not encryption.
//
// The check lives where every project-scoped read and write already goes, so a route or op that forgets it is
// not possible:
//
//   - authorizeInput, for every op that names a project (or a document, comment, tag, attachment or anything
//     else that belongs to one): after the role check, before the trash check;
//   - Env.ProjectRole, the function every op that checks a role by hand (the Compendium ops that read a
//     project's text, AI, review, Git) goes through. It is gate-aware: it answers "no access" to a person whose
//     session has not unlocked the project. The caller is found in the context (see WithPrincipal), and a
//     context with no caller is background work, which the gate is not for;
//   - the page layer (internal/server), which resolves a request's project from its address and asks the same
//     gate before any handler runs.
//
// Env.ProjectMember is the membership truth with no gate in it. Only the gate itself and the ops that must
// answer a locked caller (status, unlock, lock, set, clear) use it.

// ProjectGateFunc says whether the caller may reach the content of a project: nil when the project has no
// passphrase or the caller has unlocked it, otherwise a refusal (ProjectLocked or ProjectKeysRefused).
type ProjectGateFunc func(ctx context.Context, p Principal, projectID string) error

// Reasons carried in data.reason of a gate refusal (403).
const (
	ReasonProjectLocked      = "project_locked"
	ReasonProjectKeysRefused = "project_keys_refused"
)

// ProjectLocked is the one answer to a person whose session has not unlocked a gated project (403, data.reason
// "project_locked"). data carries the project's id, title and address: the title and the fact that a passphrase is
// needed may be shown, nothing else.
func ProjectLocked(data map[string]any) *Error {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = ReasonProjectLocked
	return &Error{Code: CodeForbidden, Message: "This project is protected with a passphrase. Unlock it to open it.", Data: data}
}

// ProjectKeysRefused is the answer to an API key that reaches a gated project whose owner does not accept keys.
func ProjectKeysRefused(data map[string]any) *Error {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = ReasonProjectKeysRefused
	return &Error{Code: CodeForbidden, Message: "This project is protected with a passphrase and its owner does not allow API keys while it is set.", Data: data}
}

// IsLocked reports whether err is a gate refusal, and why.
func IsLocked(err error) (reason string, ok bool) {
	e := AsError(err)
	if e == nil || e.Code != CodeForbidden || e.Data == nil {
		return "", false
	}
	r, _ := e.Data["reason"].(string)
	if r == ReasonProjectLocked || r == ReasonProjectKeysRefused {
		return r, true
	}
	return "", false
}

// projectGateOK names the project ops that answer a caller whose session has not unlocked the project. They are
// the way in (status, unlock) and the owner's own settings (set, clear), which check the passphrase themselves.
// Every other op that names a project is gated by default, including project.get, so every page that loads a
// project through it is closed too.
var projectGateOK = map[string]bool{
	"project.passphrase.get": true, "project.passphrase.set": true, "project.passphrase.clear": true,
	"project.unlock": true, "project.lock": true,
}

// GateExempt reports whether an op answers a locked caller (see projectGateOK).
func GateExempt(op string) bool { return projectGateOK[op] }

type ctxKey int

const (
	keyPrincipal ctxKey = iota
	keyClient
	keyPassive
	keyPrecheck
)

// WithPrincipal records who the caller is in a context, so a function that gets only a context (the role
// function, a worker started by a request) can tell a person from background work. Registry.Call and the HTTP
// layer set it; nothing else should.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFrom returns the caller recorded in ctx. ok is false for background work.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(keyPrincipal).(Principal)
	return p, ok
}

// WithClient records a non-reversible bucket for the caller's network address (never the address itself), for
// the lockout that counts wrong passphrases per address.
func WithClient(ctx context.Context, bucket string) context.Context {
	return context.WithValue(ctx, keyClient, bucket)
}

// ClientFrom returns the address bucket recorded in ctx, or "".
func ClientFrom(ctx context.Context) string {
	s, _ := ctx.Value(keyClient).(string)
	return s
}

// Passive marks a context whose use of a project is not activity: a long-lived stream asking every half minute
// whether the caller may still read must not keep an unlock alive past its idle limit.
func Passive(ctx context.Context) context.Context { return context.WithValue(ctx, keyPassive, true) }

// IsPassive reports whether ctx is passive.
func IsPassive(ctx context.Context) bool { v, _ := ctx.Value(keyPassive).(bool); return v }

// Precheck marks the early authorization a binder makes before it reads a request body, which the call itself repeats:
// the gate counts a key's refusal there (the call never comes) but not a key's use (the call counts it).
func Precheck(ctx context.Context) context.Context { return context.WithValue(ctx, keyPrecheck, true) }

// IsPrecheck reports whether ctx is an early authorization (see Precheck).
func IsPrecheck(ctx context.Context) bool { v, _ := ctx.Value(keyPrecheck).(bool); return v }
