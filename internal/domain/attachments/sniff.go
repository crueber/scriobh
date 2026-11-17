package attachments

import (
	"bytes"
	"image"
	_ "image/gif" // decoders for DecodeConfig: dimensions only, never a full decode
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
)

// The type policy. A file is kept only if its bytes are one of a short list of
// kinds, whatever the client says it is:
//
//   - images: PNG, JPEG, GIF, WebP;
//   - PDF;
//   - plain text and Markdown (UTF-8, no NUL bytes, not a script).
//
// SVG is refused. It is XML that can carry script and external references, and
// the only safe policies are to rasterise it (no renderer is bundled, and
// adding one is a dependency and an attack surface) or to sanitise it (an
// allow-list sanitiser written here would be the weakest link). A writer who
// needs a vector figure can upload a PNG, or a PDF. Nothing executable is kept:
// an extension outside the allow-list is refused, as are scripts and the
// executable formats, which fail the text check by their bytes.
//
// Detection looks at the content (magic bytes and http.DetectContentType). The
// file name and the declared Content-Type are only cross-checked: a name or
// type that claims one kind while the bytes are another is refused rather than
// believed or silently corrected.

// kindInfo is what a detected type becomes.
type kindInfo struct {
	Mime string
	Ext  string
	Kind string // image, pdf, text
}

var (
	kPNG  = kindInfo{"image/png", "png", "image"}
	kJPEG = kindInfo{"image/jpeg", "jpg", "image"}
	kGIF  = kindInfo{"image/gif", "gif", "image"}
	kWebP = kindInfo{"image/webp", "webp", "image"}
	kPDF  = kindInfo{"application/pdf", "pdf", "pdf"}
	kText = kindInfo{"text/plain; charset=utf-8", "txt", "text"}
	kMD   = kindInfo{"text/markdown; charset=utf-8", "md", "text"}
)

// extKinds maps a file name's extension to the kind it claims.
var extKinds = map[string]kindInfo{
	"png": kPNG, "jpg": kJPEG, "jpeg": kJPEG, "gif": kGIF, "webp": kWebP, "pdf": kPDF,
	"txt": kText, "md": kMD, "markdown": kMD,
}

// MaxPixels bounds an image's area, so a small file cannot be a decompression
// bomb for whatever renders it later.
const MaxPixels = 100_000_000

// HeadSize is how much of a file detection reads.
const HeadSize = 4096

func unsupported(msg string) *ops.Error {
	return &ops.Error{Code: ops.CodeUnsupportedType, Message: msg}
}

// detect decides what the bytes are. head is the first HeadSize bytes (or the
// whole file if shorter); whole says whether it is the whole file. name and
// claimed come from the client and are only cross-checked.
func detect(head []byte, size int64, name, claimed string) (kindInfo, error) {
	whole := size <= int64(len(head))
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	byName, nameKnown := extKinds[ext]
	if ext != "" && !nameKnown {
		return kindInfo{}, unsupported("This kind of file is not kept. Images (PNG, JPEG, GIF, WebP), PDF, plain text and Markdown are.")
	}
	var got kindInfo
	var binary bool
	switch {
	case isPNG(head):
		got, binary = kPNG, true
	case isJPEG(head):
		got, binary = kJPEG, true
	case isGIF(head):
		got, binary = kGIF, true
	case isWebP(head, size):
		got, binary = kWebP, true
	case pdfHeader(head):
		got, binary = kPDF, true
	default:
		if err := checkText(head, whole); err != nil {
			return kindInfo{}, err
		}
		got = kText
		if byName == kMD {
			got = kMD
		}
	}
	if binary && nameKnown && byName.Kind != "text" && byName.Mime != got.Mime {
		return kindInfo{}, unsupported("The file's contents do not match its name: the name says " + byName.Mime + " but the contents are " + got.Mime + ".")
	}
	if !binary && nameKnown && byName.Kind != "text" {
		return kindInfo{}, unsupported("The file's contents do not match its name: the name says " + byName.Mime + " but the contents are not.")
	}
	if binary && nameKnown && byName.Kind == "text" {
		return kindInfo{}, unsupported("The file's contents do not match its name: the name says text but the contents are " + got.Mime + ".")
	}
	if claimed != "" {
		if mt, _, err := mime.ParseMediaType(claimed); err == nil && mt != "application/octet-stream" && mt != "binary/octet-stream" {
			if strings.HasPrefix(mt, "image/") || mt == "application/pdf" {
				if mt != strings.SplitN(got.Mime, ";", 2)[0] && !(mt == "image/jpg" && got.Mime == "image/jpeg") {
					return kindInfo{}, unsupported("The declared type " + mt + " does not match the contents, which are " + got.Mime + ".")
				}
			}
		}
	}
	return got, nil
}

