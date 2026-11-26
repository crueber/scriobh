package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/domain/admin"
	"github.com/crueber/scriobh/internal/ops"
)

// What the administrators' diagnostics may know of the running server: the shape of its configuration, from an
// allow-list, and a short ring of its warnings and errors, scrubbed as they are kept.

// configCovered names every field of config.Config and says whether the diagnostics show it ("shown": as a number,
// a switch or whether it is set) or hide it on purpose ("hidden": a secret, an address, a path or a group name).
// TestEveryConfigFieldIsDecided fails for a field that is in neither list, so a new setting cannot reach the bundle
// by being forgotten.
var configCovered = map[string]string{
	"mu": "hidden", "envRead": "hidden", "BaseURL": "hidden", "Listen": "hidden", "DataDir": "hidden", "PortFile": "hidden",
	"OIDCIssuer": "hidden", "OIDCClientID": "hidden", "OIDCClientSecret": "hidden", "OIDCStub": "shown", "DevAnyListen": "shown",
	"AdminGroup": "hidden", "WriterGroup": "hidden", "ReaderGroup": "hidden",
	"SecretKey": "hidden", "SessionHours": "shown", "SessionIdleMinutes": "shown",
	"TrustProxy": "shown", "RateLimit": "shown", "ShareRateLimit": "shown", "ShareLinkRate": "shown", "ShareCommentsPerHour": "shown",
	"MaxKeysPerUser": "shown", "MaxAltsPerDoc": "shown", "IdempotencyMaxBytes": "shown", "IdempotencyMaxEntries": "shown",
	"LogLevel": "shown", "Version": "shown",
	"MirrorDisabled": "shown", "MirrorDebounceMs": "shown", "BackupIntervalHours": "shown", "BackupKeep": "shown",
	"SSEPerUser": "shown", "SSETotal": "shown", "SSEReplayEvents": "shown", "SSEReplayHours": "shown",
	"UploadMaxMB": "shown", "QuotaUserMB": "shown", "QuotaProjectMB": "shown", "QuotaInstanceMB": "shown", "UploadConcurrency": "shown",
	"ImportStageHours": "shown", "ImportStageMaxOpen": "shown", "ImportStageUserMB": "shown", "ImportStageTotalMB": "shown",
	"GitMaxRepoMB": "shown", "GitAllowPrivate": "shown", "GitUserQuotaMB": "shown", "GitTotalQuotaMB": "shown", "GitBundlesKeep": "shown", "GitKeepImportsDays": "shown",
	"AI":                 "shown",
	"CompileConcurrency": "shown", "CompilePDFMaxWords": "shown", "SiteDisabled": "shown", "SiteDir": "hidden", "SitePublish": "shown",
	"MCP": "shown", "Webhook": "shown",
	"Desktop": "shown", "DesktopExternal": "shown", "LaunchToken": "hidden", "dataDirSet": "hidden", "listenSet": "hidden", "desktopAdminGroupEnv": "hidden",
}

