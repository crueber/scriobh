// Command scriobh is the server and the full CLI.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// When this program is started as the resource-limit helper for Pandoc, it sets the limits and
	// becomes Pandoc; it never returns here (see internal/pandoc).
	if pandoc.RunHelperIfRequested() {
		return
	}

	// The PDF reader runs as this same program started again with one argument (internal/imports/pdfchild.go):
	// that must be the first thing a process does, before any configuration or database.
	imports.MaybeRunPDFChild()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `scriobh: a quiet room for the long thing you are writing

usage: scriobh [command]

  serve          run the server (default)
  migrate        apply database migrations and exit
  healthcheck    exit 0 when the local server answers /healthz
  mentions       rebuild the mention index (stop the server first)
  passphrase     remove a project's forgotten passphrase (stop the server first)
  search         rebuild the search index (stop the server first)
  import         preview or create a project from a folder of Markdown
  stats          recompute writing totals
  bench          generate large projects and measure the performance budgets
  backup         write a consistent archive of the data directory
  restore        restore an archive into the data directory (server stopped)
  desktop        use-local-profile: take a desktop instance back to its local profile (stop the app first)
  config check   validate the environment and print what is missing
  ops            print the op schema as JSON
  version        print the version
`

func run(args []string, stdout, stderr io.Writer) int {
	// A flag in the command's place asks for help or the version; it must never
	// start the server (the default command) by accident.
	if len(args) > 0 {
		switch args[0] {
		case "-h", "-help", "--help":
			args = []string{"help"}
		case "-v", "-version", "--version":
			args = []string{"version"}
		}
	}
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(stdout, stderr)
	case "migrate":
		return migrate(stdout, stderr)
	case "healthcheck":
		return healthcheck(stderr)
	case "search":
		return searchCmd(args, stdout, stderr)
	case "mentions":
		return mentionsCmd(args, stdout, stderr)
	case "passphrase":
		return passphraseCmd(args, stdout, stderr)
	case "import":
		return importCmd(args, stdout, stderr)
	case "stats":
		return statsCmd(args, stdout, stderr)
	case "bench":
		return benchCmd(args, stdout, stderr)
	case "backup":
		return backupCmd(args, stdout, stderr)
	case "restore":
		return restoreCmd(args, stdout, stderr)
	case "desktop":
		return desktopCmd(args, stdout, stderr)
	case "config":
		if len(args) == 1 && args[0] == "check" {
			return configCheck(stdout, stderr)
		}
	case "ops":
		reg := registry.Build(nil, nil, "")
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(reg.Schema())
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func loadConfig(stderr io.Writer) (*config.Config, bool) {
	cfg, err := config.Load(nil)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return nil, false
	}
	cfg.Version = version
	return cfg, true
}

func logger(cfg *config.Config, w io.Writer) *slog.Logger {
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}

func openDB(cfg *config.Config, log *slog.Logger) (*db.DB, error) {
	d, err := db.Open(filepath.Join(cfg.DataDir, "scriobh.db"))
	if err != nil {
		return nil, err
	}
	res, err := d.Migrate(context.Background(), filepath.Join(cfg.DataDir, "backups", "migrate"))
	if err != nil {
		d.Close()
		return nil, err
	}
	if len(res.Applied) > 0 {
		log.Info("migrations applied", "versions", res.Applied, "backup", res.Backup)
	}
	return d, nil
}

func serve(stdout, stderr io.Writer) int {
	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "scriobh: configuration problems:\n"+err.Error())
		return 1
	}
	if cfg.OIDCStub && !server.DevIdPAvailable {
		fmt.Fprintln(stderr, "scriobh: SCRIOBH_OIDC_STUB is set but this is a production build without the development issuer")
		return 1
	}
	if cfg.Desktop {
		// A shell killed outright leaves no reader on stdout and stderr; the next log line must not end the process.
		surviveClosedOutput()
	}
	log := logger(cfg, stderr)
	for _, w := range cfg.UnknownEnv(os.Environ()) {
		log.Warn("unknown environment variable ignored", "name", w.Name, "hint", w.Hint)
	}
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		var locked *db.LockedError
		if cfg.Desktop && errors.As(err, &locked) {
			// The desktop shell reads this line and the status, and brings the first window forward.
			fmt.Fprintln(stdout, alreadyRunning)
			return exitAlreadyRunning
		}
		return 1
	}
	defer release()
	d, err := openDB(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	listen := cfg.Listen
	if cfg.Desktop {
		if listen, err = desktopPrepare(cfg, d, log); err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
	}
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d, Log: log})
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	if cfg.Desktop {
		// The token has been turned into the launch gate; nothing else needs it, and no child process is to inherit it.
		cfg.LaunchToken = ""
		_ = os.Unsetenv("SCRIOBH_LAUNCH_TOKEN")
		_ = os.Unsetenv("SCRIOBH_SECRET_KEY")
	}
	var ln net.Listener
	if cfg.Desktop {
		ln, err = desktopListen(cfg, listen, log)
	} else {
		ln, err = net.Listen("tcp", listen)
	}
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Track background goroutines so they finish before d.Close() and db.Lock release.
	// Defers run LIFO: cancel() -> bgWG.Wait() -> d.Close() -> release().
	var bgWG sync.WaitGroup
	defer bgWG.Wait()
	// The stdin watcher gets its own cancel: NotifyContext's stop also unregisters the signal handlers, and a SIGTERM
	// that arrives during the graceful shutdown (the shell's quit path sends both) would then kill the process.
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()
	if cfg.Desktop {
		stopWhenStdinCloses(os.Stdin, cancel, log)
	}
	bgWG.Add(2)
	go func() { defer bgWG.Done(); rebuildLinksOnce(ctx, d, log) }()
	go func() { defer bgWG.Done(); mentions.Run(ctx, d, log) }()
	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	log.Info("stopped")
	return 0
}

