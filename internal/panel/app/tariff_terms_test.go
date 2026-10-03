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

type termView struct {
	Days       int64  `json:"days"`
	PriceStars *int64 `json:"price_stars"`
	PriceRub   *int64 `json:"price_rub"`
}

type tariffWithTerms struct {
	ID           int64      `json:"id"`
	DurationDays int64      `json:"duration_days"`
	PriceStars   *int64     `json:"price_stars"`
	PriceRub     *int64     `json:"price_rub"`
	Terms        []termView `json:"terms"`
}

// The terms of a tariff through the API: the whole list in a PUT, the first one mirrored
// in the tariff's own fields, a client that does not know terms keeping them, and the
// lists the API refuses leaving the tariff as it was.
func TestTariffTermsAPI(t *testing.T) {
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
	put := func(body map[string]any) (int, tariffWithTerms, string) {
		t.Helper()
		body["name"], body["reset_strategy"] = "Std", "period"
		resp, raw := h.do(http.MethodPut, path, body, csrf)
		var v tariffWithTerms
		_ = json.Unmarshal(raw, &v)
		return resp.StatusCode, v, string(raw)
	}
	terms := []map[string]any{{"days": 30, "price_rub": 26900}, {"days": 7, "price_rub": 9900, "price_stars": 50}, {"days": 90, "price_rub": 64900}}
	code, v, raw := put(map[string]any{"duration_days": 1, "terms": terms, "on_sale": true})
	if code != http.StatusOK || len(v.Terms) != 3 || v.Terms[1].Days != 7 || *v.Terms[1].PriceStars != 50 {
		t.Fatalf("three terms: %d %s", code, raw)
	}
	// The tariff's own fields are the first term, whatever duration_days said.
	if v.DurationDays != 30 || v.PriceRub == nil || *v.PriceRub != 26900 || v.PriceStars != nil {
		t.Fatalf("first term not mirrored: %+v", v)
	}

	// A client that does not know terms changes the first one and keeps the rest.
	code, v, raw = put(map[string]any{"duration_days": 31, "price_rub": 27900, "on_sale": true})
	if code != http.StatusOK || len(v.Terms) != 3 || v.Terms[0].Days != 31 || *v.Terms[0].PriceRub != 27900 || v.Terms[2].Days != 90 {
		t.Fatalf("an old client's PUT: %d %s", code, raw)
	}

	// Refused: a term twice, a term with no price among several, an empty list.
	for name, c := range map[string]struct {
		terms []map[string]any
		want  string
	}{
		"repeat":   {[]map[string]any{{"days": 30, "price_rub": 100}, {"days": 30, "price_rub": 200}}, `"location":"body.terms[1].days"`},
		"no price": {[]map[string]any{{"days": 30, "price_rub": 100}, {"days": 7}}, "term_no_price"},
		"empty":    {[]map[string]any{}, "terms_empty"},
	} {
		code, _, raw := put(map[string]any{"duration_days": 30, "terms": c.terms})
		if code != http.StatusUnprocessableEntity || !strings.Contains(raw, c.want) {
			t.Fatalf("%s: %d %s", name, code, raw)
		}
	}
	rows, _ := h.st.Q.ListTariffTerms(ctx, tariffs[1].ID)
	if len(rows) != 3 || rows[0].Days != 31 {
		t.Fatalf("a refused list changed the terms: %+v", rows)
	}

	// Back to one term: no rows, the tariff alone, as before terms.
	code, v, raw = put(map[string]any{"duration_days": 30, "terms": []map[string]any{{"days": 14, "price_stars": 75}}})
	if code != http.StatusOK || len(v.Terms) != 1 || v.Terms[0].Days != 14 || v.DurationDays != 14 || v.PriceStars == nil || *v.PriceStars != 75 {
		t.Fatalf("one term: %d %s", code, raw)
	}
	if rows, _ := h.st.Q.ListTariffTerms(ctx, tariffs[1].ID); len(rows) != 0 {
		t.Fatalf("rows left for one term: %+v", rows)
	}

	// A new tariff with terms, and the list shows them.
	resp, body := h.do(http.MethodPost, api+"/tariffs", map[string]any{"name": "Multi", "duration_days": 0, "reset_strategy": "none",
		"terms": []map[string]any{{"days": 30, "price_stars": 100}, {"days": 365, "price_stars": 1000}}}, csrf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodGet, api+"/tariffs", nil, nil)
	var list []tariffWithTerms
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	found := false
	for _, x := range list {
		if len(x.Terms) == 0 {
			t.Fatalf("a tariff with no terms in the list: %+v", x)
		}
		if len(x.Terms) == 2 && x.Terms[1].Days == 365 && x.DurationDays == 30 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new tariff's terms: %s", body)
	}
}
