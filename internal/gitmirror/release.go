package gitmirror

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Publish a compiled edition as a release asset (F-369).
//
// After a push has reached a GitHub or Forgejo remote, repo.release compiles an edition (EPUB, DOCX, PDF or a
// Markdown bundle) with the project's own export ops, creates a release for a tag at the commit the remote has,
// and uploads the file as an asset. It is a job (a compile and an upload are longer than a request may take), run
// as the person who asked, and it is for the owner in a browser session.
//
//   - What goes in the file is what a reader sees: the formats are a fixed list of presets that carry no contact
//     block, and the options are forced to no notes and no ghosted text whatever a custom preset says;
//   - the release notes carry the compile's manifest hash (input_hash) and the file's SHA-256;
//   - a retry with the same tag and the same manifest hash adds no second asset: an asset by that name and size
//     is left as it is, a half-uploaded one is replaced; a tag that already has another edition, or a release
//     this server did not make, is refused (tag_conflict) and nothing is changed there;
//   - the file is capped (64 MB) before anything is created at the host.

// DefaultReleaseMaxMB is the default cap of a release asset.
const DefaultReleaseMaxMB = 64

// Release codes.
const (
	CodeNotPushed      = "not_pushed"
	CodeReleaseBlocked = "release_unsupported"
	CodeEditionFailed  = "edition_failed"
)

// Edition is a built file.
type Edition struct {
	Filename    string
	ContentType string
	Body        []byte
	SHA256      string
	InputHash   string // the compile's manifest hash; an export with none uses the file's own hash
	Documents   int
}

// EditionRequest says what to build.
type EditionRequest struct {
	ProjectID string
	Format    string // epub, docx, pdf, md or md_bundle
	Preset    string // a built-in preset id (compile formats only)
	// IncludeCover is the release's remote's include_cover: an EPUB uses the project's own cover only when it is on.
	IncludeCover bool
}

// Compiler builds an edition as the principal. The server supplies one that calls export.compile and
// export.markdown; it is the same code, authorization and record as an export made by hand.
type Compiler func(ctx context.Context, p ops.Principal, req EditionRequest) (*Edition, error)

// presetsFor is the whole list of presets a release may use, by format. None of them has a contact block.
var presetsFor = map[string][]string{
	"epub": {"epub3"},
	"docx": {"styled-docx"},
	"md":   {"plain-markdown"},
	"pdf":  {"print-6x9", "print-5.5x8.5", "print-5x8", "print-a5", "proof-letter", "proof-a4"},
	// md_bundle is the reader's Markdown tree as a zip (see markdownBundle); it has no preset.
	"md_bundle": nil,
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Release is one attempt as ops return it.
type Release struct {
	ID           string `json:"id"`
	RemoteID     string `json:"remote_id"`
	Tag          string `json:"tag"`
	Format       string `json:"format" enum:"epub,docx,pdf,md,md_bundle"`
	Preset       string `json:"preset,omitempty"`
	Status       string `json:"status" enum:"running,ok,error"`
	CommitSHA    string `json:"commit_sha,omitempty" desc:"The commit the tag points at."`
	InputHash    string `json:"input_hash,omitempty" desc:"The compile's manifest hash, in the release notes."`
	FileSHA256   string `json:"file_sha256,omitempty"`
	AssetName    string `json:"asset_name,omitempty"`
	AssetBytes   int64  `json:"asset_bytes,omitempty"`
	Reused       bool   `json:"reused" desc:"The asset was already at the host (a retry): nothing was uploaded."`
	ReleaseURL   string `json:"release_url,omitempty" desc:"The release's page at the host."`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

// RepoReleaseIn starts a release.
type RepoReleaseIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `json:"remote_id,omitempty" desc:"The remote. Optional while the project has exactly one."`
	Tag       string `json:"tag" desc:"The release's tag, like v1.0. Letters, digits, dots, dashes and underscores, up to 64."`
	Format    string `json:"format" enum:"epub,docx,pdf,md,md_bundle" desc:"epub (EPUB 3), docx (styled reading copy), pdf (a print PDF), md (plain Markdown) or md_bundle (the Markdown files a public remote carries, with their pictures, as a zip). None carries notes, private notes, ghosted text, alternatives or the author's contact block."`
	Preset    string `json:"preset,omitempty" desc:"For pdf: print-6x9 (default), print-5.5x8.5, print-5x8, print-a5, proof-letter or proof-a4."`
	Preview   bool   `json:"preview,omitempty" desc:"Say what would happen (the repository, the tag, the commit, whether the release exists) and change nothing."`
}

func (in RepoReleaseIn) ProjectRef() string { return in.ProjectID }

// RepoReleaseOut is the started job, or, for a preview, what would happen.
type RepoReleaseOut struct {
	Preview       bool     `json:"preview"`
	Item          *Release `json:"item,omitempty"`
	Repository    string   `json:"repository,omitempty"`
	Commit        string   `json:"commit,omitempty"`
	Preset        string   `json:"preset,omitempty"`
	MaxBytes      int64    `json:"max_bytes,omitempty"`
	ReleaseExists bool     `json:"release_exists,omitempty"`
	Forced        []string `json:"forced_options,omitempty" desc:"Options held to what a reader sees whatever the preset says."`
}

// ReleaseGetIn names a release.
type ReleaseGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ReleaseID string `query:"release_id" json:"-"`
}

