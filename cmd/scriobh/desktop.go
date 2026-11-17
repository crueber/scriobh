package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/desktop"
	"github.com/crueber/scriobh/internal/secrets"
)

// What a second desktop instance says and exits with (EX_TEMPFAIL): the shell reads both.
const (
	alreadyRunning     = "ALREADY_RUNNING"
	exitAlreadyRunning = 75
)

// desktopPrepare is what desktop mode does between opening the database and building the server: it makes a switch
// of identity provider that was asked for before this start, and, when the instance signs in through an external
// provider, puts that provider into the configuration. It returns the address to listen on: the configured one, or
// the provider's fixed loopback port when its settings name one and the configured port is "any".
func desktopPrepare(cfg *config.Config, d *db.DB, log *slog.Logger) (string, error) {
	key, err := cfg.EnsureSecretKey()
	if err != nil {
		return "", fmt.Errorf("secret key: %w", err)
	}
	box := secrets.New(key)
	st, err := desktop.ApplyPending(context.Background(), d, box, log)
	if err != nil {
		return "", fmt.Errorf("desktop: reading the identity settings: %w", err)
	}
	if st.Mode != desktop.ModeExternal {
		return cfg.Listen, nil
	}
	x, err := desktop.ExternalConfig(st, box)
	if err != nil {
		return "", fmt.Errorf("desktop: this instance signs in through an external provider, but %v. To go back to the local profile, run: scriobh desktop use-local-profile", err)
	}
	if err := cfg.ApplyDesktopExternal(x); err != nil {
		return "", err
	}
	listen := cfg.Listen
	if host, port, err := net.SplitHostPort(cfg.Listen); err == nil && port == "0" && st.External != nil && st.External.Port != 0 {
		listen = net.JoinHostPort(host, strconv.Itoa(st.External.Port))
	}
	return listen, nil
}

// desktopPortFile is where a desktop instance remembers the port it last bound, under the data directory.
const desktopPortFile = "desktop/port"

// desktopListen binds the listener of a desktop instance. The shell asks for "any port", and a browser keeps what a
// page stores (unsaved drafts, the service worker) by origin, which includes the port: so the port bound last time is
// tried first, and a free one is taken only when it is in use. The port is remembered for the next start. It is not a
// secret and nothing treats it as one. listen is the provider's fixed port when the instance signs in through an
// external provider that has one; it comes first.
func desktopListen(cfg *config.Config, listen string, log *slog.Logger) (net.Listener, error) {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cfg.DataDir, filepath.FromSlash(desktopPortFile))
	var tries []string
	if listen != cfg.Listen {
		tries = append(tries, listen)
	}
	if port == "0" {
		if raw, rerr := os.ReadFile(path); rerr == nil && len(raw) < 16 {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && n >= 1024 && n <= 65535 {
				tries = append(tries, net.JoinHostPort(host, strconv.Itoa(n)))
			}
		}
	}
	tries = append(tries, cfg.Listen)
	var ln net.Listener
	lc := desktopListenConfig()
	for i, addr := range tries {
		if ln, err = lc.Listen(context.Background(), "tcp", addr); err == nil {
			break
		}
		if i < len(tries)-1 {
			if addr == listen && listen != cfg.Listen {
				log.Warn("desktop: the fixed port of the external provider's settings is in use; another is used, and sign-in at the provider may refuse its redirect address until the port is free", "err", err)
			} else {
				log.Warn("desktop: the port used last time is in use; a free one is used. What this browser kept for the old address (unsaved drafts) is not offered at the new one", "err", err)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if _, bound, serr := net.SplitHostPort(ln.Addr().String()); serr == nil && port == "0" {
		if werr := os.MkdirAll(filepath.Dir(path), 0o700); werr == nil {
			werr = os.WriteFile(path, []byte(bound+"\n"), 0o600)
			if werr != nil {
				log.Warn("desktop: the port could not be remembered for the next start", "err", werr)
			}
		}
	}
	return ln, nil
}

// stopWhenStdinCloses makes a desktop instance stop, as on SIGTERM, when its standard input reaches its end: the
// shell holds the other end of that pipe and closes it when it quits, and the system closes it when the shell dies
// any other way, so a server never outlives its shell holding the data directory. It applies only when standard
// input was a pipe (or a socket, which some launchers use for one) at start: a terminal, /dev/null or a closed
// descriptor is not a shell holding a pipe, and never stops the server.
func stopWhenStdinCloses(in *os.File, stop func(), log *slog.Logger) {
	fi, err := in.Stat()
	if err != nil || fi.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, in) // nothing is read from it for meaning; only its end matters
		log.Info("desktop: standard input closed, so the shell is gone; stopping")
		stop()
	}()
}

