package site_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sitedomain "github.com/crueber/scriobh/internal/domain/site"
	"github.com/crueber/scriobh/internal/registry"
	sitegen "github.com/crueber/scriobh/internal/site"
)

func TestParsePublishedChecksTheRangeOnEveryForm(t *testing.T) {
	for _, bad := range []string{"0001-01-01", "0000-12-31", "1969-12-31", "1969-12-31T23:59:59Z", "10000-01-01", "yesterday", "2026-13-01", "2026-02-30"} {
		if got, err := sitedomain.ParsePublished(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
	for in, want := range map[string]string{"2026-03-01": "2026-03-01T00:00:00Z", "2026-03-01T10:00:00+01:00": "2026-03-01T09:00:00Z", "1970-01-01": "1970-01-01T00:00:00Z", "9999-12-31": "9999-12-31T00:00:00Z", "": ""} {
		if got, err := sitedomain.ParsePublished(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
}

// A project deleted for good takes its published site with it, but not while a
// publish of it is writing.
func TestThePurgeWaitsForARunningPublish(t *testing.T) {
	root := t.TempDir()
	reg := registry.Build(nil, nil, t.TempDir())
	svc := sitedomain.NewService(sitedomain.Config{Enabled: true, Root: root})
	reg.Env().SetService(sitedomain.ServiceName, svc)
	name := sitegen.DirName("blog", "prj_purge1")
	files := []sitegen.File{{Path: "index.html", Data: []byte("<!doctype html>")}}
	if _, err := sitegen.WriteDir(root, name, "prj_purge1", "h", files); err != nil {
		t.Fatal(err)
	}
	unlock := svc.Hold("prj_purge1") // a publish is writing
	done := make(chan struct{})
	go func() {
		reg.Env().ProjectPurged(context.Background(), "prj_purge1")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the purge went ahead while a publish held the project")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(root, name, "public", "index.html")); err != nil {
		t.Fatal("the site was removed under a running publish")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge never ran")
	}
	if _, err := os.Stat(filepath.Join(root, name, "public")); err == nil {
		t.Error("the purged project's site is still there")
	}
}
