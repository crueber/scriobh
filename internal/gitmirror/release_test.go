package gitmirror_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// stubCompiler stands in for the server's export ops: it returns a file of a chosen size and manifest hash.
type stubCompiler struct {
	mu     sync.Mutex
	hash   string
	size   int
	err    error
	calls  int
	by     string
	reqs   []gitmirror.EditionRequest
	holdOn chan struct{}
}

func (c *stubCompiler) compile(ctx context.Context, p ops.Principal, req gitmirror.EditionRequest) (*gitmirror.Edition, error) {
	c.mu.Lock()
	c.calls++
	c.by = p.UserID
	c.reqs = append(c.reqs, req)
	hash, size, err, hold := c.hash, c.size, c.err, c.holdOn
	c.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if err != nil {
		return nil, err
	}
	body := []byte(strings.Repeat("EPUB", size/4+1))[:size]
	sum := sha256.Sum256(body)
	return &gitmirror.Edition{Filename: "the-salt-road-epub3.epub", ContentType: "application/epub+zip", Body: body, SHA256: hex.EncodeToString(sum[:]), InputHash: hash, Documents: 2}, nil
}

func (c *stubCompiler) set(hash string, size int) {
	c.mu.Lock()
	c.hash, c.size = hash, size
	c.mu.Unlock()
}

const hashA = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
const hashB = "b1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func setupRelease(t *testing.T, flavor string, tune ...func(*gitmirror.Options)) (*harness, *fakeHost, gitmirror.Remote, *stubCompiler) {
	t.Helper()
	f := newFakeHost(t, flavor)
	st := &stubCompiler{hash: hashA, size: 3000}
	h := newHarness(t, append([]func(*gitmirror.Options){func(o *gitmirror.Options) { o.Compile = st.compile }}, tune...)...)
	h.start()
	acct := h.connect(f)
	out, push := h.publish(h.planIn(acct.ID), "")
	if push.Status != "ok" {
		t.Fatalf("publish: %+v", push)
	}
	return h, f, out.Remote, st
}

func (h *harness) release(remoteID, tag, format string) gitmirror.Release {
	h.t.Helper()
	out := h.must(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: remoteID, Tag: tag, Format: format}).(gitmirror.RepoReleaseOut)
	h.svc.WaitReleases()
	got := h.must(h.owner, "repo.release_get", gitmirror.ReleaseGetIn{ProjectID: h.pid, ReleaseID: out.Item.ID}).(struct {
		Item gitmirror.Release `json:"item"`
	})
	return got.Item
}

