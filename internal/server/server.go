// Package server is the HTTP layer: routes, middleware, page handlers and the
// REST binding that the op registry generates. Handlers never write SQL; every
// mutation goes through ops.
package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/admin"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/domain/share"
	sitedomain "github.com/crueber/scriobh/internal/domain/site"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/idempotency"
	"github.com/crueber/scriobh/internal/mail"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/webhook"
	"github.com/crueber/scriobh/web"
)

// Deps are what a Server needs.
type Deps struct {
	Cfg    *config.Config
	DB     *db.DB
	Log    *slog.Logger
	Events *events.Store
	// Templates and Static default to the embedded ones; tests override them.
	Templates fs.FS
	Static    fs.FS
}

// Server wires everything together.
type Server struct {
	Cfg    *config.Config
	DB     *db.DB
	Log    *slog.Logger
	Events *events.Store
	// Hub fans committed events out to live streams (GET /api/v1/events).
	Hub   *events.Hub
	Reg   *ops.Registry
	Sess  *auth.Sessions
	Keys  *auth.Keys
	Idem  *idempotency.Store
	Users *auth.Users
	OIDC  *auth.OIDC
	// Mirror writes the Markdown projection; Backups takes backups. Both are
	// started by Serve, not by New, so a server built only for its handler
	// leaves no goroutine behind.
	Mirror  *mirror.Mirror
	Backups *backup.Service
	// Git pushes the mirror to remotes and imports repositories; also started
	// by Serve.
	Git *gitmirror.Service
	// Webhooks is the outbox and its workers; started by Serve like the mirror.
	Webhooks *webhook.Service
	// Mail is the mail outbox, its dispatcher and the digest scheduler; started by Serve.
	Mail *mail.Service

	flash   flashStore
	plans   planStore // reviewed publish plans, against the session (planstore.go)
	sse     *sseState
	sseTick func(d time.Duration) (<-chan time.Time, func()) // the heartbeat clock; nil means real time
	// presTick is the clock of a stream's presence checks (presence.go); nil means real time.
	presTick              func(d time.Duration) (<-chan time.Time, func())
	assets                *assets
	pwa                   *pwaState
	tmpl                  map[string]*template.Template
	limiter               *limiter
	mcp                   *mcpState     // the MCP endpoint (mcp.go); nil when it is off
	shr                   shareState    // the public share routes (share.go)
	cards                 *studio.Cards // the private progress cards' public side (card.go)
	ring                  *logRing      // the warnings and errors kept for the diagnostics (adminsvc.go)
	bigBody               []*ops.Entry  // ops with their own request body limit (Op.MaxBody)
	bodySlots             chan struct{} // the pictures' bodies read at once (uiRouteStream)
	bodyUsers             bodyOwner     // and who has one being read: one each
	bodyIdle, bodyCeiling time.Duration // the pace a picture's body must keep (pacedBody)
	bodyGrace             time.Duration // ... waiting before the floor applies
	bodyFloor             int64         // ... bytes a second, once it does
	schema                ops.Schema    // the op schema, built once after the ops are bound
	mux                   *http.ServeMux
	handler               http.Handler
	desk                  *desktopRT // desktop mode (desktop.go); nil when SCRIOBH_DESKTOP is unset
}

// httpServer is the http.Server the process runs, with its timeouts. The cover form's route moves its own read deadline
// with the bytes it gets (uiRouteStream), in place of ReadTimeout.
func (s *Server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.Log.Handler(), slog.LevelWarn),
	}
}

