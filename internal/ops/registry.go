// Package ops is THE op registry. Every capability is one registration; the
// REST routes, the op schema, the CLI snapshot, the skill and the
// authorization sweep are all generated from it. UI handlers mutate through
// ops, never by writing SQL.
package ops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/crueber/scriobh/internal/db"
)

// Op is an op's static description.
type Op struct {
	// Name is noun.verb, e.g. "project.create".
	Name   string
	Method string // REST method
	Path   string // REST path, e.g. /api/v1/projects/{id}
	// UI is the optional HTML route that drives this op, e.g.
	// "POST /ui/projects". The server must register it; a test checks.
	UI   string
	Desc string
	Auth Auth
	// Scope is the API-key scope the op needs: read, write or admin. It
	// defaults from the method and Auth (GET is read, admin-only ops are admin,
	// everything else write). Session principals are unrestricted.
	Scope string
	// Status is the HTTP status on success (default 200; 201 for creates).
	Status int
	// MaxBody overrides the server's default request body limit, in bytes, for
	// ops that take uploads (imports). Zero keeps the default. The op is still
	// authorized before its body is read.
	MaxBody int64
	// RawBody marks an op whose request body is the bytes of a file, not JSON.
	// Its input struct holds only path and query fields; the body arrives as a
	// stream through Ctx.RawBody, is never buffered whole by the REST binding,
	// and is never kept for idempotent replay (content addressing makes a
	// repeat harmless). It needs MaxBody, since the default limit is for JSON.
	RawBody bool
	// NoDryRun marks an op that cannot be previewed safely because it has
	// effects outside the database (files, in-memory state, a remote) or
	// returns a secret. A dry run of it is refused with dry_run_unsupported
	// rather than run for real. Every op that does anything outside the
	// database must set it; the dry-run sweep test catches the ones it can
	// drive.
	NoDryRun bool
	// Ephemeral marks an op that never takes part in idempotency: an Idempotency-Key on it is ignored, nothing is stored and
	// nothing is replayed. For ops whose answer is about the moment and about the caller's current standing and must not be
	// kept in the database or answered again after the caller's role changes (presence, F-244).
	Ephemeral bool
	// SecretOutput marks an op whose response carries a secret (a new API
	// key). Its response is never stored for idempotent replay.
	SecretOutput bool
	// SecretInput marks an op whose request holds a secret the caller typed (a passphrase). The request is never
	// fingerprinted or kept for idempotent replay (a stored hash of the body would be a hash of the passphrase),
	// so an Idempotency-Key on it is ignored, and it is never previewed (see NoDryRun). One that also has SecretOutput
	// (share.create) keeps the conflict on a repeated key, with a fingerprint that leaves the body out.
	SecretInput bool
	// Permanent marks an op that destroys data beyond the trash: what it removes
	// (or merges away) cannot be brought back with another op, though a backup
	// or the history may still hold it. Every op that deletes, removes, prunes,
	// merges or clears must either set it or be listed with a reason why it is
	// reversible (the audit test in internal/registry). A client that wants to
	// keep an agent from losing work (the MCP endpoint) refuses these by default.
	Permanent bool
}

// Event is a durable event row. Payloads carry ids and counts, never prose.
type Event struct {
	Type       string
	ProjectID  string
	DocumentID string
	// CompendiumID names the Compendium the event is about. The mirror follows
	// it to every project whose projection changes.
	CompendiumID string
	Payload      map[string]any
}

// AuditEntry is one audit-log line.
type AuditEntry struct {
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
}

// Sink persists events and audit entries after an op succeeds.
type Sink interface {
	Flush(ctx context.Context, actorID string, events []Event, audit []AuditEntry) error
}

// TxSink can write events and audit rows inside the caller's transaction, so
// they commit or roll back together with the content they describe. The mirror,
// Git push and realtime all depend on events never being lost.
type TxSink interface {
	FlushTx(tx *sql.Tx, actorID string, events []Event, audit []AuditEntry) error
}

