package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Custom fields are defined per Compendium and per entity kind, and typed:
// text, number, date or choice. A value is stored as a canonical string, so the
// same value is always the same bytes (the mirror and the export depend on it).
//
// The five kinds are fixed (they are a CHECK constraint). A writer who needs
// something else models it as a field on the closest kind or as a concept; a
// custom-kind table would be a new migration, not a change to these ops.

// Kinds an entity can have.
var Kinds = []string{"character", "place", "item", "org", "concept"}

// FieldTypes a custom field can have.
var FieldTypes = []string{"text", "number", "date", "choice"}

const (
	maxFieldKey    = 32
	maxFieldLabel  = 60
	maxFieldText   = 4000
	maxChoices     = 50
	maxChoiceLen   = 80
	maxFieldsDefs  = 60 // per kind
	maxFieldValues = 60 // per entity
)

var fieldKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// FieldDef is one custom field of an entity kind.
type FieldDef struct {
	Kind    string   `json:"kind" enum:"character,place,item,org,concept"`
	Key     string   `json:"key" desc:"Lower-case letters, digits and underscores; starts with a letter. Entities store values under it."`
	Label   string   `json:"label"`
	Type    string   `json:"type" enum:"text,number,date,choice"`
	Choices []string `json:"choices" desc:"The allowed values of a choice field; empty for the other types."`
}

// starter fields: what a new Compendium offers out of the box.
var starterFields = []FieldDef{
	{Kind: "character", Key: "role", Label: "Role", Type: "text"},
	{Kind: "character", Key: "age", Label: "Age", Type: "text"},
	{Kind: "character", Key: "appearance", Label: "Appearance", Type: "text"},
	{Kind: "character", Key: "voice", Label: "Voice", Type: "text"},
	{Kind: "character", Key: "wants", Label: "Wants", Type: "text"},
	{Kind: "character", Key: "fears", Label: "Fears", Type: "text"},
	{Kind: "character", Key: "secrets", Label: "Secrets", Type: "text"},
	{Kind: "character", Key: "arc", Label: "Arc", Type: "text"},
	{Kind: "place", Key: "region", Label: "Region", Type: "text"},
	{Kind: "place", Key: "atmosphere", Label: "Atmosphere", Type: "text"},
	{Kind: "item", Key: "owner", Label: "Owner", Type: "text"},
	{Kind: "item", Key: "origin", Label: "Origin", Type: "text"},
	{Kind: "org", Key: "purpose", Label: "Purpose", Type: "text"},
	{Kind: "org", Key: "leader", Label: "Leader", Type: "text"},
}

