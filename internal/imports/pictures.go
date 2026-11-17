package imports

import (
	"database/sql"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// Pictures that come with a converted source: a Scrivener project's
// Files/Data (a research image, a picture placed in the text), and the images
// folder of a Ghost or WordPress export sent as a zip. Nothing is ever fetched:
// a picture is imported only when its bytes are in what the writer sent. Each is
// checked and cleaned (attachments.PreparePicture: the upload's type policy,
// then imagesafe, so no location, camera or thumbnail survives), counted in the
// preview, and stored as an attachment of the project by the apply, in the
// import's transaction.
//
// The converter writes a picture as ![description](scriobh-picture:N), through
// the sink like any other construct, so foreign text can never make one (its
// brackets are escaped); the apply replaces N with the attachment's address. A
// picture is part of the token (its hash and its number), so an apply cannot
// store anything the preview did not count.

const (
	pictureScheme   = "scriobh-picture:"
	maxPictureFile  = 25 << 20  // one picture, as sent (the attachments default)
	maxPictures     = 2000      // pictures of one import
	maxPictureTotal = 192 << 20 // all of them, as sent
	maxPictureStore = 128 << 20 // all of them, cleaned
	// maxPictureTime bounds the cleaning of one import's pictures: a JPEG that
	// says it is rotated is decoded, turned and written again, and two thousand
	// of them must not hold a conversion slot for minutes.
	maxPictureTime = 20 * time.Second
)

// Picture options.
const (
	PicturesImport = "import"
	PicturesSkip   = "skip"
)

// cpicture is one picture a converter will import.
type cpicture struct {
	n   int // 1-based, in the order they were met
	src string
	pic attachments.Picture
	// always stores the picture even when no text shows it (a book's cover).
	always bool
}

// picBox collects the pictures of one conversion.
type picBox struct {
	on      bool
	list    []*cpicture
	bySrc   map[string]*cpicture
	failed  map[string]string // source path -> why it was not kept
	stored  int64
	refused *counter
	missing int // pictures the text names that were not in the upload
	left    int // pictures left out because the writer said so
	spent   time.Duration
	stop    func() error // the caller has gone
}

func newPicBox(o convOpts) *picBox {
	return &picBox{on: o.pictures != PicturesSkip, bySrc: map[string]*cpicture{}, failed: map[string]string{}, refused: newCounter(), stop: o.stop}
}

// isPictureName says a path names a picture by its extension (what is read of a
// zip or a folder; what it really is is decided from its bytes).
func isPictureName(lower string) bool {
	switch path.Ext(lower) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tif", ".tiff", ".heic", ".bmp", ".svg":
		return true
	}
	return false
}

// add takes the picture at src (a path in the source) with its bytes. It returns
// the picture, or "" and the reason it was not kept.
func (b *picBox) add(src string, data []byte, name string) (*cpicture, string) {
	if b == nil {
		return nil, "pictures are not read here"
	}
	key := strings.ToLower(src)
	if p, ok := b.bySrc[key]; ok {
		return p, ""
	}
	if why, ok := b.failed[key]; ok {
		return nil, why
	}
	why := ""
	switch {
	case !b.on:
		b.left++
		why = "pictures left out"
	case len(data) == 0:
		why = "not in what was sent"
	case len(b.list) >= maxPictures:
		why = fmt.Sprintf("over the %d-picture limit", maxPictures)
	case b.spent > maxPictureTime:
		why = "over the time the pictures of one import may take"
	case b.stop != nil && b.stop() != nil:
		why = "the import was stopped"
	}
	if why == "" {
		t0 := time.Now()
		pic, err := attachments.PreparePicture(name, data, maxPictureFile)
		b.spent += time.Since(t0)
		switch {
		case err != nil:
			why = err.Error()
		case b.stored+int64(len(pic.Data)) > maxPictureStore:
			why = fmt.Sprintf("over the %d MB of pictures one import holds", maxPictureStore>>20)
		default:
			p := &cpicture{n: len(b.list) + 1, src: src, pic: pic}
			b.stored += int64(len(pic.Data))
			b.list = append(b.list, p)
			b.bySrc[key] = p
			return p, ""
		}
	}
	if why != "pictures left out" {
		b.refused.add(why)
	}
	b.failed[key] = why
	return nil, why
}

// seg is the picture as a finished segment of a paragraph.
func (p *cpicture) seg(alt string) seg {
	alt = cleanText(alt, 120)
	return seg{wiki: "![" + escMD(alt) + "](" + pictureScheme + strconv.Itoa(p.n) + ")"}
}

