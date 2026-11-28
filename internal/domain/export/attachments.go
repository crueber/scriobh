package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// MaxAttachmentBytes bounds the attachments a bundle carries. An export is
// built in memory (see the package comment), so past this it is refused with a
// way out (attachments: false) instead of exhausting the process.
const MaxAttachmentBytes = 192 << 20

// manifestAttachment lists one attachment of the bundle. The Markdown still
// links /files/att_<id>; this maps the id to the file.
type manifestAttachment struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	Alt      string `json:"alt"`
}

// bundleAttachments are the files of the project that the exported documents
// link to, under attachments/ with deterministic names (the first twelve
// characters of the hash and a slug of the display name), byte for byte as
// stored. A file missing from the disk is left out of the bundle and its
// manifest entry, not an error: the Markdown that links it is unchanged.
func bundleAttachments(ctx context.Context, env *ops.Env, projectID string, included []*doc) ([]entry, []manifestAttachment, error) {
	if len(included) == 0 {
		return nil, nil, nil
	}
	ids := make([]any, len(included))
	ph := make([]string, len(included))
	for i, d := range included {
		ids[i], ph[i] = d.ID, "?"
	}
	rows, err := env.DB.Read().QueryContext(ctx, `SELECT DISTINCT a.id, a.sha256, a.bytes, a.mime, a.ext, a.filename, a.alt FROM attachments a
		JOIN attachment_refs r ON r.attachment_id = a.id AND r.ref_kind = 'doc'
		WHERE a.project_id = ? AND r.ref_id IN (`+strings.Join(ph, ",")+`) ORDER BY a.id`, append([]any{projectID}, ids...)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type row struct {
		m   manifestAttachment
		ext string
	}
	var rs []row
	var total int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.m.ID, &r.m.SHA256, &r.m.Bytes, &r.m.Mime, &r.ext, &r.m.Filename, &r.m.Alt); err != nil {
			return nil, nil, err
		}
		total += r.m.Bytes
		rs = append(rs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if total > MaxAttachmentBytes {
		return nil, nil, &ops.Error{Code: ops.CodeTooLarge, Message: "The attachments are too large to put in one bundle (over 192 MB). Export without them (attachments: false) and download them one by one.",
			Data: map[string]any{"attachment_bytes": total}}
	}
	var entries []entry
	var metas []manifestAttachment
	used := map[string]bool{}
	for _, r := range rs {
		p, err := attachments.BlobPath(env.DataDir, attachments.OwnerProject, projectID, r.m.SHA256, r.ext)
		if err != nil {
			continue
		}
		f, _, err := attachments.OpenRegular(env.DataDir, p)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, r.m.Bytes+1))
		f.Close()
		if err != nil || int64(len(b)) > r.m.Bytes {
			continue
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != r.m.SHA256 {
			continue // damaged on disk: never export bytes that do not match their name
		}
		base := strings.TrimSuffix(r.m.Filename, filepath.Ext(r.m.Filename))
		name := "attachments/" + r.m.SHA256[:12] + "-" + slug(base) + "." + r.ext
		if used[name] {
			name = "attachments/" + r.m.SHA256 + "." + r.ext
		}
		used[name] = true
		r.m.Path = name
		entries = append(entries, entry{path: name, data: b})
		metas = append(metas, r.m)
	}
	return entries, metas, nil
}
