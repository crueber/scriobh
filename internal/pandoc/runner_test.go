package pandoc_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/testutil"
)

func newRunner(t *testing.T, lim pandoc.Limits) *pandoc.Runner {
	t.Helper()
	return pandoc.NewRunner(lim, t.TempDir(), "")
}

func locate(t *testing.T, r *pandoc.Runner, path string) pandoc.Binary {
	t.Helper()
	b, err := r.Locate(context.Background(), path)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	return b
}

func kindOf(err error) pandoc.ErrorKind {
	if e, ok := pandoc.AsError(err); ok {
		return e.Kind
	}
	return "other:" + pandoc.ErrorKind(fmt.Sprint(err))
}

func TestLocateChecksTheBinary(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	ctx := context.Background()
	dir, bin := fakeDir(t)

	b, err := r.Locate(ctx, bin)
	if err != nil || b.Version != "3.1.9" {
		t.Fatalf("%v %+v", err, b)
	}
	// On PATH.
	t.Setenv("PATH", dir)
	if b, err := r.Locate(ctx, ""); err != nil || b.Version != "3.1.9" {
		t.Fatalf("PATH: %v %+v", err, b)
	}
	t.Setenv("PATH", "/nonexistent")
	if _, err := r.Locate(ctx, ""); kindOf(err) != pandoc.KindNotFound {
		t.Fatalf("a missing binary: %v", err)
	}
	// Through a symbolic link: what is checked is the file it names.
	link := filepath.Join(t.TempDir(), "pandoc-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if b, err := r.Locate(ctx, link); err != nil || b.Resolved == link {
		t.Fatalf("link: %v %+v", err, b)
	}

	bad := []struct {
		name string
		do   func(t *testing.T) string
		kind pandoc.ErrorKind
	}{
		{"a relative path", func(t *testing.T) string { return "pandoc" }, pandoc.KindUnsafe},
		{"no such file", func(t *testing.T) string { return filepath.Join(dir, "nope") }, pandoc.KindNotFound},
		{"a directory", func(t *testing.T) string { return dir }, pandoc.KindUnsafe},
		{"not executable", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "pandoc")
			os.WriteFile(p, []byte(fakeScript), 0o644)
			return p
		}, pandoc.KindUnsafe},
		{"writable by its group", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "pandoc")
			os.WriteFile(p, []byte(fakeScript), 0o775)
			os.Chmod(p, 0o775)
			return p
		}, pandoc.KindUnsafe},
		{"writable by everyone", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "pandoc")
			os.WriteFile(p, []byte(fakeScript), 0o777)
			os.Chmod(p, 0o777)
			return p
		}, pandoc.KindUnsafe},
		{"in a folder anyone can write to", func(t *testing.T) string {
			d := t.TempDir()
			p := filepath.Join(d, "pandoc")
			os.WriteFile(p, []byte(fakeScript), 0o755)
			os.Chmod(d, 0o777)
			return p
		}, pandoc.KindUnsafe},
		{"too old", func(t *testing.T) string {
			d, p := fakeDir(t)
			setMode(t, d, "old")
			return p
		}, pandoc.KindVersion},
		{"not pandoc", func(t *testing.T) string {
			d, p := fakeDir(t)
			setMode(t, d, "notpandoc")
			return p
		}, pandoc.KindVersion},
		{"a path with a control character", func(t *testing.T) string { return "/usr/bin/pandoc\n--version" }, pandoc.KindNotFound},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Locate(ctx, tc.do(t))
			if kindOf(err) != tc.kind {
				t.Fatalf("want %s, got %v", tc.kind, err)
			}
			if e, _ := pandoc.AsError(err); e != nil && e.Message() == "" {
				t.Error("no message")
			}
		})
	}
	// A version below the minimum says which.
	d, p := fakeDir(t)
	setMode(t, d, "old")
	_, err = r.Locate(ctx, p)
	if e, _ := pandoc.AsError(err); e == nil || !strings.Contains(e.Message(), "2.14.2") || !strings.Contains(e.Message(), "3.1.6") {
		t.Fatalf("%v", err)
	}
}

