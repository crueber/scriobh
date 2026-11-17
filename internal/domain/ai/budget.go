package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Budgets. Caps are monthly (calendar months, UTC), in tokens and in money
// (micro-units of the instance currency), and exist at four levels: the
// instance, a person, a project (both set by an administrator) and the
// project's owner's own cap for it (in the policy). Every one that applies must
// have room.
//
// A cap is enforced before the call, in the same writer transaction that
// records the run: the sums of what was spent (ai_usage_daily) and of what is
// reserved for runs still in flight (the running rows of ai_runs) plus this
// request's worst case must fit. Concurrent runs queue on the one writer, so
// two cannot both pass a cap only one fits under. The reservation is replaced by
// the actual usage when the run ends.
//
// Money caps count what the instance pays for: a provider an administrator
// added that is not local. A person's own provider is paid by that person and a
// local one costs nothing; both count against token caps, neither against money
// caps. A money cap meeting a paid model with no known price refuses the request
// (an unknown price is not zero); token caps always work.

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// CapLine is one cap that applies, with its use this month.
type CapLine struct {
	Scope              string `json:"scope" enum:"instance,user,project,project_owner" desc:"Whose cap: the whole instance, you, the project (set by an administrator), or the project's owner's own cap."`
	MaxCostMicros      int64  `json:"max_cost_micros" desc:"0: no money cap."`
	MaxTokens          int64  `json:"max_tokens" desc:"0: no token cap."`
	SpentCostMicros    int64  `json:"spent_cost_micros"`
	SpentTokens        int64  `json:"spent_tokens"`
	ReservedCostMicros int64  `json:"reserved_cost_micros" desc:"Held for requests in flight."`
	ReservedTokens     int64  `json:"reserved_tokens"`
}

// BudgetView is the caps that apply to a request or a person, with their use.
type BudgetView struct {
	Month    string    `json:"month" desc:"YYYY-MM, UTC. Caps reset on the first of the month."`
	Currency string    `json:"currency"`
	Lines    []CapLine `json:"lines"`
}

type budgetResult struct {
	View    BudgetView
	Refusal *ops.Error
	Code    string
}

func (s *Service) month() string { return now()[:7] }

func monthBounds(m string) (string, string) { return m + "-01", m + "-31" }

type capRow struct{ cost, tokens int64 }

func loadCap(ctx context.Context, q queryer, scope, id string) (capRow, bool) {
	var c capRow
	err := q.QueryRowContext(ctx, `SELECT max_cost_micros, max_tokens FROM ai_budgets WHERE scope = ? AND scope_id = ?`, scope, id).Scan(&c.cost, &c.tokens)
	return c, err == nil
}

// sums returns what was spent this month and what is reserved, for a filter on
// the user or the project (or neither, for the instance).
func sums(ctx context.Context, q queryer, month, userID, projectID string) (spentCost, spentTok, resCost, resTok int64, err error) {
	from, to := monthBounds(month)
	where, args := "", []any{from, to}
	rwhere, rargs := "", []any{month}
	if userID != "" {
		where += " AND user_id = ?"
		args = append(args, userID)
		rwhere += " AND user_id = ?"
		rargs = append(rargs, userID)
	}
	if projectID != "" {
		where += " AND project_id = ?"
		args = append(args, projectID)
		rwhere += " AND project_id = ?"
		rargs = append(rargs, projectID)
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros),0), COALESCE(SUM(input_tokens + output_tokens),0) FROM ai_usage_daily WHERE day >= ? AND day <= ?`+where, args...).Scan(&spentCost, &spentTok); err != nil {
		return
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_micros),0), COALESCE(SUM(reserved_tokens),0) FROM ai_runs WHERE (status = 'running' OR (status = 'unsettled' AND substr(created_at, 1, 7) = ?))`+rwhere, rargs...).Scan(&resCost, &resTok)
	return
}