func (in ReleaseGetIn) ProjectRef() string { return in.ProjectID }

// ReleaseListIn lists the releases of a remote.
type ReleaseListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RemoteID  string `query:"remote_id" json:"-" desc:"The remote. Optional while the project has exactly one."`
}

func (in ReleaseListIn) ProjectRef() string { return in.ProjectID }

// ReleaseList lists releases, newest first.
type ReleaseList struct {
	Items []Release `json:"items"`
}

func registerRelease(r *ops.Registry) {
	owner := ops.RequireProject(ops.ProjectOwner).ForSessions()
	ops.Register(r, ops.Op{NoDryRun: true, Name: "repo.release", Method: "POST", Path: "/api/v1/projects/{id}/repo/release", Status: 202,
		Desc: "Publish a compiled edition (EPUB, DOCX, PDF or a Markdown bundle) as a release asset at a GitHub or Forgejo remote, after a successful push: a release for the tag at the commit the remote has, the file as an asset, the manifest hash and the file's SHA-256 in the notes. Runs as a job; read repo.release_get. A retry with the same tag and manifest hash adds no second asset; a tag that already has another edition is refused. The file holds what a reader sees and is capped (64 MB). Owner only, browser session only.",
		Auth: owner}, startRelease)
	ops.Register(r, ops.Op{Name: "repo.release_get", Method: "GET", Path: "/api/v1/projects/{id}/repo/release",
		Desc: "One release attempt: its status, the asset, the hashes, and why it failed. Owner only.",
		Auth: owner}, getRelease)
	ops.Register(r, ops.Op{Name: "repo.release_list", Method: "GET", Path: "/api/v1/projects/{id}/repo/releases",
		Desc: "A remote's release attempts, newest first (the last 30). Owner only.",
		Auth: owner}, listReleases)
}

type releaseRow struct {
	ID, RemoteID, ProjectID, Tag, Format, Preset, Status, CommitSHA, InputHash, FileSHA, Asset string
	Bytes                                                                                      int64
	Reused                                                                                     bool
	URL, ErrCode, ErrMsg, StartedBy, StartedAt, FinishedAt                                     string
}

const releaseCols = `id, remote_id, project_id, tag, format, preset, status, commit_sha, input_hash, file_sha256, asset_name, asset_bytes, reused, release_url, error_code, error_message, COALESCE(started_by,''), started_at, COALESCE(finished_at,'')`

func scanRelease(sc scanner) (*releaseRow, error) {
	var x releaseRow
	var reused int
	err := sc.Scan(&x.ID, &x.RemoteID, &x.ProjectID, &x.Tag, &x.Format, &x.Preset, &x.Status, &x.CommitSHA, &x.InputHash, &x.FileSHA, &x.Asset, &x.Bytes, &reused, &x.URL, &x.ErrCode, &x.ErrMsg, &x.StartedBy, &x.StartedAt, &x.FinishedAt)
	x.Reused = reused != 0
	return &x, err
}