func TestAReleaseCarriesTheEditionTheHashesAndNothingTwice(t *testing.T) {
	for _, flavor := range flavors {
		t.Run(flavor, func(t *testing.T) {
			h, f, rem, st := setupRelease(t, flavor)
			rel := h.release(rem.ID, "v1.0", "epub")
			if rel.Status != "ok" || rel.Reused || rel.AssetBytes != 3000 || rel.InputHash != hashA || len(rel.FileSHA256) != 64 {
				t.Fatalf("release = %+v", rel)
			}
			if st.by != h.owner.UserID || st.reqs[0].Format != "epub" || st.reqs[0].Preset != "epub3" || st.reqs[0].ProjectID != h.pid {
				t.Errorf("compiled as %s with %+v", st.by, st.reqs)
			}
			if want := "the-salt-road-epub3-v1.0-" + hashA[:12] + ".epub"; rel.AssetName != want {
				t.Errorf("asset name = %q", rel.AssetName)
			}
			host := f.release("mairead", "salt-road", "v1.0")
			if host == nil || len(host.Assets) != 1 || host.Assets[0].Name != rel.AssetName || host.Assets[0].Size != 3000 {
				t.Fatalf("at the host: %+v", host)
			}
			if !strings.Contains(host.Body, "scriobh-edition-manifest: "+hashA) || !strings.Contains(host.Body, "file-sha256: "+rel.FileSHA256) {
				t.Errorf("the notes do not carry the hashes: %q", host.Body)
			}
			if sum := sha256.Sum256(host.Assets[0].Data); hex.EncodeToString(sum[:]) != rel.FileSHA256 {
				t.Errorf("the uploaded file is not the one that was hashed")
			}
			if host.Target != h.remoteByID(rem.ID).Status.LastCommitSHA {
				t.Errorf("the release is at %s, the remote has %s", host.Target, h.remoteByID(rem.ID).Status.LastCommitSHA)
			}
			if !strings.HasPrefix(rel.ReleaseURL, "https://"+f.host()+"/mairead/salt-road/releases/tag/v1.0") {
				t.Errorf("release url = %q (it is built here, never taken from the host's answer)", rel.ReleaseURL)
			}
			// A retry: same tag, same manifest hash. Nothing uploaded, one asset.
			uploads := f.uploads
			again := h.release(rem.ID, "v1.0", "epub")
			if again.Status != "ok" || !again.Reused || f.uploads != uploads || len(f.release("mairead", "salt-road", "v1.0").Assets) != 1 {
				t.Errorf("retry = %+v (uploads %d then %d)", again, uploads, f.uploads)
			}
			// Another manifest hash under the same tag is another edition: refused, the release untouched.
			st.set(hashB, 3100)
			other := h.release(rem.ID, "v1.0", "epub")
			if other.Status != "error" || other.ErrorCode != "tag_conflict" || f.uploads != uploads || len(f.release("mairead", "salt-road", "v1.0").Assets) != 1 {
				t.Errorf("another edition under the tag: %+v", other)
			}
			// A new tag is a new release.
			fresh := h.release(rem.ID, "v1.1", "epub")
			if fresh.Status != "ok" || f.release("mairead", "salt-road", "v1.1") == nil {
				t.Errorf("a new tag: %+v", fresh)
			}
		})
	}
}

func TestAReleaseThatWasStartedIsFinishedAndAHandMadeOneIsLeftAlone(t *testing.T) {
	h, f, rem, st := setupRelease(t, "forgejo")
	// A release this server made whose upload did not complete: the release is there, the asset is cut short.
	name := "the-salt-road-epub3-v2-" + hashA[:12] + ".epub"
	f.mu.Lock()
	f.rels["mairead/salt-road/v2"] = &fakeRel{ID: 900, Tag: "v2", Body: "x\nscriobh-edition-manifest: " + hashA + "\n", Assets: []*fakeAsset{{ID: 901, Name: name, Size: 10, Data: []byte("short")}}}
	f.mu.Unlock()
	rel := h.release(rem.ID, "v2", "epub")
	got := f.release("mairead", "salt-road", "v2")
	if rel.Status != "ok" || rel.Reused || len(got.Assets) != 1 || got.Assets[0].Size != 3000 {
		t.Errorf("a cut-short asset: %+v / %+v", rel, got.Assets)
	}
	// A release somebody made by hand at that tag: not ours, not touched.
	f.mu.Lock()
	f.rels["mairead/salt-road/v3"] = &fakeRel{ID: 910, Tag: "v3", Body: "Release notes written by hand."}
	f.mu.Unlock()
	uploads := f.uploads
	hand := h.release(rem.ID, "v3", "epub")
	if hand.Status != "error" || hand.ErrorCode != "tag_conflict" || f.uploads != uploads || len(f.release("mairead", "salt-road", "v3").Assets) != 0 {
		t.Errorf("a hand-made release: %+v", hand)
	}
	_ = st
}

func TestAReleaseIsCappedBeforeAnythingIsCreated(t *testing.T) {
	h, f, rem, st := setupRelease(t, "forgejo", func(o *gitmirror.Options) { o.ReleaseMaxBytes = 2048 })
	st.set(hashA, 4096)
	rel := h.release(rem.ID, "v1", "epub")
	if rel.Status != "error" || rel.ErrorCode != "too_large" || f.release("mairead", "salt-road", "v1") != nil || f.uploads != 0 {
		t.Errorf("release = %+v", rel)
	}
}