// New builds a Server. It does not listen.
func New(d Deps) (*Server, error) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Events == nil {
		d.Events = events.NewStore(d.DB, d.Log)
	}
	if d.Cfg.OIDCStub && !devIdPAvailable {
		return nil, errors.New("SCRIOBH_OIDC_STUB is set but this is a production build without the development issuer")
	}
	// A restore that did not finish its pauses (it was interrupted, or a step failed) leaves a marker. Nothing is
	// started until every pause has been made: a copy that sends is the one thing a restore must never leave behind.
	// This is here, before any service exists, so that every way of starting a server is covered.
	if d.Cfg.DataDir != "" {
		if pending, notes, err := backup.FinishPending(context.Background(), d.DB, d.Cfg.DataDir); pending {
			if err != nil {
				return nil, fmt.Errorf("refusing to start: a restore did not finish its pauses.\n%w\nFix the cause and run `scriobh restore --finish`, or start again", err)
			}
			d.Log.Warn("a restore had not finished its pauses; they were made now, before anything started. Senders stay off until their owners switch them on", "notes", notes)
		}
	}
	key, err := d.Cfg.EnsureSecretKey()
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	tfs, sfs := d.Templates, d.Static
	if tfs == nil {
		if tfs, err = fs.Sub(web.FS, "templates"); err != nil {
			return nil, err
		}
	}
	if sfs == nil {
		if sfs, err = fs.Sub(web.FS, "static"); err != nil {
			return nil, err
		}
	}
	ring := &logRing{dir: d.Cfg.DataDir}
	d.Log = slog.New(ringHandler{inner: d.Log.Handler(), ring: ring})
	s := &Server{
		bodySlots: make(chan struct{}, bodySlotCount), bodyIdle: bodyIdle, bodyCeiling: bodyCeiling, bodyGrace: bodyGrace, bodyFloor: bodyFloor,
		Cfg: d.Cfg, DB: d.DB, Log: d.Log, Events: d.Events,
		Sess:    &auth.Sessions{DB: d.DB, Cfg: d.Cfg, Key: key},
		Keys:    &auth.Keys{DB: d.DB},
		Idem:    idempotency.New(d.DB),
		Users:   &auth.Users{DB: d.DB, Cfg: d.Cfg},
		OIDC:    auth.NewOIDC(d.Cfg, key),
		limiter: newLimiter(),
		mux:     http.NewServeMux(),
		sse:     newSSEState(),
		ring:    ring,
	}
	s.Hub = events.NewHub(d.Events, events.HubOptions{})
	s.OIDC.Log = d.Log
	s.shr = shareState{meter: share.NewMeter(d.DB), downloads: share.NewDownloads(), key: share.UnlockKey(key)}
	s.Reg = registry.BuildWith(d.DB, d.Events, d.Cfg.DataDir, registry.Options{Log: d.Log, Attachments: attachments.Limits{
		MaxBytes: int64(d.Cfg.UploadMaxMB) << 20, UserBytes: int64(d.Cfg.QuotaUserMB) << 20,
		OwnerBytes: int64(d.Cfg.QuotaProjectMB) << 20, InstanceBytes: int64(d.Cfg.QuotaInstanceMB) << 20, MaxInFlight: d.Cfg.UploadConcurrency},
		AI: aiOptions(d.Cfg, secrets.New(key), d.Log), Webhook: webhookLimits(d.Cfg), Pandoc: pandocOptions(d.Cfg, d.Log)})
	s.Mirror = mirror.New(mirror.Options{DataDir: d.Cfg.DataDir, DB: d.DB, Log: d.Log, Notifier: d.Events, Disabled: d.Cfg.MirrorDisabled,
		Debounce: time.Duration(d.Cfg.MirrorDebounceMs) * time.Millisecond})
	s.Backups = &backup.Service{DB: d.DB, DataDir: d.Cfg.DataDir, Version: d.Cfg.Version, Log: d.Log}
	env := s.Reg.Env()
	s.Idem.MaxBytes, s.Idem.MaxEntries = d.Cfg.IdempotencyMaxBytes, d.Cfg.IdempotencyMaxEntries
	env.SetService(account.TestLocalesService, s.testLocales())
	env.SetService(keys.LimitsService, keys.Limits{MaxKeysPerUser: d.Cfg.MaxKeysPerUser})
	env.SetService(admin.ServiceName, &admin.Service{Limits: attachments.Limits{MaxBytes: int64(d.Cfg.UploadMaxMB) << 20, UserBytes: int64(d.Cfg.QuotaUserMB) << 20,
		OwnerBytes: int64(d.Cfg.QuotaProjectMB) << 20, InstanceBytes: int64(d.Cfg.QuotaInstanceMB) << 20, MaxInFlight: d.Cfg.UploadConcurrency},
		MaxKeysPerUser: d.Cfg.MaxKeysPerUser, AdminFromIdP: d.Cfg.AdminGroup != "", Version: d.Cfg.Version, DataDir: d.Cfg.DataDir,
		Config: func() map[string]any { return configShape(d.Cfg) }, Log: ring.snapshot})
	env.SetService(share.MeterService, s.shr.meter)
	s.cards = studio.NewCards(d.DB)
	env.SetService(studio.CardsService, s.cards)
	// Only the per-document figure is configurable; the others keep their defaults (a zero limit would refuse every wording).
	lim := alts.DefaultLimits
	if d.Cfg.MaxAltsPerDoc > 0 {
		lim.PerDoc = d.Cfg.MaxAltsPerDoc
	}
	env.SetService(altstore.ServiceLimits, lim)
	cgate := compile.NewGate(d.Cfg.CompileConcurrency, d.Cfg.CompilePDFMaxWords)
	env.SetService(compile.LimitsService, cgate)
	env.SetService(mirror.ServiceName, s.Mirror)
	env.SetService(sitedomain.LimitsService, sitedomain.NewGateOn(cgate.Slots()))
	env.SetService(coverlayout.GateService, coverlayout.NewGateOn(cgate.Slots())) // a cover's pictures are made inside the same limit
	env.SetService(sitedomain.ServiceName, sitedomain.NewService(sitedomain.Config{Enabled: !d.Cfg.SiteDisabled, Root: d.Cfg.SiteRoot()}))
	env.SetService(backup.ServiceName, s.Backups)
	env.AddPurgeHook(s.Mirror.ProjectPurged)
	env.AddCompendiumPurgeHook(s.Mirror.CompendiumPurged)
	s.Git = gitmirror.New(gitmirror.Options{DataDir: d.Cfg.DataDir, DB: d.DB, Source: s.Mirror, Box: secrets.New(key), Log: d.Log,
		MaxRepoBytes: int64(d.Cfg.GitMaxRepoMB) << 20, AllowPrivate: d.Cfg.GitAllowPrivate,
		UserQuotaBytes: quotaBytes(d.Cfg.GitUserQuotaMB), TotalQuotaBytes: quotaBytes(d.Cfg.GitTotalQuotaMB), BundlesKeep: d.Cfg.GitBundlesKeep, KeepImportDays: keepDays(d.Cfg.GitKeepImportsDays), Compile: s.gitCompiler()})
	s.Webhooks = webhook.New(webhook.Options{DB: d.DB, Box: secrets.New(key), Events: d.Events, Log: d.Log, Version: d.Cfg.Version, Limits: webhookLimits(d.Cfg)})
	env.SetService(webhook.ServiceName, s.Webhooks)
	s.Mail = mail.New(mail.Options{DB: d.DB, Box: secrets.New(key), Log: d.Log, DataDir: d.Cfg.DataDir, BaseURL: d.Cfg.BaseURLString, Compile: s.mailCompiler()})
	env.SetService(mail.ServiceName, s.Mail)
	s.Mirror.OnWrite(s.Git.Notify)
	env.SetService(gitmirror.ServiceName, s.Git)
	env.AddPurgeHook(s.Git.ProjectPurged)
	if ai, ok := aicore.ServiceOf(env); ok {
		ai.CloseInterrupted(context.Background(), d.DB) // runs a stopped server left running no longer hold budget
	}
	if d.Cfg.Desktop {
		if err := s.initDesktop(key); err != nil {
			return nil, fmt.Errorf("desktop mode: %w", err)
		}
	}
	if s.assets, err = newAssets(sfs); err != nil {
		return nil, err
	}
	if s.tmpl, err = s.parseTemplates(tfs); err != nil {
		return nil, err
	}
	if err := s.initPWA(); err != nil {
		return nil, err
	}
	s.routes()
	if err := s.mountMCP(); err != nil {
		return nil, err
	}
	s.handler = s.chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Who is asking, and from which address bucket, for every function that gets only a context: the
		// gate-aware role function and the passphrase lockout (F-252).
		r = r.WithContext(s.withCaller(r))
		if err := s.pageGate(r); err != nil {
			s.failLocked(w, r, err)
			return
		}
		if s.adminOutsider(r) { // an administrator is a stranger to what was not shared with them, on the pages
			s.fail(w, r, ops.NotFound("project"))
			return
		}
		s.mux.ServeHTTP(w, r)
		if ri, ok := r.Context().Value(keyInfo).(*reqInfo); ok {
			ri.route = r.Pattern
		}
	}))
	return s, nil
}

