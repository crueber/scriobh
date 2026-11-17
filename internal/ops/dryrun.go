package ops

import (
	"context"
	"os"

	"github.com/crueber/scriobh/internal/db"
)

// DryRunResult is what a dry run produced.
type DryRunResult struct {
	// Out is what the op would have returned.
	Out any
	// Changes counts the rows the op would have inserted, updated or deleted.
	Changes int64
}

// CallDry runs an op as a preview. Authorization, validation and the handler
// run exactly as in Call, inside the writer transaction, and the transaction is
// rolled back: nothing is committed, no event or audit row is written, nothing
// is delivered, and the handler sees a scratch data directory and no services.
//
// An op that declares NoDryRun is refused with dry_run_unsupported before any
// handler runs, a GET included. Other read-only ops (GET and HEAD) run as they
// are, since they change nothing.
func (r *Registry) CallDry(ctx context.Context, p Principal, name string, in any) (DryRunResult, error) {
	// The preview shares one SQLite connection among the pools it hands the op;
	// a cancelled request must not have database/sql tear transactions down from
	// another goroutine while the op is mid-statement. A preview is short and
	// always rolled back, so it runs to its end.
	ctx = Passive(context.WithoutCancel(ctx)) // a preview is not use, of a project or of a key
	e, ok := r.Lookup(name)
	if !ok {
		return DryRunResult{}, NotFound("op " + name)
	}
	if err := r.PreAuthorize(p, e); err != nil {
		return DryRunResult{}, err
	}
	if e.NeedsInputAuth() {
		if err := r.AuthorizeInput(ctx, p, e, in); err != nil {
			return DryRunResult{}, err
		}
	}
	// An op that declares NoDryRun is refused before any handler runs, whatever its method: a GET marked so
	// reaches outside the database (a search that sends its question to a model endpoint), and "reads only" is
	// a claim about the database.
	if e.Op.NoDryRun {
		return DryRunResult{}, DryRunUnsupported(name)
	}
	if e.Op.Method == "GET" || e.Op.Method == "HEAD" {
		out, err := r.Call(ctx, p, name, in)
		return DryRunResult{Out: out}, err
	}
	if r.env == nil || r.env.DB == nil {
		return DryRunResult{}, Internal(nil)
	}
	scratch, err := os.MkdirTemp("", "scriobh-dryrun-")
	if err != nil {
		return DryRunResult{}, Internal(err)
	}
	defer os.RemoveAll(scratch)
	var out any
	var callErr error
	changes, err := r.env.DB.DryRun(ctx, func(view *db.DB) error {
		env := &Env{DB: view, ProjectRole: r.env.ProjectRole, ProjectMember: r.env.ProjectMember, ProjectGate: r.env.ProjectGate, CompendiumRole: r.env.CompendiumRole, CompendiumTrashed: r.env.CompendiumTrashed, ProjectTrashed: r.env.ProjectTrashed, ProjectPrimary: r.env.ProjectPrimary, Log: r.env.Log, DataDir: scratch}
		// A preview is not activity (and must not take the writer a gate's bookkeeping would wait for).
		r.env.svcMu.RLock()
		for name, v := range r.env.previewServices {
			env.SetService(name, v) // read-only deciders only: see SetPreviewService
		}
		r.env.svcMu.RUnlock()
		c := &Ctx{Context: Passive(WithPrincipal(ctx, p)), P: p, Env: env, dry: true}
		out, callErr = e.call(c, in)
		return nil // always rolled back
	})
	if err != nil {
		return DryRunResult{}, Internal(err)
	}
	if callErr != nil {
		ae := AsError(callErr)
		if ae.Code == CodeInternal && r.env.Log != nil {
			r.env.Log.Error("op failed in dry run", "op", name, "principal", p.UserID, "err", ae.Cause())
		}
		return DryRunResult{}, ae
	}
	return DryRunResult{Out: out, Changes: changes}, nil
}
