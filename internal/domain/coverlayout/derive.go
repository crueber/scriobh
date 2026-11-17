package coverlayout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/imagesafe"
	"github.com/crueber/scriobh/internal/ops"
)

// The pictures the screens and the ebooks show are made from the layout and the attachments on first use, kept
// under files/projects/<id>/covers/, and swept when the layout changes. They are never the source of truth and are
// not in a backup: a restored project makes them again.
//
//	front   the front, at most 2560 px on the long side, JPEG
//	back    the back, the same
//	thumb   the front 480 px wide, for the library and the tree row
//
// Making one decodes a picture that may be tens of megapixels, so it runs inside the compile gate (the same slots,
// SCRIOBH_COMPILE_CONCURRENCY) and one at a time per project. A caller that already holds a slot (a compile, a
// site build) says so, and takes no second one.

// Derivative names.
const (
	NameFront = "front"
	NameBack  = "back"
	NameThumb = "thumb"
)

// GateService is the name the server registers a *Gate under in ops.Env.
const GateService = "coverlayout.gate"

const busyWait = 2 * time.Second

// Gate bounds how many derivatives are made at once. The server builds it over the compile gate's own slots
// (NewGateOn), so a compile, a site build and a cover together never exceed one limit.
type Gate struct {
	slots chan struct{}
	Wait  time.Duration
}

// NewGateOn makes a gate over slots another gate owns.
func NewGateOn(slots chan struct{}) *Gate { return &Gate{slots: slots, Wait: busyWait} }

var (
	defaultGateOnce sync.Once
	defaultGate     *Gate
)

func gateOf(env *ops.Env) *Gate {
	if g, ok := ops.Service[*Gate](env, GateService); ok && g != nil {
		return g
	}
	defaultGateOnce.Do(func() { defaultGate = &Gate{slots: make(chan struct{}, 2), Wait: busyWait} })
	return defaultGate
}

func (g *Gate) acquire(ctx context.Context) (func(), error) {
	release := func() { <-g.slots }
	select {
	case g.slots <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(g.Wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy preparing other books. Try again in a few seconds.", RetryAfterSeconds: 5}
	}
}

// projectLocks serialise the work of one project's derivatives that does not hold a slot of the compile gate, and the
// sweeps. The order is one everywhere: the lock, then a slot, then the memory. A caller that is already inside the gate
// (a compile or a site build holding its slot) takes no lock, since it would wait for one while the holder waits for a
// slot: what it writes is safe without it (a derivative is named by its key, so two makers write the same bytes, the write
// is atomic, and it is checked once more after it is in place, see writeDerived). A lock is a channel of one so that
// waiting for it can be given up (a cancelled request, the busy wait).
var projectLocks sync.Map

func projectLock(projectID string) chan struct{} {
	v, _ := projectLocks.LoadOrStore(projectID, make(chan struct{}, 1))
	return v.(chan struct{})
}

// lockProjectWait takes the lock, however long it takes: for a sweep, which must not give up (it removes what a deleted
// picture left). Whoever holds the lock is bounded: it waits for a slot for a few seconds at most and then builds.
func lockProjectWait(projectID string) (unlock func()) {
	ch := projectLock(projectID)
	ch <- struct{}{}
	return func() { <-ch }
}

func lockProject(ctx context.Context, projectID string) (unlock func(), err error) {
	ch := projectLock(projectID)
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	default:
	}
	t := time.NewTimer(busyWait * 3)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy preparing this book's cover. Try again in a few seconds.", RetryAfterSeconds: 5}
	}
}

// decodeMem bounds the memory all the covers being decoded at once may take, whatever the gate's width: a derivative
// asks for what imagesafe.DecodeBytes says its picture takes (and room for the scaler's output), and waits, then is told
// the server is busy, when that is not free. One that needs more than the whole is let through alone.
var decodeMem = &memGate{cap: 2 * MaxDecodeBytes, wait: busyWait, wake: make(chan struct{})}

type memGate struct {
	mu   sync.Mutex
	used int64
	cap  int64
	wait time.Duration
	wake chan struct{}
}

func (g *memGate) acquire(ctx context.Context, n int64) (func(), error) {
	deadline := time.NewTimer(g.wait)
	defer deadline.Stop()
	for {
		g.mu.Lock()
		if g.used == 0 || g.used+n <= g.cap {
			g.used += n
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				g.used -= n
				old := g.wake
				g.wake = make(chan struct{})
				g.mu.Unlock()
				close(old)
			}, nil
		}
		w := g.wake
		g.mu.Unlock()
		select {
		case <-w:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy preparing other covers. Try again in a few seconds.", RetryAfterSeconds: 5}
		}
	}
}