func releaseView(x *releaseRow) Release {
	return Release{ID: x.ID, RemoteID: x.RemoteID, Tag: x.Tag, Format: x.Format, Preset: x.Preset, Status: x.Status, CommitSHA: x.CommitSHA, InputHash: x.InputHash,
		FileSHA256: x.FileSHA, AssetName: x.Asset, AssetBytes: x.Bytes, Reused: x.Reused, ReleaseURL: x.URL, ErrorCode: x.ErrCode, ErrorMessage: x.ErrMsg, StartedAt: x.StartedAt, FinishedAt: x.FinishedAt}
}

func (s *Service) releaseCap() int64 {
	if s.o.ReleaseMaxBytes > 0 {
		return s.o.ReleaseMaxBytes
	}
	return DefaultReleaseMaxMB << 20
}

// releaseTarget works out the host, the repository and the token of a remote that can carry a release.
func (s *Service) releaseTarget(r *remoteRow) (t providerTarget, owner, repo, token string, e *Error) {
	if r.AuthKind != AuthHTTPS || (r.Preset != ProviderGitHub && r.Preset != ProviderForgejo) {
		return t, "", "", "", gerr(CodeReleaseBlocked, "A release needs a remote at GitHub or a Forgejo or Gitea host with a token (made with the GitHub or Forgejo preset).")
	}
	ep, pe := parseURL(r.URL, false)
	if pe != nil {
		return t, "", "", "", pe
	}
	host := ep.Host
	if ep.Port != 443 {
		host = ep.HostPort()
	}
	if t, e = newTarget(r.Preset, host); e != nil {
		return t, "", "", "", e
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git"), "/")
	if len(parts) != 2 || !validOwner(parts[0]) || !validRepoName(parts[1]) {
		return t, "", "", "", gerr(CodeReleaseBlocked, "The remote's address does not name one repository as owner/name.")
	}
	plain, err := s.o.Box.Open(r.SecretCipher, sealContext(r.ID, ep))
	if err != nil {
		return t, "", "", "", gerr(CodeCredentialsBad, "The remote's token cannot be read with this installation's secret key. Enter it again.")
	}
	token = string(plain)
	clear(plain)
	return t, parts[0], parts[1], token, nil
}

func startRelease(c *ops.Ctx, in RepoReleaseIn) (RepoReleaseOut, error) {
	s, err := service(c)
	if err != nil {
		return RepoReleaseOut{}, err
	}
	if !tagRe.MatchString(in.Tag) || strings.Contains(in.Tag, "..") || strings.HasSuffix(in.Tag, ".lock") || strings.HasSuffix(in.Tag, ".") {
		return RepoReleaseOut{}, ops.Invalid("tag", "A tag has letters, digits, dots, dashes and underscores, starts with a letter or digit, and is at most 64 characters.")
	}
	presets, ok := presetsFor[in.Format]
	if !ok {
		return RepoReleaseOut{}, ops.Invalid("format", "format is epub, docx, pdf, md or md_bundle.")
	}
	preset := ""
	if len(presets) > 0 {
		preset = presets[0]
		if in.Preset != "" {
			found := false
			for _, p := range presets {
				found = found || p == in.Preset
			}
			if !found {
				return RepoReleaseOut{}, ops.Invalid("preset", "That preset is not one a release uses for "+in.Format+": "+strings.Join(presets, ", ")+".")
			}
			preset = in.Preset
		}
	} else if in.Preset != "" {
		return RepoReleaseOut{}, ops.Invalid("preset", "A Markdown bundle has no preset.")
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return RepoReleaseOut{}, err
	}
	if !r.Enabled {
		return RepoReleaseOut{}, ops.Conflict("This remote is switched off. Switch it on, push, then release.")
	}
	if r.State != StateSynced || r.LastCommitSHA == "" || r.LocalSHA != r.LastCommitSHA {
		return RepoReleaseOut{}, ops.ConflictWith("The remote has not received the project's newest commit yet. Push first; a release is made from what the remote has.", map[string]any{"git_code": CodeNotPushed})
	}
	t, owner, repo, token, e := s.releaseTarget(r)
	if e != nil {
		return RepoReleaseOut{}, failure("", e)
	}
	if s.o.Compile == nil && in.Format != "md_bundle" {
		return RepoReleaseOut{}, ops.Conflict("Compiling is not available on this server.")
	}
	if r.OnlyDone {
		return RepoReleaseOut{}, failure("", gerr(CodeReleaseBlocked, "This remote carries only finished documents, but an edition is built from every document that is not marked do not compile, drafts included. Release from a remote that carries what a reader sees, or finish the drafts first."))
	}
	forced := []string{"notes: none", "ghosted text: omitted", "alternatives: left out", "contact block: not on any of these presets"}
	if in.Preview {
		rel, exists, he := newProviderClient(t, token).releaseByTag(c, owner, repo, in.Tag)
		if he != nil {
			return RepoReleaseOut{}, failure("", he)
		}
		_ = rel
		return RepoReleaseOut{Preview: true, Repository: owner + "/" + repo, Commit: r.LastCommitSHA, Preset: preset, MaxBytes: s.releaseCap(), ReleaseExists: exists, Forced: forced}, nil
	}
	row := &releaseRow{ID: db.NewID("grl"), RemoteID: r.ID, ProjectID: r.ProjectID, Tag: in.Tag, Format: in.Format, Preset: preset, Status: "running", CommitSHA: r.LastCommitSHA,
		StartedBy: c.P.UserID, StartedAt: stamp(s.o.Clock.Now())}
	_, ierr := s.o.DB.Writer().ExecContext(c, `INSERT INTO git_releases(id, remote_id, project_id, tag, format, preset, status, commit_sha, started_by, started_at) VALUES (?,?,?,?,?,?, 'running', ?,?,?)`,
		row.ID, row.RemoteID, row.ProjectID, row.Tag, row.Format, row.Preset, row.CommitSHA, nullIfEmpty(row.StartedBy), row.StartedAt)
	if ierr != nil {
		if db.IsUniqueViolation(ierr) {
			return RepoReleaseOut{}, ops.Conflict("A release of that tag is already running for this remote. Wait for it to end.")
		}
		return RepoReleaseOut{}, ops.Internal(ierr)
	}
	c.Audit(ops.AuditEntry{Action: "repo.release", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "release_id": row.ID, "tag": in.Tag, "format": in.Format, "repository": owner + "/" + repo, "commit": r.LastCommitSHA}})
	v := releaseView(row) // before the job starts: the job writes the row
	s.relWG.Add(1)
	go s.runRelease(c.P, row, r, t, owner, repo, token)
	return RepoReleaseOut{Item: &v}, nil
}

