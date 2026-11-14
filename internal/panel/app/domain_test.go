package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The panel takes a domain only when public DNS leads it to this server: someone else's
// domain would go into every subscription.
func TestDomainMustLeadHere(t *testing.T) {
	records := map[string]string{"vpn.example.com": "203.0.113.10", "other.example.com": "198.51.100.7"}
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 3, "Answer": []any{}}
		if ip, ok := records[r.URL.Query().Get("name")]; ok {
			ans["Status"] = 0
			if r.URL.Query().Get("type") == "A" {
				ans["Answer"] = []any{map[string]any{"type": 1, "data": ip}}
			}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(doh.Close)
	h := newHarness(t, func(o *Options) { o.DNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient} })
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/settings"
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"public_host": "203.0.113.10"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("public host: %d %s", resp.StatusCode, body)
	}
	for name, code := range map[string]string{"other.example.com": "domain_elsewhere", "nowhere.example.com": "domain_not_found"} {
		resp, body := h.do(http.MethodPatch, api, map[string]any{"domain": name}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), code) {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"domain": "vpn.example.com"}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"domain":"vpn.example.com"`) {
		t.Fatalf("own domain: %d %s", resp.StatusCode, body)
	}
	// Moving the server's address away from the domain is refused too; the domain itself
	// is not asked about again on every save.
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"public_host": "203.0.113.99"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "domain_elsewhere") {
		t.Fatalf("address moved off the domain: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"brand": "X", "domain": "vpn.example.com"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the same domain: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"domain": ""}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("clearing the domain: %d %s", resp.StatusCode, body)
	}
}

// The subscription domain is a name of its own that leads here, never the panel's: links
// move to it, the server is told at once, and clearing it takes the links back.
func TestSubDomainSetting(t *testing.T) {
	records := map[string]string{"vpn.example.com": "203.0.113.10", "sub.example.com": "203.0.113.10", "other.example.com": "198.51.100.7"}
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 3, "Answer": []any{}}
		if ip, ok := records[r.URL.Query().Get("name")]; ok {
			ans["Status"] = 0
			if r.URL.Query().Get("type") == "A" {
				ans["Answer"] = []any{map[string]any{"type": 1, "data": ip}}
			}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(doh.Close)
	var served []string
	h := newHarness(t, func(o *Options) {
		o.DNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient}
		o.SubDomain = func(name string) { served = append(served, name) }
	})
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/settings"
	if err := settings.Set(t.Context(), settings.New(h.st.Q), settings.KeyPanelPort, 2053); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"public_host": "203.0.113.10", "domain": "vpn.example.com"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("panel domain: %d %s", resp.StatusCode, body)
	}
	for name, code := range map[string]string{"vpn.example.com": "sub_domain_panel", "other.example.com": "domain_elsewhere", "203.0.113.10": "sub_domain_invalid", "sub": "sub_domain_invalid"} {
		resp, body := h.do(http.MethodPatch, api, map[string]any{"sub_domain": name}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), code) || !strings.Contains(string(body), "body.sub_domain") {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if len(served) != 0 {
		t.Fatalf("refused domains were served: %v", served)
	}
	var v struct {
		SubDomain  string `json:"sub_domain"`
		SubBaseURL string `json:"sub_base_url"`
		AdminURL   string `json:"admin_url"`
	}
	resp, body := h.do(http.MethodPatch, api, map[string]any{"sub_domain": "sub.example.com"}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.SubDomain != "sub.example.com" ||
		!strings.HasPrefix(v.SubBaseURL, "https://sub.example.com:") || !strings.HasPrefix(v.AdminURL, "https://vpn.example.com:") {
		t.Fatalf("sub domain: %d %s", resp.StatusCode, body)
	}
	if len(served) != 1 || served[0] != "sub.example.com" {
		t.Fatalf("served: %v", served)
	}
	// The server's address moving off the subscription domain is refused.
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"public_host": "203.0.113.99", "domain": ""}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "body.sub_domain") {
		t.Fatalf("address moved off the subscription domain: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, api, map[string]any{"sub_domain": ""}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.SubDomain != "" || !strings.HasPrefix(v.SubBaseURL, "https://vpn.example.com:") {
		t.Fatalf("cleared: %d %s", resp.StatusCode, body)
	}
	if len(served) != 2 || served[1] != "" {
		t.Fatalf("served after clearing: %v", served)
	}
}

// A change of the domain through the settings page: the name left behind becomes a former
// name the panel keeps answering at, the address of a panel that got its first domain too;
// the panel's own node shows its self-steal site under the new domain, the old one kept
// after it; the admin lets a former name go, and the site leaves it.
func TestDomainChangeIsFollowed(t *testing.T) {
	records := map[string]string{"a.example.com": "203.0.113.10", "b.example.com": "203.0.113.10"}
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 3, "Answer": []any{}}
		if ip, ok := records[r.URL.Query().Get("name")]; ok {
			ans["Status"] = 0
			if r.URL.Query().Get("type") == "A" {
				ans["Answer"] = []any{map[string]any{"type": 1, "data": ip}}
			}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(doh.Close)
	h := newHarness(t, func(o *Options) { o.DNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient} })
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/settings"
	set := settings.New(h.st.Q)
	if err := settings.Set(ctx, set, settings.KeyPanelPort, 2053); err != nil {
		t.Fatal(err)
	}
	type view struct {
		Domain  string `json:"domain"`
		Formers []struct {
			Name string `json:"name"`
		} `json:"former_domains"`
	}
	patch := func(body map[string]any) view {
		t.Helper()
		resp, raw := h.do(http.MethodPatch, api, body, csrf)
		var v view
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &v) != nil {
			t.Fatalf("%v: %d %s", body, resp.StatusCode, raw)
		}
		return v
	}
	formers := func(v view) []string {
		var out []string
		for _, f := range v.Formers {
			out = append(out, f.Name)
		}
		return out
	}
	patch(map[string]any{"public_host": "203.0.113.10"})
	if v := patch(map[string]any{"domain": "a.example.com"}); !slices.Equal(formers(v), []string{"203.0.113.10"}) {
		t.Fatalf("the first domain: %v", formers(v))
	}

	// The panel's own node shows the panel's HTTPS under its domain.
	ins, err := h.st.Q.ListNodeInbounds(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var own db.Inbound
	for _, in := range ins {
		if tpl, err := proto.Parse(in.Config); err == nil {
			if dest, _ := presets.Dest(tpl); dest != "" {
				own = in
				break
			}
		}
	}
	dest, name := "127.0.0.1:2053", "a.example.com"
	if _, _, err := domain.NewInbounds(h.st, nil, h.clock).Update(ctx, own.ID, domain.InboundPatch{Dest: &dest, ServerName: &name}); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		t.Helper()
		row, err := h.st.Q.GetInbound(ctx, own.ID)
		if err != nil {
			t.Fatal(err)
		}
		tpl, err := proto.Parse(row.Config)
		if err != nil {
			t.Fatal(err)
		}
		_, n := presets.Dest(tpl)
		return n
	}

	v := patch(map[string]any{"domain": "b.example.com"})
	if v.Domain != "b.example.com" || !slices.Equal(formers(v), []string{"203.0.113.10", "a.example.com"}) {
		t.Fatalf("a new domain: %s %v", v.Domain, formers(v))
	}
	if got := names(); !slices.Equal(got, []string{"b.example.com", "a.example.com"}) {
		t.Fatalf("the self-steal site: %v", got)
	}
	if applied, _, err := settings.Get[settings.Address](ctx, set, settings.KeyAddressApplied); err != nil || applied.Domain != "b.example.com" {
		t.Fatalf("followed: %+v %v", applied, err)
	}

	if resp, body := h.do(http.MethodDelete, api+"/former-domains/nowhere.example.com", nil, csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown name: %d %s", resp.StatusCode, body)
	}
	resp, raw := h.do(http.MethodDelete, api+"/former-domains/A.example.com", nil, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &v) != nil || !slices.Equal(formers(v), []string{"203.0.113.10"}) {
		t.Fatalf("let go: %d %s", resp.StatusCode, raw)
	}
	if got := names(); !slices.Equal(got, []string{"b.example.com"}) {
		t.Fatalf("the self-steal site after the old name went: %v", got)
	}
}
