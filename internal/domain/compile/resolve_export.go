package compile

import (
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// ResolveImages resolves the destinations of the images a text links to into
// pictures that are safe to publish, by the rules every compile follows: a
// project's own raster attachment (PNG, JPEG, GIF, WebP, sniffed from its bytes
// and checked against its stored hash) or a small inline raster picture, cleaned
// of metadata and turned upright (internal/imagesafe); never an SVG, a web
// address or a path. Destinations that are not resolved are left out of the
// result; the warnings say why (too large, not found, a web address, rotated).
// The static site builder uses it, so its images come through the same door as
// the DOCX and EPUB ones.
func ResolveImages(c *ops.Ctx, projectID string, keys []string) (map[string]publish.Image, []Warning) {
	return resolverFor(c, projectID, true)(keys)
}
