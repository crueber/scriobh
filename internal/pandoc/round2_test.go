package pandoc_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/pandoc"
)

// Tests of the second review round. Everything here runs against the fake program (pandoctest): what a real
// Pandoc does with a real document is not shown by any of it.

// The helper does nothing when it is typed by hand: there is no pipe from a supervisor.
func TestHelperFlagIsInertWhenTypedByHand(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	_, bin := fakeDir(t)
	for name, mk := range map[string]func() *exec.Cmd{
		"no pipe and no token": func() *exec.Cmd {
			return exec.Command(self, "--scriobh-pandoc-helper", "{}", "--", bin, "--version")
		},
		"a guessed token and no pipe": func() *exec.Cmd {
			c := exec.Command(self, "--scriobh-pandoc-helper", "{}", "--", bin, "--version")
			c.Env = append(os.Environ(), "SCRIOBH_PANDOC_HELPER_TOKEN="+strings.Repeat("a", 64))
			return c
		},
		"a token and a pipe that disagree": func() *exec.Cmd {
			c := exec.Command(self, "--scriobh-pandoc-helper", "{}", "--", bin, "--version")
			pr, pw, _ := os.Pipe()
			_, _ = pw.WriteString(strings.Repeat("b", 64) + "\n" + bin + "\n")
			pw.Close()
			c.ExtraFiles = []*os.File{pr}
			c.Env = append(os.Environ(), "SCRIOBH_PANDOC_HELPER_TOKEN="+strings.Repeat("a", 64))
			return c
		},
		"the right token for another program": func() *exec.Cmd {
			c := exec.Command(self, "--scriobh-pandoc-helper", "{}", "--", "/usr/bin/id")
			pr, pw, _ := os.Pipe()
			tok := strings.Repeat("c", 64)
			_, _ = pw.WriteString(tok + "\n" + bin + "\n")
			pw.Close()
			c.ExtraFiles = []*os.File{pr}
			c.Env = append(os.Environ(), "SCRIOBH_PANDOC_HELPER_TOKEN="+tok)
			return c
		},
	} {
		out, err := mk().CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 125 {
			t.Errorf("%s: want exit 125, got %v (%q)", name, err, out)
		}
		if strings.Contains(string(out), "pandoc 3") || strings.Contains(string(out), "uid=") {
			t.Errorf("%s: the program ran: %q", name, out)
		}
	}
}

// Through the runner, the helper runs Pandoc, and Pandoc does not see the token.
func TestHelperRunsThePandocItWasToldAndStripsTheToken(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	r := pandoc.NewRunner(pandoc.Limits{}, t.TempDir(), self)
	if !r.HasResourceLimits() {
		t.Skip("no resource limits on this platform")
	}
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	if _, err := r.Import(context.Background(), b, "rst", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if env := readLog(t, dir, "env.log"); env == "" || strings.Contains(env, "SCRIOBH_PANDOC_HELPER_TOKEN") {
		t.Fatalf("the environment Pandoc saw:\n%s", env)
	}
}

// A person has one conversion running or waiting; another person is not held up by it.
func TestAPersonHasOneConversionAtATime(t *testing.T) {
	r := pandoc.NewRunner(pandoc.Limits{Wait: 100 * time.Millisecond}, t.TempDir(), "")
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "slow")
	run := func(who string, errs chan<- error) {
		_, err := r.Import(pandoc.WithWho(context.Background(), who), b, "rst", []byte("x"))
		errs <- err
	}
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); run("alice", errs) }()
	time.Sleep(200 * time.Millisecond) // alice's is running
	run("alice", errs)                 // her second is refused at once, not queued
	if err := <-errs; kindOf(err) != pandoc.KindBusy {
		t.Fatalf("a second conversion from one person: %v", err)
	}
	run("bob", errs) // another person gets the second place
	if err := <-errs; err != nil {
		t.Fatalf("another person was held up: %v", err)
	}
	wg.Wait()
	if err := <-errs; err != nil {
		t.Fatalf("the first conversion: %v", err)
	}
	// Once it has finished she can run another.
	run("alice", errs)
	if err := <-errs; err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
}