// report puts the counts in a converter's report and its warnings.
func (b *picBox) report(r *cresult) {
	if b == nil {
		return
	}
	r.meta["pictures"] = len(b.list)
	if b.stored > 0 {
		r.meta["picture_bytes"] = b.stored
	}
	if !b.refused.empty() {
		r.meta["pictures_not_kept"] = b.refused.m
		r.warn("%d picture(s) were not kept (%s); each is marked [image] in the text.", b.refused.total(), topList(b.refused.m))
	}
	if b.missing > 0 {
		r.meta["pictures_not_sent"] = b.missing
	}
	if b.left > 0 {
		r.meta["pictures_left_out"] = b.left
	}
	if len(b.list) > 0 {
		r.warn("%d picture(s) will be imported as attachments of the project, cleaned of their location, camera and other details.", len(b.list))
	}
}

// picRe finds the converter's own picture links. Foreign text cannot write one:
// the sink escapes every bracket of it.
var picRe = regexp.MustCompile(`!\[((?:\\.|[^\]\\\n])*)\]\(` + pictureScheme + `([0-9]{1,5})\)`)

// placePictures replaces the picture links of a body with the stored addresses;
// a picture that has none (it could not be stored) becomes the [image] mark.
func placePictures(body string, urls map[int]string) string {
	if !strings.Contains(body, pictureScheme) {
		return body
	}
	return picRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := picRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[2])
		if u, ok := urls[n]; ok {
			return "![" + sub[1] + "](" + u + ")"
		}
		if sub[1] == "" {
			return `*\[image\]*`
		}
		return `*\[image: ` + sub[1] + `\]*`
	})
}

// pictureWant is how much of a picture is read from a zip.
func pictureWant(lower string) (int64, bool) {
	if isPictureName(lower) {
		return maxPictureFile, true
	}
	return 0, false
}

// applyPictures is the pictures of one apply: their ids, their addresses in the
// text, and the files staged for the transaction.
type applyPictures struct {
	im      *attachments.Import
	lim     attachments.Limits
	stored  int
	dropped int
	urls    map[int]string // picture number -> its address in the project
}

// preparePictures gives the pictures of an intake their attachments (new ones,
// or the ones the project already holds with the same bytes), writes their
// addresses into the bodies, and stages the new files. It runs before the
// transaction and before the bodies are hashed. Nothing to do returns nil.
func preparePictures(c *ops.Ctx, in *intake, projectID string, nodes []*pnode, rs []*rplan, out *ApplyOut) (*applyPictures, error) {
	if in.extras == nil || len(in.extras.pictures) == 0 {
		return nil, nil
	}
	ap := &applyPictures{lim: attachments.LimitsOf(c.Env)}
	var keep []attachments.Picture
	var nums []int
	// Only the pictures a kept document or note shows: one left out with its text is not stored.
	shown := shownPictures(nodes, rs)
	for _, p := range in.extras.pictures {
		if !shown[p.n] && !p.always {
			continue
		}
		if ap.lim.MaxBytes > 0 && int64(len(p.pic.Data)) > ap.lim.MaxBytes {
			ap.dropped++ // the server takes smaller files than the importer reads
			continue
		}
		keep = append(keep, p.pic)
		nums = append(nums, p.n)
	}
	urls := map[int]string{}
	ap.urls = urls
	if len(keep) > 0 {
		im, err := attachments.NewImport(c, c.Env, ap.lim, c.P.UserID, projectID, keep, c.DryRun())
		if err != nil {
			return nil, err
		}
		ap.im = im
		for i, ip := range im.Pictures() {
			urls[nums[i]] = ip.URL
		}
		ap.stored = len(keep)
	}
	for _, n := range nodes {
		n.body = placePictures(n.body, urls)
	}
	for _, r := range rs {
		r.body = placePictures(r.body, urls)
	}
	out.Pictures = ap.stored
	if ap.dropped > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d picture(s) are larger than this server keeps (%d MB) and are marked [image] instead.", ap.dropped, ap.lim.MaxBytes>>20))
	}
	return ap, nil
}

// lock takes the files lock before the transaction (nil does nothing).
func (ap *applyPictures) lock() {
	if ap != nil && ap.im != nil {
		ap.im.LockFiles()
	}
}

// commit stores the pictures in the transaction, before the documents.
func (ap *applyPictures) commit(c *ops.Ctx, tx *sql.Tx) error {
	if ap == nil || ap.im == nil {
		return nil
	}
	return ap.im.Commit(c, tx, ap.lim)
}

// done ends it: on failure the files this apply placed are removed.
func (ap *applyPictures) done(committed bool) {
	if ap != nil && ap.im != nil {
		ap.im.Done(committed)
	}
}

// shownPictures says which pictures the documents and notes that remain show.
func shownPictures(nodes []*pnode, rs []*rplan) map[int]bool {
	out := map[int]bool{}
	scan := func(body string) {
		if !strings.Contains(body, pictureScheme) {
			return
		}
		for _, m := range picRe.FindAllStringSubmatch(body, -1) {
			n, _ := strconv.Atoi(m[2])
			out[n] = true
		}
	}
	for _, n := range nodes {
		scan(n.body)
	}
	for _, r := range rs {
		scan(r.body)
	}
	return out
}