// capLines gathers the caps that apply to this person on this project.
func (s *Service) capLines(ctx context.Context, q queryer, userID, projectID string, pol policy) ([]CapLine, error) {
	month := s.month()
	var out []CapLine
	add := func(scope string, c capRow, uid, pid string) error {
		if c.cost == 0 && c.tokens == 0 {
			return nil
		}
		sc, st, rc, rt, err := sums(ctx, q, month, uid, pid)
		if err != nil {
			return err
		}
		out = append(out, CapLine{Scope: scope, MaxCostMicros: c.cost, MaxTokens: c.tokens, SpentCostMicros: sc, SpentTokens: st, ReservedCostMicros: rc, ReservedTokens: rt})
		return nil
	}
	if c, ok := loadCap(ctx, q, "instance", ""); ok {
		if err := add("instance", c, "", ""); err != nil {
			return nil, err
		}
	}
	uc, ok := loadCap(ctx, q, "user", userID)
	if !ok {
		uc = capRow{cost: s.cfg.DefaultUserMicros, tokens: s.cfg.DefaultUserTokens}
	}
	if err := add("user", uc, userID, ""); err != nil {
		return nil, err
	}
	if projectID != "" {
		if c, ok := loadCap(ctx, q, "project", projectID); ok {
			if err := add("project", c, "", projectID); err != nil {
				return nil, err
			}
		}
		if err := add("project_owner", capRow{cost: pol.BudgetMicros, tokens: pol.BudgetTokens}, "", projectID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// commas writes a whole number with thousands separators: 2180 as 2,180.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}

func money(micros int64, cur string) string {
	return fmt.Sprintf("%.2f %s", float64(micros)/1e6, cur)
}

func who(scope string) string {
	switch scope {
	case "instance":
		return "This server's monthly AI limit"
	case "user":
		return "Your monthly AI limit"
	case "project":
		return "This project's monthly AI limit"
	}
	return "The project owner's monthly AI limit for this project"
}

// budgetCheck judges a request against every cap. When it passes it returns the
// view; when it does not, the refusal says whose cap and how much is used.
func (s *Service) budgetCheck(ctx context.Context, q queryer, userID, projectID string, pol policy, est reserveEst, reserving bool) (budgetResult, error) {
	cur := s.currencyQ(ctx, q)
	lines, err := s.capLines(ctx, q, userID, projectID, pol)
	if err != nil {
		return budgetResult{}, err
	}
	res := budgetResult{View: BudgetView{Month: s.month(), Currency: cur, Lines: lines}}
	if res.View.Lines == nil {
		res.View.Lines = []CapLine{}
	}
	for _, l := range lines {
		if l.MaxTokens > 0 && l.SpentTokens+l.ReservedTokens+est.Tokens > l.MaxTokens {
			msg := fmt.Sprintf("%s is reached: %s of %s tokens used this month, and this request could count up to %s against it (its input once for every try, and the longest answer). It resets on the first of the month (UTC).",
				who(l.Scope), commas(l.SpentTokens+l.ReservedTokens), commas(l.MaxTokens), commas(est.Tokens))
			res.Refusal, res.Code = refuse("budget_exceeded", msg), "budget_exceeded"
			return res, nil
		}
		if l.MaxCostMicros > 0 && est.Paid {
			if !est.PriceKnown {
				msg := "A spending limit applies, but no price is known for a model this request may use, so its cost cannot be bounded. An administrator can set the model's price, or the limit can be lifted."
				res.Refusal, res.Code = refuse("price_unknown", msg), "price_unknown"
				return res, nil
			}
			if l.SpentCostMicros+l.ReservedCostMicros+est.Micros > l.MaxCostMicros {
				msg := fmt.Sprintf("%s is reached: %s of %s used this month, and this request could cost up to %s. It resets on the first of the month (UTC).",
					who(l.Scope), money(l.SpentCostMicros+l.ReservedCostMicros, cur), money(l.MaxCostMicros, cur), money(est.Micros, cur))
				res.Refusal, res.Code = refuse("budget_exceeded", msg), "budget_exceeded"
				return res, nil
			}
		}
	}
	return res, nil
}

func (s *Service) currencyQ(ctx context.Context, q queryer) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'ai.currency'`).Scan(&v); err == nil && v != "" {
		return v
	}
	return s.cfg.Currency
}

// staleRun is how long a running row is believed. A run longer than the longest
// stream plus a margin has died with its process.
const staleRun = time.Hour

// staleCutoff is the time before which a running row is believed dead, on the
// database's own clock (db.Now), the one created_at is written with.
func staleCutoff() string {
	t, err := time.Parse(time.RFC3339, now())
	if err != nil {
		return "" // an unreadable clock closes nothing
	}
	return t.Add(-staleRun).UTC().Format(time.RFC3339)
}

// reserve checks the caps and records the run as running, with its worst case
// held, in one writer transaction.
func (s *Service) reserve(ctx context.Context, d *db.DB, run runRow, pol policy, est reserveEst) (*ops.Error, error) {
	var refusal *ops.Error
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		cutoff := staleCutoff()
		if _, err := tx.Exec(`UPDATE ai_runs SET status = 'error', error_code = 'interrupted', reserved_micros = 0, reserved_tokens = 0, finished_at = ? WHERE status = 'running' AND created_at < ?`, now(), cutoff); err != nil {
			return err
		}
		res, err := s.budgetCheck(ctx, tx, run.UserID, run.ProjectID, pol, est, true)
		if err != nil {
			return err
		}
		if res.Refusal != nil {
			refusal = res.Refusal
			return nil
		}
		run.ReservedTokens = est.Tokens
		if est.Paid && est.PriceKnown {
			run.ReservedMicros = est.Micros
		}
		_, err = tx.Exec(`INSERT INTO ai_runs(id, user_id, project_id, document_id, recipe, recipe_version, provider_id, model, via, status, currency,
			reserved_micros, reserved_tokens, payload_hash, scope_json, created_at) VALUES (?,?,?,?,?,?,?,?,?,'running',?,?,?,?,?,?)`,
			run.ID, run.UserID, run.ProjectID, run.DocumentID, run.Recipe, run.RecipeVersion, run.ProviderID, run.Model, run.Via, run.Currency,
			run.ReservedMicros, run.ReservedTokens, run.PayloadHash, run.ScopeJSON, run.CreatedAt)
		return err
	})
	return refusal, err
}

// runRow is an ai_runs row as it is first written.
type runRow struct {
	ID, UserID, ProjectID, DocumentID, Recipe, ProviderID, Model, Via, Currency, PayloadHash, ScopeJSON, CreatedAt string
	RecipeVersion                                                                                                  int
	ReservedMicros, ReservedTokens                                                                                 int64
}

// closeInterrupted ends the runs a stopped process left running; called at start.
func (s *Service) closeInterrupted(ctx context.Context, d *db.DB) {
	_ = d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE ai_runs SET status = 'error', error_code = 'interrupted', reserved_micros = 0, reserved_tokens = 0, finished_at = ? WHERE status = 'running'`, now())
		return err
	})
}

