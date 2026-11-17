package studio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The private progress card (F-516).
//
// A card is a link to a small page, and an SVG of the same, that shows a streak and a goal summary and nothing of
// any project's text: no document title, no comment, no word of a manuscript, and no project title unless the owner
// ticked that for this card. The token is unguessable (32 random bytes; only a SHA-256 of it is stored, and it is
// shown once), the link expires (at most a year) and can be revoked, and the owner sees how many times it was viewed.
// The page and the SVG carry no cookie, no script, no font and no address that is not this server's own, and say
// noindex. The PNG is made in the browser from the SVG; there is no rasteriser here.
//
// What a card shows is worked out each time it is opened, through NoSessionViewer: a project that has a passphrase
// at all, one in the trash or one the person has left contributes nothing, so locking a project takes it off every
// card at once.

const (
	maxCards       = 5
	maxCardLife    = 365
	defaultCardDay = 30
	maxCardLabel   = 60 // the owner's own note
	maxCardTitle   = 40 // a project's title on a card
	maxCardLine    = 80 // any line of the picture
	maxGoalLabel   = 52 // the words of a goal and its project: they end before the figures on the right
)

var cardTokenRe = regexp.MustCompile(`^(crd_[0-9A-HJKMNP-TV-Z]{26})\.([A-Za-z0-9_-]{43})$`)

func cardHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

var cardDummy = cardHash("no such card")

// CardIn makes a card.
type CardIn struct {
	Label        string `json:"label,omitempty" desc:"Your own note, up to 60 characters. Never shown on the card."`
	ShowProjects bool   `json:"show_projects,omitempty" desc:"Name the project of a goal that is about one project. Off by default: the card then says only that it is a project goal."`
	ExpiresDays  int    `json:"expires_days,omitempty" desc:"1 to 365. Default 30."`
}

// Validate checks the card.
func (in *CardIn) Validate() error {
	in.Label = strings.TrimSpace(in.Label)
	if len([]rune(in.Label)) > maxCardLabel || strings.IndexFunc(in.Label, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ops.Invalid("label", "A label is up to 60 characters with no line breaks.")
	}
	if in.ExpiresDays == 0 {
		in.ExpiresDays = defaultCardDay
	}
	if in.ExpiresDays < 1 || in.ExpiresDays > maxCardLife {
		return ops.Invalid("expires_days", fmt.Sprintf("A card lasts between 1 and %d days.", maxCardLife))
	}
	return nil
}