// Warnings on standard error, however many, do not fail a conversion that worked.
func TestStandardErrorOverTheLimitDoesNotFailAGoodConversion(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	b := locate(t, r, bin)
	setMode(t, dir, "stderrwarn")
	res, err := r.Import(context.Background(), b, "rst", []byte("x"))
	if err != nil || !strings.Contains(string(res.Markdown), "Hello world") {
		t.Fatalf("%v", err)
	}
}

// Below the free-space floor nothing is started.
func TestTooLittleFreeSpaceRefusesBeforeStarting(t *testing.T) {
	r := pandoc.NewRunner(pandoc.Limits{MinFreeBytes: 1 << 62}, t.TempDir(), "")
	dir, bin := fakeDir(t)
	b := locate(t, newRunner(t, pandoc.Limits{}), bin) // checking the program needs room too, so it is found by another runner
	if _, err := r.Import(context.Background(), b, "rst", []byte("x")); kindOf(err) != pandoc.KindFailed {
		t.Fatalf("%v", err)
	}
	if _, err := r.Export(context.Background(), b, "odt", []byte("x")); kindOf(err) != pandoc.KindFailed {
		t.Fatalf("%v", err)
	}
	if readLog(t, dir, "args.log") != "" {
		t.Fatal("Pandoc was started with too little room")
	}
}

// A program inside a refused folder (the data directory) is not accepted, whatever its owner and mode.
func TestAProgramUnderARefusedFolderIsNotAccepted(t *testing.T) {
	r := newRunner(t, pandoc.Limits{})
	dir, bin := fakeDir(t)
	r.RefuseUnder(dir)
	if _, err := r.Locate(context.Background(), bin); kindOf(err) != pandoc.KindUnsafe {
		t.Fatalf("%v", err)
	}
	// A sibling folder with the same name prefix is not "under" it.
	d2, bin2 := fakeDir(t)
	r.RefuseUnder(d2 + "-other")
	if _, err := r.Locate(context.Background(), bin2); err != nil {
		t.Fatalf("a folder that only shares a prefix was refused: %v", err)
	}
}

