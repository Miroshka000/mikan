package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// Happ over the API: the settings are checked and session only, Happ alone gets its
// headers, and the page's and the card's crypt links hide the address.
func TestHappOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	patch := func(body map[string]any) (int, string) {
		t.Helper()
		resp, raw := k.do(http.MethodPatch, k.api+"/settings", body, k.csrf)
		return resp.StatusCode, string(raw)
	}
	for _, c := range []struct {
		body      map[string]any
		want, loc string
	}{
		{map[string]any{"happ_routing": "https://example.com/routing"}, "happ_routing", "body.happ_routing"},
		{map[string]any{"happ_provider_id": "two words"}, "happ_provider_id", "body.happ_provider_id"},
		{map[string]any{"happ_hide_settings": true}, "happ_needs_provider", "body.happ_hide_settings"},
	} {
		if code, body := patch(c.body); code != http.StatusUnprocessableEntity || !strings.Contains(body, c.want) || !strings.Contains(body, c.loc) {
			t.Errorf("%v: %d %s", c.body, code, body)
		}
	}
	routing := "happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte(`{"Name":"RU"}`))
	if code, body := patch(map[string]any{"happ_routing": routing, "happ_provider_id": "prov-1", "happ_hide_settings": true, "happ_crypt": "local"}); code != http.StatusOK ||
		!strings.Contains(body, `"happ_provider_id":"prov-1"`) || !strings.Contains(body, `"happ_hide_settings":true`) || !strings.Contains(body, `"happ_crypt":"local"`) {
		t.Fatalf("save: %d %s", code, body)
	}
	for field, value := range map[string]any{"happ_routing": "", "happ_provider_id": "x", "happ_hide_settings": false, "happ_crypt": "off"} {
		if resp, body := k.asKey(k.full, http.MethodPatch, "/settings", map[string]any{field: value}); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
			t.Errorf("%s with a full key: %d %s", field, resp.StatusCode, body)
		}
	}

	clock := func() time.Time { return k.now }
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(ua string) http.Header {
		t.Helper()
		resp, _ := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": ua})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription for %s: %d", ua, resp.StatusCode)
		}
		return resp.Header
	}
	if hd := fetch("Happ/3.6.0/Android/1"); hd.Get("routing") != routing || hd.Get("providerid") != "prov-1" || hd.Get("hide-settings") != "1" {
		t.Errorf("Happ headers: %v", hd)
	}
	if hd := fetch("v2RayTun/5.1.0"); hd.Get("routing") != "" || hd.Get("providerid") != "" || hd.Get("hide-settings") != "" {
		t.Errorf("another app got Happ's headers: %v", hd)
	}

	resp, raw := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"/info", nil, nil)
	var info struct {
		HappLink string `json:"happ_link"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &info) != nil || !strings.HasPrefix(info.HappLink, "happ://crypt5/") || strings.Contains(info.HappLink, u.SubToken) {
		t.Fatalf("the page's Happ link: %d %s", resp.StatusCode, raw)
	}

	path := "/users/" + idOf(u.ID) + "/happ-link"
	if resp, body := k.do(http.MethodGet, k.api+path, nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "happ://crypt5/") {
		t.Errorf("the card's link: %d %s", resp.StatusCode, body)
	}
	if resp, body := k.asKey(k.read, http.MethodGet, path, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a read key got the link: %d %s", resp.StatusCode, body)
	}
	if resp, _ := k.asKey(k.full, http.MethodGet, path, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("a full key: %d", resp.StatusCode)
	}

	// Without a provider id the switch would do nothing: it goes off with it.
	if code, body := patch(map[string]any{"happ_provider_id": ""}); code != http.StatusOK || !strings.Contains(body, `"happ_hide_settings":false`) {
		t.Fatalf("provider cleared: %d %s", code, body)
	}
	if code, body := patch(map[string]any{"happ_crypt": "off"}); code != http.StatusOK || !strings.Contains(body, `"happ_crypt":"off"`) {
		t.Fatalf("crypt off: %d %s", code, body)
	}
	if resp, body := k.do(http.MethodGet, k.api+path, nil, nil); resp.StatusCode != http.StatusOK || strings.Contains(string(body), "happ://") {
		t.Errorf("crypt off, no link: %d %s", resp.StatusCode, body)
	}
}

// Happ's routing made by the panel: the routes of Settings → Routing reach Happ as its
// routing profile, and follow them when they change.
func TestHappAutoRouting(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	patch := func(body map[string]any) {
		t.Helper()
		if resp, raw := k.do(http.MethodPatch, k.api+"/settings", body, k.csrf); resp.StatusCode != http.StatusOK {
			t.Fatalf("%v: %d %s", body, resp.StatusCode, raw)
		}
	}
	patch(map[string]any{"happ_routing": "auto", "sub_routing": "ru_direct", "sub_routes": map[string]any{"services": map[string]string{"youtube": "vpn", "ads": "block"}}})
	clock := func() time.Time { return k.now }
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	profile := func() map[string]any {
		t.Helper()
		k.advance(time.Minute) // past the settings cache
		resp, _ := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "Happ/3.6.0/Android/1"})
		link := resp.Header.Get("routing")
		payload, ok := strings.CutPrefix(link, "happ://routing/onadd/")
		raw, err := base64.StdEncoding.DecodeString(payload)
		var p map[string]any
		if resp.StatusCode != http.StatusOK || !ok || err != nil || json.Unmarshal(raw, &p) != nil {
			t.Fatalf("routing header: %d %q", resp.StatusCode, link)
		}
		return p
	}
	p := profile()
	if p["GlobalProxy"] != "true" || !strings.Contains(strings.Join(anyStrings(p["ProxySites"]), " "), "geosite:youtube") ||
		!strings.Contains(strings.Join(anyStrings(p["BlockSites"]), " "), "geosite:category-ads-all") || !strings.Contains(strings.Join(anyStrings(p["DirectSites"]), " "), "geosite:category-ru") {
		t.Fatalf("auto routing: %v", p)
	}
	patch(map[string]any{"sub_routing": "blocked"})
	if p := profile(); p["GlobalProxy"] != "false" || !strings.Contains(strings.Join(anyStrings(p["ProxySites"]), " "), "geosite:ru-blocked") {
		t.Fatalf("blocked mode: %v", p)
	}
	// INCY reads the same profile in its own scheme, and none of Happ's other headers.
	patch(map[string]any{"happ_provider_id": "prov-1"})
	resp, _ := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "INCY/2.0.8"})
	if link := resp.Header.Get("routing"); !strings.HasPrefix(link, "incy://routing/onadd/") || resp.Header.Get("providerid") != "" {
		t.Fatalf("INCY: %q %v", link, resp.Header)
	}
	if resp, _ := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "v2RayTun/5.1.0"}); resp.Header.Get("routing") != "" {
		t.Fatal("v2RayTun takes no routing header")
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// Happ gets the xray JSON profile while sub_xray_json is on, as it is by default, and its
// links once the admin turns it off; the settings say which.
func TestXrayJSONSwitch(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	clock := func() time.Time { return k.now }
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func() (*http.Response, []byte) {
		t.Helper()
		k.advance(time.Minute) // past the settings cache
		return k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "Happ/3.6.0/Android/1", "X-Hwid": "phone-0123456789"})
	}
	var configs []map[string]any
	if resp, raw := fetch(); resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
		json.Unmarshal(raw, &configs) != nil || len(configs) == 0 || configs[0]["remarks"] == nil {
		t.Fatalf("on by default: %d %v %.200s", resp.StatusCode, resp.Header, raw)
	}
	var view struct {
		XrayJSON bool `json:"sub_xray_json"`
	}
	resp, raw := k.do(http.MethodPatch, k.api+"/settings", map[string]any{"sub_xray_json": false}, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &view) != nil || view.XrayJSON {
		t.Fatalf("switching off: %d %s", resp.StatusCode, raw)
	}
	resp, raw = fetch()
	if links, err := base64.StdEncoding.DecodeString(string(raw)); err != nil || !strings.Contains(string(links), "vless://") {
		t.Fatalf("off: %v %.200s", resp.Header, raw)
	}
}