// CardOut is a card as its owner sees it.
type CardOut struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	ShowProjects bool   `json:"show_projects"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
	Revoked      bool   `json:"revoked"`
	Expired      bool   `json:"expired"`
	Views        int    `json:"views" desc:"How many times the page was opened."`
	LastViewedAt string `json:"last_viewed_at,omitempty"`
	Token        string `json:"token,omitempty" desc:"Only in the answer that makes the card, once: the secret. The link is /card/ followed by it. It cannot be shown again."`
	Path         string `json:"path,omitempty" desc:"Only with the token: the page's address on this server, /card/<token>. The SVG is that address followed by /card.svg and the numbers as JSON /card.json."`
}

// CardItem wraps a card.
type CardItem struct {
	Item CardOut `json:"item"`
}

func cardCreate(c *ops.Ctx, in CardIn) (CardItem, error) {
	var out CardOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var n int
		now := db.NowTime()
		if err := tx.QueryRow(`SELECT count(*) FROM studio_cards WHERE user_id = ? AND revoked_at = '' AND expires_at > ?`, c.P.UserID, db.Format(now)).Scan(&n); err != nil {
			return err
		}
		if n >= maxCards {
			return ops.ConflictWith(fmt.Sprintf("You have %d cards already. Revoke one to make another.", maxCards), map[string]any{"reason": "too_many_cards"})
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return ops.Internal(err)
		}
		secret := base64.RawURLEncoding.EncodeToString(b[:])
		id := db.NewID("crd")
		show := 0
		if in.ShowProjects {
			show = 1
		}
		exp := now.Add(time.Duration(in.ExpiresDays) * 24 * time.Hour)
		if _, err := tx.Exec(`INSERT INTO studio_cards(id, user_id, token_hash, label, show_projects, expires_at, created_at) VALUES (?,?,?,?,?,?,?)`,
			id, c.P.UserID, cardHash(secret), in.Label, show, db.Format(exp), db.Format(now)); err != nil {
			return err
		}
		out = CardOut{ID: id, Label: in.Label, ShowProjects: in.ShowProjects, CreatedAt: db.Format(now), ExpiresAt: db.Format(exp),
			Token: id + "." + secret, Path: "/card/" + id + "." + secret}
		return nil
	})
	return CardItem{Item: out}, err
}

// CardListIn lists the person's cards.
type CardListIn struct{}

// CardListOut is the person's cards.
type CardListOut struct {
	Items []CardOut `json:"items" desc:"Newest first. The secret is never in this list."`
}

func cardList(c *ops.Ctx, _ CardListIn) (CardListOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, label, show_projects, created_at, expires_at, revoked_at, views, last_viewed_at FROM studio_cards
		WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT 50`, c.P.UserID)
	if err != nil {
		return CardListOut{}, err
	}
	defer rows.Close()
	out := CardListOut{Items: []CardOut{}}
	now := db.Now()
	cards := cardsOf(c.Env)
	for rows.Next() {
		var o CardOut
		var show int
		var revoked string
		if err := rows.Scan(&o.ID, &o.Label, &show, &o.CreatedAt, &o.ExpiresAt, &revoked, &o.Views, &o.LastViewedAt); err != nil {
			return out, err
		}
		o.ShowProjects, o.Revoked, o.Expired = show == 1, revoked != "", o.ExpiresAt <= now
		if n, last := cards.Pending(o.ID); n > 0 { // views counted in memory and not yet written
			o.Views += n
			o.LastViewedAt = max(o.LastViewedAt, last)
		}
		out.Items = append(out.Items, o)
	}
	return out, rows.Err()
}

// CardRevokeIn revokes a card.
type CardRevokeIn struct {
	ID string `path:"id" json:"-" desc:"Card id."`
}

// Validate checks the id.
func (in *CardRevokeIn) Validate() error {
	if !db.ValidID(in.ID) || !strings.HasPrefix(in.ID, "crd_") {
		return ops.Invalid("id", "That is not a card id.")
	}
	return nil
}

