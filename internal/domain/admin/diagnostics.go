package admin

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The diagnostics bundle is built to be sent to someone else. It holds numbers, names of settings, names of tables
// and states, and the last warnings with every path reduced to its route pattern; it is assembled from an allow-list
// (a table's name and its row count; a setting only if the server's list names it; a log attribute only if it is
// one of a few), and every string that reaches it is scrubbed besides, so a mistake on one side is caught by the
// other. Nothing from a manuscript, a comment, a person (name or email), a link or a secret is read at all.

var scrubbers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`/s/[^\s"'/?#{}]+`), "/s/…"},
	{regexp.MustCompile(`(?i)(bearer|basic|token)\s+[A-Za-z0-9._~+/=\-]{4,}`), "$1 […]"},
	{regexp.MustCompile(`://[^/\s:@]+(:[^/\s@]*)?@`), "://[credentials]@"},
	{regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`\b(scriobh_|shl_|sk-|sk_|pk_|ghp_|gho_|github_pat_|glpat-|xox[abprs]-|AKIA)[A-Za-z0-9_\-.]{4,}`), "[token]"},
	{regexp.MustCompile(`\?[^\s"']*`), "?…"},
	{regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`), "[secret]"},
}

// Scrub removes from s anything that looks like a link token, a bearer or API credential, an email address, a
// credential in a URL, a query string or a long secret-looking run. It is applied to every string that enters a
// diagnostics bundle or its log buffer.
func Scrub(s string) string {
	for _, sc := range scrubbers {
		s = sc.re.ReplaceAllString(s, sc.with)
	}
	return s
}

var (
	reURL  = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]{1,15}://[^\s"'<>)]+`)
	rePath = regexp.MustCompile(`(?i)(?:[a-z]:\\|/)(?:users|home|var|tmp|private|etc|opt|srv|mnt|root|usr|data|volumes|app|run|library|snap|nix)(?:[/\\][^\s"'<>:;,)]*)*`)
)

// ScrubText is Scrub for free text (a message, an error): besides what Scrub removes it takes out every whole URL and
// every absolute file path, and any of hide (the data directory, a home directory) wherever it appears.
func ScrubText(s string, hide ...string) string {
	for _, h := range hide {
		if len(h) > 3 {
			s = strings.ReplaceAll(s, h, "[path]")
		}
	}
	s = reURL.ReplaceAllString(s, "[url]")
	s = rePath.ReplaceAllString(s, "[path]")
	return Scrub(s)
}

