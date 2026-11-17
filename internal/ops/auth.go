package ops

import "context"

// Instance roles, derived from IdP groups.
const (
	RoleAdmin  = "admin"
	RoleWriter = "writer"
	RoleReader = "reader"
)

// Project roles, in increasing power.
const (
	ProjectReader   = "reader"
	ProjectReviewer = "reviewer"
	ProjectEditor   = "editor"
	ProjectOwner    = "owner"
)

// API-key scopes, in increasing power.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

var scopeRank = map[string]int{ScopeRead: 1, ScopeWrite: 2, ScopeAdmin: 3}

var projectRank = map[string]int{ProjectReader: 1, ProjectReviewer: 2, ProjectEditor: 3, ProjectOwner: 4}

// Principal is who is calling: a person with a session, or a machine with a key.
type Principal struct {
	UserID  string
	Email   string
	Name    string
	Role    string // instance role; empty for anonymous
	IsAgent bool
	Via     string // "session" or "apikey"
	// SessionID names the browser session behind a session principal (never the
	// cookie secret), so the session list can mark the current one.
	SessionID string
	// Key restrictions (Phase 3): scope is read, write or admin; a key may be
	// pinned to one project or one compendium.
	Scope        string
	ProjectID    string
	CompendiumID string
	// KeyID is the API key behind a key principal; empty for a session.
	KeyID string
}

// Authenticated reports whether the caller is signed in.
func (p Principal) Authenticated() bool { return p.UserID != "" }

// AuthKind says what an op requires.
type AuthKind string

// Auth kinds.
const (
	AuthAuthenticated AuthKind = "authenticated" // any signed-in principal
	AuthWriter        AuthKind = "writer"        // instance writer or admin
	AuthAdmin         AuthKind = "admin"         // instance admin
	AuthProject       AuthKind = "project"       // membership with at least Role
	// AuthCompendium is membership of a Compendium with at least Role.
	// Compendium membership is separate from project membership: being on a
	// project never grants it.
	AuthCompendium AuthKind = "compendium"
)

// Auth is an op's declared authorization. Every op must declare one; the
// authorization sweep test calls every op as every kind of caller.
type Auth struct {
	Kind AuthKind `json:"kind"`
	Role string   `json:"role,omitempty"`
	// SessionOnly refuses an API key at any scope or pin: the op is for a person
	// signed in with a browser (a private scratchpad a leaked agent key must not read).
	SessionOnly bool `json:"session_only,omitempty"`
	// Hidden makes an op answer 404 to a caller who may not use it, as for a project they cannot see, instead of 403:
	// the console's ops, whose existence is no business of anyone else.
	Hidden bool `json:"hidden,omitempty"`

	// Resolve finds the project (or, for AuthCompendium, the Compendium) an op
	// acts on when its input does not carry that id (a document id, an entity
	// id). It must look the id up in the database and return ok=false when the
	// object does not exist; the caller then gets the same 404 as for one they
	// cannot see. A handler that takes a child id must still scope its queries
	// by the resolved id.
	Resolve func(ctx context.Context, env *Env, in any) (id string, ok bool) `json:"-"`
	// RefuseTrashed makes authorizeInput answer 409 when the project or Compendium is in the trash. The registry
	// sets it for every write op that names one (see trashedOK and projectTrashedOK for the few that must still
	// work there), after the role check, so a caller who may not see a trashed project still gets the 404.
	RefuseTrashed bool `json:"-"`
	// Passive makes the op's authorization not count as activity: the passphrase gate is asked as a stream asks it
	// (see Passive), so a call that is made on a timer by an open page (presence) cannot keep an unlock alive past
	// its idle limit. Binders also read the session without refreshing its idle timer for an op that sets it.
	Passive bool `json:"-"`
	// GateExempt is set by the registry for the ops in projectGateOK: they answer a caller whose session has not
	// unlocked a passphrase-protected project. Every other project op is gated.
	GateExempt bool `json:"-"`
}

// RequireCompendium requires membership of the input's Compendium with at
// least role. The input type must implement CompendiumRef.
func RequireCompendium(role string) Auth { return Auth{Kind: AuthCompendium, Role: role} }

