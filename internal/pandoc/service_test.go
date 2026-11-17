package pandoc_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t      *testing.T
	reg    *ops.Registry
	svc    *pandoc.Service
	admin  ops.Principal
	writer ops.Principal
	dir    string
	bin    string
	db     *db.DB
}

func newFx(t *testing.T) *fx {
	t.Helper()
	d := testutil.DB(t)
	dir, bin := fakeDir(t)
	reg := registry.BuildWith(d, events.NewStore(d, nil), t.TempDir(), registry.Options{Pandoc: pandoc.Options{TempRoot: t.TempDir()}})
	svc, ok := pandoc.ServiceOf(reg.Env())
	if !ok {
		t.Fatal("no service")
	}
	p := func(role, name string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, name+"@test.local"), Role: role, Name: name, Via: "session"}
	}
	return &fx{t: t, reg: reg, svc: svc, admin: p(ops.RoleAdmin, "admin"), writer: p(ops.RoleWriter, "writer"), dir: dir, bin: bin, db: d}
}

func (f *fx) call(p ops.Principal, name string, in any) (any, *ops.Error) {
	out, err := f.reg.Call(context.Background(), p, name, in)
	return out, ops.AsError(err)
}

func TestBridgeIsOffUntilAnAdministratorSwitchesItOn(t *testing.T) {
	f := newFx(t)
	out, err := f.call(f.admin, "pandoc.config.get", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(pandoc.ConfigOut)
	if c.Enabled || len(c.ImportFormats) != 10 || len(c.ExportFormats) != 5 || c.MinVersion != "3.1.6" {
		t.Fatalf("%+v", c)
	}
	// Disabled: nothing converts, and nothing is run.
	if _, err := f.svc.ToMarkdown(context.Background(), "rst", []byte("x"), pandoc.ImportOptions{}); err == nil {
		t.Fatal("converted while off")
	} else if e, _ := pandoc.AsError(err); e == nil || e.Kind != pandoc.KindDisabled {
		t.Fatalf("%v", err)
	}
	if _, err := f.svc.FromMarkdown(context.Background(), "odt", []byte("x")); err == nil {
		t.Fatal("converted while off")
	}
	if f.svc.Version(context.Background()) != "" {
		t.Fatal("a version while off")
	}
	// Found on PATH is not enough: it must be switched on.
	t.Setenv("PATH", f.dir)
	if f.svc.Status(context.Background()).Err == nil {
		t.Fatal("usable while off")
	}
}

func TestSettingsAreForAdministratorsInBrowserSessionsOnly(t *testing.T) {
	f := newFx(t)
	yes := true
	for _, name := range []string{"pandoc.config.get", "pandoc.config.set", "pandoc.config.test"} {
		var in any = struct{}{}
		if name == "pandoc.config.set" {
			in = pandoc.SetIn{Enabled: &yes}
		}
		if _, err := f.call(f.writer, name, in); err == nil || (err.Code != ops.CodeForbidden && err.Code != ops.CodeNotFound) {
			t.Errorf("%s as a writer: %v", name, err)
		}
		key := f.admin
		key.Via, key.Scope = "apikey", ops.ScopeAdmin
		if _, err := f.call(key, name, in); err == nil || err.Code != ops.CodeForbidden {
			t.Errorf("%s with an API key: %v", name, err)
		}
	}
}

func TestSwitchingOnNeedsABinaryThatPasses(t *testing.T) {
	f := newFx(t)
	yes := true
	bad := "/nonexistent/pandoc"
	_, err := f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &bad})
	if err == nil || err.Code != ops.CodeInvalid {
		t.Fatalf("%v", err)
	}
	if c, _ := f.call(f.admin, "pandoc.config.get", struct{}{}); c.(pandoc.ConfigOut).Enabled {
		t.Fatal("a refused switch-on changed the setting")
	}
	rel := "pandoc"
	if _, err := f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &rel}); err == nil || err.Code != ops.CodeInvalid {
		t.Fatalf("a relative path: %v", err)
	}
	weird := "/usr/bin/pandoc\n--lua-filter=x"
	if _, err := f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Path: &weird}); err == nil || err.Code != ops.CodeInvalid {
		t.Fatalf("%v", err)
	}
	// An unsafe file is refused with the reason.
	writable := filepath.Join(t.TempDir(), "pandoc")
	os.WriteFile(writable, []byte(fakeScript), 0o777)
	os.Chmod(writable, 0o777)
	if _, err := f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &writable}); err == nil || !strings.Contains(err.Message, "changed by other users") {
		t.Fatalf("%v", err)
	}
	// A good one.
	out, err := f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(pandoc.ConfigOut)
	if !c.Enabled || !c.Found || c.Version != "3.1.9" || c.Path != f.bin || c.Problem != "" {
		t.Fatalf("%+v", c)
	}
	imp, ierr := f.svc.ToMarkdown(context.Background(), "rst", []byte("x"), pandoc.ImportOptions{})
	if ierr != nil || !strings.Contains(string(imp.Markdown), "Hello world") || imp.Version != "3.1.9" {
		t.Fatalf("%v %+v", ierr, imp)
	}
	if v := f.svc.Version(context.Background()); v != "3.1.9" {
		t.Fatalf("version %q", v)
	}
	// The audit line says it was switched on and not where the program is.
	rows, _ := f.reg.Env().DB.Read().Query(`SELECT action, detail_json FROM audit_log WHERE action = 'pandoc.config.set'`)
	n := 0
	for rows.Next() {
		var a, d string
		rows.Scan(&a, &d)
		n++
		if strings.Contains(d, f.bin) || strings.Contains(d, "/") {
			t.Errorf("the audit line names a path: %s", d)
		}
	}
	rows.Close()
	if n != 1 {
		t.Fatalf("%d audit lines", n)
	}
	// Switched off again: stops at once.
	no := false
	f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &no})
	if _, err := f.svc.ToMarkdown(context.Background(), "rst", []byte("x"), pandoc.ImportOptions{}); err == nil {
		t.Fatal("converted after being switched off")
	}
}