const desktopUsage = `usage: scriobh desktop use-local-profile [--adopt-admin <email-or-id>]

use-local-profile alone takes a desktop instance that signs in through an external identity
provider back to its embedded issuer and local profile, at its next start. For when the provider
cannot be reached and nobody can sign in to switch back from the settings.

--adopt-admin <email-or-id> is for a data directory that came from a server instance (a restored
backup): its accounts belong to another identity provider and the local profile has none, so the
window is refused. It binds the local profile to that one existing administrator account, which
you name exactly by id or by email address. Nothing is matched automatically. It is recorded in
the audit log and ends that account's browser sessions.

Stop the application first. The data directory is SCRIOBH_DATA_DIR, or the desktop location with
SCRIOBH_DESKTOP=1.
`

// parseDesktopArgs reads the arguments after "desktop". adopt is the account named by --adopt-admin, if any.
func parseDesktopArgs(args []string) (adopt string, ok bool) {
	if len(args) == 0 || args[0] != "use-local-profile" {
		return "", false
	}
	rest := args[1:]
	switch {
	case len(rest) == 0:
		return "", true
	case len(rest) == 2 && rest[0] == "--adopt-admin" && strings.TrimSpace(rest[1]) != "":
		return strings.TrimSpace(rest[1]), true
	case len(rest) == 1 && strings.HasPrefix(rest[0], "--adopt-admin=") && strings.TrimSpace(strings.TrimPrefix(rest[0], "--adopt-admin=")) != "":
		return strings.TrimSpace(strings.TrimPrefix(rest[0], "--adopt-admin=")), true
	}
	return "", false
}

func desktopCmd(args []string, stdout, stderr io.Writer) int {
	adopt, ok := parseDesktopArgs(args)
	if !ok {
		fmt.Fprint(stderr, desktopUsage)
		return 2
	}
	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err, "(stop the application first)")
		return 1
	}
	defer release()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "scriobh.db")); err != nil {
		fmt.Fprintln(stderr, "scriobh: there is no Scríobh database in", cfg.DataDir)
		return 1
	}
	d, err := db.Open(filepath.Join(cfg.DataDir, "scriobh.db"))
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	if adopt != "" {
		a, err := desktop.AdoptAdmin(context.Background(), d, adopt)
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
		fmt.Fprintf(stdout, "the local profile is now the account %s (%s); open the application and it signs in as that administrator\n", a.UserID, strings.TrimSpace(a.Name+" "+a.Email))
		return 0
	}
	changed, err := desktop.RequestEmbedded(context.Background(), d)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	if changed {
		fmt.Fprintln(stdout, "at its next start this instance signs in through its embedded issuer, as the local profile")
	} else {
		fmt.Fprintln(stdout, "this instance already signs in through its embedded issuer; nothing was changed")
		if desktop.NeedsAdoption(context.Background(), d) {
			fmt.Fprintln(stdout, "but the local profile has no account here and other accounts exist (data from a server instance): to give the local profile an existing administrator's account, run: scriobh desktop use-local-profile --adopt-admin <email-or-id>")
		}
	}
	return 0
}
