package app

import (
	"net/http"
	"strings"
	"testing"
)

// A panel with nothing in a list answers with an empty array: the pages read its length,
// and null broke the promo codes tab on a panel without codes.
func TestEmptyListsAreArrays(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	api := "/" + adminPath + "/api/v1"
	for _, path := range []string{"/promocodes", "/promocodes/redemptions"} {
		resp, body := h.do(http.MethodGet, api+path, nil, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}
