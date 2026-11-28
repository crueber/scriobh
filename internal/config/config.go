// Package config reads the environment into one validated Config.
//
// Environment first, database overrides second, UI last. Secrets only come
// from the environment or a key file, never from the database.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config is the process configuration. BaseURL may be filled in after the
// listener is bound when the port is ephemeral, so read it through
// BaseURLString rather than copying the field.
type Config struct {
	mu sync.RWMutex

	// envRead is every environment name Load looked at; see UnknownEnv.
	envRead map[string]bool

	// BaseURL is the public URL. Empty means "derive it from the listener".
	BaseURL string
	Listen  string
	DataDir string
	// PortFile, when set, receives the bound port once the server listens.
	PortFile string

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	// OIDCStub enables the development issuer mounted at /dev-idp/.
	OIDCStub bool
	// DevAnyListen lets the development issuer run on a non-loopback listen
	// address. Only the dev compose stack sets it, and only with the host port
	// published to 127.0.0.1.
	DevAnyListen bool

	AdminGroup  string
	WriterGroup string
	ReaderGroup string

	SecretKey    []byte
	SessionHours int
	// SessionIdleMinutes is the idle cap; 0 disables it.
	SessionIdleMinutes int

	TrustProxy bool
	RateLimit  int
	// Share links (public, no account). ShareRateLimit is the requests a minute one client
	// address may make to /s/ in all (SCRIOBH_SHARE_RATE_LIMIT), ShareLinkRate the requests
	// a minute one link may take from everyone (SCRIOBH_SHARE_LINK_RATE) and
	// ShareCommentsPerHour the comments the guests of one link may write in an hour
	// (SCRIOBH_SHARE_COMMENTS_PER_HOUR). 0 disables the first two; the third must be at least 1.
	ShareRateLimit       int
	ShareLinkRate        int
	ShareCommentsPerHour int
	// MaxKeysPerUser caps one person's live API keys (SCRIOBH_MAX_KEYS_PER_USER).
	MaxKeysPerUser int
	// MaxAltsPerDoc caps the alternatives one document may hold
	// (SCRIOBH_MAX_ALTS_PER_DOC).
	MaxAltsPerDoc int
	// CompileConcurrency is how many compiles (previews, exports, covers) and
	// site builds run at once, in one gate (SCRIOBH_COMPILE_CONCURRENCY, default 2); the rest wait two seconds and
	// are told to try again. CompilePDFMaxWords refuses a PDF compile of a longer
	// book (SCRIOBH_COMPILE_PDF_MAX_WORDS, default 600,000).
	CompileConcurrency int
	CompilePDFMaxWords int
	// IdempotencyMaxBytes and IdempotencyMaxEntries cap what one caller's
	// stored idempotent responses may hold; the oldest go first.
	IdempotencyMaxBytes   int
	IdempotencyMaxEntries int
	LogLevel              string
	Version               string

	// MirrorDisabled turns the continuous Markdown mirror off
	// (SCRIOBH_MIRROR=off). The mirror is on by default.
	MirrorDisabled bool
	// MirrorDebounceMs is how long after the last change a project's mirror is
	// rewritten (SCRIOBH_MIRROR_DEBOUNCE_MS, default 2000).
	MirrorDebounceMs int
	// BackupIntervalHours is the gap between scheduled backups; 0 turns them
	// off (SCRIOBH_BACKUP_INTERVAL_HOURS, default 24). BackupKeep is how many
	// scheduled backups to keep (SCRIOBH_BACKUP_KEEP, default 7); manual
	// backups are never pruned.
	BackupIntervalHours int
	BackupKeep          int
	// Live updates (server-sent events). SSEPerUser and SSETotal cap the open
	// streams of one person and of the instance (SCRIOBH_SSE_PER_USER, default
	// 10; SCRIOBH_SSE_TOTAL, default 200); SSEReplayEvents and SSEReplayHours
	// bound how far back a reconnecting client can resume
	// (SCRIOBH_SSE_REPLAY_EVENTS, default 1000; SCRIOBH_SSE_REPLAY_HOURS,
	// default 24). 0 turns a cap off for the first two.
	SSEPerUser, SSETotal, SSEReplayEvents, SSEReplayHours int
	// Uploads (attachments). UploadMaxMB is the largest file accepted
	// (SCRIOBH_UPLOAD_MAX_MB, default 25). The quotas count stored bytes after
	// de-duplication, per person, per project or Compendium, and for the whole
	// instance, in MB; 0 means no quota (SCRIOBH_QUOTA_USER_MB default 2048,
	// SCRIOBH_QUOTA_PROJECT_MB default 1024, SCRIOBH_QUOTA_INSTANCE_MB default 0).
	UploadMaxMB, QuotaUserMB, QuotaProjectMB, QuotaInstanceMB int
	// UploadConcurrency is how many uploads one person may have in flight at once
	// (SCRIOBH_UPLOAD_CONCURRENCY, default 3; 0 turns the cap off).
	UploadConcurrency int
	// GitMaxRepoMB caps what a repository import clone, or the scratch fetch
	// behind a refused push or a force push, may hold
	// (SCRIOBH_GIT_MAX_REPO_MB, default 512).
	GitMaxRepoMB int
	// Staged imports (docs/OPERATIONS.md, "Importing"): how long a draft may sit idle before it is removed
	// (SCRIOBH_IMPORT_STAGE_HOURS, default 72), how many open drafts one person may hold
	// (SCRIOBH_IMPORT_STAGE_MAX_OPEN, default 5), and the staged bytes one person (SCRIOBH_IMPORT_STAGE_USER_MB,
	// default 256) and the instance (SCRIOBH_IMPORT_STAGE_TOTAL_MB, default 2048) may hold.
	ImportStageHours, ImportStageMaxOpen, ImportStageUserMB, ImportStageTotalMB int
	// GitAllowPrivate lets Git sync connect to loopback, private and other
	// internal addresses (SCRIOBH_GIT_ALLOW_PRIVATE=1). Off by default.
	GitAllowPrivate bool
	// Disk Git sync may use: per person for import clones, for the instance in
	// all (0 turns a quota off); how many bundles of a remote to keep; how many
	// days a clone kept beside an imported project lives (0 is forever).
	GitUserQuotaMB     int
	GitTotalQuotaMB    int
	GitBundlesKeep     int
	GitKeepImportsDays int

	// SiteDisabled turns static site publishing off (SCRIOBH_SITE=off): no
	// preview, no build, no publish. It is on by default. SiteDir is the folder
	// sites are published under (SCRIOBH_SITE_DIR); SitePublish
	// (SCRIOBH_SITE_PUBLISH=1) enables publishing to <data>/sites when no folder
	// is given. Only the administrator sets either: a request never names a path.
	SiteDisabled bool
	SiteDir      string
	SitePublish  bool

	// Desktop is desktop mode (SCRIOBH_DESKTOP=1): the process is the sidecar of a desktop shell, bound to loopback,
	// with the embedded issuer for one local profile. Everything about it is in desktop.go. LaunchToken is the
	// shell's per-launch secret (SCRIOBH_LAUNCH_TOKEN); it is never logged, shown or stored.
	Desktop     bool
	LaunchToken string
	// DesktopExternal is set, after the database is open, when an administrator switched this desktop instance to an
	// external identity provider (see ApplyDesktopExternal). It is never read from the environment.
	DesktopExternal bool
	// desktopAdminGroupEnv records that the environment named an administrators' group in desktop mode (refused by Validate).
	desktopAdminGroupEnv bool
	// dataDirSet and listenSet say whether the environment named a data directory and a listen address; desktop mode
	// has its own defaults for both.
	dataDirSet, listenSet bool

	// AI is the AI core's configuration (SCRIOBH_AI_*), see ai.go.
	AI AI
	// MCP and Webhook are the integration surfaces (SCRIOBH_MCP_*, SCRIOBH_WEBHOOK_*), see integrations.go.
	MCP     MCP
	Webhook Webhook
}

