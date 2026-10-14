package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
)

// A tariff's description for buyers through the API: stored trimmed with plain line
// breaks, shown in the tariff and in the list, cleared by a PUT without it, and a text that
// is too long or has too many lines refused, leaving the stored one as it was.
func TestTariffDescriptionAPI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	path := api + "/tariffs/" + strconv.FormatInt(tariffs[1].ID, 10)
	type view struct {
		Description string `json:"description"`
	}
	put := func(description *string) (int, view, string) {
		t.Helper()
		body := map[string]any{"name": "Std", "reset_strategy": "period", "duration_days": 30}
		if description != nil {
			body["description"] = *description
		}
		resp, raw := h.do(http.MethodPut, path, body, csrf)
		var v view
		_ = json.Unmarshal(raw, &v)
		return resp.StatusCode, v, string(raw)
	}
	text := func(s string) *string { return &s }

	const stored = "С белыми списками\nбез рекламы"
	code, v, raw := put(text("  С белыми списками\r\nбез рекламы \n"))
	if code != http.StatusOK || v.Description != stored {
		t.Fatalf("a description: %d %s", code, raw)
	}
	if row, _ := h.st.Q.GetTariff(ctx, tariffs[1].ID); row.Description != stored {
		t.Fatalf("stored: %q", row.Description)
	}
	resp, body := h.do(http.MethodGet, api+"/tariffs", nil, nil)
	var list []struct {
		ID          int64  `json:"id"`
		Description string `json:"description"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list) == 0 {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	for _, x := range list {
		want := ""
		if x.ID == tariffs[1].ID {
			want = stored
		}
		if x.Description != want {
			t.Fatalf("tariff %d in the list has %q", x.ID, x.Description)
		}
	}

	// 300 Russian letters are fine (600 bytes), 301 are not; nor are six lines. A refusal
	// names the field and leaves the stored text.
	if code, _, raw := put(text(strings.Repeat("я", 300))); code != http.StatusOK {
		t.Fatalf("300 letters: %d %s", code, raw)
	}
	for name, c := range map[string]struct{ in, want string }{
		"long":  {strings.Repeat("я", 301), "description_too_long"},
		"lines": {"1\n2\n3\n4\n5\n6", "description_too_many_lines"},
	} {
		if code, _, raw := put(text("kept")); code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		code, _, raw := put(text(c.in))
		if code != http.StatusUnprocessableEntity || !strings.Contains(raw, c.want) || !strings.Contains(raw, `"location":"body.description"`) {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
		if row, _ := h.st.Q.GetTariff(ctx, tariffs[1].ID); row.Description != "kept" {
			t.Fatalf("%s: a refused text changed the stored one: %q", name, row.Description)
		}
	}

	// A PUT replaces the tariff: without a description it is cleared.
	if code, v, raw := put(nil); code != http.StatusOK || v.Description != "" {
		t.Fatalf("cleared: %d %s", code, raw)
	}

	// A new tariff takes one too, and is refused one that is too long.
	resp, body = h.do(http.MethodPost, api+"/tariffs", map[string]any{"name": "WL", "duration_days": 30, "reset_strategy": "none", "description": "С белыми списками"}, csrf)
	var created view
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &created) != nil || created.Description != "С белыми списками" {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp, body = h.do(http.MethodPost, api+"/tariffs", map[string]any{"name": "WL2", "duration_days": 30, "reset_strategy": "none", "description": strings.Repeat("x", 301)}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create too long: %d %s", resp.StatusCode, body)
	}
}