func TestAReleaseNeedsAPushedGitHubOrForgejoRemoteAndSaysWhatItWillDo(t *testing.T) {
	h, f, rem, st := setupRelease(t, "forgejo")
	// A remote that has not pushed yet.
	fresh := h.addRemote("https://forge.example.org/mairead/other.git", func(in *gitmirror.CreateIn) {
		in.Name = "Fresh"
		in.Token = "tok_abcdefghijklmnopqrstuvwxyz"
		in.Preset = "forgejo"
	})
	if _, err := h.call(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: fresh.ID, Tag: "v1", Format: "epub"}); h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "Push first") {
		t.Errorf("a remote that has not pushed: %v", err)
	}
	// A plain Git server with no API.
	url, _, _ := h.bareRemote()
	plain := h.addRemote(url, func(in *gitmirror.CreateIn) { in.Name = "Plain" })
	h.pushRemote(plain.ID)
	if _, err := h.call(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: plain.ID, Tag: "v1", Format: "epub"}); err == nil || !strings.Contains(err.Error(), "GitHub or a Forgejo") {
		t.Errorf("a remote that is not at a host with an API: %v", err)
	}
	// What is asked is checked.
	for name, in := range map[string]gitmirror.RepoReleaseIn{
		"a tag with a slash": {Tag: "v1/x", Format: "epub"}, "no tag": {Format: "epub"}, "a leading dot": {Tag: ".v1", Format: "epub"},
		"an unknown format": {Tag: "v1", Format: "mobi"}, "a manuscript preset": {Tag: "v1", Format: "docx", Preset: "shunn-us"},
		"a custom preset": {Tag: "v1", Format: "epub", Preset: "cpr_x"}, "a preset for a bundle": {Tag: "v1", Format: "md_bundle", Preset: "epub3"},
	} {
		in.ProjectID, in.RemoteID = h.pid, rem.ID
		if _, err := h.call(h.owner, "repo.release", in); h.code(err) != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if st.calls != 0 {
		t.Errorf("an edition was built for a request that was refused")
	}
	// A preview says what would happen and changes nothing.
	f.mu.Lock()
	f.rels["mairead/salt-road/v9"] = &fakeRel{ID: 920, Tag: "v9", Body: "x"}
	f.mu.Unlock()
	writes := f.writes()
	pv := h.must(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: rem.ID, Tag: "v9", Format: "pdf", Preview: true}).(gitmirror.RepoReleaseOut)
	if !pv.Preview || pv.Item != nil || !pv.ReleaseExists || pv.Preset != "print-6x9" || pv.Commit == "" || len(pv.Forced) == 0 || pv.Repository != "mairead/salt-road" || f.writes() != writes || st.calls != 0 {
		t.Errorf("preview = %+v", pv)
	}
}