// Env is what ops need from the process. It is deliberately small.
type Env struct {
	DB   *db.DB
	Sink Sink
	// ProjectRole resolves a person's role on a project and is gate-aware (see gate.go): a person whose session has
	// not unlocked a passphrase-protected project gets "no access". Every op that checks a role by hand goes
	// through it. ProjectMember is the same without the gate: the membership truth, for the gate itself and the
	// few ops that answer a locked caller.
	ProjectRole   ProjectRoleFunc
	ProjectMember ProjectRoleFunc
	// ProjectGate is the passphrase gate (F-252). The registry asks it in authorizeInput for every op that names
	// a project, except the ones in projectGateOK. Nil means no project has a gate (a schema-only build).
	ProjectGate ProjectGateFunc
	// CompendiumRole resolves Compendium membership for AuthCompendium ops.
	CompendiumRole CompendiumRoleFunc
	// CompendiumTrashed reports whether a Compendium is in the trash. Writes to one are refused (see Auth.RefuseTrashed);
	// reads, restore and purge are not.
	CompendiumTrashed func(ctx context.Context, compendiumID string) bool
	// ProjectTrashed reports whether a project is in the trash. A project there is read-only until it is restored
	// (see Auth.RefuseTrashed): its owner and an instance admin still reach it, to read it and to get it out.
	ProjectTrashed func(ctx context.Context, projectID string) bool
	// ProjectPrimary names a project's primary Compendium ("" when it has
	// none). A key pinned to a project may reach that Compendium, and only it.
	ProjectPrimary func(ctx context.Context, projectID string) string
	Log            *slog.Logger
	// DataDir is the data directory, for the few ops that write files
	// (conflict copies, exports of record).
	DataDir string

	// purgeHooks run after a project has been permanently deleted, with its
	// id, to remove whatever the project left outside the database (conflict
	// copies, the Markdown mirror). They are best effort: a failure is logged
	// by the hook and never reaches the caller. Attach them with AddPurgeHook.
	purgeHooks []func(ctx context.Context, projectID string)
	// compendiumPurgeHooks are the same for a Compendium (the mirror folder of
	// a standalone Compendium).
	compendiumPurgeHooks []func(ctx context.Context, compendiumID string)

	svcMu    sync.RWMutex
	services map[string]any
	// previewServices are the few services that are also present in a dry run's environment: the ones a read-only filter
	// needs to answer the same as the real call (the passphrase gate's list of locked projects). Everything else is absent
	// in a preview, which is how it has no side effects.
	previewServices map[string]any
}

// SetService registers a runtime service (the mirror, the backup service) that
// ops registered in other packages reach through Service. Absent in schema-only
// builds, so handlers must cope with a missing service.
func (e *Env) SetService(name string, v any) {
	e.svcMu.Lock()
	defer e.svcMu.Unlock()
	if e.services == nil {
		e.services = map[string]any{}
	}
	e.services[name] = v
}

// SetPreviewService registers a service that a dry run's environment has too. Only a service that reads and decides, and
// has no effect outside the database, belongs here.
func (e *Env) SetPreviewService(name string, v any) {
	e.SetService(name, v)
	e.svcMu.Lock()
	defer e.svcMu.Unlock()
	if e.previewServices == nil {
		e.previewServices = map[string]any{}
	}
	e.previewServices[name] = v
}

// Service returns a registered service as T.
func Service[T any](e *Env, name string) (T, bool) {
	var zero T
	if e == nil {
		return zero, false
	}
	e.svcMu.RLock()
	defer e.svcMu.RUnlock()
	v, ok := e.services[name].(T)
	if !ok {
		return zero, false
	}
	return v, true
}

// AddPurgeHook appends a hook run after a project is purged.
func (e *Env) AddPurgeHook(h func(ctx context.Context, projectID string)) {
	e.svcMu.Lock()
	defer e.svcMu.Unlock()
	e.purgeHooks = append(e.purgeHooks, h)
}

// ProjectPurged runs the purge hooks.
func (e *Env) ProjectPurged(ctx context.Context, projectID string) {
	e.svcMu.RLock()
	hooks := append([]func(context.Context, string){}, e.purgeHooks...)
	e.svcMu.RUnlock()
	for _, h := range hooks {
		h(ctx, projectID)
	}
}

// AddCompendiumPurgeHook appends a hook run after a Compendium is purged.
func (e *Env) AddCompendiumPurgeHook(h func(ctx context.Context, compendiumID string)) {
	e.svcMu.Lock()
	defer e.svcMu.Unlock()
	e.compendiumPurgeHooks = append(e.compendiumPurgeHooks, h)
}

// CompendiumPurged runs the Compendium purge hooks.
func (e *Env) CompendiumPurged(ctx context.Context, compendiumID string) {
	e.svcMu.RLock()
	hooks := append([]func(context.Context, string){}, e.compendiumPurgeHooks...)
	e.svcMu.RUnlock()
	for _, h := range hooks {
		h(ctx, compendiumID)
	}
}