// pdfHeader reports whether the file starts like a PDF. The specification
// allows the header within the first 1024 bytes; accepting that would let a
// file that is mostly something else pass, so it must start the file (after an
// optional byte-order mark).
func pdfHeader(head []byte) bool {
	h := bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	return len(h) >= 8 && bytes.HasPrefix(h, []byte("%PDF-")) && h[5] >= '1' && h[5] <= '2' && h[6] == '.' && h[7] >= '0' && h[7] <= '9'
}

// The image checks are more than a prefix where that is cheap: a file is the
// kind it claims only if its first structure is the one that kind starts with.
// (image.DecodeConfig then reads the real header of PNG, JPEG and GIF.)

// isPNG: the signature, then the first chunk, which must be IHDR of length 13.
func isPNG(h []byte) bool {
	return len(h) >= 33 && bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")) &&
		h[8] == 0 && h[9] == 0 && h[10] == 0 && h[11] == 13 && bytes.Equal(h[12:16], []byte("IHDR"))
}

// isJPEG: the start-of-image marker followed by another marker segment.
func isJPEG(h []byte) bool {
	return len(h) >= 4 && h[0] == 0xff && h[1] == 0xd8 && h[2] == 0xff && h[3] >= 0xc0 && h[3] != 0xff
}

// isGIF: the header and the 7-byte logical screen descriptor, with a size.
func isGIF(h []byte) bool {
	return len(h) >= 13 && (bytes.HasPrefix(h, []byte("GIF87a")) || bytes.HasPrefix(h, []byte("GIF89a"))) &&
		(h[6] != 0 || h[7] != 0) && (h[8] != 0 || h[9] != 0)
}

// isWebP: RIFF, a size that is the whole file less 8 bytes, WEBP, and a known
// first chunk that fits inside it.
func isWebP(h []byte, size int64) bool {
	if len(h) < 20 || !bytes.Equal(h[:4], []byte("RIFF")) || !bytes.Equal(h[8:12], []byte("WEBP")) {
		return false
	}
	riff := int64(h[4]) | int64(h[5])<<8 | int64(h[6])<<16 | int64(h[7])<<24
	if riff+8 != size || riff < 12 {
		return false
	}
	switch string(h[12:16]) {
	case "VP8 ", "VP8L", "VP8X":
	default:
		return false
	}
	chunk := int64(h[16]) | int64(h[17])<<8 | int64(h[18])<<16 | int64(h[19])<<24
	return chunk <= riff-12
}

// checkText refuses what is not plain text: invalid UTF-8, NUL bytes, scripts
// and the XML family (SVG among them). A text file is only ever served as a
// download of type text/plain, so HTML in a .txt is inert; what is refused is
// what looks like a program or a vector image.
func checkText(head []byte, whole bool) error {
	if bytes.IndexByte(head, 0) >= 0 {
		return unsupported("This looks like a binary file, which is not kept. Images (PNG, JPEG, GIF, WebP), PDF, plain text and Markdown are.")
	}
	h := head
	if !whole {
		// The cut can fall inside a character: drop an incomplete tail.
		for i := 0; i < 3 && len(h) > 0 && !utf8.Valid(h); i++ {
			h = h[:len(h)-1]
		}
	}
	if !utf8.Valid(h) {
		return unsupported("This is not UTF-8 text, and not an image or PDF, so it is not kept.")
	}
	trim := bytes.TrimLeft(bytes.TrimPrefix(h, []byte("\xef\xbb\xbf")), " \t\r\n")
	low := bytes.ToLower(trim)
	switch {
	case bytes.HasPrefix(trim, []byte("#!")):
		return unsupported("A script is not kept.")
	case bytes.HasPrefix(low, []byte("<svg")), bytes.HasPrefix(low, []byte("<?xml")),
		bytes.HasPrefix(low, []byte("<!doctype svg")), bytes.Contains(low[:min(len(low), 1024)], []byte("<svg")):
		return unsupported("SVG is not kept: it can carry script. Upload a PNG or a PDF instead.")
	}
	if strings.HasPrefix(http.DetectContentType(h), "text/xml") {
		return unsupported("XML is not kept.")
	}
	return nil
}

// checkWholeText reads a stored text file end to end: no NUL byte and valid
// UTF-8 throughout. The file is bounded by the upload limit.
func checkWholeText(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return ops.Internal(err)
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return unsupported("This is not plain UTF-8 text, so it is not kept.")
	}
	return nil
}

// dimensions reads an image's size from its header.
func dimensions(path string, k kindInfo) (w, h int, err error) {
	if k.Kind != "image" || k.Ext == "webp" {
		return 0, 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, unsupported("The image is damaged or not really " + k.Mime + ".")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return 0, 0, unsupported("The image is too large to keep (over 100 megapixels).")
	}
	return cfg.Width, cfg.Height, nil
}

// CleanName makes a stored display name: no path, no control characters, a
// bounded length. It is never used to build a path.
func CleanName(name string) string {
	// Take the last path segment of either kind before sanitising, so
	// ..\..\evil.png and /etc/passwd become evil.png and passwd.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return ops.SafeFilename(name)
}
