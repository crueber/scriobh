package entities_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

func TestAnAliasIsAtMostTwelveWords(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "Words")
	e := x.ent(x.owner, cid, "character", "Ada")
	long := strings.TrimSpace(strings.Repeat("word ", 13))
	x.expect(x.owner, "entity.alias.add", entities.AliasAddIn{ID: e.ID, Alias: long}, ops.CodeInvalid)
	x.expect(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Bea", Aliases: []string{long}}, ops.CodeInvalid)
	ok := strings.TrimSpace(strings.Repeat("word ", 12))
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: e.ID, Alias: ok})
}