// Ctx is passed to op handlers.
type Ctx struct {
	context.Context
	P   Principal
	Env *Env

	events  []Event
	audit   []AuditEntry
	flushed bool
	dry     bool
}

// DryRun reports whether the op is running as a preview: its database writes
// are rolled back, no event or audit row is written, the data directory is a
// scratch directory and runtime services (the mirror, backups) are absent.
// Most ops need not look; one that must skip a side effect it cannot undo
// checks this, or declares NoDryRun.
func (c *Ctx) DryRun() bool { return c.dry }

// DB returns the database.
func (c *Ctx) DB() *db.DB { return c.Env.DB }

// WithTx runs fn in a writer transaction and flushes any events and audit rows
// queued inside fn into the same transaction before commit. If fn or commit
// fails, queued events and audit rows are restored to their pre-WithTx state so
// a failed or retried transaction never leaks or drops side effects.
func (c *Ctx) WithTx(fn func(tx *sql.Tx) error) error {
	evSnap := append([]Event(nil), c.events...)
	auSnap := append([]AuditEntry(nil), c.audit...)
	flSnap := c.flushed
	err := c.Env.DB.WithTx(c, func(tx *sql.Tx) error {
		c.events = append([]Event(nil), evSnap...)
		c.audit = append([]AuditEntry(nil), auSnap...)
		c.flushed = flSnap
		if err := fn(tx); err != nil {
			return err
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		c.events, c.audit, c.flushed = evSnap, auSnap, flSnap
	}
	return err
}

// FlushTo writes every queued event and audit entry into tx, atomically with
// the caller's writes, and clears the queue so Call does not write them again.
// Ops that change content should call it as the last statement of their
// transaction; ops that do not need atomicity can simply Emit.
func (c *Ctx) FlushTo(tx *sql.Tx) error {
	if len(c.events) == 0 && len(c.audit) == 0 {
		return nil
	}
	ts, ok := c.Env.Sink.(TxSink)
	if !ok {
		return nil // left queued: Call flushes them after commit
	}
	if err := ts.FlushTx(tx, c.P.UserID, c.events, c.audit); err != nil {
		return err
	}
	c.events, c.audit, c.flushed = nil, nil, true
	return nil
}

// Emit queues a durable event, written after the op succeeds.
func (c *Ctx) Emit(e Event) { c.events = append(c.events, e) }

// Audit queues an audit entry, written after the op succeeds.
func (c *Ctx) Audit(a AuditEntry) { c.audit = append(c.audit, a) }

// Entry is a registered op with its types.
type Entry struct {
	Op   Op
	In   reflect.Type
	Out  reflect.Type
	call func(*Ctx, any) (any, error)
}

// NewIn returns a pointer to a zero input value.
func (e *Entry) NewIn() any { return reflect.New(e.In).Interface() }

// Registry holds every op.
type Registry struct {
	mu    sync.RWMutex
	env   *Env
	byKey map[string]*Entry
}

// NewRegistry returns an empty registry. env may be nil when only the schema
// is needed (skill generation).
func NewRegistry(env *Env) *Registry {
	return &Registry{env: env, byKey: map[string]*Entry{}}
}

// Env returns the registry's environment.
func (r *Registry) Env() *Env { return r.env }

// Register adds an op. It panics on a malformed registration: that is a
// programming error caught the first time the process starts.
func Register[In, Out any](r *Registry, op Op, fn func(*Ctx, In) (Out, error)) {
	if !validName(op.Name) {
		panic(fmt.Sprintf("ops: %q is not noun.verb", op.Name))
	}
	if op.Method == "" || !strings.HasPrefix(op.Path, "/api/v1/") {
		panic(fmt.Sprintf("ops: %s needs a Method and a /api/v1/ Path", op.Name))
	}
	if op.Auth.Kind == "" {
		panic(fmt.Sprintf("ops: %s must declare Auth", op.Name))
	}
	if op.Desc == "" {
		panic(fmt.Sprintf("ops: %s needs a Desc", op.Name))
	}
	if op.Status == 0 {
		op.Status = 200
	}
	if op.RawBody && (op.MaxBody <= 0 || (op.Method != "POST" && op.Method != "PUT")) {
		panic(fmt.Sprintf("ops: %s takes a raw body, so it needs MaxBody and a POST or PUT route", op.Name))
	}
	if op.Scope == "" {
		switch {
		case op.Auth.Kind == AuthAdmin:
			op.Scope = ScopeAdmin
		case op.Method == "GET" || op.Method == "HEAD":
			op.Scope = ScopeRead
		default:
			op.Scope = ScopeWrite
		}
	}
	if op.Auth.Kind == AuthProject && projectGateOK[op.Name] {
		op.Auth.GateExempt = true
	}
	if op.SecretInput {
		op.NoDryRun = true // a rolled-back wrong guess would be a free guess
	}
	if op.Auth.Kind == AuthCompendium && op.Method != "GET" && op.Method != "HEAD" && !trashedOK[op.Name] {
		op.Auth.RefuseTrashed = true
	}
	// A project in the trash is read-only too. A write is anything that is not a GET or HEAD and not declared a read
	// (the previews are POSTs with scope read); the few that must still work are named in projectTrashedOK.
	if op.Auth.Kind == AuthProject && op.Method != "GET" && op.Method != "HEAD" && op.Scope != ScopeRead && !projectTrashedOK[op.Name] {
		op.Auth.RefuseTrashed = true
	}
	inT := reflect.TypeOf((*In)(nil)).Elem()
	if op.Auth.Kind == AuthCompendium && op.Auth.Resolve == nil {
		if !reflect.PointerTo(inT).Implements(reflect.TypeOf((*CompendiumRef)(nil)).Elem()) && !inT.Implements(reflect.TypeOf((*CompendiumRef)(nil)).Elem()) {
			panic(fmt.Sprintf("ops: %s requires a compendium but its input does not implement CompendiumRef", op.Name))
		}
	}
	if op.Auth.Kind == AuthProject && op.Auth.Resolve == nil {
		if !reflect.PointerTo(inT).Implements(reflect.TypeOf((*ProjectRef)(nil)).Elem()) && !inT.Implements(reflect.TypeOf((*ProjectRef)(nil)).Elem()) {
			panic(fmt.Sprintf("ops: %s requires a project but its input does not implement ProjectRef", op.Name))
		}
	}
	e := &Entry{
		Op:  op,
		In:  inT,
		Out: reflect.TypeOf((*Out)(nil)).Elem(),
		call: func(c *Ctx, in any) (any, error) {
			var v In
			switch x := in.(type) {
			case *In:
				v = *x
			case In:
				v = x
			default:
				return nil, Invalid("", fmt.Sprintf("%s: wrong input type %T", op.Name, in))
			}
			if vv, ok := any(&v).(interface{ Validate() error }); ok {
				if err := vv.Validate(); err != nil {
					return nil, err
				}
			} else if vv, ok := any(v).(interface{ Validate() error }); ok {
				if err := vv.Validate(); err != nil {
					return nil, err
				}
			}
			return fn(c, v)
		},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKey[op.Name]; dup {
		panic("ops: duplicate op " + op.Name)
	}
	for _, o := range r.byKey {
		if o.Op.Method == op.Method && o.Op.Path == op.Path {
			panic(fmt.Sprintf("ops: %s and %s share %s %s", o.Op.Name, op.Name, op.Method, op.Path))
		}
	}
	r.byKey[op.Name] = e
}

func validName(n string) bool {
	parts := strings.Split(n, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.ToLower(p) != p || strings.ContainsAny(p, " /-") {
			return false
		}
	}
	return true
}

// Lookup returns an op by name.
func (r *Registry) Lookup(name string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byKey[name]
	return e, ok
}

// Entries returns every op sorted by name.
func (r *Registry) Entries() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.byKey))
	for _, e := range r.byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Op.Name < out[j].Op.Name })
	return out
}