// Handler is the full handler, middleware included.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() {
	m := s.mux
	m.Handle("GET /healthz", s.desktopHealthz(http.HandlerFunc(s.healthz)))
	m.HandleFunc("GET /readyz", s.readyz)
	m.Handle("GET /static/", s.assets)
	m.HandleFunc("GET /auth/login", s.login)
	m.HandleFunc("GET /auth/oidc/callback", s.callback)
	m.HandleFunc("POST /auth/logout", s.logout)
	m.HandleFunc("GET /auth/claim-admin", s.claimPage)
	m.HandleFunc("POST /auth/claim-admin", s.claimSubmit)
	s.mountDevIdP() // build-tagged: a no-op in production builds
	s.registerDesktop(m)

	m.HandleFunc("GET /api/v1/events", s.eventsStream)
	m.HandleFunc("GET /files/{id}", s.serveFile)

	m.HandleFunc("GET /{$}", s.library)
	m.HandleFunc("GET /p/{slug}", s.writeProject)
	m.HandleFunc("GET /p/{slug}/d/{docid}", s.writeDoc)
	m.HandleFunc("GET /p/{slug}/search", s.searchProject)
	m.HandleFunc("GET /p/{slug}/goals", s.goalsPage)
	s.uiRoute("POST /ui/projects/{id}/goals", s.uiGoals)
	m.HandleFunc("GET /p/{slug}/outline", s.outlinePage)
	m.HandleFunc("GET /ui/projects/{id}/outline", s.uiOutline)
	m.HandleFunc("GET /search", s.searchAll)
	s.registerImportMirrorBackup(m)
	s.registerGit(m)
	s.registerAI(m)
	s.registerAIUI(m)
	s.registerCompendiums(m)
	s.registerAttachments(m)
	s.registerCoverUI(m)
	s.registerTimeline(m)
	s.registerImportRepo(m)
	s.registerBoards(m)
	s.registerEntities(m)
	s.registerResearch(m)
	s.registerShare(m)
	s.registerCard(m)
	s.registerStudioUI(m)
	s.registerReviewUI(m)
	s.registerShareAdmin(m)
	s.registerGate(m)
	s.registerPassphrase(m)
	s.registerSemantic(m)
	s.registerAdminUI(m)
	s.registerCompileUI(m)
	s.registerMailUI(m)
	s.registerPandocUI(m)
	s.registerPublishUI(m)
	s.registerEditionsUI(m)
	s.registerEditionDiffUI(m)
	s.registerMCPUI(m)
	s.registerSiteUI(m)
	s.registerWebhookUI(m)
	m.HandleFunc("GET /ui/projects/{id}/export", s.uiExportForm)
	s.uiRoute("POST /ui/projects/{id}/export", s.uiExport)
	s.uiRoute("POST /ui/projects", s.uiProjectCreate)
	m.HandleFunc("GET /ui/projects/new", s.uiProjectNew)
	m.HandleFunc("GET /ui/projects/{id}/tree", s.uiTree)
	m.HandleFunc("GET /ui/projects/{id}/menu", s.uiProjectMenu)
	m.HandleFunc("GET /ui/projects/{id}/purge", s.uiProjectPurgeForm)
	s.uiRoute("POST /ui/projects/{id}/pin", s.uiProjectPin)
	s.uiRoute("POST /ui/projects/{id}/archive", s.uiProjectMove("project.archive", "Archived", "unarchive"))
	s.uiRoute("POST /ui/projects/{id}/unarchive", s.uiProjectMove("project.unarchive", "Brought back", ""))
	s.uiRoute("POST /ui/projects/{id}/trash", s.uiProjectMove("project.trash", "Moved to the trash:", "restore"))
	s.uiRoute("POST /ui/projects/{id}/restore", s.uiProjectMove("project.restore", "Restored", ""))
	s.uiRoute("POST /ui/projects/{id}/purge", s.uiProjectPurge)
	m.HandleFunc("GET /ui/csrf", s.uiCSRF)
	m.HandleFunc("GET /settings", s.settingsPage)
	s.uiRoute("POST /ui/settings/prefs", s.uiSettingsPrefs)
	s.uiRoute("POST /ui/settings/profile", s.uiSettingsProfile)
	s.uiRoute("POST /ui/sessions/{sid}/revoke", s.uiSessionRevoke)
	s.uiRoute("POST /ui/sessions/revoke-others", s.uiSessionsRevokeOthers)
	s.uiRoute("POST /ui/keys", s.uiKeyCreate)
	s.uiRoute("POST /ui/keys/{id}/revoke", s.uiKeyRevoke)
	s.uiRoute("POST /ui/projects/{id}/docs", s.uiDocCreate)
	s.uiRoute("POST /ui/docs/{id}/move", s.uiDocMove)
	s.uiRoute("POST /ui/docs/{id}/trash", s.uiDocTrash)
	s.uiRoute("POST /ui/docs/{id}/restore", s.uiDocRestore)
	s.uiRoute("POST /ui/docs/{id}/rename", s.uiDocRename)

	s.pwaRoutes()
	s.bindOps()
	m.HandleFunc("/", s.notFound)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.DB.Read().QueryRowContext(r.Context(), "SELECT 1").Err(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"unavailable"}`)
		return
	}
	fmt.Fprintf(w, `{"status":"ok","version":%q}`, s.Cfg.Version)
}

// readyz says whether the instance can do real work: the database answers and
// every migration is applied. A load balancer or wrapper waits on this, not
// on /healthz (which only says the process is alive).
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	all, err := db.Migrations()
	var n int
	if err == nil {
		err = s.DB.Read().QueryRowContext(r.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&n)
	}
	if err != nil || n != len(all) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"not ready"}`)
		return
	}
	fmt.Fprint(w, `{"status":"ready"}`)
}