// Derived is a made picture.
type Derived struct {
	Name          string
	Key           string // the cache key: it changes with the layout, the rules and the book's direction
	Data          []byte
	Width, Height int
}

// ETag is a strong validator for the picture.
func (d *Derived) ETag() string { return `"` + d.Key[:24] + "-" + d.Name + `"` }

// key is the cache key of a layout's derivatives: its hash, the version of the rules, and the direction of the book
// (a wrap's front is on the other side for a right-to-left book, and the language is not part of the layout).
func key(l *Layout, rtl bool) string {
	d := "ltr"
	if rtl {
		d = "rtl"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00v%d\x00%s", l.Hash, Version, d)))
	return hex.EncodeToString(sum[:])
}

// Key is the cache key for a layout in a project of the given direction.
func Key(l *Layout, rtl bool) string { return key(l, rtl) }

func derivedPath(dataDir, projectID, k, name string) (string, error) {
	dir, err := attachments.CoversDir(dataDir, projectID)
	if err != nil {
		return "", err
	}
	if len(k) != 64 || strings.Trim(k, "0123456789abcdef") != "" || (name != NameFront && name != NameBack && name != NameThumb) {
		return "", errors.New("coverlayout: bad derivative name")
	}
	return filepath.Join(dir, k+"-"+name+".jpg"), nil
}

// ErrNoPicture says the layout has no picture for the part asked for: the generated cover applies.
var ErrNoPicture = errNoPicture

// Make returns one derivative of the layout in effect for a project, making it when it is not yet kept. It returns
// nil, nil when the project has no layout or the layout gives that part no picture (the caller shows the generated
// cover). held says the caller already holds a slot of the compile gate. A layout whose picture has gone, is damaged or
// is no longer a cover is ErrNoPicture, which the caller treats as the generated cover (it is logged here).
func Make(ctx context.Context, env *ops.Env, projectID, name string, held bool) (*Derived, *Layout, error) {
	return MakeFor(ctx, env, projectID, name, held, "")
}

// MakeFor is Make for a book read in a language other than the project's own (a compile's language option): a wrap's
// front is on the left for a right-to-left language. "" is the project's language.
func MakeFor(ctx context.Context, env *ops.Env, projectID, name string, held bool, language string) (*Derived, *Layout, error) {
	lay, err := Load(ctx, env.DB.Read(), projectID)
	if err != nil || lay == nil {
		return nil, nil, err
	}
	switch {
	case name == NameBack && !lay.HasBack(), name != NameBack && !lay.HasFront():
		return nil, lay, nil
	}
	pr, err := loadProject(ctx, env.DB.Read(), projectID)
	if err != nil {
		return nil, lay, err
	}
	if language != "" {
		pr.Language = language
	}
	d, err := derive(ctx, env, lay, pr.rtl(), name, held)
	return d, lay, err
}

// Warm makes the front and the thumbnail of a layout that has just been saved, so the first look at the cover does not
// wait for them (and does not meet a busy server). It is best effort: it takes a slot of the gate like any other, and a
// failure only means they are made when they are asked for.
func Warm(ctx context.Context, env *ops.Env, projectID string) {
	if _, _, err := MakeFor(ctx, env, projectID, NameThumb, false, ""); err != nil && !errors.Is(err, ErrNoPicture) && env.Log != nil {
		env.Log.Debug("cover not warmed", "project", projectID, "err", err.Error())
	}
}

// derive finds or makes a derivative of a layout.
func derive(ctx context.Context, env *ops.Env, lay *Layout, rtl bool, name string, held bool) (*Derived, error) {
	k := key(lay, rtl)
	path, err := derivedPath(env.DataDir, lay.ProjectID, k, name)
	if err != nil {
		return nil, ops.Internal(err)
	}
	if d := readDerived(env.DataDir, path, k, name); d != nil {
		return d, nil
	}
	if !held { // lock, then slot, then memory: the one order (see projectLocks)
		unlock, err := lockProject(ctx, lay.ProjectID)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if d := readDerived(env.DataDir, path, k, name); d != nil { // made while this waited
			return d, nil
		}
		release, err := gateOf(env).acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	d, err := deriveLocked(ctx, env, lay, rtl, name)
	if err != nil {
		// A picture that is gone (a restore made without files), damaged, or no longer a cover is not a reason to
		// refuse an export or a page: the generated cover stands in, and the cover ops show the problem as a warning.
		var oe *ops.Error
		if errors.As(err, &oe) && (oe.Code == ops.CodeNotFound || oe.Code == ops.CodeInvalid) {
			if env.Log != nil {
				env.Log.Warn("a cover picture cannot be used; the generated cover stands in", "project", lay.ProjectID, "part", name, "reason", oe.Message)
			}
			return nil, fmt.Errorf("%w (%s)", ErrNoPicture, oe.Message)
		}
		return nil, err
	}
	return d, nil
}

// afterBuild, when set, is called in a test between making a picture and keeping it.
var afterBuild func()

// deriveLocked makes a derivative with a slot held (and the project's lock too, unless the caller is inside the gate). The thumbnail is made from the front, which
// is made (and kept) first.
func deriveLocked(ctx context.Context, env *ops.Env, lay *Layout, rtl bool, name string) (*Derived, error) {
	k := key(lay, rtl)
	path, err := derivedPath(env.DataDir, lay.ProjectID, k, name)
	if err != nil {
		return nil, ops.Internal(err)
	}
	if d := readDerived(env.DataDir, path, k, name); d != nil {
		return d, nil
	}
	var front *Derived
	if name == NameThumb {
		if front, err = deriveLocked(ctx, env, lay, rtl, NameFront); err != nil {
			return nil, err
		}
	}
	data, err := build(ctx, env, lay, rtl, name, front)
	if err != nil {
		return nil, err
	}
	if afterBuild != nil {
		afterBuild()
	}
	// Kept only if the layout is still the one it was made for, and checked again once it is in place: a picture deleted or
	// a layout changed while this was being made must not leave a file made from the old one behind. The change commits
	// before its sweep runs, so a file that is in place when the sweep looks is removed by it, and one that is put in place
	// after is removed by its maker's second check.
	current := func() bool {
		cur, lerr := Load(ctx, env.DB.Read(), lay.ProjectID)
		return lerr == nil && cur != nil && key(cur, rtl) == k
	}
	if current() {
		if err := writeDerived(env.DataDir, path, k, data, current); err != nil && env.Log != nil {
			env.Log.Warn("cover derivative not kept", "err", err)
		}
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ops.Internal(err)
	}
	return &Derived{Name: name, Key: k, Data: data, Width: cfg.Width, Height: cfg.Height}, nil
}

// readDerived reads a kept derivative; a missing or damaged file is no derivative.
func readDerived(dataDir, path, k, name string) *Derived {
	f, fi, err := attachments.OpenRegular(dataDir, path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi.Size() > 32<<20 {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, fi.Size()+1))
	if err != nil || int64(len(b)) != fi.Size() {
		return nil
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return &Derived{Name: name, Key: k, Data: b, Width: cfg.Width, Height: cfg.Height}
}

func writeDerived(dataDir, path, k string, data []byte, current func() bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	if !current() { // the layout changed while this was being put in place, and its sweep may have passed: take it out again
		_ = os.Remove(path)
		return nil
	}
	sweepOthers(dir, k)
	return nil
}

// staleTemp is how old a temporary file is before it is taken for the leavings of a write that was cut short: a write
// takes seconds, and a sweep never removes the file of one that is under way.
const staleTemp = 10 * time.Minute

// sweepOthers removes every kept picture that is not of the key (keep "" removes them all), and a temporary file that is
// old enough to be the leavings of a write that did not finish.
func sweepOthers(dir, keep string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		switch {
		case strings.HasPrefix(n, "tmp-"):
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleTemp {
				_ = os.Remove(filepath.Join(dir, n))
			}
		case keep == "" || !strings.HasPrefix(n, keep+"-"):
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	if keep == "" {
		_ = os.Remove(dir) // when it is empty
	}
}

// Sweep removes a project's kept cover pictures: when the layout is cleared, or changes to one with another key.
func Sweep(dataDir, projectID, keep string) {
	dir, err := attachments.CoversDir(dataDir, projectID)
	if err != nil {
		return
	}
	defer lockProjectWait(projectID)() // after a derivation that is under way has kept or dropped its picture; it waits as long as that takes
	sweepOthers(dir, keep)
}

// readPicture reads a stored attachment's bytes through the checks every file read goes through.
func readPicture(ctx context.Context, env *ops.Env, projectID, attID string) ([]byte, error) {
	a, path, err := attachments.Open(ctx, env, attachments.OwnerProject, projectID, attID)
	if err != nil {
		return nil, err
	}
	f, _, err := attachments.OpenRegular(env.DataDir, path)
	if err != nil {
		return nil, ops.NotFound("attachment")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, a.Bytes+1))
	if err != nil || int64(len(b)) != a.Bytes {
		return nil, ops.NotFound("attachment")
	}
	return b, nil
}

// build makes a derivative's bytes.
func build(ctx context.Context, env *ops.Env, lay *Layout, rtl bool, name string, front *Derived) ([]byte, error) {
	if name == NameThumb {
		return renderFrom(ctx, front.Data, ThumbQuality, func(d Decoded) (*image.RGBA, error) { return RenderThumb(d.Img), nil })
	}
	t, ok := LookupTrim(lay.Trim)
	if !ok {
		return nil, ops.Internal(fmt.Errorf("coverlayout: trim %q", lay.Trim))
	}
	pics := newPictureSet(ctx, env.DB.Read(), lay.ProjectID)
	var side Side
	isFront := name == NameFront
	if lay.Shape == ShapeWrap && lay.Wrap != nil {
		p, ok := pics.look(lay.Wrap.Attachment)
		if !ok {
			return nil, ErrNoPicture
		}
		raw, err := readPicture(ctx, env, lay.ProjectID, p.ID)
		if err != nil {
			return nil, err
		}
		return renderFrom(ctx, raw, FrontQuality, func(d Decoded) (*image.RGBA, error) {
			g, err := MeasureWrap(d.W, d.H, t, lay.Wrap.Bleed(), rtl)
			if err != nil {
				return nil, err
			}
			r := g.Back
			if isFront {
				r = g.Front
			}
			return RenderCrop(d, r, MaxDerivedSide), nil
		})
	}
	if isFront {
		side = lay.Front
	} else {
		side = lay.Back
	}
	if side.Mode != ModeImage && side.Mode != ModePage {
		return nil, ErrNoPicture
	}
	if _, ok := pics.look(side.Attachment); !ok {
		return nil, ErrNoPicture
	}
	raw, err := readPicture(ctx, env, lay.ProjectID, side.Attachment)
	if err != nil {
		return nil, err
	}
	return renderFrom(ctx, raw, FrontQuality, func(d Decoded) (*image.RGBA, error) {
		if side.Mode == ModeImage {
			return RenderCrop(d, CropToAspect(d.W, d.H, t), MaxDerivedSide), nil
		}
		return RenderPage(d, t, side)
	})
}

// renderFrom decodes a picture (once the memory it takes is free to take), draws what render says from it, and writes the
// result as a JPEG that carries the picture's own colour profile.
func renderFrom(ctx context.Context, raw []byte, quality int, render func(Decoded) (*image.RGBA, error)) ([]byte, error) {
	est := decodeEstimate(raw)
	release, err := decodeMem.acquire(ctx, est)
	if err != nil {
		return nil, err
	}
	defer func() {
		release()
		if est > 160<<20 { // a big picture: give what it took back to the system now, not at the collector's leisure
			debug.FreeOSMemory()
		}
	}()
	d, err := Decode(raw)
	if err != nil {
		return nil, refuse(err)
	}
	out, err := render(d)
	if err != nil {
		return nil, refuse(err)
	}
	d.Img = nil
	jpg, err := EncodeJPEG(out, quality)
	if err != nil {
		return nil, ops.Internal(err)
	}
	icc, _ := profileOf(raw)
	return withICC(jpg, icc), nil
}

// decodeEstimate is what decoding and drawing a picture takes: its planes (and a progressive JPEG's coefficients), or
// eight bytes a pixel for a PNG, and room for the picture being made.
func decodeEstimate(raw []byte) int64 {
	if n, ok := imagesafe.DecodeBytes(raw); ok {
		return n + decodeRoom
	}
	if w, h, _, _, err := Header(raw); err == nil {
		return int64(w)*int64(h)*8 + decodeRoom
	}
	return decodeRoom
}

// decodeRoom is what a decode is given beyond the picture itself: the scaler's output, the canvas and the cut.
const decodeRoom = 64 << 20

// refuse turns a Refusal into the error an op returns; anything else is internal.
func refuse(err error) error {
	var r *Refusal
	if errors.As(err, &r) {
		return &ops.Error{Code: ops.CodeInvalid, Message: r.Message, Data: map[string]any{"reason": r.Code}}
	}
	if _, ok := err.(*ops.Error); ok {
		return err
	}
	return ops.Internal(err)
}