// PreAuthorize runs the checks that do not need the input. Binders call it
// before reading a request body.
func (r *Registry) PreAuthorize(p Principal, e *Entry) error {
	if err := r.authorizeEarly(p, e.Op.Auth); err != nil {
		return err
	}
	if p.Scope != "" && scopeRank[p.Scope] < scopeRank[e.Op.Scope] {
		return Forbidden("this key cannot do that; it needs the " + e.Op.Scope + " scope")
	}
	return nil
}

// NeedsInputAuth reports whether authorization depends on the input.
func (e *Entry) NeedsInputAuth() bool { return e.Op.Auth.needsInput() }

// AuthorizeInput runs the input-dependent check (project membership).
func (r *Registry) AuthorizeInput(ctx context.Context, p Principal, e *Entry, in any) error {
	return r.authorizeInput(ctx, p, e.Op.Auth, derefIn(in))
}

// derefIn gives authorizeInput the value or pointer, whichever implements
// ProjectRef or CompendiumRef.
func derefIn(in any) any {
	if _, ok := in.(ProjectRef); ok {
		return in
	}
	if _, ok := in.(CompendiumRef); ok {
		return in
	}
	v := reflect.ValueOf(in)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		return v.Elem().Interface()
	}
	return in
}

// Call runs an op as principal p. in is the op's input value or a pointer to it.
// Authorization, validation, the handler and event/audit flushing all happen here.
func (r *Registry) Call(ctx context.Context, p Principal, name string, in any) (any, error) {
	e, ok := r.Lookup(name)
	if !ok {
		return nil, NotFound("op " + name)
	}
	if err := r.PreAuthorize(p, e); err != nil {
		return nil, err
	}
	if e.NeedsInputAuth() {
		if err := r.AuthorizeInput(ctx, p, e, in); err != nil {
			return nil, err
		}
	}
	c := &Ctx{Context: WithPrincipal(ctx, p), P: p, Env: r.env}
	out, err := e.call(c, in)
	if err != nil {
		ae := AsError(err)
		if ae.Code == CodeInternal && r.env != nil && r.env.Log != nil {
			r.env.Log.Error("op failed", "op", name, "principal", p.UserID, "err", ae.Cause())
		}
		return nil, ae
	}
	if c.flushed {
		if w, ok := r.env.Sink.(interface{ Wake() }); ok {
			w.Wake()
		}
	}
	if r.env != nil && r.env.Sink != nil && (len(c.events) > 0 || len(c.audit) > 0) {
		// The content transaction has committed. A failure here is logged by
		// the sink; it must never turn a successful write into an error.
		_ = r.env.Sink.Flush(ctx, p.UserID, c.events, c.audit)
	}
	return out, nil
}