// Something a conversion makes beside its folder is deleted and the conversion fails.
func TestAFileMadeBesideTheCallFolderFailsTheConversion(t *testing.T) {
	root := t.TempDir()
	r := pandoc.NewRunner(pandoc.Limits{}, root, "")
	dir, bin := fakeDir(t)
	s, _ := os.ReadFile(bin)
	// the fake, changed to write next to its own working folder, as an extraction bug would
	if err := os.WriteFile(bin, []byte(strings.Replace(string(s), `pwd > "$D/cwd.log"`, `pwd > "$D/cwd.log"; echo owned > "$(dirname "$(pwd)")/planted"`, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	b := locate(t, r, bin)
	_, err := r.Import(context.Background(), b, "rst", []byte("x"))
	if kindOf(err) != pandoc.KindFailed {
		t.Fatalf("%v", err)
	}
	if _, serr := os.Stat(root + "/planted"); serr == nil {
		t.Fatal("what the conversion planted was left")
	}
	_ = dir
}

// A restore switches the bridge off, and keeps the path, so one click brings it back.
func TestRestoreSwitchesTheBridgeOff(t *testing.T) {
	f := newFx(t)
	yes := true
	if _, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin}); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, step := range backup.PostRestore {
		m, err := step(context.Background(), f.db, nil)
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	out, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.get", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(pandoc.ConfigOut)
	if c.Enabled || c.Path != f.bin {
		t.Fatalf("enabled %v, path %q", c.Enabled, c.Path)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "Pandoc") {
		t.Fatalf("the restore did not say so: %q", msgs)
	}
	// Nothing converts until an administrator switches it on again.
	if _, err := f.svc.ToMarkdown(context.Background(), "rst", []byte("x"), pandoc.ImportOptions{}); kindOf(err) != pandoc.KindDisabled {
		t.Fatalf("%v", err)
	}
	// A bridge that was off stays off and the step says nothing.
	for _, step := range backup.PostRestore {
		if m, err := step(context.Background(), f.db, nil); err != nil || strings.Contains(m, "Pandoc") {
			t.Fatalf("%q %v", m, err)
		}
	}
}

// problem_kind never leaves its declared enum.
func TestConfigProblemKindStaysInsideItsEnum(t *testing.T) {
	f := newFx(t)
	out, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.get", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(pandoc.ConfigOut)
	allowed := map[string]bool{"": true, "disabled": true, "not_found": true, "unsafe_binary": true, "version": true, "unsupported_platform": true, "busy": true, "cancelled": true, "failed": true}
	if !allowed[c.ProblemKind] {
		t.Fatalf("%q", c.ProblemKind)
	}
}

// The settings page says a restore switched the bridge off only when one did: a restore sets the mark whatever the
// path, an administrator switching off by hand does not, and switching on clears it.
func TestRestoreLeavesAMarkOnlySwitchingOnClears(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	yes, no := true, false
	get := func() pandoc.ConfigOut {
		out, err := f.reg.Call(ctx, f.admin, "pandoc.config.get", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		return out.(pandoc.ConfigOut)
	}
	if _, err := f.reg.Call(ctx, f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.Call(ctx, f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	if get().RestorePaused {
		t.Fatal("an administrator switching off by hand is not a restore")
	}
	if _, err := f.reg.Call(ctx, f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes}); err != nil {
		t.Fatal(err)
	}
	for _, step := range backup.PostRestore {
		if _, err := step(ctx, f.db, nil); err != nil {
			t.Fatal(err)
		}
	}
	if c := get(); c.Enabled || !c.RestorePaused {
		t.Fatalf("after a restore: enabled %v, paused %v", c.Enabled, c.RestorePaused)
	}
	empty := ""
	if _, err := f.reg.Call(ctx, f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &no, Path: &empty}); err != nil {
		t.Fatal(err)
	}
	if !get().RestorePaused {
		t.Fatal("saving the settings while off lifted a restore's pause; only switching on does")
	}
	if _, err := f.reg.Call(ctx, f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin}); err != nil {
		t.Fatal(err)
	}
	if get().RestorePaused {
		t.Fatal("switching on left the mark")
	}
}

// Off means not run: reading the settings does not start the program, after a restore or before the bridge was ever on.
func TestConfigGetRunsNothingWhileTheBridgeIsOff(t *testing.T) {
	versions := func(f *fx) int { return strings.Count(readLog(t, f.dir, "version.log"), "run") }
	f := newFx(t)
	t.Setenv("PATH", f.dir) // found on PATH, and never switched on
	for i := 0; i < 2; i++ {
		out, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.get", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if c := out.(pandoc.ConfigOut); c.Enabled || c.Found || c.ProblemKind != "disabled" {
			t.Fatalf("%+v", c)
		}
	}
	if n := versions(f); n != 0 {
		t.Fatalf("the program was run %d time(s) to read the settings of a bridge that was never on", n)
	}
	// On: it is run (the check), then a restore switches it off and reading the settings runs nothing more.
	yes := true
	if _, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin}); err != nil {
		t.Fatal(err)
	}
	if versions(f) == 0 {
		t.Fatal("switching on did not check the program")
	}
	for _, step := range backup.PostRestore {
		if _, err := step(context.Background(), f.db, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := versions(f)
	for i := 0; i < 3; i++ {
		out, err := f.reg.Call(context.Background(), f.admin, "pandoc.config.get", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		c := out.(pandoc.ConfigOut)
		if c.Enabled || c.Path != f.bin || c.ProblemKind != "disabled" {
			t.Fatalf("%+v", c)
		}
	}
	if n := versions(f); n != before {
		t.Fatalf("reading the settings after a restore ran the program %d time(s)", n-before)
	}
	// A dry run of the read is no different.
	if _, err := f.reg.CallDry(context.Background(), f.admin, "pandoc.config.get", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if n := versions(f); n != before {
		t.Fatalf("a dry run ran the program")
	}
}