// RequireCompendiumVia is RequireCompendium for inputs that name a child
// object (an entity): resolve finds the owning Compendium.
func RequireCompendiumVia(role string, resolve func(ctx context.Context, env *Env, in any) (string, bool)) Auth {
	return Auth{Kind: AuthCompendium, Role: role, Resolve: resolve}
}

// CompendiumRef is implemented by op inputs that name a Compendium.
type CompendiumRef interface{ CompendiumRef() string }

// CompendiumRoleFunc resolves a user's role on a Compendium. ok is false when
// it does not exist or the user has no access: the two are indistinguishable
// on purpose. Instance admins act as owners, as they do for projects.
type CompendiumRoleFunc func(ctx context.Context, userID, compendiumID string) (role string, ok bool)

// Pinned reports whether the key is restricted to one project or Compendium.
// A project pin reaches that project and its primary Compendium; a Compendium
// pin reaches that Compendium and no project. A key carries one pin, never both.
func (p Principal) Pinned() bool { return p.ProjectID != "" || p.CompendiumID != "" }

// RequireProjectVia is RequireProject for inputs that name a child object.
func RequireProjectVia(role string, resolve func(ctx context.Context, env *Env, in any) (string, bool)) Auth {
	return Auth{Kind: AuthProject, Role: role, Resolve: resolve}
}

// ForSessions returns the auth with API keys refused: only a person signed in
// with a browser session may call the op.
func (a Auth) ForSessions() Auth { a.SessionOnly = true; return a }

// AsPassive makes the op's authorization passive: not activity for the project passphrase gate.
func (a Auth) AsPassive() Auth { a.Passive = true; return a }

// NotFoundForOthers makes the op answer 404, not 403, to a caller it refuses for want of the role.
func (a Auth) NotFoundForOthers() Auth { a.Hidden = true; return a }

// Authenticated requires any signed-in principal.
func Authenticated() Auth { return Auth{Kind: AuthAuthenticated} }

// Writer requires the instance writer role (or admin).
func Writer() Auth { return Auth{Kind: AuthWriter} }

// Admin requires the instance admin role.
func Admin() Auth { return Auth{Kind: AuthAdmin} }

// RequireProject requires membership of the input's project with at least
// role. The input type must implement ProjectRef.
func RequireProject(role string) Auth { return Auth{Kind: AuthProject, Role: role} }

// ProjectRef is implemented by op inputs that name a project.
type ProjectRef interface{ ProjectRef() string }

// ProjectRoleFunc resolves a user's role on a project. ok is false when the
// project does not exist or the user has no access: the two are
// indistinguishable on purpose.
type ProjectRoleFunc func(ctx context.Context, userID, projectID string) (role string, ok bool)

// GateRole makes a role function gate-aware: for a caller found in the context (see WithPrincipal) who asks about
// their own role, a project the gate refuses answers "no access", so a check done by hand is closed like the
// registry's. Asking about someone else's role, or with no caller in the context (background work), is the
// membership as it is.
func GateRole(member ProjectRoleFunc, gate ProjectGateFunc) ProjectRoleFunc {
	return func(ctx context.Context, userID, projectID string) (string, bool) {
		role, ok := member(ctx, userID, projectID)
		if !ok || gate == nil {
			return role, ok
		}
		if p, has := PrincipalFrom(ctx); has && p.UserID == userID && p.Via != "cli" {
			// A hand-written role check is not the call itself (the registry has asked, and counted it): it must not
			// count a key's use again or move an unlock's idle clock.
			if gate(Passive(ctx), p, projectID) != nil {
				return "", false
			}
		}
		return role, ok
	}
}

// needsInput reports whether authorization depends on the op's input.
func (a Auth) needsInput() bool { return a.Kind == AuthProject || a.Kind == AuthCompendium }