func TestAFailedEditionSaysSoWithoutItsInternalsAndAnInterruptedOneIsClosed(t *testing.T) {
	h, f, rem, st := setupRelease(t, "forgejo")
	st.mu.Lock()
	st.err = errors.New("open /var/lib/scriobh/secret-internals: permission denied")
	st.mu.Unlock()
	rel := h.release(rem.ID, "v1", "epub")
	if rel.Status != "error" || rel.ErrorCode != "edition_failed" || strings.Contains(rel.ErrorMessage, "secret-internals") || f.release("mairead", "salt-road", "v1") != nil {
		t.Errorf("release = %+v", rel)
	}
	// Two at once for one tag: the second is refused while the first runs.
	st.mu.Lock()
	st.err = nil
	st.holdOn = make(chan struct{})
	st.mu.Unlock()
	out := h.must(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: rem.ID, Tag: "v2", Format: "epub"}).(gitmirror.RepoReleaseOut)
	if _, err := h.call(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: rem.ID, Tag: "v2", Format: "epub"}); h.code(err) != ops.CodeConflict {
		t.Errorf("a second release of a tag that is running: %v", err)
	}
	close(st.holdOn)
	h.svc.WaitReleases()
	if got := h.must(h.owner, "repo.release_get", gitmirror.ReleaseGetIn{ProjectID: h.pid, ReleaseID: out.Item.ID}).(struct {
		Item gitmirror.Release `json:"item"`
	}); got.Item.Status != "ok" {
		t.Errorf("release = %+v", got.Item)
	}
	list := h.must(h.owner, "repo.release_list", gitmirror.ReleaseListIn{ProjectID: h.pid, RemoteID: rem.ID}).(gitmirror.ReleaseList)
	if len(list.Items) != 2 || list.Items[0].Tag != "v2" {
		t.Errorf("list = %+v", list.Items)
	}
	// A server that stopped mid-release closes the row at its next start.
	_, err := h.d.Writer().Exec(`INSERT INTO git_releases(id, remote_id, project_id, tag, format, status, started_at) VALUES ('grl_stuck', ?, ?, 'v3', 'epub', 'running', ?)`, rem.ID, h.pid, "2026-06-01T09:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	h.useUnstartedService()
	h.start()
	var status, code string
	_ = h.d.Read().QueryRow(`SELECT status, error_code FROM git_releases WHERE id = 'grl_stuck'`).Scan(&status, &code)
	if status != "error" || code != "interrupted" {
		t.Errorf("a release left running: %s %s", status, code)
	}
}

func TestTheMarkdownBundleIsTheReadersTreeAndIsTheSameEveryTime(t *testing.T) {
	h, f, rem, _ := setupRelease(t, "forgejo")
	h.src.WriteRich("The Salt Road", canaryDocs(), map[string][]byte{"keep.png": []byte("PNGKEEP"), "drop.png": []byte("PNGDROP")})
	one := h.release(rem.ID, "v1", "md_bundle")
	two := h.release(rem.ID, "v2", "md_bundle")
	if one.Status != "ok" || two.Status != "ok" || one.InputHash == "" || one.InputHash != two.InputHash || one.FileSHA256 != two.FileSHA256 {
		t.Fatalf("releases = %+v %+v", one, two)
	}
	if !strings.HasSuffix(one.AssetName, ".zip") {
		t.Errorf("asset = %q", one.AssetName)
	}
	data := f.release("mairead", "salt-road", "v1").Assets[0].Data
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var all strings.Builder
	for _, zf := range zr.File {
		names = append(names, zf.Name)
		rc, _ := zf.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		all.WriteString(zf.Name)
		all.Write(b)
	}
	if strings.Join(names, ",") != keepAtt+",manuscript/01-opening.md,manuscript/04-closing.md,scriobh.json" {
		t.Errorf("entries = %v", names)
	}
	for _, c := range canaries {
		if strings.Contains(all.String(), c) {
			t.Errorf("the bundle holds %s", c)
		}
	}
	if !strings.Contains(all.String(), "The harbour woke.") {
		t.Errorf("the chapter is not in the bundle")
	}
}

func TestAReleaseIsNotOfferedForARemoteThatCarriesOnlyFinishedDocuments(t *testing.T) {
	// An edition compiles every included document, drafts too: next to a repository that withholds them it would publish what the mirror holds back.
	h, f, rem, st := setupRelease(t, "forgejo")
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: rem.ID, OnlyDone: ptr(true)})
	h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: rem.ID, Confirm: rem.Name})
	if err := h.svc.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.call(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: rem.ID, Tag: "v1", Format: "epub"}); err == nil || !strings.Contains(err.Error(), "only finished documents") {
		t.Errorf("release from an only-done remote: %v", err)
	}
	if st.calls != 0 || f.release("mairead", "salt-road", "v1") != nil {
		t.Errorf("something was built or created")
	}
}
