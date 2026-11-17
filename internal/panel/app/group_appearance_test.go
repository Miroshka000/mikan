package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// A group's appearance over HTTP (#290): its clients alone get its template, texts, headers,
// device rules, address and page, over the panel's settings; a client of no group gets the
// panel's as before. It is the admin session's to change, as the settings it stands for are,
// and it is held to their rules.
func TestGroupAppearanceOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	q := k.st.Q
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	ins, _ := q.ListInbounds(ctx)
	local := ins[0]
	tariffs, _ := q.ListTariffs(ctx)
	clock := func() time.Time { return k.now }
	users := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock)
	anna, err := users.Create(ctx, domain.CreateInput{Name: "anna", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	boris, err := users.Create(ctx, domain.CreateInput{Name: "boris", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	ok := func(resp *http.Response, raw []byte, want int, what string) {
		t.Helper()
		if resp.StatusCode != want {
			t.Fatalf("%s: %d %s", what, resp.StatusCode, raw)
		}
	}
	resp, raw := k.do(http.MethodPatch, k.api+"/settings", map[string]any{"sub_announce": "Global news", "support_url": "https://t.me/all"}, k.csrf)
	ok(resp, raw, http.StatusOK, "settings")

	// A Clash template of the group's, made from the starter as the editor makes one.
	resp, raw = k.do(http.MethodPost, k.api+"/sub-templates/preview", map[string]any{"core": "mihomo", "body": "", "starter": true}, k.csrf)
	ok(resp, raw, http.StatusOK, "starter")
	var starter struct {
		Profile string `json:"profile"`
	}
	_ = json.Unmarshal(raw, &starter)
	body := strings.Replace(starter.Profile, "rules:\n", "rules:\n  - DOMAIN-SUFFIX,vip.example,DIRECT\n", 1)
	resp, raw = k.do(http.MethodPost, k.api+"/sub-templates", map[string]any{"core": "mihomo", "name": "VIP", "body": body}, k.csrf)
	ok(resp, raw, http.StatusCreated, "template")
	var tpl struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(raw, &tpl)

	resp, raw = k.do(http.MethodPost, k.api+"/groups", map[string]any{"name": "VIP"}, k.csrf)
	ok(resp, raw, http.StatusCreated, "group")
	var g struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(raw, &g)
	resp, raw = k.do(http.MethodPatch, k.api+"/users/"+id(anna.ID), map[string]any{"group_id": g.ID}, k.csrf)
	ok(resp, raw, http.StatusOK, "into the group")

	look := map[string]any{
		"templates":    map[string]any{"mihomo": tpl.ID},
		"title":        "VIP {name}",
		"announce":     "",
		"support_url":  "https://t.me/vip",
		"update_hours": 6,
		"headers":      []map[string]string{{"name": "X-Group", "value": "vip"}},
		"require_hwid": true,
		"unbind":       map[string]any{"limit": 3, "days": 7, "return_hours": 0},
		"addresses":    []map[string]any{{"inbound_id": local.ID, "address": "edge.example.com", "port": 8443, "name": "Edge"}},
	}
	groupPath := k.api + "/groups/" + id(g.ID)
	// A key may not change it: the settings it stands for are the session's.
	if resp, raw := k.asKey(k.full, http.MethodPatch, "/groups/"+id(g.ID), map[string]any{"appearance": look}); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "session_only") {
		t.Fatalf("a key set the appearance: %d %s", resp.StatusCode, raw)
	}
	// It is held to the rules of what it stands for.
	for name, c := range map[string]struct {
		patch map[string]any
		code  string
	}{
		"a template of another core": {map[string]any{"templates": map[string]any{"singbox": tpl.ID}}, "group_template"},
		"a cache header":             {map[string]any{"templates": map[string]any{}, "headers": []map[string]string{{"name": "Cache-Control", "value": "max-age=60"}}}, "resp_header_cache"},
		"two lines of a title":       {map[string]any{"templates": map[string]any{}, "title": "a\nb"}, "one_line"},
		"an address of no protocol":  {map[string]any{"templates": map[string]any{}, "addresses": []map[string]any{{"inbound_id": 999999, "address": "x.example.com"}}}, "inbound_not_found"},
		"an address that is empty":   {map[string]any{"templates": map[string]any{}, "addresses": []map[string]any{{"inbound_id": local.ID}}}, "address_empty"},
	} {
		if resp, raw := k.do(http.MethodPatch, groupPath, map[string]any{"appearance": c.patch}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), c.code) {
			t.Errorf("%s: %d %s, want %s", name, resp.StatusCode, raw, c.code)
		}
	}
	resp, raw = k.do(http.MethodPatch, groupPath, map[string]any{"appearance": look}, k.csrf)
	ok(resp, raw, http.StatusOK, "appearance")
	var view struct {
		Appearance struct {
			Templates struct {
				Mihomo *int64 `json:"mihomo"`
			} `json:"templates"`
			Title     *string `json:"title"`
			Announce  *string `json:"announce"`
			UnbindOwn bool    `json:"unbind_own"`
			Addresses []struct {
				Address string `json:"address"`
			} `json:"addresses"`
			Page bool `json:"page"`
		} `json:"appearance"`
	}
	if json.Unmarshal(raw, &view) != nil || view.Appearance.Templates.Mihomo == nil || *view.Appearance.Templates.Mihomo != tpl.ID || view.Appearance.Title == nil ||
		view.Appearance.Announce == nil || *view.Appearance.Announce != "" || !view.Appearance.UnbindOwn || len(view.Appearance.Addresses) != 1 || view.Appearance.Page {
		t.Fatalf("the group's view: %s", raw)
	}

	fetch := func(token string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		resp, raw := k.do(http.MethodGet, "/"+subPath+"/"+token, nil, hdr)
		ok(resp, raw, http.StatusOK, "fetch")
		return resp, raw
	}
	app := map[string]string{"User-Agent": "mihomo/1.19.31", "X-Hwid": "phone-0123456789"}
	title := func(resp *http.Response) string {
		v, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(resp.Header.Get("Profile-Title"), "base64:"))
		return string(v)
	}
	type proxy struct {
		Name   string `json:"name"`
		Server string `json:"server"`
		Port   int    `json:"port"`
	}
	proxies := func(raw []byte) []proxy {
		var prof struct {
			Proxies []proxy `json:"proxies"`
		}
		_ = unmarshalProfile(raw, &prof)
		return prof.Proxies
	}
	resp, raw = fetch(anna.SubToken, app)
	hd := resp.Header
	if title(resp) != "VIP anna" || hd.Get("Announce") != "" || hd.Get("Support-Url") != "https://t.me/vip" || hd.Get("Profile-Update-Interval") != "6" || hd.Get("X-Group") != "vip" {
		t.Errorf("the group's headers: %q %v", title(resp), hd)
	}
	if !strings.Contains(string(raw), "vip.example") {
		t.Errorf("the group's template is not the profile's:\n%.600s", raw)
	}
	if !slices.ContainsFunc(proxies(raw), func(p proxy) bool { return p.Name == "Edge" && p.Server == "edge.example.com" && p.Port == 8443 }) {
		t.Errorf("the group's address: %+v", proxies(raw))
	}
	resp, raw = fetch(boris.SubToken, app)
	hd = resp.Header
	if title(resp) == "VIP boris" || hd.Get("Announce") == "" || hd.Get("Support-Url") != "https://t.me/all" || hd.Get("Profile-Update-Interval") != "1" || hd.Get("X-Group") != "" ||
		strings.Contains(string(raw), "vip.example") || slices.ContainsFunc(proxies(raw), func(p proxy) bool { return p.Server == "edge.example.com" }) {
		t.Errorf("a client of no group got the group's: %q %v", title(resp), hd)
	}
	// An app without a device id: the group requires one, the panel does not.
	resp, _ = fetch(anna.SubToken, map[string]string{"User-Agent": "mihomo/1.19.31"})
	if resp.Header.Get("X-Hwid-Not-Supported") != "true" {
		t.Errorf("the group requires a device id: %v", resp.Header)
	}
	resp, _ = fetch(boris.SubToken, map[string]string{"User-Agent": "mihomo/1.19.31"})
	if resp.Header.Get("X-Hwid-Not-Supported") != "" {
		t.Errorf("the panel requires no device id: %v", resp.Header)
	}
	// The page tells the group's unbinding rules.
	info := func(token string) int {
		resp, raw := k.do(http.MethodGet, "/"+subPath+"/"+token+"/info", nil, nil)
		ok(resp, raw, http.StatusOK, "info")
		var v struct {
			UnbindLimit int `json:"unbind_limit"`
		}
		_ = json.Unmarshal(raw, &v)
		return v.UnbindLimit
	}
	if info(anna.SubToken) != 3 || info(boris.SubToken) == 3 {
		t.Errorf("unbinding: the group's %d, the panel's %d", info(anna.SubToken), info(boris.SubToken))
	}

	// The group's own page: a key may not set it; the session does, from the panel's.
	resp, raw = k.do(http.MethodGet, groupPath+"/page", nil, nil)
	ok(resp, raw, http.StatusOK, "group page")
	var page struct {
		Own    bool           `json:"own"`
		Config map[string]any `json:"config"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Own {
		t.Fatalf("a group without a page: %s", raw)
	}
	page.Config["brand"].(map[string]any)["subtitle"] = "VIP page"
	if resp, raw := k.asKey(k.full, http.MethodPut, "/groups/"+id(g.ID)+"/page", map[string]any{"config": page.Config}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a key set the group's page: %d %s", resp.StatusCode, raw)
	}
	resp, raw = k.do(http.MethodPut, groupPath+"/page", map[string]any{"config": page.Config}, k.csrf)
	ok(resp, raw, http.StatusOK, "put group page")
	subtitle := func(token string) string {
		_, body := k.do(http.MethodGet, "/"+subPath+"/"+token, nil, browser)
		return pageData(t, body).Config.Brand.Subtitle
	}
	if subtitle(anna.SubToken) != "VIP page" || subtitle(boris.SubToken) == "VIP page" {
		t.Errorf("the pages: the group's %q, the panel's %q", subtitle(anna.SubToken), subtitle(boris.SubToken))
	}
	resp, raw = k.do(http.MethodDelete, groupPath+"/page", nil, k.csrf)
	ok(resp, raw, http.StatusNoContent, "delete group page")
	if subtitle(anna.SubToken) == "VIP page" {
		t.Errorf("the group's page outlived its deletion")
	}

	// Without the group the client is the panel's again, and the group goes with its appearance.
	resp, raw = k.do(http.MethodDelete, groupPath, nil, k.csrf)
	ok(resp, raw, http.StatusNoContent, "delete group")
	resp, _ = fetch(anna.SubToken, app)
	if title(resp) == "VIP anna" || resp.Header.Get("X-Group") != "" {
		t.Errorf("the deleted group's appearance stayed: %v", resp.Header)
	}
}