func isLoopbackListen(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// configShape is the configuration as the diagnostics show it.
func configShape(c *config.Config) map[string]any {
	scheme := ""
	if u, err := url.Parse(c.BaseURLString()); err == nil {
		scheme = u.Scheme
	}
	set := func(s string) bool { return strings.TrimSpace(s) != "" }
	m := map[string]any{
		"listen_loopback": isLoopbackListen(c.Listen), "base_url_scheme": scheme, "base_url_set": set(c.BaseURL),
		"data_dir_set": set(c.DataDir), "oidc_issuer_set": set(c.OIDCIssuer), "oidc_client_id_set": set(c.OIDCClientID), "oidc_client_secret_set": set(c.OIDCClientSecret),
		"oidc_stub": c.OIDCStub, "dev_any_listen": c.DevAnyListen, "desktop": c.Desktop, "desktop_external": c.DesktopExternal,
		"admin_group_set": set(c.AdminGroup), "writer_group_set": set(c.WriterGroup), "reader_group_set": set(c.ReaderGroup), "secret_key_set": len(c.SecretKey) > 0,
		"session_hours": c.SessionHours, "session_idle_minutes": c.SessionIdleMinutes, "trust_proxy": c.TrustProxy, "rate_limit": c.RateLimit,
		"share_rate_limit": c.ShareRateLimit, "share_link_rate": c.ShareLinkRate, "share_comments_per_hour": c.ShareCommentsPerHour,
		"max_keys_per_user": c.MaxKeysPerUser, "max_alts_per_doc": c.MaxAltsPerDoc, "idempotency_max_bytes": c.IdempotencyMaxBytes, "idempotency_max_entries": c.IdempotencyMaxEntries,
		"log_level": c.LogLevel, "mirror_enabled": !c.MirrorDisabled, "mirror_debounce_ms": c.MirrorDebounceMs,
		"backup_interval_hours": c.BackupIntervalHours, "backup_keep": c.BackupKeep,
		"sse_per_user": c.SSEPerUser, "sse_total": c.SSETotal, "sse_replay_events": c.SSEReplayEvents, "sse_replay_hours": c.SSEReplayHours,
		"upload_max_mb": c.UploadMaxMB, "quota_user_mb": c.QuotaUserMB, "quota_project_mb": c.QuotaProjectMB, "quota_instance_mb": c.QuotaInstanceMB, "upload_concurrency": c.UploadConcurrency,
		"import_stage_hours": c.ImportStageHours, "import_stage_max_open": c.ImportStageMaxOpen, "import_stage_user_mb": c.ImportStageUserMB, "import_stage_total_mb": c.ImportStageTotalMB,
		"git_max_repo_mb": c.GitMaxRepoMB, "git_allow_private": c.GitAllowPrivate, "git_user_quota_mb": c.GitUserQuotaMB, "git_total_quota_mb": c.GitTotalQuotaMB,
		"git_bundles_keep": c.GitBundlesKeep, "git_keep_imports_days": c.GitKeepImportsDays,
		"ai_off": c.AI.Off, "ai_user_providers": c.AI.UserProviders, "ai_max_request_kb": c.AI.MaxRequestKB, "ai_rate_per_min": c.AI.RatePerMin,
		"ai_default_user_tokens": c.AI.DefaultUserTokens, "ai_default_user_cost_micros": c.AI.DefaultUserCostMicros, "ai_currency": c.AI.Currency,
		"compile_concurrency": c.CompileConcurrency, "compile_pdf_max_words": c.CompilePDFMaxWords, "site_enabled": !c.SiteDisabled, "site_publish": c.SitePublish, "site_dir_set": set(c.SiteDir),
		"mcp_off": c.MCP.Off, "mcp_admin_tools": c.MCP.AdminTools, "mcp_permanent_tools": c.MCP.PermanentTools, "mcp_origins": len(c.MCP.AllowedOrigins), "mcp_max_result_kb": c.MCP.MaxResultKB, "mcp_max_batch": c.MCP.MaxBatch, "mcp_call_timeout_s": c.MCP.CallTimeoutS,
		"webhooks_off": c.Webhook.Off, "webhook_allow_private": c.Webhook.AllowPrivate, "webhook_max_per_project": c.Webhook.MaxPerProject, "webhook_max_total": c.Webhook.MaxTotal, "webhook_rate_per_min": c.Webhook.RatePerMin,
		"webhook_concurrency": c.Webhook.Concurrency, "webhook_timeout_s": c.Webhook.TimeoutS,
	}
	return m
}

// logRing keeps the last warnings and errors of the process, scrubbed as they come in: the route pattern and a short
// message, never a path with a token in it and never what a person typed.
type logRing struct {
	mu    sync.Mutex
	lines []admin.LogLine
	dir   string // the data directory, taken out of any text kept
}

// errClass reduces an error to its kind: "ops:internal", "*fs.PathError", never its text, which holds paths, SQL, addresses
// and names. A value that is not an error is "text".
func errClass(v any) string {
	err, ok := v.(error)
	if !ok {
		return "text"
	}
	var oe *ops.Error
	if errors.As(err, &oe) {
		return "ops:" + string(oe.Code)
	}
	var parts []string
	for i := 0; err != nil && i < 3; i++ {
		parts = append(parts, fmt.Sprintf("%T", err))
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " > ")
}

const ringSize = 200

// ringAttrs are the only attributes a kept line carries.
var ringAttrs = map[string]bool{"route": true, "method": true, "status": true, "dur": true, "req": true, "op": true, "component": true, "err": true}

func (r *logRing) add(rec slog.Record) {
	l := admin.LogLine{At: rec.Time.UTC().Format(time.RFC3339), Level: rec.Level.String(), Msg: admin.ScrubLine(rec.Message, 200, r.dir), Attrs: map[string]string{}}
	rec.Attrs(func(a slog.Attr) bool {
		switch {
		case a.Key == "err":
			l.Attrs[a.Key] = errClass(a.Value.Any())
		case a.Key == "route":
			l.Attrs[a.Key] = admin.ScrubLine(a.Value.String(), 200)
		case ringAttrs[a.Key]:
			l.Attrs[a.Key] = admin.ScrubLine(a.Value.String(), 200, r.dir)
		}
		return true
	})
	r.mu.Lock()
	r.lines = append(r.lines, l)
	if len(r.lines) > ringSize {
		r.lines = append([]admin.LogLine(nil), r.lines[len(r.lines)-ringSize:]...)
	}
	r.mu.Unlock()
}

func (r *logRing) snapshot() []admin.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]admin.LogLine(nil), r.lines...)
}

// ringHandler passes every record on and keeps the warnings and errors.
type ringHandler struct {
	inner slog.Handler
	ring  *logRing
}

func (h ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn || h.inner.Enabled(ctx, l)
}
func (h ringHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.ring.add(r)
	}
	if h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}
func (h ringHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return ringHandler{inner: h.inner.WithAttrs(a), ring: h.ring}
}
func (h ringHandler) WithGroup(n string) slog.Handler {
	return ringHandler{inner: h.inner.WithGroup(n), ring: h.ring}
}
