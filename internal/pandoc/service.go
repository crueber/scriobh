package pandoc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the service is registered under in the op environment.
const ServiceName = "pandoc"

// Settings keys.
const (
	settingEnabled = "pandoc.enabled"
	settingPath    = "pandoc.path"
	settingMemory  = "pandoc.memory_mb"
	// settingRestorePaused is "1" after a restore switched the bridge off; switching it on clears it.
	settingRestorePaused = "pandoc.restore_paused"
)

// DefaultMemoryMB is Pandoc's heap limit (+RTS -M) unless an administrator
// changes it. 0 passes no option (for a build that does not accept RTS options).
const DefaultMemoryMB = 1024

// Options are what the server gives the service.
type Options struct {
	// Helper is the path of an executable that calls RunHelperIfRequested: the
	// server's own binary. Empty runs Pandoc without resource limits.
	Helper   string
	TempRoot string
	// RefuseUnder lists folders (the data directory) no Pandoc binary may live under.
	RefuseUnder []string
	Limits      Limits
	Log         *slog.Logger
}

// Service is the bridge: the administrator's settings, the located binary and
// the runner.
type Service struct {
	env *ops.Env
	r   *Runner
	log *slog.Logger

	mu      sync.Mutex
	bin     Binary
	binFor  string // the configured path the cached binary was found for
	binTime time.Time
}

// ServiceOf returns the service from the op environment.
func ServiceOf(env *ops.Env) (*Service, bool) { return ops.Service[*Service](env, ServiceName) }

// Config is the administrator's setting.
type Config struct {
	Enabled  bool
	Path     string
	MemoryMB int
}