// authorizeEarly checks everything that does not need the input. It is cheap
// and runs before any request body is read.
func (r *Registry) authorizeEarly(p Principal, a Auth) error {
	if !p.Authenticated() {
		return Unauthenticated()
	}
	// A person at a browser ("session") or the operator's own shell on the server ("cli", which runs with the
	// database open and is the operator by definition, as the project gate also treats it). Anything else, a key
	// of any scope or a kind of caller added later, is refused: the kind is checked for what it is, not for what it
	// is not. (A test that calls the registry without saying how has no Via and no key, and passes as before.)
	if a.SessionOnly && (p.KeyID != "" || (p.Via != "" && p.Via != "session" && p.Via != "cli")) {
		return Forbidden("this is for a person signed in with a browser; an API key cannot use it")
	}
	// A key pinned to one project or Compendium may only call ops that name a
	// project or a Compendium (and authorizeInput holds it to its own);
	// everything instance-wide is out of its reach.
	if p.Pinned() && a.Kind != AuthProject && a.Kind != AuthCompendium {
		return Forbidden("this key is restricted to one project or compendium")
	}
	switch a.Kind {
	case AuthWriter:
		if p.Role != RoleWriter && p.Role != RoleAdmin {
			return Forbidden("your account cannot do this")
		}
	case AuthAdmin:
		if p.Role != RoleAdmin {
			if a.Hidden {
				return NotFound("page")
			}
			return Forbidden("administrators only")
		}
	}
	return nil
}

// authorizeInput checks input-dependent access. Missing access is NotFound so
// project and Compendium existence does not leak. A pinned key is held to its
// own object: a project pin reaches only that project (and its primary
// Compendium), a Compendium pin only that Compendium and no project.
func (r *Registry) authorizeInput(ctx context.Context, p Principal, a Auth, in any) error {
	if a.Kind != AuthProject && a.Kind != AuthCompendium {
		return nil
	}
	what := "project"
	if a.Kind == AuthCompendium {
		what = "compendium"
	}
	var id string
	if a.Resolve != nil {
		var found bool
		if id, found = a.Resolve(ctx, r.env, in); !found {
			return NotFound(what)
		}
	} else if a.Kind == AuthCompendium {
		ref, ok := in.(CompendiumRef)
		if !ok {
			return Internal(nil)
		}
		id = ref.CompendiumRef()
	} else {
		ref, ok := in.(ProjectRef)
		if !ok {
			return Internal(nil)
		}
		id = ref.ProjectRef()
	}
	if a.Kind == AuthProject {
		if p.CompendiumID != "" || (p.ProjectID != "" && p.ProjectID != id) { // a key pinned elsewhere
			return NotFound(what)
		}
		if r.env == nil || r.env.ProjectRole == nil {
			return NotFound(what)
		}
		// Membership first, without the gate: a stranger must get the 404 a missing project gives, whether or not
		// the project has a passphrase.
		member := r.env.ProjectMember
		if member == nil {
			member = r.env.ProjectRole
		}
		role, ok := member(ctx, p.UserID, id)
		if !ok {
			return NotFound(what)
		}
		if projectRank[role] < projectRank[a.Role] {
			return Forbidden("your role on this project cannot do this")
		}
		if !a.GateExempt && r.env.ProjectGate != nil {
			gctx := ctx
			if a.Passive && p.KeyID == "" { // a key's refusal is still counted (it never gets as far as the op: presence is for sessions)
				gctx = Passive(ctx)
			}
			if err := r.env.ProjectGate(gctx, p, id); err != nil {
				return err
			}
		}
		if a.RefuseTrashed && r.env.ProjectTrashed != nil && r.env.ProjectTrashed(ctx, id) {
			return ProjectInTrash()
		}
		return nil
	}
	switch {
	case p.CompendiumID != "":
		if p.CompendiumID != id { // a key pinned to another Compendium
			return NotFound(what)
		}
	case p.ProjectID != "":
		// A key pinned to a project reaches that project's primary Compendium
		// (and only that one), so one scoped key can fill a book's bible and
		// draft its chapters.
		if r.env == nil || r.env.ProjectPrimary == nil || r.env.ProjectPrimary(ctx, p.ProjectID) != id {
			return NotFound(what)
		}
	}
	if r.env == nil || r.env.CompendiumRole == nil {
		return NotFound(what)
	}
	role, ok := r.env.CompendiumRole(ctx, p.UserID, id)
	if !ok {
		return NotFound(what)
	}
	if projectRank[role] < projectRank[a.Role] {
		return Forbidden("your role on this compendium cannot do this")
	}
	if a.RefuseTrashed && r.env.CompendiumTrashed != nil && r.env.CompendiumTrashed(ctx, id) {
		return CompendiumInTrash()
	}
	return nil
}