// SiteRoot is the folder static sites are published under, or "" when
// publishing to a folder is off.
func (c *Config) SiteRoot() string {
	switch {
	case c.SiteDisabled:
		return ""
	case c.SiteDir != "":
		return c.SiteDir
	case c.SitePublish:
		return filepath.Join(c.DataDir, "sites")
	}
	return ""
}

// Default values.
const (
	DefaultListen = "127.0.0.1:8080"
	DefaultBase   = "http://localhost:8080"
)

// Load reads the environment. getenv is injectable for tests; pass os.LookupEnv.
func Load(lookup func(string) (string, bool)) (*Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	// Every name Load reads is remembered, so that UnknownEnv can tell a setting this version reads from a typo.
	read := map[string]bool{}
	raw := lookup
	lookup = func(k string) (string, bool) { read[k] = true; return raw(k) }
	get := func(k, def string) string {
		if v, ok := lookup(k); ok {
			return strings.TrimSpace(v)
		}
		return def
	}
	getInt := func(k string, def int) (int, error) {
		v, ok := lookup(k)
		if !ok || strings.TrimSpace(v) == "" {
			return def, nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s: %q is not a non-negative integer", k, v)
		}
		return n, nil
	}
	// A yes/no setting is one of the spellings in BoolValues or unset; anything else is an error naming the
	// variable (a mistyped TRUST_PROXY used to be silently off). Problems are collected and reported together.
	var badBools []string
	getBool := func(k string, def bool) bool {
		v, _ := lookup(k)
		b, err := ParseBool(k, v, def)
		if err != nil {
			badBools = append(badBools, err.Error())
		}
		return b
	}

	c := &Config{
		BaseURL:  strings.TrimRight(get("SCRIOBH_BASE_URL", ""), "/"),
		Listen:   get("SCRIOBH_LISTEN", DefaultListen),
		DataDir:  get("SCRIOBH_DATA_DIR", "data"),
		PortFile: get("SCRIOBH_PORT_FILE", ""),
		// The issuer is compared exactly by the OIDC client; never trim it.
		OIDCIssuer:       get("SCRIOBH_OIDC_ISSUER", ""),
		OIDCClientID:     get("SCRIOBH_OIDC_CLIENT_ID", ""),
		OIDCClientSecret: get("SCRIOBH_OIDC_CLIENT_SECRET", ""),
		OIDCStub:         getBool("SCRIOBH_OIDC_STUB", false),
		DevAnyListen:     getBool("SCRIOBH_DEV_ALLOW_ANY_LISTEN", false),
		AdminGroup:       get("SCRIOBH_ADMIN_GROUP", "scriobh-admins"),
		WriterGroup:      get("SCRIOBH_WRITER_GROUP", "scriobh-writers"),
		ReaderGroup:      get("SCRIOBH_READER_GROUP", "scriobh-readers"),
		TrustProxy:       getBool("SCRIOBH_TRUST_PROXY", false),
		LogLevel:         strings.ToLower(get("SCRIOBH_LOG_LEVEL", "info")),
	}
	c.Desktop = getBool("SCRIOBH_DESKTOP", false)
	// Not trimmed into anything else and never echoed: see Validate for what it must be.
	c.LaunchToken = get("SCRIOBH_LAUNCH_TOKEN", "")
	_, c.dataDirSet = raw("SCRIOBH_DATA_DIR")
	_, c.listenSet = raw("SCRIOBH_LISTEN")
	_, adminGroupSet := raw("SCRIOBH_ADMIN_GROUP")
	c.MirrorDisabled = !getBool("SCRIOBH_MIRROR", true)
	c.GitAllowPrivate = getBool("SCRIOBH_GIT_ALLOW_PRIVATE", false)
	c.SiteDisabled = !getBool("SCRIOBH_SITE", true)
	c.SitePublish = getBool("SCRIOBH_SITE_PUBLISH", false)
	if len(badBools) > 0 {
		return nil, errors.New(strings.Join(badBools, "\n"))
	}
	var err error
	if c.SessionHours, err = getInt("SCRIOBH_SESSION_HOURS", 24); err != nil {
		return nil, err
	}
	if c.SessionIdleMinutes, err = getInt("SCRIOBH_SESSION_IDLE_MINUTES", 480); err != nil {
		return nil, err
	}
	if c.RateLimit, err = getInt("SCRIOBH_RATE_LIMIT", 600); err != nil {
		return nil, err
	}
	if c.MaxKeysPerUser, err = getInt("SCRIOBH_MAX_KEYS_PER_USER", 25); err != nil {
		return nil, err
	}
	if c.ShareRateLimit, err = getInt("SCRIOBH_SHARE_RATE_LIMIT", 120); err != nil {
		return nil, err
	}
	if c.ShareLinkRate, err = getInt("SCRIOBH_SHARE_LINK_RATE", 600); err != nil {
		return nil, err
	}
	if c.ShareCommentsPerHour, err = getInt("SCRIOBH_SHARE_COMMENTS_PER_HOUR", 60); err != nil {
		return nil, err
	}
	if c.ShareCommentsPerHour < 1 {
		return nil, errors.New("SCRIOBH_SHARE_COMMENTS_PER_HOUR must be at least 1")
	}
	if c.MaxAltsPerDoc, err = getInt("SCRIOBH_MAX_ALTS_PER_DOC", 200); err != nil {
		return nil, err
	}
	if c.MaxAltsPerDoc < 1 || c.MaxAltsPerDoc > 5000 {
		return nil, errors.New("SCRIOBH_MAX_ALTS_PER_DOC must be between 1 and 5000")
	}
	if c.CompileConcurrency, err = getInt("SCRIOBH_COMPILE_CONCURRENCY", 2); err != nil {
		return nil, err
	}
	if c.CompileConcurrency < 1 || c.CompileConcurrency > 32 {
		return nil, errors.New("SCRIOBH_COMPILE_CONCURRENCY must be between 1 and 32")
	}
	if c.CompilePDFMaxWords, err = getInt("SCRIOBH_COMPILE_PDF_MAX_WORDS", 600_000); err != nil {
		return nil, err
	}
	if c.CompilePDFMaxWords < 10_000 || c.CompilePDFMaxWords > 2_000_000 {
		return nil, errors.New("SCRIOBH_COMPILE_PDF_MAX_WORDS must be between 10000 and 2000000")
	}
	if c.IdempotencyMaxBytes, err = getInt("SCRIOBH_IDEMPOTENCY_MAX_BYTES", 8<<20); err != nil {
		return nil, err
	}
	if c.IdempotencyMaxEntries, err = getInt("SCRIOBH_IDEMPOTENCY_MAX_ENTRIES", 200); err != nil {
		return nil, err
	}
	if c.MaxKeysPerUser == 0 || c.IdempotencyMaxBytes == 0 || c.IdempotencyMaxEntries == 0 {
		return nil, errors.New("SCRIOBH_MAX_KEYS_PER_USER, SCRIOBH_IDEMPOTENCY_MAX_BYTES and SCRIOBH_IDEMPOTENCY_MAX_ENTRIES must be at least 1")
	}
	if c.SessionHours == 0 {
		return nil, errors.New("SCRIOBH_SESSION_HOURS must be at least 1")
	}
	if c.MirrorDebounceMs, err = getInt("SCRIOBH_MIRROR_DEBOUNCE_MS", 2000); err != nil {
		return nil, err
	}
	if c.BackupIntervalHours, err = getInt("SCRIOBH_BACKUP_INTERVAL_HOURS", 24); err != nil {
		return nil, err
	}
	if c.BackupKeep, err = getInt("SCRIOBH_BACKUP_KEEP", 7); err != nil {
		return nil, err
	}
	if c.BackupKeep == 0 {
		return nil, errors.New("SCRIOBH_BACKUP_KEEP must be at least 1 (set SCRIOBH_BACKUP_INTERVAL_HOURS=0 to turn scheduled backups off)")
	}
	c.SiteDir = strings.TrimSpace(get("SCRIOBH_SITE_DIR", ""))
	if c.GitUserQuotaMB, err = getInt("SCRIOBH_GIT_USER_QUOTA_MB", 2048); err != nil {
		return nil, err
	}
	if c.GitTotalQuotaMB, err = getInt("SCRIOBH_GIT_TOTAL_QUOTA_MB", 20480); err != nil {
		return nil, err
	}
	if c.GitBundlesKeep, err = getInt("SCRIOBH_GIT_BUNDLES_KEEP", 5); err != nil {
		return nil, err
	}
	if c.GitBundlesKeep == 0 {
		return nil, errors.New("SCRIOBH_GIT_BUNDLES_KEEP must be at least 1")
	}
	if c.GitKeepImportsDays, err = getInt("SCRIOBH_GIT_KEEP_IMPORTS_DAYS", 30); err != nil {
		return nil, err
	}
	if c.GitMaxRepoMB, err = getInt("SCRIOBH_GIT_MAX_REPO_MB", 512); err != nil {
		return nil, err
	}
	if c.GitMaxRepoMB == 0 {
		return nil, errors.New("SCRIOBH_GIT_MAX_REPO_MB must be at least 1")
	}
	for _, f := range []struct {
		env string
		def int
		dst *int
	}{
		{"SCRIOBH_IMPORT_STAGE_HOURS", 72, &c.ImportStageHours}, {"SCRIOBH_IMPORT_STAGE_MAX_OPEN", 5, &c.ImportStageMaxOpen},
		{"SCRIOBH_IMPORT_STAGE_USER_MB", 256, &c.ImportStageUserMB}, {"SCRIOBH_IMPORT_STAGE_TOTAL_MB", 2048, &c.ImportStageTotalMB},
	} {
		if *f.dst, err = getInt(f.env, f.def); err != nil {
			return nil, err
		}
		if *f.dst < 1 {
			return nil, errors.New(f.env + " must be at least 1")
		}
	}
	for _, f := range []struct {
		env string
		def int
		dst *int
	}{
		{"SCRIOBH_SSE_PER_USER", 10, &c.SSEPerUser}, {"SCRIOBH_SSE_TOTAL", 200, &c.SSETotal},
		{"SCRIOBH_SSE_REPLAY_EVENTS", 1000, &c.SSEReplayEvents}, {"SCRIOBH_SSE_REPLAY_HOURS", 24, &c.SSEReplayHours},
		{"SCRIOBH_UPLOAD_MAX_MB", 25, &c.UploadMaxMB}, {"SCRIOBH_QUOTA_USER_MB", 2048, &c.QuotaUserMB},
		{"SCRIOBH_QUOTA_PROJECT_MB", 1024, &c.QuotaProjectMB}, {"SCRIOBH_QUOTA_INSTANCE_MB", 0, &c.QuotaInstanceMB},
		{"SCRIOBH_UPLOAD_CONCURRENCY", 3, &c.UploadConcurrency},
	} {
		if *f.dst, err = getInt(f.env, f.def); err != nil {
			return nil, err
		}
		if *f.dst < 0 {
			return nil, fmt.Errorf("%s cannot be negative", f.env)
		}
	}
	if c.SSEReplayEvents == 0 || c.SSEReplayHours == 0 || c.UploadMaxMB == 0 {
		return nil, errors.New("SCRIOBH_SSE_REPLAY_EVENTS, SCRIOBH_SSE_REPLAY_HOURS and SCRIOBH_UPLOAD_MAX_MB must be at least 1")
	}
	// SCRIOBH_IMPORT_CONCURRENCY is read by the importer itself on first use (internal/imports/limiter.go, which
	// ignores a value it cannot read); it is checked here so a typo is not silently the default.
	if v, ok := lookup("SCRIOBH_IMPORT_CONCURRENCY"); ok && strings.TrimSpace(v) != "" {
		if n, perr := strconv.Atoi(strings.TrimSpace(v)); perr != nil || n < 1 {
			return nil, fmt.Errorf("SCRIOBH_IMPORT_CONCURRENCY: %q is not an integer of at least 1", v)
		}
	}
	// SCRIOBH_PDF_TIMEOUT_SECONDS and SCRIOBH_PDF_MEMORY_MB are read by the importer when it starts the PDF reader
	// (internal/imports/pdfchild.go); they can only lower the limits (20 s, 512 MiB), and a typo is not silently the default.
	for _, name := range []string{"SCRIOBH_PDF_TIMEOUT_SECONDS", "SCRIOBH_PDF_MEMORY_MB"} {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			if n, perr := strconv.Atoi(strings.TrimSpace(v)); perr != nil || n < 1 {
				return nil, fmt.Errorf("%s: %q is not an integer of at least 1", name, v)
			}
		}
	}
	if err := loadAI(lookup, c); err != nil {
		return nil, err
	}
	if err := loadIntegrations(lookup, c); err != nil {
		return nil, err
	}
	if k := get("SCRIOBH_SECRET_KEY", ""); k != "" {
		if len(k) < 32 {
			return nil, errors.New("SCRIOBH_SECRET_KEY must be at least 32 characters (try: openssl rand -hex 32)")
		}
		c.SecretKey = []byte(k)
	}
	c.envRead = read
	if c.Desktop {
		if err := c.desktopDefaults(raw, adminGroupSet); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// BaseURLString returns the effective base URL.
func (c *Config) BaseURLString() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBase
}