// settleRelease writes a release's outcome. It is called once, whatever happens.
func (s *Service) settleRelease(row *releaseRow, e *Error) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), 10*time.Second)
	defer cancel()
	row.Status = "ok"
	if e != nil {
		row.Status, row.ErrCode, row.ErrMsg = "error", e.Code, e.Message
	}
	row.FinishedAt = stamp(s.o.Clock.Now())
	_, err := s.o.DB.Writer().ExecContext(bg, `UPDATE git_releases SET status = ?, input_hash = ?, file_sha256 = ?, asset_name = ?, asset_bytes = ?, reused = ?, release_url = ?, error_code = ?, error_message = ?, finished_at = ? WHERE id = ?`,
		row.Status, row.InputHash, row.FileSHA, row.Asset, row.Bytes, b2i(row.Reused), row.URL, row.ErrCode, row.ErrMsg, row.FinishedAt, row.ID)
	if err != nil {
		s.log.Warn("git: could not record a release", "release", row.ID, "err", cause(err))
	}
	_, _ = s.o.DB.Writer().ExecContext(bg, `DELETE FROM git_releases WHERE remote_id = ? AND status <> 'running' AND id NOT IN (SELECT id FROM git_releases WHERE remote_id = ? ORDER BY id DESC LIMIT 100)`, row.RemoteID, row.RemoteID)
}

