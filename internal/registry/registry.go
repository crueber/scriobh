// Package registry assembles the op registry from every domain package. The
// server, the skill generator and the tests all build it here, so there is
// exactly one list of ops.
package registry

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/admin"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/desktop"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/presence"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/domain/share"
	sitedomain "github.com/crueber/scriobh/internal/domain/site"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/domain/typography"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/webhook"
)

// Options are what the registry needs from the configuration: limits that ops
// capture when they are registered, so a preview sees the same ones.
type Options struct {
	Log         *slog.Logger
	Attachments attachments.Limits
	// AI configures the AI core (zero value: defaults, no secret key).
	AI aicore.Options
	// Webhook is what the webhook ops validate against and what a preview sees
	// (zero value: the defaults, no private targets).
	Webhook webhook.Limits
	// Semantic is the semantic index's limits (zero value: the defaults).
	Semantic semantic.Config
	// Pandoc is the optional Pandoc bridge's runner settings (zero value: the defaults,
	// no resource-limit helper).
	Pandoc pandoc.Options
}

// Build returns the full registry. d and sink may be nil when only the
// schema is needed (skill generation, the op list).
func Build(d *db.DB, sink ops.Sink, dataDir string, log ...*slog.Logger) *ops.Registry {
	o := Options{Attachments: attachments.DefaultLimits}
	if len(log) > 0 {
		o.Log = log[0]
	}
	return BuildWith(d, sink, dataDir, o)
}

// BuildWith is Build with options.
func BuildWith(d *db.DB, sink ops.Sink, dataDir string, o Options) *ops.Registry {
	env := &ops.Env{DB: d, Sink: sink, DataDir: dataDir, Log: o.Log}
	gate := projectgate.New(d)
	if d != nil {
		// The role function every hand-written role check goes through is gate-aware (F-252); the registry asks the
		// gate itself in authorizeInput, with the membership truth below it.
		env.ProjectMember = projects.RoleFunc(d)
		env.ProjectGate = gate.Check
		env.ProjectRole = ops.GateRole(env.ProjectMember, env.ProjectGate)
		env.SetPreviewService(projectgate.ServiceName, gate) // a preview filters locked projects exactly as the real call does
		env.CompendiumRole = compaccess.RoleFunc(d)
		env.CompendiumTrashed = func(ctx context.Context, id string) bool {
			var n int
			_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM compendiums WHERE id = ? AND trashed_at IS NOT NULL`, id).Scan(&n)
			return n > 0
		}
		env.ProjectTrashed = func(ctx context.Context, id string) bool {
			var n int
			_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = ? AND trashed_at IS NOT NULL`, id).Scan(&n)
			return n > 0
		}
		env.ProjectPrimary = func(ctx context.Context, projectID string) string {
			var cid sql.NullString
			_ = d.Read().QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&cid)
			return cid.String
		}
	}
	r := ops.NewRegistry(env)
	projects.Register(r)
	projectgate.Register(r, gate)
	pres := presence.New() // F-244: memory only; asks the database and the gate only to decide who may still be shown
	if d != nil {
		pres.Bind(presence.Binding{DB: d, Gate: env.ProjectGate, Trashed: env.ProjectTrashed})
	}
	presence.Register(r, pres)
	gate.SetOnRelock(pres.InvalidateGate) // a lock or a new passphrase is seen at once, not after the presence service's 5 seconds
	typography.Register(r)                // F-085
	docs.Register(r)
	compendiums.Register(r)
	entities.Register(r)
	mentions.Register(r)
	scenes.Register(r)
	timeline.Register(r)
	entities.RegisterMergeStep(entities.MergeStep{Name: "mentions",
		Count: func(ctx context.Context, q entities.Querier, source, target string) (int, error) {
			return mentions.MergeCount(ctx, q, source, target)
		},
		Apply: mentions.MergeApply})
	account.Register(r)
	search.Register(r)
	export.Register(r)
	compile.Register(r)
	sitedomain.Register(r)
	cover.Register(r)
	goals.Register(r)
	studio.Register(r) // Phase 7.5: the writing studio
	attachments.Register(r, o.Attachments)
	research.Register(r)
	stash.Register(r)
	keys.Register(r)
	imports.Register(r)
	editions.Register(r)
	mirror.Register(r)
	backup.Register(r)
	gitmirror.Register(r)
	if o.AI.Log == nil {
		o.AI.Log = o.Log
	}
	aicore.Register(r, o.AI)
	semantic.Register(r, o.Semantic)
	aiwork.Register(r)
	review.Register(r)
	editiondiff.Register(r) // after docs and review: it writes through their seams
	share.Register(r)
	admin.Register(r)
	desktop.Register(r)
	webhook.Register(r, o.Webhook)
	if o.Pandoc.Log == nil {
		o.Pandoc.Log = o.Log
	}
	// No Pandoc binary may live in the data directory: what a person can put there is not a program to run.
	o.Pandoc.RefuseUnder = append(o.Pandoc.RefuseUnder, dataDir)
	pandoc.Register(r, o.Pandoc)
	mail.Register(r)
	return r
}