// Serve runs until ctx is cancelled, then shuts down gracefully. It prints
// the listening URL (for wrappers and tests that use an ephemeral port) and
// optionally writes the port file.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.Cfg.SetBoundAddr(ln.Addr())
	if err := config.ValidateStubBase(s.Cfg); err != nil {
		return err
	}
	if s.Cfg.PortFile != "" {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		tmp := s.Cfg.PortFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(port+"\n"), 0o644); err != nil {
			return fmt.Errorf("port file: %w", err)
		}
		if err := os.Rename(tmp, s.Cfg.PortFile); err != nil {
			return fmt.Errorf("port file: %w", err)
		}
	}
	srv := s.httpServer()
	go s.sweepLoop(ctx)
	defer s.observeOutbound()()
	attachments.SweepTemp(s.Cfg.DataDir) // uploads that were in flight when the last process stopped
	// Not derived from ctx: the mirror keeps working while the last requests
	// finish, and is stopped (and drained) only after the HTTP server has.
	bg, stopBackground := context.WithCancel(context.Background())
	s.Mirror.Start(bg)
	s.Git.Start(bg)
	s.Webhooks.Start(bg)
	sem, hasSem := semantic.ServiceOf(s.Reg.Env())
	if hasSem {
		sem.Start(bg)
	}
	s.Mail.Start(bg)
	sched := &backup.Scheduler{Svc: s.Backups, Interval: time.Duration(s.Cfg.BackupIntervalHours) * time.Hour, Keep: s.Cfg.BackupKeep, Log: s.Log}
	go sched.Run(bg)
	defer func() {
		// main closes the database next: let the mirror finish what is pending.
		stopBackground()
		s.Git.Wait()
		s.Webhooks.Wait()
		if hasSem {
			sem.Wait()
		}
		s.Mail.Wait()
		s.Mirror.Wait()
		_ = s.shr.meter.Flush(context.Background()) // the share links' use counts, before the database closes
		_ = s.cards.Flush(context.Background())     // and the progress cards' views
		s.gateService().Flush(context.Background()) // the keys' use of protected projects
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Printf("LISTEN %s\n", boundURL(ln.Addr()))
	s.Log.Info("listening", "addr", ln.Addr().String(), "base_url", s.Cfg.BaseURLString(), "stub_idp", s.Cfg.OIDCStub)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	imports.StopPDFChildren() // a PDF being read is not worth waiting for
	s.StopStreams()           // Shutdown waits for open requests; a live stream never ends by itself
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close() // connections that outlive the grace period are cut
		return err
	}
	return nil
}