func (s *Service) config(ctx context.Context) (Config, error) {
	c := Config{MemoryMB: DefaultMemoryMB}
	rows, err := s.env.DB.Read().QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN (?,?,?)`, settingEnabled, settingPath, settingMemory)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return c, err
		}
		switch k {
		case settingEnabled:
			c.Enabled = v == "1"
		case settingPath:
			c.Path = v
		case settingMemory:
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 65536 {
				c.MemoryMB = n
			}
		}
	}
	return c, rows.Err()
}

// locate finds the binary for a configured path, running --version only when the
// path or the file changed since the last time (or a minute has passed).
func (s *Service) locate(ctx context.Context, path string) (Binary, error) {
	s.mu.Lock()
	b, ok := s.bin, s.bin.Resolved != "" && s.binFor == path && time.Since(s.binTime) < time.Minute
	s.mu.Unlock()
	if ok {
		if fi, err := os.Stat(b.Resolved); err == nil && b.same(fi) {
			return b, nil
		}
	}
	nb, err := s.r.Locate(ctx, path)
	if err != nil {
		// A busy server or a cancelled request says nothing about the program: what was found stays.
		if e, ok := AsError(err); !ok || (e.Kind != KindBusy && e.Kind != KindCancelled) {
			s.mu.Lock()
			s.bin, s.binFor = Binary{}, ""
			s.mu.Unlock()
		}
		return Binary{}, err
	}
	s.mu.Lock()
	s.bin, s.binFor, s.binTime = nb, path, time.Now()
	s.mu.Unlock()
	return nb, nil
}

// Info says whether the bridge is usable now, and what it would use.
type Info struct {
	Enabled bool
	Binary  Binary
	Err     error
}

// Status is the bridge's state for a caller who only wants a yes or no and the version.
func (s *Service) Status(ctx context.Context) Info {
	cfg, err := s.config(ctx)
	if err != nil {
		return Info{Err: fail(KindFailed, "The Pandoc settings could not be read.")}
	}
	if !cfg.Enabled {
		return Info{Err: fail(KindDisabled, "Pandoc is not switched on. An administrator can switch it on in the Pandoc settings.")}
	}
	b, err := s.locate(ctx, cfg.Path)
	if err != nil {
		return Info{Enabled: true, Err: err}
	}
	s.r.SetMemoryMB(cfg.MemoryMB)
	return Info{Enabled: true, Binary: b}
}

// Version is the version of the Pandoc that would be used, "" when the bridge is not usable.
func (s *Service) Version(ctx context.Context) string {
	if i := s.Status(ctx); i.Err == nil {
		return i.Binary.Version
	}
	return ""
}

// ToMarkdown converts an uploaded document to GitHub-flavoured Markdown.
func (s *Service) ToMarkdown(ctx context.Context, format string, data []byte, o ImportOptions) (*Imported, error) {
	i := s.Status(ctx)
	if i.Err != nil {
		return nil, i.Err
	}
	return s.r.ImportWith(ctx, i.Binary, format, data, o)
}

// FromMarkdown converts the compile Markdown to a file.
func (s *Service) FromMarkdown(ctx context.Context, format string, md []byte) (*Exported, error) {
	i := s.Status(ctx)
	if i.Err != nil {
		return nil, i.Err
	}
	return s.r.Export(ctx, i.Binary, format, md)
}

// SetMemoryMB changes Pandoc's heap limit for later runs.
func (r *Runner) SetMemoryMB(n int) {
	r.mu.Lock()
	r.lim.MemoryMB = n
	r.mu.Unlock()
}

// ---- ops -------------------------------------------------------------------------------------------

// Register adds the Pandoc settings ops and the service.
func Register(r *ops.Registry, o Options) *Service {
	lg := o.Log
	if lg == nil {
		lg = slog.Default()
	}
	s := &Service{env: r.Env(), r: NewRunner(o.Limits, o.TempRoot, o.Helper), log: lg}
	s.r.RefuseUnder(o.RefuseUnder...)
	if r.Env() != nil {
		r.Env().SetService(ServiceName, s)
	}
	admin := ops.Admin().ForSessions()
	ops.Register(r, ops.Op{Name: "pandoc.config.get", Method: "GET", Path: "/api/v1/admin/pandoc", Auth: admin,
		Desc: "Administrators, browser sessions only. The Pandoc bridge: whether it is switched on, the path set (or found on PATH), the version, whether the binary passed the checks (checked only while the bridge is on: while it is off, after a restore included, nothing is run and the stored path is shown as disabled; a regular executable owned by root or the server's user, not writable by others, in a folder that is not), the formats it would read and write, and whether Pandoc runs under resource limits on this server."}, s.get)
	ops.Register(r, ops.Op{Name: "pandoc.config.set", Method: "PUT", Path: "/api/v1/admin/pandoc", Auth: admin, NoDryRun: true,
		Desc: "Administrators, browser sessions only. Switch the Pandoc bridge on or off and set the absolute path of the binary (empty: find pandoc on PATH) and Pandoc's heap limit in MB (0: pass none). Switching on needs a binary that passes the checks and is version 3.1.6 or later (older ones can be made to write a file anywhere the server can); it runs the program once with --version. Import of odt, rtf, latex, mediawiki, org, textile, rst, fb2, typst and docbook, and export to odt, rtf, latex, rst and mediawiki, become available to whoever may import or export."}, s.set)
	ops.Register(r, ops.Op{Name: "pandoc.config.test", Method: "POST", Path: "/api/v1/admin/pandoc/test", Auth: admin, NoDryRun: true,
		Desc: "Administrators, browser sessions only. Run a tiny conversion in each direction with exactly the arguments, environment and limits a real one gets (including --sandbox), and report whether each worked. It sends nothing anywhere and holds no manuscript text."}, s.test)
	return s
}

// ConfigOut is the bridge's state.
type ConfigOut struct {
	Enabled       bool     `json:"enabled"`
	Path          string   `json:"path" desc:"The path an administrator set; empty means pandoc is looked for on the server's PATH."`
	MemoryMB      int      `json:"memory_mb" desc:"Pandoc's own heap limit (+RTS -M). 0 passes none."`
	PlatformOK    bool     `json:"platform_supported" desc:"False off Unix: the bridge cannot be used there."`
	Found         bool     `json:"found" desc:"A binary was found and passed the checks."`
	ResolvedPath  string   `json:"resolved_path,omitempty" desc:"The file that would be run, after links."`
	Version       string   `json:"version,omitempty"`
	MinVersion    string   `json:"min_version"`
	ProblemKind   string   `json:"problem_kind,omitempty" enum:"disabled,not_found,unsafe_binary,version,unsupported_platform,busy,cancelled,failed"`
	Problem       string   `json:"problem,omitempty"`
	ImportFormats []string `json:"import_formats"`
	ExportFormats []string `json:"export_formats"`
	Limits        bool     `json:"resource_limits" desc:"Pandoc runs under CPU, open-file and file-size limits set just before it starts."`
	Concurrent    int      `json:"concurrent" desc:"Conversions at once."`
	RestorePaused bool     `json:"restore_paused" desc:"A restore from a backup switched the bridge off. Only an administrator switching it on again clears this."`
}

func (s *Service) out(ctx context.Context) (ConfigOut, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return ConfigOut{}, ops.Internal(err)
	}
	out := ConfigOut{Enabled: cfg.Enabled, Path: cfg.Path, MemoryMB: cfg.MemoryMB, PlatformOK: supported, MinVersion: fmt.Sprintf("%d.%d.%d", MinVersion[0], MinVersion[1], MinVersion[2]),
		ImportFormats: ImportFormats(), ExportFormats: ExportFormats(), Limits: s.r.HasResourceLimits(), Concurrent: s.r.Limits().Concurrent}
	var paused string
	_ = s.env.DB.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingRestorePaused).Scan(&paused)
	out.RestorePaused = paused == "1" && !cfg.Enabled
	if !supported {
		out.ProblemKind, out.Problem = string(KindUnsupported), "Pandoc cannot be used on this platform."
		return out, nil
	}
	if !cfg.Enabled {
		// Off means not run: reading the settings never starts the program (after a restore it is a file an
		// administrator has not checked on this machine). What was found while the bridge was on, if this process
		// remembers it, is shown; nothing is looked for.
		out.ProblemKind, out.Problem = string(KindDisabled), "Pandoc is not switched on. An administrator can switch it on in the Pandoc settings."
		s.mu.Lock()
		if s.bin.Resolved != "" && s.binFor == cfg.Path {
			out.ResolvedPath, out.Version = s.bin.Resolved, s.bin.Version
		}
		s.mu.Unlock()
		return out, nil
	}
	b, err := s.locate(ctx, cfg.Path)
	if err != nil {
		if e, ok := AsError(err); ok {
			out.ProblemKind, out.Problem = string(e.Kind), e.Msg
		} else {
			out.ProblemKind, out.Problem = string(KindFailed), "Pandoc could not be checked."
		}
		return out, nil
	}
	out.Found, out.ResolvedPath, out.Version = true, b.Resolved, b.Version
	return out, nil
}

func (s *Service) get(c *ops.Ctx, _ struct{}) (ConfigOut, error) { return s.out(c) }

// SetIn changes the bridge's settings. Only the fields sent change.
type SetIn struct {
	Enabled  *bool   `json:"enabled,omitempty"`
	Path     *string `json:"path,omitempty" desc:"Absolute path of the pandoc binary; empty: find it on PATH."`
	MemoryMB *int    `json:"memory_mb,omitempty" desc:"Pandoc's heap limit in MB, 0 to 65536; 0 passes none."`
}

// Validate checks the shapes that stand alone.
func (in *SetIn) Validate() error {
	if in.Path != nil {
		p := strings.TrimSpace(*in.Path)
		if len(p) > 4096 || strings.ContainsAny(p, "\x00\n\r") {
			return ops.Invalid("path", "That is not a usable path.")
		}
		in.Path = &p
	}
	if in.MemoryMB != nil && (*in.MemoryMB < 0 || *in.MemoryMB > 65536) {
		return ops.Invalid("memory_mb", "Between 0 and 65536.")
	}
	return nil
}

func (s *Service) set(c *ops.Ctx, in SetIn) (ConfigOut, error) {
	cur, err := s.config(c)
	if err != nil {
		return ConfigOut{}, ops.Internal(err)
	}
	next := cur
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.Path != nil {
		next.Path = *in.Path
	}
	if in.MemoryMB != nil {
		next.MemoryMB = *in.MemoryMB
	}
	if next.Enabled {
		// Switching on, or changing the path of a bridge that is on, needs a binary that
		// passes: the reason is shown plainly, and nothing changes if it does not.
		if _, err := s.locate(c, next.Path); err != nil {
			if e, ok := AsError(err); ok {
				return ConfigOut{}, ops.Invalid("path", e.Msg)
			}
			return ConfigOut{}, ops.Invalid("path", "Pandoc could not be checked.")
		}
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		put := func(k, v string) error {
			_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, v, db.Now())
			return err
		}
		en := "0"
		if next.Enabled {
			en = "1"
		}
		kvs := [][2]string{{settingEnabled, en}, {settingPath, next.Path}, {settingMemory, strconv.Itoa(next.MemoryMB)}}
		if next.Enabled {
			kvs = append(kvs, [2]string{settingRestorePaused, "0"}) // switching on is what lifts a restore's pause
		}
		for _, kv := range kvs {
			if err := put(kv[0], kv[1]); err != nil {
				return err
			}
		}
		// The path itself is not in the audit line: it names the server's file system.
		c.Audit(ops.AuditEntry{Action: "pandoc.config.set", TargetType: "instance", Detail: map[string]any{"enabled": next.Enabled, "path_set": next.Path != "", "memory_mb": next.MemoryMB}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ConfigOut{}, ops.AsError(err)
	}
	s.mu.Lock()
	s.binFor = "" // check again at the next use
	s.mu.Unlock()
	return s.out(c)
}

// TestOut is what a test conversion found.
type TestOut struct {
	Version string     `json:"version,omitempty"`
	Import  TestResult `json:"import"`
	Export  TestResult `json:"export"`
	OK      bool       `json:"ok"`
}

// TestResult is one direction.
type TestResult struct {
	OK        bool   `json:"ok"`
	Format    string `json:"format"`
	LatencyMS int64  `json:"latency_ms"`
	Problem   string `json:"problem,omitempty"`
}

func (s *Service) test(c *ops.Ctx, _ struct{}) (TestOut, error) {
	var out TestOut
	info := s.Status(c)
	if info.Err != nil {
		e, _ := AsError(info.Err)
		msg := "Pandoc is not usable."
		if e != nil {
			msg = e.Msg
		}
		out.Import = TestResult{Format: "rst", Problem: msg}
		out.Export = TestResult{Format: "rst", Problem: msg}
		return out, nil
	}
	out.Version = info.Binary.Version
	start := time.Now()
	imp, err := s.r.Import(c, info.Binary, "rst", []byte("Title\n=====\n\nA short test paragraph.\n"))
	out.Import = TestResult{Format: "rst", LatencyMS: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		out.Import.Problem = problemOf(err)
	case !strings.Contains(string(imp.Markdown), "test paragraph"):
		out.Import.Problem = "Pandoc ran but did not return the text it was given."
	default:
		out.Import.OK = true
	}
	start = time.Now()
	exp, err := s.r.Export(c, info.Binary, "rst", []byte("# Title\n\nA short test paragraph.\n"))
	out.Export = TestResult{Format: "rst", LatencyMS: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		out.Export.Problem = problemOf(err)
	case !strings.Contains(string(exp.Body), "test paragraph"):
		out.Export.Problem = "Pandoc ran but did not return the text it was given."
	default:
		out.Export.OK = true
	}
	out.OK = out.Import.OK && out.Export.OK
	return out, nil
}

func problemOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return "The conversion failed."
}