// SetBoundAddr derives the base URL from the bound listener when none was
// configured. It is a no-op when BaseURL is explicit.
func (c *Config) SetBoundAddr(addr net.Addr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.BaseURL != "" {
		return
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "localhost"
	} else if ip.To4() == nil {
		host = "[" + host + "]"
	}
	c.BaseURL = "http://" + host + ":" + port
}

// Secure reports whether cookies must carry the Secure attribute.
func (c *Config) Secure() bool {
	return strings.HasPrefix(c.BaseURLString(), "https://")
}

// OIDCCallbackURL is the redirect URI registered with the identity provider.
func (c *Config) OIDCCallbackURL() string {
	return c.BaseURLString() + "/auth/oidc/callback"
}

// Validate checks the whole set and returns every problem at once.
func (c *Config) Validate() error {
	var probs []string
	if c.DataDir == "" {
		probs = append(probs, "SCRIOBH_DATA_DIR is empty")
	}
	if c.SiteDir != "" {
		if !filepath.IsAbs(c.SiteDir) {
			probs = append(probs, fmt.Sprintf("SCRIOBH_SITE_DIR %q is not an absolute path", c.SiteDir))
		} else if d, err := filepath.Abs(c.DataDir); err == nil {
			sd := resolveExisting(filepath.Clean(c.SiteDir))
			d = resolveExisting(d)
			sep := string(filepath.Separator)
			switch {
			case sd == filepath.Dir(sd):
				probs = append(probs, "SCRIOBH_SITE_DIR may not be the root of the file system")
			case sd == d || strings.HasPrefix(d+sep, sd+sep) || strings.HasPrefix(sd+sep, d+sep):
				probs = append(probs, "SCRIOBH_SITE_DIR must be outside the data directory altogether (not the data directory, a folder that holds it, or a folder inside it such as files/): a published site is replaced as a whole and served to the public. Use SCRIOBH_SITE_PUBLISH=1 for the data directory's own sites folder")
			}
		}
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			probs = append(probs, fmt.Sprintf("SCRIOBH_BASE_URL %q is not an http(s) URL", c.BaseURL))
		}
	}
	if c.Desktop {
		probs = append(probs, c.desktopProblems()...)
	} else if c.OIDCStub {
		if c.BaseURL != "" && !IsLoopbackURL(c.BaseURL) {
			probs = append(probs, "SCRIOBH_OIDC_STUB is on but SCRIOBH_BASE_URL is not a loopback address; the development issuer is refused on a public URL")
		}
		// The development issuer has no credentials, so it must not be reachable
		// from the network: both the base URL and the listen address are checked.
		if !IsLoopbackListen(c.Listen) && !c.DevAnyListen {
			probs = append(probs, "SCRIOBH_OIDC_STUB is on but SCRIOBH_LISTEN is not loopback; the development issuer has no credentials and would be reachable by anyone on the network. Listen on 127.0.0.1")
		}
	} else {
		if c.OIDCIssuer == "" {
			probs = append(probs, "SCRIOBH_OIDC_ISSUER is required (or set SCRIOBH_OIDC_STUB=1 for development)")
		}
		if c.OIDCClientID == "" {
			probs = append(probs, "SCRIOBH_OIDC_CLIENT_ID is required")
		}
		if c.OIDCClientSecret == "" {
			probs = append(probs, "SCRIOBH_OIDC_CLIENT_SECRET is required")
		}
	}
	if len(probs) > 0 {
		return errors.New(strings.Join(probs, "\n"))
	}
	return nil
}