// CallJSON decodes raw JSON into the op's input and calls it.
func (r *Registry) CallJSON(ctx context.Context, p Principal, name string, raw []byte) (any, error) {
	e, ok := r.Lookup(name)
	if !ok {
		return nil, NotFound("op " + name)
	}
	in := e.NewIn()
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(in); err != nil {
			return nil, Invalid("", "malformed JSON: "+err.Error())
		}
	}
	return r.Call(ctx, p, name, in)
}

// projectTrashedOK names the project ops that keep working on a project in the trash, the rule for a Compendium
// applied to a project: the ways out (restore, purge, trash, archive, unarchive), who has access, the exports of what
// is in it, the mirror's rebuild (a trashed project stays mirrored and says so), and the bookkeeping that is the
// person's own and not the project's (opening it, pinning it). Every other write that names a project, or a
// document, comment, tag or anything else that belongs to one, answers 409 with data.reason "project_trashed" until it
// is restored. A new op is covered by default.
var projectTrashedOK = map[string]bool{
	"project.trash": true, "project.restore": true, "project.purge": true, "project.archive": true, "project.unarchive": true,
	"project.member.add": true, "project.member.update": true, "project.member.remove": true,
	"project.open": true, "project.pin": true,
	// The passphrase (F-252): unlocking and locking are the person's own session state, and removing the passphrase is the way
	// out for the owner of a project in the trash that they have not unlocked in this session (restore is gated too).
	"project.unlock": true, "project.lock": true, "project.passphrase.clear": true,
	"mirror.rebuild":  true,
	"export.markdown": true, "export.html": true, "export.compile": true, "export.cover": true, "export.submission": true,
	"site.build": true, // a build is an export of the site as an archive; publishing it to the folder is refused
}

// trashedOK names the Compendium ops that must keep working on a Compendium in the trash: the ones that get it out
// again or remove it for good, the export of what is in it, the mirror, and membership (an owner may still tidy who
// has access). Every other write that names a Compendium is refused with a 409 while it is in the trash.
var trashedOK = map[string]bool{
	"compendium.trash": true, "compendium.restore": true, "compendium.archive": true, "compendium.unarchive": true,
	"compendium.purge": true, "compendium.export": true, "mirror.compendium.rebuild": true,
	"compendium.member.add": true, "compendium.member.update": true, "compendium.member.remove": true,
}
