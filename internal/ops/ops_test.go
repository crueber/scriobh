package ops

import (
	"context"
	"strings"
	"testing"
)

type pingIn struct {
	Name string `json:"name" desc:"who to greet"`
	N    int    `json:"n,omitempty"`
}

func (in *pingIn) Validate() error {
	if in.Name == "" {
		return Invalid("name", "name is required")
	}
	return nil
}

type pingOut struct {
	Msg string `json:"msg"`
}

type projIn struct {
	ID string `path:"id" json:"-"`
}

func (p projIn) ProjectRef() string { return p.ID }

func newReg() *Registry {
	r := NewRegistry(&Env{ProjectRole: func(_ context.Context, uid, pid string) (string, bool) {
		if uid == "member" && pid == "p1" {
			return ProjectEditor, true
		}
		return "", false
	}})
	Register(r, Op{Name: "ping.say", Method: "POST", Path: "/api/v1/ping", Desc: "Greet.", Auth: Authenticated()},
		func(c *Ctx, in pingIn) (pingOut, error) { return pingOut{"hi " + in.Name}, nil })
	Register(r, Op{Name: "proj.read", Method: "GET", Path: "/api/v1/proj/{id}", Desc: "Read.", Auth: RequireProject(ProjectReviewer)},
		func(c *Ctx, in projIn) (pingOut, error) { return pingOut{"ok"}, nil })
	return r
}

func TestCallAuthAndValidation(t *testing.T) {
	r := newReg()
	ctx := context.Background()
	if _, err := r.Call(ctx, Principal{}, "ping.say", pingIn{Name: "x"}); AsError(err).Code != CodeUnauthenticated {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := r.Call(ctx, Principal{UserID: "u"}, "ping.say", pingIn{}); AsError(err).Code != CodeInvalid || AsError(err).Field != "name" {
		t.Errorf("validation: %v", err)
	}
	out, err := r.Call(ctx, Principal{UserID: "u"}, "ping.say", &pingIn{Name: "Máire"})
	if err != nil || out.(pingOut).Msg != "hi Máire" {
		t.Errorf("happy path: %v %v", out, err)
	}
	if _, err := r.Call(ctx, Principal{UserID: "stranger"}, "proj.read", projIn{ID: "p1"}); AsError(err).Code != CodeNotFound {
		t.Errorf("non-member must get not_found, got %v", err)
	}
	if _, err := r.Call(ctx, Principal{UserID: "member"}, "proj.read", projIn{ID: "p1"}); err != nil {
		t.Errorf("member: %v", err)
	}
	// A key pinned to another project cannot reach this one.
	if _, err := r.Call(ctx, Principal{UserID: "member", ProjectID: "p2"}, "proj.read", projIn{ID: "p1"}); AsError(err).Code != CodeNotFound {
		t.Errorf("pinned key: %v", err)
	}
	if _, err := r.Call(ctx, Principal{UserID: "u"}, "no.such", nil); AsError(err).Code != CodeNotFound {
		t.Errorf("unknown op: %v", err)
	}
}

func TestCallJSONRejectsUnknownFields(t *testing.T) {
	r := newReg()
	if _, err := r.CallJSON(context.Background(), Principal{UserID: "u"}, "ping.say", []byte(`{"name":"a","sneaky":1}`)); AsError(err).Code != CodeInvalid {
		t.Errorf("unknown field: %v", err)
	}
}

func TestRegistrationRules(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected a panic", name)
			}
		}()
		f()
	}
	r := newReg()
	h := func(c *Ctx, in pingIn) (pingOut, error) { return pingOut{}, nil }
	mustPanic("duplicate name", func() {
		Register(r, Op{Name: "ping.say", Method: "POST", Path: "/api/v1/other", Desc: "d", Auth: Authenticated()}, h)
	})
	mustPanic("duplicate route", func() {
		Register(r, Op{Name: "ping.two", Method: "POST", Path: "/api/v1/ping", Desc: "d", Auth: Authenticated()}, h)
	})
	mustPanic("no auth", func() { Register(r, Op{Name: "a.b", Method: "GET", Path: "/api/v1/x", Desc: "d"}, h) })
	mustPanic("no desc", func() { Register(r, Op{Name: "a.c", Method: "GET", Path: "/api/v1/y", Auth: Authenticated()}, h) })
	mustPanic("bad name", func() {
		Register(r, Op{Name: "Bad-Name", Method: "GET", Path: "/api/v1/z", Desc: "d", Auth: Authenticated()}, h)
	})
	mustPanic("project auth without ProjectRef", func() {
		Register(r, Op{Name: "a.d", Method: "GET", Path: "/api/v1/w", Desc: "d", Auth: RequireProject(ProjectReader)}, h)
	})
}

func TestSchemaIsDeterministicAndHashChanges(t *testing.T) {
	a, b := newReg().Schema(), newReg().Schema()
	if a.Hash != b.Hash || !strings.HasPrefix(a.Hash, "sha256:") {
		t.Fatalf("hash not stable: %s vs %s", a.Hash, b.Hash)
	}
	r := newReg()
	Register(r, Op{Name: "ping.more", Method: "GET", Path: "/api/v1/more", Desc: "More.", Auth: Authenticated()},
		func(c *Ctx, in projIn) (pingOut, error) { return pingOut{}, nil })
	if r.Schema().Hash == a.Hash {
		t.Error("adding an op must change the schema hash")
	}
	var say OpSchema
	for _, o := range a.Ops {
		if o.Name == "ping.say" {
			say = o
		}
	}
	props := say.Input["properties"].(map[string]any)
	if props["name"].(map[string]any)["description"] != "who to greet" {
		t.Error("desc tags must reach the schema")
	}
	req := say.Input["required"].([]string)
	if len(req) != 1 || req[0] != "name" {
		t.Errorf("required = %v", req)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := map[Code]int{CodeInvalid: 422, CodeUnauthenticated: 401, CodeNotFound: 404, CodeConflict: 409, CodeRateLimited: 429, CodeForbidden: 403,
		CodeDryRunUnsupported: 422, CodeIdempotencyReuse: 422}
	for c, want := range cases {
		if c.HTTPStatus() != want {
			t.Errorf("%s: http %d, want %d", c, c.HTTPStatus(), want)
		}
	}
	if AsError(context.Canceled).Code != CodeInternal {
		t.Error("foreign errors must become internal and must not leak their text")
	}
	if strings.Contains(AsError(context.Canceled).Message, "cancel") {
		t.Error("internal errors must not leak details")
	}
}