func TestBinaryChangedAfterTheCheckIsNotRun(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	// Replaced after it was approved (the timestamp and size are no longer the ones checked).
	if err := os.WriteFile(bin, []byte(fakeScript+"\n# changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := r.Import(context.Background(), b, "rst", []byte("x"))
	if kindOf(err) != pandoc.KindUnsafe {
		t.Fatalf("a changed binary was run: %v", err)
	}
	if readLog(t, dir, "args.log") != "" {
		t.Fatal("it ran")
	}
	// And one made writable afterwards.
	_, bin2 := fakeDir(t)
	b2 := locate(t, r, bin2)
	os.Chmod(bin2, 0o777)
	if _, err := r.Import(context.Background(), b2, "rst", []byte("x")); kindOf(err) != pandoc.KindUnsafe {
		t.Fatalf("%v", err)
	}
}

// The argument list is a fixed shape; the only thing that varies is a name from the allow-list.
func TestArgumentsAreAFixedShapeWithNothingFromTheRequest(t *testing.T) {
	r := newRunner(t, pandoc.Limits{MemoryMB: 512})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	ctx := context.Background()
	for _, f := range pandoc.ImportFormats() {
		if _, err := r.Import(ctx, b, f, []byte("some input --lua-filter=/etc/passwd")); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	for _, f := range pandoc.ExportFormats() {
		if _, err := r.Export(ctx, b, f, []byte("# Title\n")); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	calls := callsOf(t, dir)
	if len(calls) != len(pandoc.ImportFormats())+len(pandoc.ExportFormats()) {
		t.Fatalf("%d calls", len(calls))
	}
	forbidden := []string{"--lua-filter", "--filter", "-F", "-L", "--template", "--include-in-header", "--include-before-body", "--include-after-body", "--resource-path",
		"--metadata", "-M", "--metadata-file", "--defaults", "-d", "--reference-doc", "--css", "--bibliography", "--csl", "--pdf-engine", "--self-contained", "--embed-resources", "--request-header",
		"--print-default-data-file", "--abbreviations", "--syntax-definition"}
	for i, c := range calls {
		if c[0] != "--sandbox" {
			t.Errorf("call %d does not start with --sandbox: %v", i, c)
		}
		if !strings.HasPrefix(c[1], "--data-dir=") {
			t.Errorf("call %d: %v", i, c)
		}
		dd := strings.TrimPrefix(c[1], "--data-dir=")
		if entries, err := os.ReadDir(dd); err == nil && len(entries) != 0 {
			t.Errorf("the data directory was not empty")
		}
		for _, a := range c {
			for _, f := range forbidden {
				if a == f || strings.HasPrefix(a, f+"=") {
					t.Errorf("call %d carries %s: %v", i, f, c)
				}
			}
			if strings.Contains(a, "/etc/passwd") || strings.Contains(a, "some input") {
				t.Errorf("call %d carries the request's text: %v", i, c)
			}
			if strings.HasPrefix(a, "-f") && len(a) > 2 || strings.Contains(a, "+") && a != "+RTS" {
				t.Errorf("call %d: an extension or odd flag %q", i, a)
			}
		}
		if last := c[len(c)-3:]; last[0] != "+RTS" || last[1] != "-M512m" || last[2] != "-RTS" {
			t.Errorf("call %d: the heap limit is missing: %v", i, c)
		}
	}
	// The exact shape of one of each.
	imp := calls[0]
	if imp[2] != "-f" || imp[4] != "-t" || imp[5] != "gfm" || imp[6] != "--wrap=none" || imp[7] != "input" {
		t.Errorf("import shape: %v", imp)
	}
	exp := calls[len(pandoc.ImportFormats())]
	if exp[2] != "-f" || exp[3] != "gfm-raw_html-yaml_metadata_block" || exp[4] != "-t" {
		t.Errorf("export shape: %v", exp)
	}
	// The call's folder is its own: the working directory was not the server's.
	cwd := strings.TrimSpace(readLog(t, dir, "cwd.log"))
	if cwd == "" || cwd == mustGetwd(t) || !strings.Contains(cwd, "scriobh-pandoc-") {
		t.Errorf("working directory %q", cwd)
	}
}

func mustGetwd(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestNamesThatAreNotOnTheListAreRefusedBeforeAnythingRuns(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	for _, name := range []string{"", "markdown", "html", "docx", "odt+smart", "odt -t html", "../odt", "lua", "ODT", "odt\n", "epub", "native", "json", "-f", "--version"} {
		if _, err := r.Import(context.Background(), b, name, []byte("x")); kindOf(err) != pandoc.KindBadFormat {
			t.Errorf("import %q: %v", name, err)
		}
		if _, err := r.Export(context.Background(), b, name, []byte("x")); kindOf(err) != pandoc.KindBadFormat {
			t.Errorf("export %q: %v", name, err)
		}
	}
	if readLog(t, dir, "args.log") != "" {
		t.Fatal("pandoc was run for a name that is not on the list")
	}
}

func TestEnvironmentIsBuiltHereAndNothingOfTheServersReachesIt(t *testing.T) {
	t.Setenv("HOME", "/home/server-user")
	t.Setenv("HTTP_PROXY", "http://proxy.example:3128")
	t.Setenv("https_proxy", "http://proxy.example:3128")
	t.Setenv("ALL_PROXY", "socks5://x")
	t.Setenv("PANDOC_DATA_DIR", "/etc")
	t.Setenv("PANDOC_ANYTHING", "1")
	t.Setenv("SCRIOBH_SECRET_KEY", "super-secret-key-value-0123456789")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("LUA_PATH", "/tmp/x")
	t.Setenv("XDG_DATA_HOME", "/home/server-user/.local")
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	if _, err := r.Import(context.Background(), b, "rst", []byte("x")); err != nil {
		t.Fatal(err)
	}
	env := readLog(t, dir, "env.log")
	for _, banned := range []string{"/home/server-user", "proxy", "PANDOC_", "super-secret", "AWS_", "LUA_PATH", "XDG_", "SCRIOBH_"} {
		if strings.Contains(env, banned) {
			t.Errorf("the environment holds %q:\n%s", banned, env)
		}
	}
	got := map[string]string{}
	for _, l := range strings.Split(env, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	for _, want := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "SOURCE_DATE_EPOCH"} {
		if got[want] == "" {
			t.Errorf("%s is not set", want)
		}
	}
	if !strings.Contains(got["HOME"], "scriobh-pandoc-") || got["HOME"] != got["TMPDIR"] {
		t.Errorf("HOME and TMPDIR are the call's own folder: %v", got)
	}
	for k := range got {
		switch k {
		case "HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "SOURCE_DATE_EPOCH", "PWD", "_", "SHLVL", "OLDPWD":
		default:
			t.Errorf("an unexpected variable %s", k)
		}
	}
}

func TestInputIsAFileThatIsNotTheRequestsName(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	data := []byte("Title\n=====\n\nThe input bytes \x00\x01 stay as they are.\n")
	if _, err := r.Import(context.Background(), b, "rst", data); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, dir, "input.log"); got != string(data) {
		t.Fatalf("the input differs: %q", got)
	}
	if _, err := r.Import(context.Background(), b, "rst", nil); kindOf(err) != pandoc.KindFailed {
		t.Fatalf("empty input: %v", err)
	}
	small := newRunner(t, pandoc.Limits{MaxInput: 100})
	if _, err := small.Import(context.Background(), locate(t, small, bin), "rst", make([]byte, 101)); kindOf(err) != pandoc.KindTooLarge {
		t.Fatalf("input over the cap: %v", err)
	}
	if _, err := small.Export(context.Background(), locate(t, small, bin), "odt", make([]byte, 101)); kindOf(err) != pandoc.KindTooLarge {
		t.Fatalf("text over the cap: %v", err)
	}
}

func leftovers(t *testing.T, r *pandoc.Runner, root string) []string {
	t.Helper()
	es, _ := os.ReadDir(root)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// gone reports whether the process has ended. One that has exited but has not
// been reaped (a zombie: it runs nothing) counts as ended; that is what an orphan
// is when PID 1 does not reap, as in a container whose first process is not an init.
func gone(pid int) bool {
	if probePID(pid) != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return os.IsNotExist(err) // reaped between the two calls; any other error: can not tell, still there
	}
	// "pid (name) S ...": the name may hold spaces and brackets, so the state follows the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] == 'Z'
}

func TestAHungPandocIsKilledWithEverythingItStarted(t *testing.T) {
	root := t.TempDir()
	r := pandoc.NewRunner(pandoc.Limits{Timeout: 400 * time.Millisecond}, root, "")
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "hang")
	start := time.Now()
	_, err := r.Import(context.Background(), b, "rst", []byte("x"))
	took := time.Since(start)
	if kindOf(err) != pandoc.KindTimeout {
		t.Fatalf("%v after %v", err, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 4*time.Second {
			t.Errorf("the hung Pandoc was stopped after %v", took)
		}
	})
	if l := leftovers(t, r, root); len(l) != 0 {
		t.Fatalf("the working folder was left: %v", l)
	}
	// A child it started, in the same group, dies with it.
	setMode(t, dir, "grandchild")
	_, err = r.Import(context.Background(), b, "rst", []byte("x"))
	if kindOf(err) != pandoc.KindTimeout {
		t.Fatalf("%v", err)
	}
	pidText := strings.TrimSpace(readLog(t, dir, "child.pid"))
	pid, _ := strconv.Atoi(pidText)
	if pid <= 0 {
		t.Fatalf("no child pid: %q", pidText)
	}
	if !eventually(t, 3*time.Second, func() bool { return gone(pid) }) {
		killPID(pid)
		t.Fatal("a process Pandoc started outlived the conversion")
	}
}

func TestCancellingTheRequestStopsPandoc(t *testing.T) {
	root := t.TempDir()
	r := pandoc.NewRunner(pandoc.Limits{}, root, "")
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := r.Import(ctx, b, "rst", []byte("x"))
	took := time.Since(start)
	if kindOf(err) != pandoc.KindCancelled {
		t.Fatalf("%v after %v", err, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 4*time.Second {
			t.Errorf("the cancelled Pandoc was stopped after %v", took)
		}
	})
	if l := leftovers(t, r, root); len(l) != 0 {
		t.Fatalf("left: %v", l)
	}
}

func TestOutputOverTheCapStopsPandoc(t *testing.T) {
	root := t.TempDir()
	r := pandoc.NewRunner(pandoc.Limits{MaxOutput: 1 << 20}, root, "")
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "flood")
	start := time.Now()
	_, err := r.Import(context.Background(), b, "rst", []byte("x"))
	took := time.Since(start)
	if kindOf(err) != pandoc.KindTooLarge {
		t.Fatalf("%v after %v", err, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second {
			t.Errorf("the flooding Pandoc was stopped after %v", took)
		}
	})
	if l := leftovers(t, r, root); len(l) != 0 {
		t.Fatalf("left: %v", l)
	}
	// An exported file over the cap.
	setMode(t, dir, "ok")
	os.WriteFile(filepath.Join(dir, "export.out"), make([]byte, 2<<20), 0o644)
	if _, err := r.Export(context.Background(), b, "odt", []byte("# x")); kindOf(err) != pandoc.KindTooLarge {
		t.Fatalf("a written file over the cap: %v", err)
	}
}

func TestStderrIsNeverKeptOrReturned(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	for _, mode := range []string{"fail", "stderrflood"} {
		setMode(t, dir, mode)
		_, err := r.Import(context.Background(), b, "rst", []byte("x"))
		if err == nil {
			t.Fatalf("%s: no error", mode)
		}
		e, _ := pandoc.AsError(err)
		if e == nil || strings.Contains(e.Error()+e.Message(), "SECRET") {
			t.Fatalf("%s: %v", mode, err)
		}
		// Both end the way a failed conversion does. (The flood once ran `yes` into the standard error with
		// its own output pointed there too, so `head` saw nothing and the call lasted until the 60 s timeout,
		// which also passed the checks above: the test measured the deadline, not the discard.)
		if e.Kind != pandoc.KindFailed {
			t.Errorf("%s: kind %q, want %q", mode, e.Kind, pandoc.KindFailed)
		}
	}
}

func TestMediaIsReadAsRegularFilesOnlyAndWithinLimits(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "escape")
	res, err := r.ImportWith(context.Background(), b, "rst", []byte("x"), pandoc.ImportOptions{ExtractMedia: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range res.Media {
		names = append(names, m.Path)
		if strings.Contains(string(m.Data), "localhost") || strings.Contains(m.Path, "..") {
			t.Errorf("read through a link: %s", m.Path)
		}
	}
	if len(res.Media) != 1 || res.Media[0].Path != "media/real.png" || string(res.Media[0].Data) != "real image" {
		t.Fatalf("media: %v", names)
	}
	if res.MediaSkipped < 3 { // the link, the pipe, and the file nine folders down
		t.Errorf("skipped %d", res.MediaSkipped)
	}
	// What it wrote outside its folder is not read, and not followed.
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err != nil {
		t.Log("the fake did not manage to write outside (fine)")
	}
	// Limits: count, one file, all files.
	setMode(t, dir, "ok")
	os.WriteFile(filepath.Join(dir, "media.png"), make([]byte, 5000), 0o644)
	small := newRunner(t, pandoc.Limits{MaxMediaFile: 1000})
	sb := locate(t, small, bin)
	res, err = small.ImportWith(context.Background(), sb, "rst", []byte("x"), pandoc.ImportOptions{ExtractMedia: true})
	if err != nil || len(res.Media) != 0 || res.MediaSkipped != 1 {
		t.Fatalf("a file over the per-file cap: %v %+v", err, res)
	}
	tot := newRunner(t, pandoc.Limits{MaxMediaTotal: 1000, MaxMediaFile: 10000})
	res, err = tot.ImportWith(context.Background(), locate(t, tot, bin), "rst", []byte("x"), pandoc.ImportOptions{ExtractMedia: true})
	if err != nil || len(res.Media) != 0 || res.MediaSkipped != 1 {
		t.Fatalf("over the total: %v %+v", err, res)
	}
}

func TestExportReadsOnlyTheFileItWasToWrite(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	os.WriteFile(filepath.Join(dir, "export.out"), []byte("ODT-BYTES"), 0o644)
	res, err := r.Export(context.Background(), b, "odt", []byte("# Title\n"))
	if err != nil || string(res.Body) != "ODT-BYTES" || res.Ext != "odt" || res.Version != "3.1.9" || !strings.Contains(res.ContentType, "opendocument") {
		t.Fatalf("%v %+v", err, res)
	}
	if got := readLog(t, dir, "input.log"); got != "# Title\n" {
		t.Fatalf("input %q", got)
	}
}

func TestOnlyTwoConversionsRunAtOnce(t *testing.T) {
	r := pandoc.NewRunner(pandoc.Limits{Concurrent: 5, Wait: 100 * time.Millisecond}, t.TempDir(), "")
	if r.Limits().Concurrent != 2 {
		t.Fatalf("concurrency %d: at most two", r.Limits().Concurrent)
	}
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "slow")
	var wg sync.WaitGroup
	var mu sync.Mutex
	busy, ok := 0, 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Import(context.Background(), b, "rst", []byte("x"))
			mu.Lock()
			defer mu.Unlock()
			switch kindOf(err) {
			case pandoc.KindBusy:
				busy++
			default:
				if err == nil {
					ok++
				}
			}
		}()
	}
	wg.Wait()
	if ok != 2 || busy != 2 {
		t.Fatalf("%d ran, %d were told to wait", ok, busy)
	}
}

