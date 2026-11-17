package site

import (
	"crypto/sha256"
	"encoding/hex"
)

// Pictures are made fit to publish by internal/imagesafe (an allow-list of what
// each format keeps, EXIF orientation applied before it is dropped); this file
// only names them.

// imageName is the file name of a picture: the first 16 hex digits of the
// SHA-256 of its published bytes, and its extension.
func imageName(ext string, data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]) + "." + ext
}