func migrate(stdout, stderr io.Writer) int {
	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	log := logger(cfg, stderr)
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer release()
	d, err := openDB(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	d.Close()
	fmt.Fprintln(stdout, "database is up to date")
	return 0
}

// healthcheck is what the container's HEALTHCHECK runs: the distroless image
// has no shell or curl.
func healthcheck(stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || port == "0" {
		fmt.Fprintln(stderr, "scriobh: healthcheck needs a fixed SCRIOBH_LISTEN port")
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintln(stderr, "scriobh: healthz returned", resp.Status)
		return 1
	}
	return 0
}

func configCheck(stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stdout, "listen        %s\nbase url      %s\ndata dir      %s\nidentity      %s\n", cfg.Listen, cfg.BaseURLString(), cfg.DataDir, identityLine(cfg))
	// Before the problems: a mistyped name is often why a setting is "missing".
	if unknown := cfg.UnknownEnv(os.Environ()); len(unknown) > 0 {
		fmt.Fprintln(stdout)
		for _, w := range unknown {
			fmt.Fprintln(stdout, "warning:", w.Message())
		}
		fmt.Fprintln(stdout)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "\nproblems:\n"+err.Error())
		return 1
	}
	if cfg.OIDCStub && !server.DevIdPAvailable {
		fmt.Fprintln(stderr, "\nproblems:\nSCRIOBH_OIDC_STUB is set but this is a production build without the development issuer")
		return 1
	}
	private := "refused (loopback, private, link-local and cloud-metadata addresses; set SCRIOBH_GIT_ALLOW_PRIVATE=1 for a Git server on your own network)"
	if cfg.GitAllowPrivate {
		private = "allowed (SCRIOBH_GIT_ALLOW_PRIVATE)"
	}
	fmt.Fprintf(stdout, "git private   %s\ngit repo cap  %d MB per clone\n", private, cfg.GitMaxRepoMB)
	fmt.Fprintln(stdout, "\nconfiguration is valid")
	return 0
}

func identityLine(c *config.Config) string {
	if c.Desktop {
		return "the embedded issuer and one local profile (SCRIOBH_DESKTOP), unless the administrator switched to an external provider in the settings"
	}
	if c.OIDCStub {
		return "development issuer (SCRIOBH_OIDC_STUB)"
	}
	if c.OIDCIssuer == "" {
		return "not configured (SCRIOBH_OIDC_ISSUER is empty and SCRIOBH_OIDC_STUB is off)"
	}
	return c.OIDCIssuer
}

// rebuildLinksOnce keeps the wiki-link index honest across upgrades. It
// rebuilds the whole index when the stored index version is older than
// links.IndexVersion (documents written before the index existed, or a change
// to how titles are folded, which changes the stored keys), and rebuilds just
// the projects named by "links.pending.*" markers (an import or a rebuild that
// was cut short). It runs in the background, records what it finished so later
// starts skip it, and logs and retries at the next start on failure; the ops
// keep the index current from then on.
func rebuildLinksOnce(ctx context.Context, d *db.DB, log *slog.Logger) {
	var done string
	_ = d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, links.IndexKey).Scan(&done)
	pending := map[string]string{} // marker key -> project id
	if rows, err := d.Read().QueryContext(ctx, `SELECT key, value FROM settings WHERE key LIKE ?`, links.PendingPrefix+"%"); err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				pending[k] = v
			}
		}
		rows.Close()
	}
	drop := func(keys ...string) {
		_ = d.WithTx(ctx, func(tx *sql.Tx) error {
			for _, k := range keys {
				if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, k); err != nil {
					return err
				}
			}
			return nil
		})
	}
	fail := func(err error) {
		if ctx.Err() == nil {
			log.Warn("links: rebuild failed, will retry at the next start", "err", err)
		}
	}
	if done != links.IndexVersion {
		n, err := links.Rebuild(ctx, d, "")
		if err != nil {
			fail(err)
			return
		}
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				links.IndexKey, links.IndexVersion, db.Now())
			return err
		}); err != nil {
			log.Warn("links: could not record the rebuild", "err", err)
			return
		}
		for k := range pending {
			drop(k) // the full rebuild covered them
		}
		log.Info("links: index built for existing documents", "documents", n)
		return
	}
	for k, project := range pending {
		n, err := links.Rebuild(ctx, d, project)
		if err != nil {
			fail(err)
			return
		}
		drop(k)
		log.Info("links: index repaired after an interrupted import", "project", project, "documents", n)
	}
}
