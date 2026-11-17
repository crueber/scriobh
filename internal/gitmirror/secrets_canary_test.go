package gitmirror_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The trigger's secret and the secret part of its address, the signing key's private half: in no log line, no
// event, no audit line, no row in the clear (the database file itself is searched), no file of the data
// directory, no op result or error, no commit or pushed object. The one place the trigger's secret appears is the
// answer that made it.
func TestTheTriggerSecretItsPathAndTheSigningKeyAreNowhere(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	url, _, dir := h.bareRemote()
	h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid})
	r := h.addRemote(url, func(in *gitmirror.CreateIn) { in.SignCommits = ptr(true) })
	out := h.setTrigger(r.ID, rc.url("/hooks/PATH-SECRET-CANARY-9f3a?token=QUERY-SECRET-CANARY-77c1"))
	pem, err := h.svc.SigningPEM(h.pid)
	if err != nil {
		t.Fatal(err)
	}
	var pemBody []string
	for _, l := range strings.Split(pem, "\n") {
		if len(l) > 40 && !strings.HasPrefix(l, "-----") {
			pemBody = append(pemBody, l)
		}
	}
	if len(pemBody) == 0 {
		t.Fatal("no key body to search for")
	}
	canaries := append([]string{out.Secret, "PATH-SECRET-CANARY-9f3a", "QUERY-SECRET-CANARY-77c1", "BEGIN OPENSSH PRIVATE KEY"}, pemBody...)

	var seen []string
	note := func(v any, err error) {
		if err != nil {
			seen = append(seen, err.Error(), mustJSON(t, ops.AsError(err)))
		}
		if v != nil {
			seen = append(seen, mustJSON(t, v))
		}
	}
	p := h.pushRemote(r.ID)
	note(p, nil)
	waitFor(t, "the delivery", func() bool { return len(rc.received()) == 1 })
	rc.set(500, "")
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	h.pushRemote(r.ID)
	waitFor(t, "a failing delivery", func() bool { return len(rc.received()) == 2 })
	v, err := h.call(h.owner, "git.trigger.test", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID})
	note(v, err)
	v, err = h.call(h.owner, "git.trigger.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID})
	note(v, err)
	v, err = h.call(h.owner, "git.signing.get", gitmirror.ProjectIn{ProjectID: h.pid})
	note(v, err)
	v, err = h.call(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID})
	note(v, err)
	v, err = h.call(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid})
	note(v, err)
	v, err = h.call(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid, RemoteID: r.ID})
	note(v, err)
	_, err = h.call(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: r.ID, URL: ptr("http://not-https.example.org/x")})
	note(nil, err)
	pausedByRestore(t, h)
	v, err = h.call(h.owner, "git.trigger.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID})
	note(v, err)

	for _, c := range canaries {
		for _, s := range seen {
			if strings.Contains(s, c) {
				t.Errorf("%.30q is in an op result or error: %.200s", c, s)
			}
		}
		if strings.Contains(h.logs.String(), c) {
			t.Errorf("%.30q is in the log", c)
		}
		for _, q := range []string{`SELECT group_concat(payload_json || type) FROM events`, `SELECT group_concat(detail_json || action) FROM audit_log`,
			`SELECT group_concat(url || last_status || disabled_reason || secret_hint) FROM git_triggers`, `SELECT group_concat(last_status || commit_sha) FROM git_trigger_deliveries`,
			`SELECT group_concat(public_key || fingerprint) FROM git_signing_keys`, `SELECT group_concat(detail_json || error_message || manifest_json) FROM git_pushes`, `SELECT group_concat(manifest_json) FROM exports`} {
			var s *string
			if err := h.d.Read().QueryRow(q).Scan(&s); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if s != nil && strings.Contains(*s, c) {
				t.Errorf("%.30q is in the database (%s)", c, q)
			}
		}
		// Every file of the data directory, the database and its log included: what is stored is sealed.
		_ = filepath.Walk(h.dataDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				if b, _ := os.ReadFile(path); strings.Contains(string(b), c) {
					t.Errorf("%.30q is in %s", c, path)
				}
			}
			return nil
		})
		if strings.Contains(everythingIn(t, dir), c) {
			t.Errorf("%.30q was pushed", c)
		}
		if strings.Contains(everythingIn(t, h.svc.RepoDir(h.pid, r.ID)), c) {
			t.Errorf("%.30q is in the local repository", c)
		}
	}
}