func TestConfigTestRunsBothDirectionsWithProductionArguments(t *testing.T) {
	f := newFx(t)
	yes := true
	f.call(f.admin, "pandoc.config.set", pandoc.SetIn{Enabled: &yes, Path: &f.bin})
	os.WriteFile(filepath.Join(f.dir, "md.out"), []byte("A short test paragraph.\n"), 0o644)
	os.WriteFile(filepath.Join(f.dir, "export.out"), []byte("A short test paragraph.\n"), 0o644)
	out, err := f.call(f.admin, "pandoc.config.test", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(pandoc.TestOut)
	if !r.OK || !r.Import.OK || !r.Export.OK || r.Version != "3.1.9" {
		t.Fatalf("%+v", r)
	}
	calls := callsOf(t, f.dir)
	if len(calls) != 2 || calls[0][0] != "--sandbox" || calls[1][0] != "--sandbox" {
		t.Fatalf("the test did not use the production arguments: %v", calls)
	}
	// An incompatible build fails at configuration time, with a plain reason.
	setMode(t, f.dir, "fail")
	out, _ = f.call(f.admin, "pandoc.config.test", struct{}{})
	r = out.(pandoc.TestOut)
	if r.OK || r.Import.Problem == "" || strings.Contains(r.Import.Problem+r.Export.Problem, "SECRET") {
		t.Fatalf("%+v", r)
	}
	// A memory setting is passed as Pandoc's own heap limit; 0 passes none.
	setMode(t, f.dir, "ok")
	mem := 256
	f.call(f.admin, "pandoc.config.set", pandoc.SetIn{MemoryMB: &mem})
	f.call(f.admin, "pandoc.config.test", struct{}{})
	calls = callsOf(t, f.dir)
	last := calls[len(calls)-1]
	if last[len(last)-2] != "-M256m" {
		t.Fatalf("%v", last)
	}
	zero := 0
	f.call(f.admin, "pandoc.config.set", pandoc.SetIn{MemoryMB: &zero})
	f.call(f.admin, "pandoc.config.test", struct{}{})
	calls = callsOf(t, f.dir)
	for _, a := range calls[len(calls)-1] {
		if a == "+RTS" {
			t.Fatal("memory 0 still passes RTS options")
		}
	}
}