// SeedStarterFields writes the starter field definitions of a new Compendium.
func SeedStarterFields(tx *sql.Tx, compendiumID, now string) error {
	for i, f := range starterFields {
		if _, err := tx.Exec(`INSERT INTO entity_field_defs(id, compendium_id, kind, key, label, type, choices_json, position, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, db.NewID("fld"), compendiumID, f.Kind, f.Key, f.Label, f.Type, "[]", float64(i+1)*65536, now); err != nil {
			return err
		}
	}
	return nil
}

// defs is a Compendium's definitions of one kind, by key.
type defs map[string]FieldDef

// Querier is what *sql.DB and *sql.Tx share.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type queryer = Querier

func loadDefs(ctx context.Context, q queryer, compendiumID, kind string) (defs, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, key, label, type, choices_json FROM entity_field_defs WHERE compendium_id = ? AND kind = ?`, compendiumID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := defs{}
	for rows.Next() {
		var f FieldDef
		var ch string
		if err := rows.Scan(&f.Kind, &f.Key, &f.Label, &f.Type, &ch); err != nil {
			return nil, err
		}
		f.Choices = []string{}
		_ = json.Unmarshal([]byte(ch), &f.Choices)
		out[f.Key] = f
	}
	return out, rows.Err()
}

// normalize turns a submitted value into the canonical string stored for the
// field, or says why it cannot be. An empty value is valid and means "not set".
func (f FieldDef) normalize(raw any) (string, error) {
	var s string
	switch v := raw.(type) {
	case nil:
		return "", nil
	case string:
		s = v
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		s = v.String()
	case bool:
		s = strconv.FormatBool(v)
	case int:
		s = strconv.Itoa(v)
	case int64:
		s = strconv.FormatInt(v, 10)
	default:
		return "", ops.Invalid("fields."+f.Key, "A field value is text, a number or a date.")
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return "", nil
	}
	if strings.ContainsRune(s, 0) {
		return "", ops.Invalid("fields."+f.Key, "This value cannot contain control characters.")
	}
	switch f.Type {
	case "number":
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return "", ops.Invalid("fields."+f.Key, fmt.Sprintf("%s is a number.", f.Label))
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case "date":
		if !validDate(s) {
			return "", ops.Invalid("fields."+f.Key, fmt.Sprintf("%s is a date: YYYY, YYYY-MM or YYYY-MM-DD.", f.Label))
		}
		return s, nil
	case "choice":
		for _, c := range f.Choices {
			if text.FoldTitle(c) == text.FoldTitle(s) {
				return c, nil
			}
		}
		return "", ops.Invalid("fields."+f.Key, fmt.Sprintf("%s is one of: %s.", f.Label, strings.Join(f.Choices, ", ")))
	}
	if utf8.RuneCountInString(s) > maxFieldText {
		return "", ops.Invalid("fields."+f.Key, fmt.Sprintf("%s is limited to %d characters.", f.Label, maxFieldText))
	}
	return s, nil
}

func validDate(s string) bool {
	for _, layout := range []string{"2006", "2006-01", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil && t.Format(layout) == s {
			return true
		}
	}
	return false
}

// applyFields validates a map of submitted values against the definitions and
// merges it into current: a key with an empty value is cleared. It returns the
// new values. keepEmpty keeps an empty value as an explicit blank (an override
// blanking a field for one book).
func applyFields(d defs, current map[string]string, in map[string]any, keepEmpty bool) (map[string]string, error) {
	out := make(map[string]string, len(current)+len(in))
	for k, v := range current {
		out[k] = v
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		def, ok := d[k]
		if !ok {
			return nil, ops.Invalid("fields."+k, fmt.Sprintf("No field %q is defined for this kind of entity in this Compendium. Define it with entity.field.set first.", k))
		}
		v, err := def.normalize(in[k])
		if err != nil {
			return nil, err
		}
		if v == "" && !keepEmpty {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	if len(out) > maxFieldValues {
		return nil, ops.Invalid("fields", "Too many field values.")
	}
	return out, nil
}

func marshalFields(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m) // map keys are sorted: the same values are always the same bytes
	return string(b)
}

func parseFields(s string) map[string]string {
	m := map[string]string{}
	if s != "" && s != "{}" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

// ---- ops ---------------------------------------------------------------------------------------------

// FieldListIn lists a Compendium's field definitions.
type FieldListIn struct {
	ID   string `path:"id" json:"-" desc:"Compendium id."`
	Kind string `query:"kind" desc:"Only one kind of entity." enum:"character,place,item,org,concept"`
}

// CompendiumRef lets the registry authorize by membership.
func (in FieldListIn) CompendiumRef() string { return in.ID }

// FieldListOut lists the definitions, by kind then position.
type FieldListOut struct {
	Items []FieldDef `json:"items"`
}

func fieldList(c *ops.Ctx, in FieldListIn) (FieldListOut, error) {
	q := `SELECT kind, key, label, type, choices_json FROM entity_field_defs WHERE compendium_id = ?`
	args := []any{in.ID}
	if in.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, in.Kind)
	}
	rows, err := c.DB().Read().QueryContext(c, q+` ORDER BY kind, position, key`, args...)
	if err != nil {
		return FieldListOut{}, err
	}
	defer rows.Close()
	out := FieldListOut{Items: []FieldDef{}}
	for rows.Next() {
		var f FieldDef
		var ch string
		if err := rows.Scan(&f.Kind, &f.Key, &f.Label, &f.Type, &ch); err != nil {
			return FieldListOut{}, err
		}
		f.Choices = []string{}
		_ = json.Unmarshal([]byte(ch), &f.Choices)
		out.Items = append(out.Items, f)
	}
	return out, rows.Err()
}

// FieldSetIn creates or updates a field definition.
type FieldSetIn struct {
	ID      string   `path:"id" json:"-" desc:"Compendium id."`
	Kind    string   `json:"kind" enum:"character,place,item,org,concept"`
	Key     string   `json:"key" desc:"Lower-case letters, digits and underscores; starts with a letter; up to 32 characters."`
	Label   string   `json:"label,omitempty" desc:"What the field is called on a sheet. Defaults to the key."`
	Type    string   `json:"type" enum:"text,number,date,choice" desc:"Fixed once values exist: remove the field to change it."`
	Choices []string `json:"choices,omitempty" desc:"Required for choice fields: the allowed values, up to 50. Removing a value that entities use is refused."`
}

// CompendiumRef lets the registry authorize by membership.
func (in FieldSetIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *FieldSetIn) Validate() error {
	if !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(Kinds, ", ")+".")
	}
	if len(in.Key) > maxFieldKey || !fieldKeyRe.MatchString(in.Key) {
		return ops.Invalid("key", "A key is lower-case letters, digits and underscores, starts with a letter, and is at most 32 characters.")
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" {
		in.Label = in.Key
	}
	if utf8.RuneCountInString(in.Label) > maxFieldLabel || strings.IndexFunc(in.Label, unicode.IsControl) >= 0 {
		return ops.Invalid("label", "The label is limited to 60 characters on one line.")
	}
	if !contains(FieldTypes, in.Type) {
		return ops.Invalid("type", "Type must be one of: "+strings.Join(FieldTypes, ", ")+".")
	}
	if in.Type == "choice" {
		if len(in.Choices) == 0 || len(in.Choices) > maxChoices {
			return ops.Invalid("choices", "A choice field needs between 1 and 50 choices.")
		}
		seen := map[string]bool{}
		for i, ch := range in.Choices {
			ch = strings.TrimSpace(ch)
			k := text.FoldTitle(ch)
			if ch == "" || utf8.RuneCountInString(ch) > maxChoiceLen || strings.IndexFunc(ch, unicode.IsControl) >= 0 || seen[k] {
				return ops.Invalid("choices", "Choices are distinct, non-empty, one-line values of at most 80 characters.")
			}
			seen[k] = true
			in.Choices[i] = ch
		}
	} else if len(in.Choices) > 0 {
		return ops.Invalid("choices", "Only a choice field has choices.")
	}
	return nil
}