// ---- ops ------------------------------------------------------------------------------------------------------------

// BudgetGetIn asks for the caps that apply.
type BudgetGetIn struct {
	ProjectID string `query:"project_id" desc:"Also the caps of this project (you need access to it)."`
}

// BudgetRow is a cap an administrator set.
type BudgetRow struct {
	Scope         string `json:"scope" enum:"instance,user,project"`
	ScopeID       string `json:"subject,omitempty" desc:"The user or project the cap is for; empty for the instance."`
	MaxCostMicros int64  `json:"max_cost_micros"`
	MaxTokens     int64  `json:"max_tokens"`
	UpdatedAt     string `json:"updated_at"`
}

// BudgetOut is the caps that apply to the caller.
type BudgetOut struct {
	Item  BudgetView  `json:"item"`
	Rows  []BudgetRow `json:"rows,omitempty" desc:"Administrators only: every cap that was set."`
	Limit bool        `json:"limited" desc:"Some cap applies."`
}

// getBudget, getUsage and projectUsage skip the kill switch: they read what was
// already counted and send nothing, so a person can see their use while AI is off.
func getBudget(s *Service) func(*ops.Ctx, BudgetGetIn) (BudgetOut, error) {
	return func(c *ops.Ctx, in BudgetGetIn) (BudgetOut, error) {
		var pol policy
		if in.ProjectID != "" {
			if _, ok := c.Env.ProjectRole(c, c.P.UserID, in.ProjectID); !ok || (c.P.ProjectID != "" && c.P.ProjectID != in.ProjectID) {
				return BudgetOut{}, ops.NotFound("project")
			}
			var err error
			if pol, err = loadPolicy(c, c.DB().Read(), in.ProjectID); err != nil {
				return BudgetOut{}, ops.Internal(err)
			}
		}
		lines, err := s.capLines(c, c.DB().Read(), c.P.UserID, in.ProjectID, pol)
		if err != nil {
			return BudgetOut{}, ops.Internal(err)
		}
		if lines == nil {
			lines = []CapLine{}
		}
		out := BudgetOut{Item: BudgetView{Month: s.month(), Currency: s.currency(c, c.DB()), Lines: lines}, Limit: len(lines) > 0}
		if c.P.Role == ops.RoleAdmin {
			rows, err := c.DB().Read().QueryContext(c, `SELECT scope, scope_id, max_cost_micros, max_tokens, updated_at FROM ai_budgets ORDER BY scope, scope_id LIMIT 500`)
			if err != nil {
				return BudgetOut{}, ops.Internal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var r BudgetRow
				if err := rows.Scan(&r.Scope, &r.ScopeID, &r.MaxCostMicros, &r.MaxTokens, &r.UpdatedAt); err != nil {
					return BudgetOut{}, ops.Internal(err)
				}
				out.Rows = append(out.Rows, r)
			}
		}
		return out, nil
	}
}