// With the helper, Pandoc runs under resource limits.
func TestResourceLimitsAreSetBeforePandocStarts(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	r := pandoc.NewRunner(pandoc.Limits{CPUSeconds: 77, OpenFiles: 99}, t.TempDir(), self)
	if !r.HasResourceLimits() {
		t.Skip("no resource limits on this platform")
	}
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "limits")
	if _, err := r.Import(context.Background(), b, "rst", []byte("x")); err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(readLog(t, dir, "limits.log"))
	if len(lines) != 4 {
		t.Fatalf("limits: %v", lines)
	}
	if lines[0] != "77" || lines[1] != "99" || lines[2] != "0" || lines[3] == "unlimited" {
		t.Fatalf("cpu, files, core, file size: %v", lines)
	}
}

// Without the helper the gap is reported.
func TestNoHelperMeansNoResourceLimitsAndSaysSo(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	if r.HasResourceLimits() {
		t.Fatal("limits are claimed without the helper")
	}
}

func TestVersionParsing(t *testing.T) {
	cases := map[string]string{
		"pandoc 3.1.9\nFeatures: x":      "3.1.9",
		"pandoc 2.19.2":                  "2.19.2",
		"pandoc-cli 3.6":                 "3.6",
		"pandoc 3.1.11.1\nCompiled with": "3.1.11.1",
	}
	for in, want := range cases {
		if v, _, ok := pandoc.ParseVersion(in); !ok || v != want {
			t.Errorf("%q: %q %v", in, v, ok)
		}
	}
	for _, in := range []string{"", "pandoc", "hello 3.1.9", "pandocx 3.1.9", "pandoc three"} {
		if _, _, ok := pandoc.ParseVersion(in); ok {
			t.Errorf("%q parsed", in)
		}
	}
}