// FieldItem wraps a definition.
type FieldItem struct {
	Item    FieldDef `json:"item"`
	Created bool     `json:"created"`
}

func fieldSet(c *ops.Ctx, in FieldSetIn) (FieldItem, error) {
	var out FieldItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		ch := in.Choices
		if ch == nil {
			ch = []string{}
		}
		chJSON, _ := json.Marshal(ch)
		var oldType, oldChoices string
		err := tx.QueryRow(`SELECT type, choices_json FROM entity_field_defs WHERE compendium_id = ? AND kind = ? AND key = ?`, in.ID, in.Kind, in.Key).Scan(&oldType, &oldChoices)
		now := db.Now()
		switch {
		case err == sql.ErrNoRows:
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM entity_field_defs WHERE compendium_id = ? AND kind = ?`, in.ID, in.Kind).Scan(&n); err != nil {
				return err
			}
			if n >= maxFieldsDefs {
				return ops.Invalid("key", "That kind already has 60 fields.")
			}
			var pos float64
			if err := tx.QueryRow(`SELECT COALESCE(max(position), 0) + 65536 FROM entity_field_defs WHERE compendium_id = ? AND kind = ?`, in.ID, in.Kind).Scan(&pos); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO entity_field_defs(id, compendium_id, kind, key, label, type, choices_json, position, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				db.NewID("fld"), in.ID, in.Kind, in.Key, in.Label, in.Type, string(chJSON), pos, now); err != nil {
				return err
			}
			out.Created = true
		case err != nil:
			return err
		default:
			if oldType != in.Type {
				return ops.Conflict("A field's type cannot change once defined. Remove it (dropping its values) and define it again.")
			}
			if in.Type == "choice" {
				var old []string
				_ = json.Unmarshal([]byte(oldChoices), &old)
				removed := map[string]bool{}
				for _, o := range old {
					removed[o] = true
				}
				for _, n := range in.Choices {
					delete(removed, n)
				}
				for r := range removed {
					used, err := countValue(tx, in.ID, in.Kind, in.Key, r)
					if err != nil {
						return err
					}
					var inOverrides int
					if err := tx.QueryRow(`SELECT count(*) FROM project_entity_overrides o JOIN entities e ON e.id = o.entity_id
						WHERE e.compendium_id = ? AND e.kind = ? AND json_extract(o.fields_json, '$.' || ?) = ?`, in.ID, in.Kind, in.Key, r).Scan(&inOverrides); err != nil {
						return err
					}
					if used+inOverrides > 0 {
						return ops.Conflict(fmt.Sprintf("%d entities and %d project overrides use the choice %q; change them first.", used, inOverrides, r))
					}
				}
			}
			if _, err := tx.Exec(`UPDATE entity_field_defs SET label = ?, choices_json = ? WHERE compendium_id = ? AND kind = ? AND key = ?`,
				in.Label, string(chJSON), in.ID, in.Kind, in.Key); err != nil {
				return err
			}
		}
		c.Emit(ops.Event{Type: "entity.field.set", CompendiumID: in.ID, Payload: map[string]any{"kind": in.Kind, "key": in.Key}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out.Item = FieldDef{Kind: in.Kind, Key: in.Key, Label: in.Label, Type: in.Type, Choices: ch}
		return nil
	})
	return out, err
}

// countValue counts live and trashed entities of a kind holding a value.
func countValue(tx *sql.Tx, compendiumID, kind, key, value string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM entities WHERE compendium_id = ? AND kind = ? AND json_extract(fields_json, '$.' || ?) = ?`,
		compendiumID, kind, key, value).Scan(&n)
	return n, err
}