func cardRevoke(c *ops.Ctx, in CardRevokeIn) (CardItem, error) {
	var out CardOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE studio_cards SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at = ''`, db.Now(), in.ID, c.P.UserID)
		if err != nil {
			return err
		}
		var show int
		var revoked string
		err = tx.QueryRow(`SELECT id, label, show_projects, created_at, expires_at, revoked_at, views, last_viewed_at FROM studio_cards WHERE id = ? AND user_id = ?`, in.ID, c.P.UserID).
			Scan(&out.ID, &out.Label, &show, &out.CreatedAt, &out.ExpiresAt, &revoked, &out.Views, &out.LastViewedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("card")
		}
		if err != nil {
			return err
		}
		_ = res
		out.ShowProjects, out.Revoked, out.Expired = show == 1, revoked != "", out.ExpiresAt <= db.Now()
		cardsOf(c.Env).Forget(out.ID)
		return nil
	})
	return CardItem{Item: out}, err
}

// ---- what a card shows ------------------------------------------------------------------------------------

// ErrNoCard is every reason a token opens nothing: unknown, malformed, revoked, expired, or its owner gone.
var ErrNoCard = errors.New("studio: no such card")

// CardGoal is a goal on a card: numbers and the words that say what it measures, never a title.
type CardGoal struct {
	Period  string `json:"period"`
	Measure string `json:"measure"`
	Scope   string `json:"scope" enum:"all,project"`
	Project string `json:"project,omitempty" desc:"The project's title, only when the card's owner chose to show projects."`
	Target  int    `json:"target"`
	Value   int    `json:"value"`
	Percent int    `json:"percent"`
	Met     bool   `json:"met"`
}

// CardView is everything a card shows.
type CardView struct {
	Day           string     `json:"day"`
	Streak        int        `json:"streak"`
	Longest       int        `json:"longest_streak"`
	TodayHit      bool       `json:"today_hit"`
	HasGoal       bool       `json:"has_goal"`
	WritingDays30 int        `json:"writing_days_30"`
	GoalsActive   int        `json:"goals_active"`
	GoalsDone     int        `json:"goals_completed"`
	Goals         []CardGoal `json:"goals"`
	ExpiresAt     string     `json:"expires_at"`
}

// buildCardView works out what a card shows now, from the numbers of the projects that have no passphrase at all (the viewer of
// a link has no session to unlock one). show is whether the card's owner chose to name projects.
func buildCardView(ctx context.Context, d *db.DB, user, expires string, show bool) (CardView, error) {
	v, err := NoSessionViewer(ctx, d.Read(), user)
	if err != nil {
		return CardView{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return CardView{}, err
	}
	gs, err := s.goalsOut(false)
	if err != nil {
		return CardView{}, err
	}
	out := CardView{Day: v.Today, Streak: s.streak, Longest: s.longest, TodayHit: s.hits[v.Today], HasGoal: s.hasDaily,
		WritingDays30: figuresOf(s.all.Days, addDays(v.Today, -29), v.Today).WritingDays,
		GoalsActive:   gs.Active, GoalsDone: gs.Completed, Goals: []CardGoal{}, ExpiresAt: expires}
	items := append([]GoalOut(nil), gs.Items...)
	rank := map[string]int{"day": 0, "week": 1, "month": 2, "total": 3}
	sort.SliceStable(items, func(i, j int) bool { return rank[items[i].Period] < rank[items[j].Period] })
	for _, g := range items {
		if len(out.Goals) == 3 {
			break
		}
		cg := CardGoal{Period: g.Period, Measure: g.Measure, Scope: "all", Target: g.Target, Value: g.Value, Percent: g.Percent, Met: g.Met}
		if g.ProjectID != "" {
			cg.Scope = "project"
			if show {
				// A title is the project owner's text: it is cleaned once here, so the page, the JSON and the picture all
				// hold the same plain, short words.
				cg.Project = cleanText(g.ProjectTitle, maxCardTitle)
			}
		}
		out.Goals = append(out.Goals, cg)
	}
	return out, nil
}

// countT is a message with a count: key.one for 1 and key.other for anything else, with {n} the count (the same rule as
// i18n.Catalog.N, for a caller that has only a translating function).
func countT(t func(key string, args ...any) string, key string, n int, args ...any) string {
	form := ".other"
	if n == 1 {
		form = ".one"
	}
	return t(key+form, append([]any{"n", n}, args...)...)
}

// CardSVG draws a card. t gives the words (a message key and its parameters), so the page can be in the reader's
// language; every number and every word that is not from t is escaped. Flat fills and a hairline, no gradient, no
// font file, no script, nothing external.
func CardSVG(v CardView, t func(key string, args ...any) string) []byte {
	// Every dynamic string is made plain text first (what XML 1.0 forbids, bidirectional and invisible controls and anything
	// past the card's width removed, cut before it is escaped so no entity is split) and then escaped.
	e := func(s string) string { return html.EscapeString(cleanText(s, maxCardLine)) }
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 600 315" width="600" height="315" role="img" aria-labelledby="card-title">`)
	fmt.Fprintf(&b, `<title id="card-title">%s</title>`, e(t("studio.card.title")))
	b.WriteString(`<rect x="0.5" y="0.5" width="599" height="314" rx="12" fill="#FAF8F3" stroke="#CFC9BC"/>`)
	lines := func(y int, rows ...string) int {
		for _, r := range rows {
			if r == "" {
				continue
			}
			fmt.Fprintf(&b, `<text x="36" y="%d" font-family="system-ui, sans-serif" font-size="14" fill="#6B675E">%s</text>`, y, e(r))
			y += 20
		}
		return y
	}
	if v.Streak > 0 {
		fmt.Fprintf(&b, `<text x="36" y="76" font-family="Georgia, serif" font-size="64" font-weight="700" fill="#2F5D50">%d</text>`, v.Streak)
		fmt.Fprintf(&b, `<text x="36" y="104" font-family="system-ui, sans-serif" font-size="18" fill="#3B3A36">%s</text>`, e(countT(t, "studio.card.streak", v.Streak)))
		lines(132, countT(t, "studio.card.longest", v.Longest), countT(t, "studio.card.days30", v.WritingDays30), countT(t, "studio.card.goals", v.GoalsActive, "active", v.GoalsActive, "done", v.GoalsDone))
	} else {
		// A run that has lapsed is not drawn as a large 0: the card leads with what the past month holds, and says nothing
		// of a run when there is none to say.
		y := 112
		if v.WritingDays30 > 0 {
			// the line is sized to the width of the card: about 0.6 em a character in a bold serif, 528 units of room, so a longer
			// sentence (a two-digit count, another language) shrinks and never reaches the edge
			lead := countT(t, "studio.card.days30", v.WritingDays30)
			size := max(12, min(24, 528*10/(6*max(1, len([]rune(cleanText(lead, maxCardLine)))))))
			fmt.Fprintf(&b, `<text x="36" y="80" font-family="Georgia, serif" font-size="%d" font-weight="700" fill="#2F5D50">%s</text>`, size, e(lead))
		}
		longest := ""
		if v.Longest > 0 {
			longest = countT(t, "studio.card.longest", v.Longest)
		}
		lines(y, longest, countT(t, "studio.card.goals", v.GoalsActive, "active", v.GoalsActive, "done", v.GoalsDone))
	}
	y := 200
	for _, g := range v.Goals {
		label := cleanText(t("studio.card.goal", "period", t("studio.period."+g.Period), "measure", t("studio.measure."+g.Measure)), maxCardLine)
		proj := ""
		if p := cleanText(g.Project, maxCardTitle); p != "" {
			// The words of the goal and its project must end before the figures that share the line on the right.
			if room := maxGoalLabel - len([]rune(label)) - 2; room >= 6 {
				proj = cleanText(p, room)
			}
		}
		if proj != "" {
			// The project's name is a text of its own, its direction read from its first strong letter: a name in Hebrew or
			// Arabic ends, with its ellipsis, at its own left, and does not carry the goal's words into its run.
			fmt.Fprintf(&b, `<text x="36" y="%d" font-family="system-ui, sans-serif" font-size="13" fill="#3B3A36">%s, <tspan unicode-bidi="plaintext">%s</tspan></text>`, y, e(label), html.EscapeString(proj))
		} else {
			fmt.Fprintf(&b, `<text x="36" y="%d" font-family="system-ui, sans-serif" font-size="13" fill="#3B3A36">%s</text>`, y, e(label))
		}
		fmt.Fprintf(&b, `<rect x="36" y="%d" width="528" height="8" rx="4" fill="#E7E2D6"/>`, y+8)
		w := min(g.Percent, 100) * 528 / 100
		if w > 0 {
			fmt.Fprintf(&b, `<rect x="36" y="%d" width="%d" height="8" rx="4" fill="#2F5D50"/>`, y+8, w)
		}
		fmt.Fprintf(&b, `<text x="564" y="%d" text-anchor="end" font-family="system-ui, sans-serif" font-size="13" fill="#6B675E">%d / %d</text>`, y, g.Value, g.Target)
		y += 30
	}
	fmt.Fprintf(&b, `<text x="564" y="304" text-anchor="end" font-family="system-ui, sans-serif" font-size="12" fill="#6B675E">%s</text>`, e(t("studio.card.footer", "day", v.Day)))
	b.WriteString(`</svg>`)
	return []byte(b.String())
}