func assetStem(filename string) string {
	stem := filename
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	var b strings.Builder
	for _, r := range stem {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "edition"
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	return out
}

func assetExt(filename string) string {
	i := strings.LastIndexByte(filename, '.')
	if i < 0 {
		return ""
	}
	ext := strings.ToLower(filename[i:])
	if len(ext) > 8 || !regexp.MustCompile(`^\.[a-z0-9]+$`).MatchString(ext) {
		return ""
	}
	return ext
}

const editionMarker = "scriobh-edition-manifest: "

func (s *Service) runRelease(p ops.Principal, row *releaseRow, r *remoteRow, t providerTarget, owner, repo, token string) {
	defer s.relWG.Done()
	var fail *Error
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("git: release panicked", "release", row.ID, "panic", fmt.Sprint(rec))
			fail = gerr(CodeInternal, "The release failed unexpectedly. It is in the server log.")
		}
		s.settleRelease(row, fail)
	}()
	ctx, cancel := context.WithTimeout(s.baseCtx(), 20*time.Minute)
	defer cancel()
	var ed *Edition
	var err error
	if row.Format == "md_bundle" {
		ed, err = s.markdownBundle(ctx, r.ProjectID, r.IncludeCover)
	} else {
		ed, err = s.o.Compile(ctx, p, EditionRequest{ProjectID: r.ProjectID, Format: row.Format, Preset: row.Preset, IncludeCover: r.IncludeCover})
	}
	if err != nil {
		if ctx.Err() != nil {
			fail = gerr(CodeInterrupted, "The release was interrupted.")
			return
		}
		var ge *Error
		if errors.As(err, &ge) && ge.Code == CodeMirrorBehind {
			fail = ge
			return
		}
		ae := ops.AsError(err)
		msg := "The edition could not be built."
		if ae.Code != ops.CodeInternal {
			msg += " " + ae.Message
		}
		fail = gerr(CodeEditionFailed, "%s", msg)
		return
	}
	if int64(len(ed.Body)) > s.releaseCap() {
		fail = gerr(CodeTooLarge, "The edition is %d MB, over the %d MB a release may hold (64 MB). Nothing was created at the host.", len(ed.Body)>>20, s.releaseCap()>>20)
		return
	}
	sum := sha256.Sum256(ed.Body)
	file := hex.EncodeToString(sum[:])
	input := ed.InputHash
	if input == "" {
		input = file
	}
	row.InputHash, row.FileSHA, row.Bytes = input, file, int64(len(ed.Body))
	row.Asset = fmt.Sprintf("%s-%s-%s%s", assetStem(ed.Filename), row.Tag, input[:min(12, len(input))], assetExt(ed.Filename))

	cl := newProviderClient(t, token)
	rel, exists, he := cl.releaseByTag(ctx, owner, repo, row.Tag)
	if he != nil {
		fail = he
		return
	}
	prefix := assetStem(ed.Filename) + "-" + row.Tag + "-"
	var existing *assetInfo
	if exists {
		mine := strings.Contains(rel.Body, editionMarker+input)
		other := false
		for i := range rel.Assets {
			a := &rel.Assets[i]
			switch {
			case a.Name == row.Asset:
				existing = a
			case strings.HasPrefix(a.Name, prefix):
				other = true // an edition of this tag with another manifest hash
			}
		}
		if other || !mine {
			fail = gerr(CodeTagConflict, "The release %s already exists at %s and is not this edition (another manifest hash, or a release made by hand). Nothing was changed there. Use a new tag.", row.Tag, t.host)
			return
		}
	} else {
		notes := fmt.Sprintf("Edition built by Scríobh from commit %s.\n\n%s%s\nfile-sha256: %s\n", r.LastCommitSHA[:min(12, len(r.LastCommitSHA))], editionMarker, input, file)
		if rel, he = cl.createRelease(ctx, owner, repo, row.Tag, r.LastCommitSHA, row.Tag, notes); he != nil {
			fail = he
			return
		}
	}
	row.URL = "https://" + t.host + "/" + owner + "/" + repo + "/releases/tag/" + row.Tag
	if existing != nil && existing.Size == int64(len(ed.Body)) && (existing.State == "" || existing.State == "uploaded") {
		row.Reused = true
		return
	}
	if existing != nil { // a partial upload, or a file of another size by our name: replaced
		if he := cl.deleteAsset(ctx, owner, repo, rel.ID, existing.ID); he != nil {
			fail = he
			return
		}
	}
	if he := cl.uploadAsset(ctx, owner, repo, rel.ID, row.Asset, ed.ContentType, ed.Body); he != nil {
		fail = he
		return
	}
	// The host's own listing is the proof.
	got, ok, he := cl.releaseByTag(ctx, owner, repo, row.Tag)
	if he != nil {
		fail = he
		return
	}
	if ok {
		for _, a := range got.Assets {
			if a.Name == row.Asset && a.Size == int64(len(ed.Body)) {
				return
			}
		}
	}
	fail = gerr(CodeProviderBad, "%s did not list the uploaded file afterwards. Try the release again: an asset that is there is not uploaded twice.", t.host)
}