// ScrubLine is Scrub, cut to a bounded length on a character boundary.
func ScrubLine(s string, max int, hide ...string) string {
	s = strings.Join(strings.Fields(ScrubText(s, hide...)), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

var logAttrs = map[string]bool{"route": true, "method": true, "status": true, "dur": true, "req": true, "op": true, "component": true, "err": true}

var reWord = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// TableCount is a table and how many rows it holds.
type TableCount struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// QueueState counts the rows of a queue or job table by state.
type QueueState struct {
	Name   string         `json:"name"`
	States map[string]int `json:"states"`
}

// DiskLine is the space one area of the data directory takes.
type DiskLine struct {
	Area  string `json:"area"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// MigrationState says where the schema is.
type MigrationState struct {
	Applied int    `json:"applied"`
	Known   int    `json:"known"`
	Latest  string `json:"latest"`
	Pending int    `json:"pending"`
}

// Bundle is the diagnostics.
type Bundle struct {
	Schema     int            `json:"schema"`
	Version    string         `json:"version"`
	GoVersion  string         `json:"go_version"`
	OS         string         `json:"os"`
	Arch       string         `json:"arch"`
	CPUs       int            `json:"cpus"`
	Config     map[string]any `json:"config" desc:"Settings from an allow-list: numbers, switches and whether a setting is set. Never a secret, an address, a path or a name."`
	Migrations MigrationState `json:"migrations"`
	Tables     []TableCount   `json:"tables"`
	Queues     []QueueState   `json:"queues"`
	Disk       []DiskLine     `json:"disk"`
	Log        []LogLine      `json:"log" desc:"The last warnings and errors, newest last: a route pattern, a status, a short scrubbed message."`
	Redacted   []string       `json:"redacted" desc:"What was left out, in words."`
}

// DiagnosticsIn takes no input.
type DiagnosticsIn struct{}

var queueTables = []struct{ name, table, col string }{
	{"imports", "import_jobs", "status"},
	{"git pushes", "git_pushes", "status"},
	{"AI runs", "ai_runs", "status"},
	{"AI jobs", "ai_jobs", "status"},
}

var diskAreas = map[string]bool{"scriobh.db": true, "scriobh.db-wal": true, "scriobh.db-shm": true, "files": true, "mirror": true, "backups": true, "git": true, "conflicts": true}

func build(c *ops.Ctx) (Bundle, error) {
	s := svc(c)
	b := Bundle{Schema: 1, Version: Scrub(s.Version), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		Config: map[string]any{}, Tables: []TableCount{}, Queues: []QueueState{}, Disk: []DiskLine{}, Log: []LogLine{},
		Redacted: []string{
			"every secret, key, address, path and group name in the configuration (only an allow-list of settings is shown, and whether the others are set)",
			"all manuscript text, comments, notes, titles, names and email addresses (no table's rows are read, only counted)",
			"share-link tokens, API keys, session tokens and any credential in a Git address",
			"query strings and the id part of a path in the log lines (only the route pattern is kept)",
		}}
	if s.Config != nil {
		// Only a switch, a number or a short word of the kind a setting's value is ("https", "info") is shown: any other
		// string (an address, a path, a name, a secret) is withheld here too, whatever the list that built it said.
		for k, v := range s.Config() {
			switch x := v.(type) {
			case bool, int, int64, float64:
				b.Config[k] = v
			case string:
				if reWord.MatchString(x) {
					b.Config[k] = x
				} else {
					b.Config[k] = "[withheld]"
				}
			default:
				b.Config[k] = "[withheld]"
			}
		}
	}
	q := c.DB().Read()
	// migrations
	if all, err := db.Migrations(); err == nil {
		b.Migrations.Known = len(all)
		_ = q.QueryRowContext(c, `SELECT count(*), COALESCE(max(version),'') FROM schema_migrations`).Scan(&b.Migrations.Applied, &b.Migrations.Latest)
		b.Migrations.Pending = len(all) - b.Migrations.Applied
		if b.Migrations.Pending < 0 {
			b.Migrations.Pending = 0
		}
	}
	// row counts: the names come from the schema, never from a person
	rows, err := q.QueryContext(c, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%\_fts%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return b, ops.Internal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(n) {
			names = append(names, n)
		}
	}
	rows.Close()
	for _, n := range names {
		var cnt int64
		if err := q.QueryRowContext(c, `SELECT count(*) FROM "`+n+`"`).Scan(&cnt); err == nil {
			b.Tables = append(b.Tables, TableCount{Name: n, Rows: cnt})
		}
	}
	// queues and jobs
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, qt := range queueTables {
		if !have[qt.table] {
			continue
		}
		st := QueueState{Name: qt.name, States: map[string]int{}}
		r2, err := q.QueryContext(c, `SELECT `+qt.col+`, count(*) FROM `+qt.table+` GROUP BY 1`)
		if err != nil {
			continue
		}
		for r2.Next() {
			var k string
			var n int
			if r2.Scan(&k, &n) == nil {
				st.States[ScrubLine(k, 40)] = n
			}
		}
		r2.Close()
		b.Queues = append(b.Queues, st)
	}
	b.Disk = disk(s.DataDir)
	if s.Log != nil {
		for _, l := range s.Log() {
			l.Msg = ScrubLine(l.Msg, 200, s.DataDir)
			l.Level = ScrubLine(l.Level, 10)
			at := map[string]string{}
			for k, v := range l.Attrs {
				if !logAttrs[k] { // a log attribute is kept only if it is one of these, whatever put it there
					continue
				}
				if k == "route" { // a route pattern has paths in it on purpose: only credentials and tokens are taken out
					at[k] = strings.Join(strings.Fields(Scrub(v)), " ")
				} else {
					at[k] = ScrubLine(v, 200, s.DataDir)
				}
			}
			l.Attrs = at
			b.Log = append(b.Log, l)
		}
		if len(b.Log) > 100 {
			b.Log = b.Log[len(b.Log)-100:]
		}
	}
	return b, nil
}

// disk sums the data directory by area. A file's name is never recorded, only which area it is in.
func disk(dir string) []DiskLine {
	if dir == "" {
		return []DiskLine{}
	}
	by := map[string]*DiskLine{}
	seen := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > 300000 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		area := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if !diskAreas[area] {
			area = "other"
		}
		l := by[area]
		if l == nil {
			l = &DiskLine{Area: area}
			by[area] = l
		}
		if info, err := d.Info(); err == nil {
			l.Bytes += info.Size()
		}
		l.Files++
		return nil
	})
	out := make([]DiskLine, 0, len(by))
	for _, l := range by {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Area < out[j].Area })
	return out
}

func diagnostics(c *ops.Ctx, _ DiagnosticsIn) (Bundle, error) { return build(c) }

const readme = `Scríobh diagnostics

This bundle was made by an administrator of a Scríobh instance to go with a bug report. Read diagnostics.json: it is plain JSON.

What is in it: the version and build, the system, the shape of the configuration (numbers, switches and whether a setting is set), where the database schema is, how many rows each table holds, the state of queues and jobs, the space the data directory takes by area, and the last warnings and errors the server kept, each reduced to a route pattern and a short message.

What is not: no manuscript text, comment, note, title, name or email address; no secret, key, token, address or path; no link token and no credential in a Git address. Rows are counted, never read. Every string in the bundle also passes through a scrubber that removes anything that looks like a token, a credential, an email address or a query string.

It is the same bytes for the same state of the server. Nothing is sent anywhere: the file is yours to look at before you share it.
`

// Archive is the bundle as a zip (diagnostics.json and README.txt), the same bytes for the same state.
type Archive struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	Body        []byte `json:"body" desc:"The zip. The REST route returns it as the response body instead of JSON."`
}

// Download makes Archive an ops.Download.
func (a Archive) Download() (string, string, []byte) { return a.Filename, a.ContentType, a.Body }

// Zip writes a bundle as the zip a download returns.
func Zip(b Bundle) ([]byte, error) {
	js, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.txt", []byte(readme)}, {"diagnostics.json", append(js, '\n')}} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: fixed})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func diagnosticsDownload(c *ops.Ctx, _ DiagnosticsIn) (Archive, error) {
	b, err := build(c)
	if err != nil {
		return Archive{}, err
	}
	z, err := Zip(b)
	if err != nil {
		return Archive{}, ops.Internal(err)
	}
	return Archive{Filename: "scriobh-diagnostics.zip", ContentType: "application/zip", Bytes: len(z), Body: z}, nil
}

var _ = sql.ErrNoRows
var _ = os.Getenv
