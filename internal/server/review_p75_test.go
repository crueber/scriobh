package server

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Review R6: every page GET of a valid card is an UPDATE on the single writer, with no throttle or batching.
func TestRvp75ACardPageViewWritesOncePerRequest(t *testing.T) {
	s, _ := newShareServer(t, nil)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	rec := s.do(owner, "POST", "/api/v1/me/studio/cards", `{}`)
	if rec.Code != 201 {
		t.Fatalf("card: %d %s", rec.Code, rec.Body)
	}
	var made struct{ Item struct{ ID, Token string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	const n = 50
	for i := 0; i < n; i++ {
		if r := cardGet(s, "/card/"+made.Item.Token, "Accept", "text/html"); r.Code != 200 {
			t.Fatalf("view %d: %d", i, r.Code)
		}
	}
	var views int
	_ = s.DB.Read().QueryRow(`SELECT views FROM studio_cards WHERE id = ?`, made.Item.ID).Scan(&views)
	if views >= n {
		t.Errorf("%d page GETs made %d database writes: one UPDATE on the single writer per public request", n, views)
	}
}

// Review: a hostile project title on a card made with show_projects. The SVG must stay well-formed XML and hold no live markup.
func TestRvp75CardHostileProjectTitle(t *testing.T) {
	s, _ := newShareServer(t, nil)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	for _, title := range []string{
		"</text><script>alert(1)</script>&amp;\"'",
		"bidi ‮ evil ⁦ x​",
		"ctl \x01\x0b\x1f end",
		strings.Repeat("W", 300),
	} {
		out, err := s.Reg.Call(t.Context(), p, "project.create", projects.CreateIn{Title: title})
		if err != nil {
			t.Logf("title %q refused: %v", title[:min(20, len(title))], err)
			continue
		}
		pid := out.(projects.Item).Item.ID
		if rec := s.do(owner, "POST", "/api/v1/me/studio/goals", `{"period":"total","measure":"words","target":5000,"project_id":"`+pid+`"}`); rec.Code != 201 {
			t.Fatalf("goal: %d %s", rec.Code, rec.Body)
		}
	}
	rec := s.do(owner, "POST", "/api/v1/me/studio/cards", `{"show_projects":true}`)
	var made struct{ Item struct{ ID, Token string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	svg := cardGet(s, "/card/"+made.Item.Token+"/card.svg")
	body := svg.Body.String()
	d := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Errorf("the card's SVG is not well-formed XML: %v", err)
			break
		}
	}
	if strings.Contains(body, "<script") {
		t.Errorf("live <script in the SVG")
	}
	page := cardGet(s, "/card/"+made.Item.Token, "Accept", "text/html")
	if strings.Contains(page.Body.String(), "<script>alert") {
		t.Errorf("live script in the page")
	}
}