// BudgetSetIn sets a cap.
type BudgetSetIn struct {
	Scope         string `json:"scope" enum:"instance,user,project"`
	ScopeID       string `json:"subject,omitempty" desc:"The user or project id; empty for the instance."`
	MaxCostMicros int64  `json:"max_cost_micros" desc:"Monthly, in micro-units of the instance currency (1,000,000 = one unit). 0: no money cap."`
	MaxTokens     int64  `json:"max_tokens" desc:"Monthly. 0: no token cap."`
}

// Validate checks the shape.
func (in *BudgetSetIn) Validate() error {
	switch in.Scope {
	case "instance":
		in.ScopeID = ""
	case "user", "project":
		if strings.TrimSpace(in.ScopeID) == "" || len(in.ScopeID) > 60 {
			return ops.Invalid("subject", "Name the user or project.")
		}
	default:
		return ops.Invalid("scope", "Scope is instance, user or project.")
	}
	if in.MaxCostMicros < 0 || in.MaxCostMicros > 1_000_000_000_000_000 || in.MaxTokens < 0 || in.MaxTokens > 1_000_000_000_000 {
		return ops.Invalid("max_cost_micros", "Caps cannot be negative.")
	}
	return nil
}

// BudgetSetOut is the cap after the change.
type BudgetSetOut struct {
	Item BudgetRow `json:"item"`
}

