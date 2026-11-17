package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// There is no limit on how many people there are: the list pages, and a person far down it can be looked at, switched
// off and on and given a role like anyone (they are found by id, not out of a page of the list).
func TestTheConsoleReachesEveryoneWhateverTheirNumber(t *testing.T) {
	s, ada, _, _ := adminWorld(t)
	var last string
	for i := 0; i < 210; i++ {
		last = testutil.User(t, s.DB, ops.RoleWriter, fmt.Sprintf("zz%03d@test.local", i))
	}
	first := s.do(ada, "GET", "/admin/users", "").Body.String()
	if !strings.Contains(first, `rel="next"`) || !strings.Contains(first, "Page 1 of 5") {
		t.Error("the first page has no way on")
	}
	if strings.Contains(first, `id="u-`+last+`"`) {
		t.Error("the last person is on the first page")
	}
	page5 := s.do(ada, "GET", "/admin/users?page=5", "").Body.String()
	if !strings.Contains(page5, `id="u-`+last+`"`) || !strings.Contains(page5, `rel="prev"`) {
		t.Error("the last page does not hold the last person")
	}
	if rec := s.do(ada, "GET", "/admin/users/"+last+"/disable", ""); rec.Code != 200 {
		t.Errorf("the confirmation for the 214th person: %d", rec.Code)
	}
	if rec := s.postForm(t, ada, "/ui/admin/users/disable", url.Values{"id": {last}, "confirm_name": {"zz209@test.local"}}); rec.Code != 303 {
		t.Errorf("switching off the 214th person: %d", rec.Code)
	}
	if rec := s.postForm(t, ada, "/ui/admin/users/enable", url.Values{"id": {last}}); rec.Code != 303 {
		t.Errorf("switching them on: %d", rec.Code)
	}
	if rec := s.postForm(t, ada, "/ui/admin/users/role", url.Values{"id": {last}, "role": {"reader"}}); rec.Code != 303 {
		t.Errorf("a role for them: %d", rec.Code)
	}
	if rec := s.do(ada, "GET", "/api/v1/admin/users/"+last, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "zz209@test.local") {
		t.Errorf("admin.user.get: %d", rec.Code)
	}
}
