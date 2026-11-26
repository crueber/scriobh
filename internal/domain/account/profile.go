// Package account holds the ops that act on the signed-in person themselves:
// their profile, their preferences, and their sessions. Every op here is
// Authenticated and scoped to the caller's own rows by Ctx.P.UserID; none takes
// a user id from the request, so one person can never reach another's data.
package account

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	_ "time/tzdata" // zone names must validate the same on every platform (the desktop build has no system zoneinfo)
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Locales is the documented list of interface and writing locales (F-004).
// Anything else is refused rather than stored and ignored.
var Locales = []string{"en", "ga", "fr", "de", "es", "it", "pt", "nl"}

const maxDisplayName = 80

// Profile is the wire shape of the caller's profile.
type Profile struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role" desc:"Instance role: admin, writer or reader."`
	Timezone    string `json:"timezone" desc:"IANA zone name; the day boundary for goals."`
	Locale      string `json:"locale" enum:"en,ga,fr,de,es,it,pt,nl"`
	AvatarColor string `json:"avatar_color"`
	Initials    string `json:"initials"`
}

// ProfileItem wraps the profile.
type ProfileItem struct {
	Item Profile `json:"item"`
}

// EmptyIn is an op input with no fields.
type EmptyIn struct{}

// UpdateIn changes the caller's profile. A nil field is left alone.
type UpdateIn struct {
	DisplayName *string `json:"display_name,omitempty" desc:"1 to 80 characters, no control characters."`
	Timezone    *string `json:"timezone,omitempty" desc:"IANA zone name such as Europe/Dublin."`
	Locale      *string `json:"locale,omitempty" desc:"One of en, ga, fr, de, es, it, pt, nl." enum:"en,ga,fr,de,es,it,pt,nl"`
}

// ZoneDefaultIn is the browser's zone, sent once by the first signed-in page. It is stored only while the person has no zone.
type ZoneDefaultIn struct {
	Timezone string `json:"timezone" desc:"The browser's IANA zone name, such as Europe/Dublin. Stored only if the person has never chosen a zone."`
}

// checkZone returns the zone name as it is stored, or the refusal for a value that is not one.
func checkZone(zone string) (string, error) {
	z := strings.TrimSpace(zone)
	// LoadLocation("") means UTC and "Local" means whatever the server is
	// set to; neither is a choice a person made, and "Local" would make the
	// goal day depend on the host.
	if z == "" || z == "Local" {
		return "", ops.Invalid("timezone", "Pick a time zone such as Europe/Dublin.")
	}
	if _, err := time.LoadLocation(z); err != nil {
		return "", ops.Invalid("timezone", "That is not a known time zone. Use a name such as Europe/Dublin.")
	}
	return z, nil
}

// Validate trims and checks every field that was sent.
func (in *UpdateIn) Validate() error {
	if in.DisplayName != nil {
		n := strings.TrimSpace(*in.DisplayName)
		switch {
		case n == "":
			return ops.Invalid("display_name", "Give yourself a name.")
		case utf8.RuneCountInString(n) > maxDisplayName:
			return ops.Invalid("display_name", "Your name is limited to 80 characters.")
		case strings.IndexFunc(n, unicode.IsControl) >= 0:
			return ops.Invalid("display_name", "Your name cannot contain line breaks or control characters.")
		}
		in.DisplayName = &n
	}
	if in.Timezone != nil {
		z, err := checkZone(*in.Timezone)
		if err != nil {
			return err
		}
		in.Timezone = &z
	}
	if in.Locale != nil {
		l := strings.TrimSpace(*in.Locale)
		if !contains(Locales, l) {
			return ops.Invalid("locale", "Locale must be one of: "+strings.Join(Locales, ", ")+".")
		}
		in.Locale = &l
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

const profileSQL = `SELECT id, COALESCE(email,''), display_name, role, timezone, locale, avatar_color
	FROM users WHERE id = ? AND deleted_at IS NULL`

func scanProfile(row *sql.Row) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.Email, &p.DisplayName, &p.Role, &p.Timezone, &p.Locale, &p.AvatarColor)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ops.NotFound("account")
	}
	if err != nil {
		return Profile{}, err
	}
	if p.AvatarColor == "" {
		p.AvatarColor = auth.AvatarColor(p.ID)
	}
	if p.DisplayName != "" {
		p.Initials = auth.Initials(p.DisplayName)
	} else {
		p.Initials = auth.Initials(p.Email)
	}
	return p, nil
}

func profileGet(c *ops.Ctx, _ EmptyIn) (ProfileItem, error) {
	p, err := scanProfile(c.DB().Read().QueryRowContext(c, profileSQL, c.P.UserID))
	return ProfileItem{Item: p}, err
}

func profileUpdate(c *ops.Ctx, in UpdateIn) (ProfileItem, error) {
	var out Profile
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var sets []string
		var args []any
		var fields []string
		add := func(column string, v *string) {
			if v != nil {
				sets = append(sets, column+" = ?")
				args = append(args, *v)
				fields = append(fields, column)
			}
		}
		add("display_name", in.DisplayName)
		add("timezone", in.Timezone)
		if in.Timezone != nil {
			sets = append(sets, "timezone_set = 1") // a zone the person sent is a choice, even when it is UTC
		}
		add("locale", in.Locale)
		if len(sets) > 0 {
			args = append(args, db.Now(), c.P.UserID)
			if _, err := tx.Exec(`UPDATE users SET `+strings.Join(sets, ", ")+`, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, args...); err != nil {
				return err
			}
			// Field names only, never the values.
			c.Audit(ops.AuditEntry{Action: "profile.update", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"fields": fields}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		var err error
		out, err = scanProfile(tx.QueryRowContext(c, profileSQL, c.P.UserID))
		return err
	})
	return ProfileItem{Item: out}, err
}

// profileZoneDefault stores the browser's zone when the person has never chosen one, and changes nothing otherwise. The write is
// conditional in SQL (timezone_set = 0), so two pages sending at once cannot overwrite each other or a choice made in between.
// The answer is the profile as it now is, whichever it was.
func profileZoneDefault(c *ops.Ctx, in ZoneDefaultIn) (ProfileItem, error) {
	z, err := checkZone(in.Timezone)
	if err != nil {
		return ProfileItem{}, err
	}
	var out Profile
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE users SET timezone = ?, timezone_set = 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND timezone_set = 0`, z, db.Now(), c.P.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			// The field name only, never the zone, as profile.update does.
			c.Audit(ops.AuditEntry{Action: "profile.zone_default", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"fields": []string{"timezone"}}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		out, err = scanProfile(tx.QueryRowContext(c, profileSQL, c.P.UserID))
		return err
	})
	return ProfileItem{Item: out}, err
}
