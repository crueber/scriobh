package imports

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/ops"
)

// useBookCover makes an EPUB's cover picture the new project's own cover, in the import's transaction: a layout of one
// front image, whose description is the book's title. The picture is one the import stores anyway and has cleaned
// (no location, camera or thumbnail); the cover checks run on it again here (a CMYK JPEG, a GIF or a picture over the
// cover's size is not a cover). A picture that cannot be the cover does not fail the import: it stays an attachment,
// and the result says why.
func useBookCover(c *ops.Ctx, tx *sql.Tx, in *intake, pics *applyPictures, projectID, title string, out *ApplyOut) error {
	if in.epub == nil || in.epub.coverN == 0 || pics == nil || in.extras == nil {
		out.Warnings = append(out.Warnings, "The book has no cover picture that was kept, so the project keeps the generated cover.")
		return nil
	}
	url, ok := pics.urls[in.epub.coverN]
	if !ok || !strings.HasPrefix(url, "/files/") {
		out.Warnings = append(out.Warnings, "The book's cover picture was not stored, so the project keeps the generated cover.")
		return nil
	}
	for _, p := range in.extras.pictures {
		if p.n != in.epub.coverN {
			continue
		}
		alt := "Cover of " + title
		if err := coverlayout.UseAsFront(c, tx, projectID, strings.TrimPrefix(url, "/files/"), alt, p.pic.Data); err != nil {
			if e, isOps := err.(*ops.Error); isOps && e.Code == ops.CodeInvalid {
				out.Warnings = append(out.Warnings, "The book's cover was not made the project's cover: "+e.Message)
				return nil
			}
			return err
		}
		out.CoverUsed = true
		return nil
	}
	return nil
}
