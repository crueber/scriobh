package compile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/imagesafe"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// What an image may be to go into a file: a project's own attachment, or a
// small inline raster picture. Anything else (a web address, a path) is left
// out and named in a warning; a compile never makes a request.
var (
	reAttachment = regexp.MustCompile(`^/files/(att_[0-9A-Za-z]{10,40})$`)
	reDataImage  = regexp.MustCompile(`^data:image/(?:gif|png|jpeg|webp);base64,([A-Za-z0-9+/=]+)$`)
)

const (
	maxDataImage  = 512 << 10 // an inline image, as the HTML export keeps it
	maxImageBytes = 25 << 20  // one attachment
	maxImages     = 2_000     // in one file
)

// resolverFor returns the image resolver of one compile. read false only
// checks that the images exist (a preview, a dry run): the bytes are not read.
func resolverFor(c *ops.Ctx, projectID string, read bool) imageResolver {
	return func(keys []string) (map[string]publish.Image, []Warning) {
		out := map[string]publish.Image{}
		var ws []Warning
		var remote, missing, tooMany, tooBig, rotated int
		var total int64
		for _, k := range keys {
			if len(out) >= maxImages {
				tooMany++
				continue
			}
			if len(k) <= maxDataImage {
				if m := reDataImage.FindStringSubmatch(k); m != nil {
					raw, err := base64.StdEncoding.DecodeString(m[1])
					if err != nil {
						missing++
						continue
					}
					img, st := describe(raw)
					if st != "ok" {
						if st == "big" {
							tooBig++
						} else {
							missing++
						}
						continue
					}
					if img.Rotated {
						rotated++
					}
					out[k] = img
					continue
				}
			}
			if m := reAttachment.FindStringSubmatch(k); m != nil {
				id := m[1]
				img, status := attachment(c, projectID, id, read, &total)
				switch status {
				case "ok":
					if img.Rotated {
						rotated++
					}
					out[k] = img
				case "big":
					tooBig++
				default:
					missing++
				}
				continue
			}
			remote++
		}
		if remote > 0 {
			ws = append(ws, Warning{Code: "image_remote", Message: fmt.Sprintf("%d image(s) link to a web address or a path. A compile never fetches one, so they are left out.", remote)})
		}
		if missing > 0 {
			ws = append(ws, Warning{Code: "image_missing", Message: fmt.Sprintf("%d image(s) could not be found or read and are left out.", missing)})
		}
		if tooBig > 0 {
			ws = append(ws, Warning{Code: "image_skipped", Message: fmt.Sprintf("%d image(s) are too large to include (over %d MB each, or %d MB together).", tooBig, maxImageBytes>>20, export.MaxAttachmentBytes>>20)})
		}
		if rotated > 0 {
			ws = append(ws, Warning{Code: "image_rotated", Message: fmt.Sprintf("%d photo(s) carried a rotation note (EXIF orientation); they were turned upright and saved again as JPEG, since the note is removed from pictures that go into a file.", rotated)})
		}
		if tooMany > 0 {
			ws = append(ws, Warning{Code: "image_skipped", Message: fmt.Sprintf("%d image(s) beyond the first %d are left out.", tooMany, maxImages)})
		}
		return out, ws
	}
}

// describe sniffs the bytes (never the declared type), cleans the picture of
// everything but what draws it (internal/imagesafe: EXIF, XMP, comments,
// thumbnails and provenance records go; an EXIF orientation is applied to the
// pixels first) and reads the size from the cleaned picture. Every file format
// the compile writes, and the static site, gets its pictures through here.
// status is "ok", "big" (past the pixel limits) or "bad".
func describe(raw []byte) (publish.Image, string) {
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	mime := http.DetectContentType(head)
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}[mime]
	if ext == "" {
		return publish.Image{}, "bad"
	}
	clean, info, err := imagesafe.Clean(ext, raw)
	if err != nil {
		if errors.Is(err, imagesafe.ErrTooLarge) {
			return publish.Image{}, "big"
		}
		return publish.Image{}, "bad"
	}
	if int64(info.Width)*int64(info.Height) > attachments.MaxPixels {
		return publish.Image{}, "big"
	}
	sum := sha256.Sum256(raw)
	return publish.Image{Data: clean, Mime: mime, Ext: ext, Hash: hex.EncodeToString(sum[:]), Width: info.Width, Height: info.Height, Rotated: info.Rotated, Orient: 1}, "ok" // cleaned: upright, no orientation tag
}

// attachment reads one of the project's own image attachments. The id is
// untrusted text: it is looked up within this project, the file is opened as a
// regular file under the data directory, and its bytes must match their name.
func attachment(c *ops.Ctx, projectID, id string, read bool, total *int64) (publish.Image, string) {
	var sha, ext, mime string
	var n int64
	var w, h int
	err := c.DB().Read().QueryRowContext(c, `SELECT sha256, ext, mime, bytes, width, height FROM attachments WHERE id = ? AND project_id = ? AND kind = 'image'`, id, projectID).
		Scan(&sha, &ext, &mime, &n, &w, &h)
	if err != nil {
		return publish.Image{}, "missing"
	}
	if n > maxImageBytes || *total+n > export.MaxAttachmentBytes {
		return publish.Image{}, "big"
	}
	if !read {
		*total += n // a preview counts what an export will, so the two keep the same pictures
		img := publish.Image{Mime: mime, Ext: ext, Width: w, Height: h, Hash: sha}
		if ext == "jpg" || mime == "image/jpeg" {
			// A preview does not read the picture, but the orientation decides its
			// shape on the page, so the head of the file is read for it.
			img.Orient = headOrientation(c, projectID, sha, ext)
		}
		return img, "ok"
	}
	p, err := attachments.BlobPath(c.Env.DataDir, attachments.OwnerProject, projectID, sha, ext)
	if err != nil {
		return publish.Image{}, "missing"
	}
	f, _, err := attachments.OpenRegular(c.Env.DataDir, p)
	if err != nil {
		return publish.Image{}, "missing"
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil || int64(len(raw)) > maxImageBytes {
		return publish.Image{}, "missing"
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != sha {
		return publish.Image{}, "missing" // damaged on disk: never embed bytes that do not match their name
	}
	img, st := describe(raw)
	switch st {
	case "big":
		return publish.Image{}, "big"
	case "bad":
		return publish.Image{}, "missing"
	}
	*total += n
	return img, "ok"
}

// headOrientation reads the first bytes of a stored JPEG for its EXIF
// orientation; 1 when the file cannot be read (a dry run has no data directory).
func headOrientation(c *ops.Ctx, projectID, sha, ext string) int {
	p, err := attachments.BlobPath(c.Env.DataDir, attachments.OwnerProject, projectID, sha, ext)
	if err != nil {
		return 1
	}
	f, _, err := attachments.OpenRegular(c.Env.DataDir, p)
	if err != nil {
		return 1
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(f, 128<<10))
	return publish.JPEGOrientation(head)
}