func setBudget(s *Service) func(*ops.Ctx, BudgetSetIn) (BudgetSetOut, error) {
	return func(c *ops.Ctx, in BudgetSetIn) (BudgetSetOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return BudgetSetOut{}, err
		}
		out := BudgetRow{Scope: in.Scope, ScopeID: in.ScopeID, MaxCostMicros: in.MaxCostMicros, MaxTokens: in.MaxTokens, UpdatedAt: now()}
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			var n int
			switch in.Scope {
			case "user":
				_ = tx.QueryRowContext(c, `SELECT count(*) FROM users WHERE id = ?`, in.ScopeID).Scan(&n)
			case "project":
				_ = tx.QueryRowContext(c, `SELECT count(*) FROM projects WHERE id = ?`, in.ScopeID).Scan(&n)
			default:
				n = 1
			}
			if n == 0 {
				return ops.NotFound(in.Scope)
			}
			if in.MaxCostMicros == 0 && in.MaxTokens == 0 {
				if _, err := tx.Exec(`DELETE FROM ai_budgets WHERE scope = ? AND scope_id = ?`, in.Scope, in.ScopeID); err != nil {
					return err
				}
			} else if _, err := tx.Exec(`INSERT INTO ai_budgets(scope, scope_id, max_cost_micros, max_tokens, updated_by, updated_at) VALUES (?,?,?,?,?,?)
				ON CONFLICT(scope, scope_id) DO UPDATE SET max_cost_micros=excluded.max_cost_micros, max_tokens=excluded.max_tokens, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
				in.Scope, in.ScopeID, in.MaxCostMicros, in.MaxTokens, c.P.UserID, out.UpdatedAt); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.budget.set", TargetType: in.Scope, TargetID: in.ScopeID, Detail: map[string]any{"max_cost_micros": in.MaxCostMicros, "max_tokens": in.MaxTokens}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return BudgetSetOut{}, ops.AsError(err)
		}
		return BudgetSetOut{Item: out}, nil
	}
}

// ---- prices -------------------------------------------------------------------------------------------------------

// PriceRow is a model's price.
type PriceRow struct {
	ProviderID   string `json:"provider_id,omitempty" desc:"Empty: any provider."`
	Model        string `json:"model"`
	InputMicros  int64  `json:"input_micros_per_mtok" desc:"Micro-units of the instance currency per million input tokens."`
	OutputMicros int64  `json:"output_micros_per_mtok"`
	UpdatedAt    string `json:"updated_at"`
}

// PriceListOut lists the prices an administrator set.
type PriceListOut struct {
	Items []PriceRow `json:"items"`
}

func listPrices(s *Service) func(*ops.Ctx, struct{}) (PriceListOut, error) {
	return func(c *ops.Ctx, _ struct{}) (PriceListOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return PriceListOut{}, err
		}
		rows, err := c.DB().Read().QueryContext(c, `SELECT provider_id, model, input_micros, output_micros, updated_at FROM ai_prices ORDER BY model, provider_id LIMIT 2000`)
		if err != nil {
			return PriceListOut{}, ops.Internal(err)
		}
		defer rows.Close()
		out := PriceListOut{Items: []PriceRow{}}
		for rows.Next() {
			var r PriceRow
			if err := rows.Scan(&r.ProviderID, &r.Model, &r.InputMicros, &r.OutputMicros, &r.UpdatedAt); err != nil {
				return out, ops.Internal(err)
			}
			out.Items = append(out.Items, r)
		}
		return out, nil
	}
}

// PriceSetIn sets a price.
type PriceSetIn struct {
	ProviderID   string `json:"provider_id,omitempty" desc:"Empty: any provider."`
	Model        string `json:"model"`
	InputMicros  int64  `json:"input_micros_per_mtok"`
	OutputMicros int64  `json:"output_micros_per_mtok"`
}

// Validate checks the shape.
func (in *PriceSetIn) Validate() error {
	in.Model = strings.TrimSpace(in.Model)
	if in.Model == "" || len(in.Model) > 200 {
		return ops.Invalid("model", "Name the model.")
	}
	if in.ProviderID != "" {
		if e := providerID(in.ProviderID); e != nil {
			return e
		}
	}
	if in.InputMicros < 0 || in.OutputMicros < 0 || in.InputMicros > 1_000_000_000_000 || in.OutputMicros > 1_000_000_000_000 {
		return ops.Invalid("input_micros_per_mtok", "A price is between 0 and a million units per million tokens.")
	}
	return nil
}

// PriceItem wraps a price.
type PriceItem struct {
	Item PriceRow `json:"item"`
}

func setPrice(s *Service) func(*ops.Ctx, PriceSetIn) (PriceItem, error) {
	return func(c *ops.Ctx, in PriceSetIn) (PriceItem, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return PriceItem{}, err
		}
		r := PriceRow{ProviderID: in.ProviderID, Model: in.Model, InputMicros: in.InputMicros, OutputMicros: in.OutputMicros, UpdatedAt: now()}
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO ai_prices(provider_id, model, input_micros, output_micros, updated_at) VALUES (?,?,?,?,?)
				ON CONFLICT(provider_id, model) DO UPDATE SET input_micros=excluded.input_micros, output_micros=excluded.output_micros, updated_at=excluded.updated_at`,
				r.ProviderID, r.Model, r.InputMicros, r.OutputMicros, r.UpdatedAt); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.price.set", TargetType: "ai_price", TargetID: r.ProviderID, Detail: map[string]any{"input_micros": r.InputMicros, "output_micros": r.OutputMicros}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return PriceItem{}, ops.Internal(err)
		}
		return PriceItem{Item: r}, nil
	}
}