// FieldRemoveIn removes a field definition.
type FieldRemoveIn struct {
	ID         string `path:"id" json:"-" desc:"Compendium id."`
	Kind       string `query:"kind" desc:"The entity kind." enum:"character,place,item,org,concept"`
	Key        string `query:"key" desc:"The field's key."`
	DropValues bool   `query:"drop_values" desc:"Also delete the values entities hold under this key. Without it, a field that is in use is refused."`
}

// CompendiumRef lets the registry authorize by membership.
func (in FieldRemoveIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *FieldRemoveIn) Validate() error {
	if !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(Kinds, ", ")+".")
	}
	if in.Key == "" {
		return ops.Invalid("key", "Say which field.")
	}
	return nil
}

// FieldRemoveOut reports a removal.
type FieldRemoveOut struct {
	Removed        bool `json:"removed" desc:"False when there was no such field."`
	ValuesDropped  int  `json:"values_dropped" desc:"Entities that held a value, which was deleted."`
	OverridesCount int  `json:"overrides_dropped" desc:"Project overrides that held a value, which was deleted."`
}

func fieldRemove(c *ops.Ctx, in FieldRemoveIn) (FieldRemoveOut, error) {
	var out FieldRemoveOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM entity_field_defs WHERE compendium_id = ? AND kind = ? AND key = ?`, in.ID, in.Kind, in.Key).Scan(&one); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		path := `$.` + in.Key
		var used, overridden int
		if err := tx.QueryRow(`SELECT count(*) FROM entities WHERE compendium_id = ? AND kind = ? AND json_type(fields_json, ?) IS NOT NULL`, in.ID, in.Kind, path).Scan(&used); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM project_entity_overrides o JOIN entities e ON e.id = o.entity_id
			WHERE e.compendium_id = ? AND e.kind = ? AND json_type(o.fields_json, ?) IS NOT NULL`, in.ID, in.Kind, path).Scan(&overridden); err != nil {
			return err
		}
		if (used > 0 || overridden > 0) && !in.DropValues {
			return ops.ConflictWith(fmt.Sprintf("%d entities and %d project overrides hold a value for this field. Pass drop_values to delete them with it.", used, overridden),
				map[string]any{"entities": used, "overrides": overridden})
		}
		now := db.Now()
		if used > 0 {
			rows, err := tx.QueryContext(c, `SELECT id FROM entities WHERE compendium_id = ? AND kind = ? AND json_type(fields_json, ?) IS NOT NULL`, in.ID, in.Kind, path)
			if err != nil {
				return err
			}
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := tx.Exec(`UPDATE entities SET fields_json = json_remove(fields_json, ?), updated_at = ?, revision = revision + 1 WHERE id = ?`, path, now, id); err != nil {
					return err
				}
			}
		}
		if overridden > 0 {
			if _, err := tx.Exec(`UPDATE project_entity_overrides SET fields_json = json_remove(fields_json, ?), updated_at = ?
				WHERE json_type(fields_json, ?) IS NOT NULL AND entity_id IN (SELECT id FROM entities WHERE compendium_id = ? AND kind = ?)`, path, now, path, in.ID, in.Kind); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM entity_field_defs WHERE compendium_id = ? AND kind = ? AND key = ?`, in.ID, in.Kind, in.Key); err != nil {
			return err
		}
		out = FieldRemoveOut{Removed: true, ValuesDropped: used, OverridesCount: overridden}
		c.Emit(ops.Event{Type: "entity.field.removed", CompendiumID: in.ID, Payload: map[string]any{"kind": in.Kind, "key": in.Key, "values": used, "overrides": overridden}})
		if overridden > 0 {
			rows, err := tx.QueryContext(c, `SELECT DISTINCT project_id FROM project_entity_overrides o JOIN entities e ON e.id = o.entity_id WHERE e.compendium_id = ?`, in.ID)
			if err == nil {
				for rows.Next() {
					var pid string
					if rows.Scan(&pid) == nil {
						c.Emit(ops.Event{Type: "entity.field.removed", ProjectID: pid, Payload: map[string]any{"compendium_id": in.ID}})
					}
				}
				rows.Close()
			}
		}
		c.Audit(ops.AuditEntry{Action: "entity.field.remove", TargetType: "compendium", TargetID: in.ID,
			Detail: map[string]any{"kind": in.Kind, "key": in.Key, "entities": used, "overrides": overridden}})
		return c.FlushTo(tx)
	})
	return out, err
}

func registerFields(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.field.list", Method: "GET", Path: "/api/v1/compendiums/{id}/entity-fields",
		Desc: "List a Compendium's custom entity fields, by kind: key, label, type (text, number, date or choice) and the choices of a choice field. A new Compendium starts with a few starter fields (a character's role, age, appearance, voice, wants, fears, secrets and arc).",
		Auth: ops.RequireCompendium(compaccess.Reader),
	}, fieldList)
	ops.Register(r, ops.Op{
		Name: "entity.field.set", Method: "PUT", Path: "/api/v1/compendiums/{id}/entity-fields",
		Desc: "Define a custom field for one kind of entity, or change an existing one's label and choices (its type is fixed once defined). Entities can only hold values for fields defined here. Editor.",
		Auth: ops.RequireCompendium(compaccess.Editor),
	}, fieldSet)
	ops.Register(r, ops.Op{
		Name: "entity.field.remove", Permanent: true, Method: "DELETE", Path: "/api/v1/compendiums/{id}/entity-fields",
		Desc: "Remove a custom field definition. Refused while entities or project overrides hold a value for it, unless drop_values is set, which deletes those values too. Editor.",
		Auth: ops.RequireCompendium(compaccess.Editor),
	}, fieldRemove)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