// IsLoopbackURL reports whether the URL's host is a loopback name or address.
func IsLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// IsLoopbackListen reports whether a listen address binds only loopback.
func IsLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// EnsureSecretKey returns the signing/encryption key. When SCRIOBH_SECRET_KEY
// is unset it reads or creates a 0600 key file in the data directory, so a
// restart keeps sessions and encrypted values valid.
func (c *Config) EnsureSecretKey() ([]byte, error) {
	if len(c.SecretKey) > 0 {
		return c.SecretKey, nil
	}
	path := filepath.Join(c.DataDir, "secret.key")
	b, err := os.ReadFile(path)
	if err == nil {
		k := strings.TrimSpace(string(b))
		if len(k) < 32 {
			return nil, fmt.Errorf("%s is corrupt (too short); restore it from a backup or remove it to start fresh, which invalidates every session and stored credential", path)
		}
		c.SecretKey = []byte(k)
		return c.SecretKey, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(c.DataDir, 0o700)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	k := hex.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil, fmt.Errorf("read %s: %w", path, rerr)
			}
			ek := strings.TrimSpace(string(b))
			if len(ek) < 32 {
				return nil, fmt.Errorf("%s is corrupt (too short); restore it from a backup or remove it to start fresh, which invalidates every session and stored credential", path)
			}
			c.SecretKey = []byte(ek)
			return c.SecretKey, nil
		}
		return nil, err
	}
	_, werr := f.WriteString(k + "\n")
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return nil, werr
	}
	if cerr != nil {
		_ = os.Remove(path)
		return nil, cerr
	}
	c.SecretKey = []byte(k)
	return c.SecretKey, nil
}

// ValidateStubBase re-checks, once the real base URL is known, that the
// development issuer is only used on loopback.
func ValidateStubBase(c *Config) error {
	if c.OIDCStub && !IsLoopbackURL(c.BaseURLString()) {
		return errors.New("SCRIOBH_OIDC_STUB is on but the base URL is not loopback; the development issuer is refused on a public URL")
	}
	return nil
}