// PriceDeleteIn removes a price.
type PriceDeleteIn struct {
	ProviderID string `query:"provider_id" desc:"Empty: the any-provider price."`
	Model      string `query:"model"`
}

// Validate checks the shape.
func (in *PriceDeleteIn) Validate() error {
	if strings.TrimSpace(in.Model) == "" {
		return ops.Invalid("model", "Name the model.")
	}
	return nil
}

func deletePrice(s *Service) func(*ops.Ctx, PriceDeleteIn) (RemovedOut, error) {
	return func(c *ops.Ctx, in PriceDeleteIn) (RemovedOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return RemovedOut{}, err
		}
		var n int64
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			res, err := tx.Exec(`DELETE FROM ai_prices WHERE provider_id = ? AND model = ?`, in.ProviderID, in.Model)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			c.Audit(ops.AuditEntry{Action: "ai.price.delete", TargetType: "ai_price", TargetID: in.ProviderID})
			return c.FlushTo(tx)
		})
		if err != nil {
			return RemovedOut{}, ops.Internal(err)
		}
		return RemovedOut{Removed: n > 0}, nil
	}
}

// ---- usage ----------------------------------------------------------------------------------------------------

// UsageDay is one day of usage.
type UsageDay struct {
	Day          string `json:"day"`
	Runs         int    `json:"runs"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicros   int64  `json:"cost_micros" desc:"Charged against the money caps."`
	OwnMicros    int64  `json:"own_micros" desc:"Paid by the person's own provider."`
}

// UsageTotals sums usage.
type UsageTotals struct {
	Runs         int   `json:"runs"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostMicros   int64 `json:"cost_micros"`
	OwnMicros    int64 `json:"own_micros"`
}

// UsageIn picks a month.
type UsageIn struct {
	Month string `query:"month" desc:"YYYY-MM, UTC. Default: this month."`
}

// Validate checks the month.
func (in *UsageIn) Validate() error {
	if in.Month != "" {
		if _, err := time.Parse("2006-01", in.Month); err != nil {
			return ops.Invalid("month", "A month is YYYY-MM.")
		}
	}
	return nil
}

// UsageOut is a month of usage.
type UsageOut struct {
	Month    string      `json:"month"`
	Currency string      `json:"currency"`
	Totals   UsageTotals `json:"totals"`
	Days     []UsageDay  `json:"days"`
	Budget   BudgetView  `json:"budget" desc:"Your caps and how much of them is used this month."`
}

func usageRows(ctx context.Context, q queryer, month, where string, args ...any) ([]UsageDay, UsageTotals, error) {
	from, to := monthBounds(month)
	rows, err := q.QueryContext(ctx, `SELECT day, SUM(runs), SUM(input_tokens), SUM(output_tokens), SUM(cost_micros), SUM(own_micros) FROM ai_usage_daily
		WHERE day >= ? AND day <= ? `+where+` GROUP BY day ORDER BY day`, append([]any{from, to}, args...)...)
	if err != nil {
		return nil, UsageTotals{}, err
	}
	defer rows.Close()
	days, tot := []UsageDay{}, UsageTotals{}
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.Day, &u.Runs, &u.InputTokens, &u.OutputTokens, &u.CostMicros, &u.OwnMicros); err != nil {
			return nil, tot, err
		}
		days = append(days, u)
		tot.Runs += u.Runs
		tot.InputTokens += u.InputTokens
		tot.OutputTokens += u.OutputTokens
		tot.CostMicros += u.CostMicros
		tot.OwnMicros += u.OwnMicros
	}
	return days, tot, rows.Err()
}