func (s *Service) loadRelease(ctx context.Context, projectID, id string) (*releaseRow, error) {
	if id == "" {
		return nil, ops.Invalid("release_id", "Name the release with release_id.")
	}
	x, err := scanRelease(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+releaseCols+` FROM git_releases WHERE id = ? AND project_id = ?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ops.NotFound("release")
	}
	if err != nil {
		return nil, ops.Internal(err)
	}
	return x, nil
}

func getRelease(c *ops.Ctx, in ReleaseGetIn) (struct {
	Item Release `json:"item"`
}, error) {
	type out = struct {
		Item Release `json:"item"`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	x, err := s.loadRelease(c, in.ProjectID, in.ReleaseID)
	if err != nil {
		return out{}, err
	}
	return out{Item: releaseView(x)}, nil
}

func listReleases(c *ops.Ctx, in ReleaseListIn) (ReleaseList, error) {
	s, err := service(c)
	if err != nil {
		return ReleaseList{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return ReleaseList{}, err
	}
	rows, err := s.o.DB.Read().QueryContext(c, `SELECT `+releaseCols+` FROM git_releases WHERE remote_id = ? ORDER BY id DESC LIMIT 30`, r.ID)
	if err != nil {
		return ReleaseList{}, ops.Internal(err)
	}
	defer rows.Close()
	out := ReleaseList{Items: []Release{}}
	for rows.Next() {
		x, err := scanRelease(rows)
		if err != nil {
			return ReleaseList{}, ops.Internal(err)
		}
		out.Items = append(out.Items, releaseView(x))
	}
	return out, rows.Err()
}

// markdownBundle is the reader's Markdown tree as one zip: the files a public remote carries (the chapters as a
// reader sees them, the manifest from an allow-list, the pictures they link to), built by the same code, so it
// has the same guarantees. It is not export.markdown's bundle, which keeps private notes and alternatives byte
// for byte so that it can be imported again. The zip is deterministic: entries sorted, stored, a fixed time.
func (s *Service) markdownBundle(ctx context.Context, projectID string, includeCover bool) (*Edition, error) {
	sn, _, e := s.snapshotFor(ctx, &remoteRow{ProjectID: projectID, Audience: AudiencePublic, IncludeCover: includeCover})
	if e != nil {
		return nil, e
	}
	if len(sn.docs) == 0 {
		return nil, errors.New("nothing in the project is part of what a reader sees")
	}
	paths := make([]string, 0, len(sn.files))
	for p := range sn.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := sha256.New()
	for _, p := range paths {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p, Method: zip.Store, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(sn.files[p]); err != nil {
			return nil, err
		}
		fmt.Fprintf(h, "%s\x00%s\n", p, sn.hashes[p])
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	var slug string
	_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT slug FROM projects WHERE id = ?`, projectID).Scan(&slug)
	sum := sha256.Sum256(buf.Bytes())
	return &Edition{Filename: assetStem(slug) + "-markdown.zip", ContentType: "application/zip", Body: buf.Bytes(), SHA256: hex.EncodeToString(sum[:]),
		InputHash: hex.EncodeToString(h.Sum(nil)), Documents: len(sn.docs)}, nil
}