// webhookLimits turns the configuration into the webhook service's settings.
func webhookLimits(c *config.Config) webhook.Limits {
	w := c.Webhook
	debounce := time.Duration(w.DebounceMs) * time.Millisecond
	if w.DebounceMs == 0 {
		debounce = -1 // the service reads zero as "the default"; the setting's zero is "none"
	}
	return webhook.Limits{
		Off: w.Off, AllowPrivate: w.AllowPrivate, MaxPerProject: w.MaxPerProject, MaxTotal: w.MaxTotal, RatePerMin: w.RatePerMin, ProjectRatePerMin: w.ProjectRatePerMin, ProjectQueueMax: w.ProjectQueueMax,
		QueueMax: w.QueueMax, PerWebhookQueueMax: w.PerWebhookQueueMax, Concurrency: w.Concurrency, MaxAttempts: w.MaxAttempts,
		KeepDays: w.KeepDays, Timeout: time.Duration(w.TimeoutS) * time.Second, DocDebounce: debounce,
	}
}

func (s *Server) sweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.sweepAI(ctx) // what the AI recipes held, from before the last stop
	s.sweepEditions(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sess.SweepExpired(ctx); err != nil {
				s.Log.Warn("session sweep", "err", err)
			}
			if n, err := s.Idem.Sweep(ctx); err != nil {
				s.Log.Warn("idempotency sweep", "err", err)
			} else if n > 0 {
				s.Log.Info("idempotency sweep", "removed", n)
			}
			s.sweepAI(ctx)
			s.sweepEditions(ctx)
			if n, err := review.Sweep(ctx, s.DB); err != nil {
				s.Log.Warn("notification sweep", "err", err)
			} else if n > 0 {
				s.Log.Info("notification sweep", "removed", n)
			}
			_ = s.shr.meter.Flush(ctx)
			_ = s.cards.Flush(ctx)
			if n, err := s.cards.Sweep(ctx); err != nil {
				s.Log.Warn("progress card sweep", "err", err)
			} else if n > 0 {
				s.Log.Info("progress card sweep", "removed", n)
			}
			if n, err := events.Sweep(ctx, s.DB, events.Keep, time.Now()); err != nil {
				s.Log.Warn("event sweep", "err", err)
			} else if n > 0 {
				s.Log.Info("event sweep", "removed", n)
			}
			if res, err := revisions.Sweep(ctx, s.DB); err != nil {
				s.Log.Warn("revision sweep", "err", err)
			} else if res.Removed > 0 {
				s.Log.Info("revision sweep", "removed", res.Removed, "bytes", res.Bytes)
			}
		}
	}
}