func getUsage(s *Service) func(*ops.Ctx, UsageIn) (UsageOut, error) {
	return func(c *ops.Ctx, in UsageIn) (UsageOut, error) {
		month := in.Month
		if month == "" {
			month = s.month()
		}
		days, tot, err := usageRows(c, c.DB().Read(), month, "AND user_id = ?", c.P.UserID)
		if err != nil {
			return UsageOut{}, ops.Internal(err)
		}
		lines, err := s.capLines(c, c.DB().Read(), c.P.UserID, "", policy{})
		if err != nil {
			return UsageOut{}, ops.Internal(err)
		}
		if lines == nil {
			lines = []CapLine{}
		}
		cur := s.currency(c, c.DB())
		return UsageOut{Month: month, Currency: cur, Totals: tot, Days: days, Budget: BudgetView{Month: s.month(), Currency: cur, Lines: lines}}, nil
	}
}

// UsageProjectIn names a project and a month.
type UsageProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Month     string `query:"month" desc:"YYYY-MM, UTC. Default: this month."`
}

// ProjectRef lets the registry authorize by membership.
func (in UsageProjectIn) ProjectRef() string { return in.ProjectID }

// Validate checks the month.
func (in *UsageProjectIn) Validate() error {
	if in.Month != "" {
		if _, err := time.Parse("2006-01", in.Month); err != nil {
			return ops.Invalid("month", "A month is YYYY-MM.")
		}
	}
	return nil
}

// UserUsage is one person's use of a project.
type UserUsage struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	UsageTotals
}

// UsageProjectOut is a project's AI usage.
type UsageProjectOut struct {
	Month    string      `json:"month"`
	Currency string      `json:"currency"`
	Totals   UsageTotals `json:"totals"`
	Days     []UsageDay  `json:"days"`
	ByUser   []UserUsage `json:"by_user"`
	Budget   BudgetView  `json:"budget"`
}

func projectUsage(s *Service) func(*ops.Ctx, UsageProjectIn) (UsageProjectOut, error) {
	return func(c *ops.Ctx, in UsageProjectIn) (UsageProjectOut, error) {
		month := in.Month
		if month == "" {
			month = s.month()
		}
		days, tot, err := usageRows(c, c.DB().Read(), month, "AND project_id = ?", in.ProjectID)
		if err != nil {
			return UsageProjectOut{}, ops.Internal(err)
		}
		from, to := monthBounds(month)
		rows, err := c.DB().Read().QueryContext(c, `SELECT u.user_id, COALESCE(x.display_name, ''), SUM(u.runs), SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cost_micros), SUM(u.own_micros)
			FROM ai_usage_daily u LEFT JOIN users x ON x.id = u.user_id WHERE u.project_id = ? AND u.day >= ? AND u.day <= ? GROUP BY u.user_id ORDER BY u.user_id`, in.ProjectID, from, to)
		if err != nil {
			return UsageProjectOut{}, ops.Internal(err)
		}
		defer rows.Close()
		by := []UserUsage{}
		for rows.Next() {
			var u UserUsage
			if err := rows.Scan(&u.UserID, &u.DisplayName, &u.Runs, &u.InputTokens, &u.OutputTokens, &u.CostMicros, &u.OwnMicros); err != nil {
				return UsageProjectOut{}, ops.Internal(err)
			}
			by = append(by, u)
		}
		pol, err := loadPolicy(c, c.DB().Read(), in.ProjectID)
		if err != nil {
			return UsageProjectOut{}, ops.Internal(err)
		}
		lines, err := s.capLines(c, c.DB().Read(), c.P.UserID, in.ProjectID, pol)
		if err != nil {
			return UsageProjectOut{}, ops.Internal(err)
		}
		if lines == nil {
			lines = []CapLine{}
		}
		cur := s.currency(c, c.DB())
		return UsageProjectOut{Month: month, Currency: cur, Totals: tot, Days: days, ByUser: by, Budget: BudgetView{Month: s.month(), Currency: cur, Lines: lines}}, nil
	}
}