// sweepEditions removes the source files of imported books that no edition names: what a crash between placing a file
// and committing its row left behind (see editions.SweepOrphans).
func (s *Server) sweepEditions(ctx context.Context) {
	if n, err := editions.SweepOrphans(ctx, s.DB, s.Cfg.DataDir, time.Now()); err != nil {
		s.Log.Warn("edition file sweep", "err", err)
	} else if n > 0 {
		s.Log.Info("edition file sweep", "removed", n)
	}
}

// sweepAI removes held suggestions that have expired and the rest of what the AI
// recipes keep past its days (see aiwork.Sweep).
func (s *Server) sweepAI(ctx context.Context) {
	if n, err := aiwork.Sweep(ctx, s.DB); err != nil {
		s.Log.Warn("ai sweep", "err", err)
	} else if n > 0 {
		s.Log.Info("ai sweep", "removed", n)
	}
}

// boundURL is the URL a local client can use to reach the listener.
func boundURL(a net.Addr) string {
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return "http://" + a.String()
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	} else if ip.To4() == nil {
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port
}

// testLocales reports whether the pseudo-locales (xx, xx-rtl) may be chosen: with the development issuer, and in a
// development build running in desktop mode (so the desktop pages can be checked in them). Never in a production build.
func (s *Server) testLocales() bool {
	return s.Cfg.OIDCStub || (devIdPAvailable && s.Cfg.Desktop)
}

// DevIdPAvailable reports whether this build contains the development issuer.
const DevIdPAvailable = devIdPAvailable

// Port returns the numeric port of a listener address, for tests.
func Port(a net.Addr) int {
	_, p, _ := net.SplitHostPort(a.String())
	n, _ := strconv.Atoi(p)
	return n
}

// quotaBytes turns a quota in MB, where 0 means none, into the form
// gitmirror.Options takes (a negative number means none).
func quotaBytes(mb int) int64 {
	if mb <= 0 {
		return -1
	}
	return int64(mb) << 20
}

// keepDays turns the days a kept clone lives, where 0 means forever, into the
// form gitmirror.Options takes (a negative number means forever).
func keepDays(d int) int {
	if d <= 0 {
		return -1
	}
	return d
}
