package api

import (
	"context"
	"encoding/json"
	"mikan/internal/frontpage"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// postureLines are the changes as "inbound code detail", the node's own with no inbound.
func postureLines(changes []PostureChange) []string {
	var out []string
	for _, c := range changes {
		line := strings.TrimSpace(c.Inbound + " " + c.Code)
		switch c.Code {
		case "site":
			line += " " + c.Params["sni"] + "@" + c.Params["dest"]
		case "port":
			line += " " + c.Params["to"]
		case "front_page", "panel_page":
			line += " " + c.Params["page"]
		}
		if c.Manual {
			line += " (manual)"
		}
		out = append(out, line)
	}
	return out
}

func postureInbound(t *testing.T, id int64, name, preset, port, dest string) db.Inbound {
	t.Helper()
	in := exposureInbound(t, name, preset, port, dest)
	in.ID = id
	return in
}

// behind puts the inbound behind the router, as the preset leaves it.
func behind(t *testing.T, in db.Inbound) db.Inbound {
	t.Helper()
	tpl, err := proto.Parse(in.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.SetSNI443(tpl, true); err != nil {
		t.Fatal(err)
	}
	in.Config = string(tpl.JSON())
	return in
}

// The panel's own node as a fresh panel leaves it and an admin then fills it: four REALITY
// protocols on four ports with one site, one of them on 443, Hysteria2 next to them. The
// secure preset puts them behind one 443, the panel's own names first, then the sites next
// to the server, weak ones never; with no site left the protocol stays where it is and the
// admin is told.
func TestSecurePresetOfThePanelsNode(t *testing.T) {
	p := posture{own: []string{"my.example.com", "sub.example.com"}, panelPort: 21355, page: "", pagePick: "cats", nodes: []postureNode{{
		node: db.Node{ID: 1, Name: "main", Enabled: 1, ApiLockdown: 0, OwnPorts: []int64{22}},
		inbounds: []db.Inbound{
			postureInbound(t, 1, "xhttp", "vless_reality_xhttp", "443", presets.DefaultDest),
			postureInbound(t, 2, "vision", "vless_reality_vision", "8443", presets.DefaultDest),
			postureInbound(t, 3, "grpc", "vless_reality_grpc", "2053", "news.example.org:443"),
			postureInbound(t, 4, "pq", "vless_reality_xhttp_pq", "2096", "news.example.org:443"),
			postureInbound(t, 5, "hy", "hysteria2", "2083", ""),
		},
		spare:   []string{"20000", "20001"},
		scanned: true,
		sites: []scan.Result{
			{Dest: "home.duckdns.org:443", SNI: "home.duckdns.org", OK: true},
			{Dest: "broken.example.net:443", SNI: "broken.example.net"},
			{Dest: "shop.example.net:443", SNI: "shop.example.net", OK: true},
		},
	}}}
	// The two on the stock site take the panel's own names; of the two that share a site the
	// first keeps it and the second gets the neighbour that is neither weak nor broken.
	got := postureLines(p.secure())
	want := []string{
		"panel_page cats",
		"xhttp site my.example.com@127.0.0.1:21355",
		"xhttp port 20000",
		"xhttp through_443",
		"vision site sub.example.com@127.0.0.1:21355",
		"vision through_443",
		"grpc through_443",
		"pq site shop.example.net@shop.example.net:443",
		"pq through_443",
		"router_on",
		"front_page cats",
		"ssh_open (manual)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the secure preset:\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
	if name, secure, standard := p.current(); name != postureStandard || secure != 11 || standard != 0 {
		t.Fatalf("a fresh node is on the standard preset: %s, %d to secure, %d to standard", name, secure, standard)
	}

	// With no site left the protocol stays on its port, and the rest still share 443.
	p.nodes[0].sites = nil
	got = postureLines(p.secure())
	if !slices.Contains(got, "pq no_site (manual)") || slices.Contains(got, "pq through_443") || !slices.Contains(got, "router_on") {
		t.Fatalf("no site for the last protocol: %v", got)
	}
	// Before sites are looked for the need is a change of its own, with no site named.
	p.nodes[0].scanned = false
	if got = postureLines(p.secure()); !slices.Contains(got, "pq site @") {
		t.Fatalf("a site still to find: %v", got)
	}
}

// A node the preset has been through has nothing left; taking it apart again is the
// standard preset's work.
func TestPresetsOnASecureNode(t *testing.T) {
	site := func(id int64, name, preset, port, dest string) db.Inbound {
		return behind(t, postureInbound(t, id, name, preset, port, dest))
	}
	p := posture{page: "maintenance", pagePick: "cats", nodes: []postureNode{{
		node: db.Node{ID: 2, Name: "far", Address: "203.0.113.7:25305", Enabled: 1, SniRouter: 1, ApiLockdown: 1, FrontPage: "maintenance"},
		inbounds: []db.Inbound{
			site(1, "xhttp", "vless_reality_xhttp", "20000", "shop.example.net:443"),
			site(2, "vision", "vless_reality_vision", "8443", "news.example.org:443"),
			postureInbound(t, 3, "hy", "hysteria2", "443", ""),
		},
	}}}
	if got := postureLines(p.secure()); len(got) != 0 {
		t.Fatalf("nothing left to do: %v", got)
	}
	if name, _, standard := p.current(); name != postureSecure || standard != 3 {
		t.Fatalf("current %s, %d to standard", name, standard)
	}
	if got, want := postureLines(p.standard()), []string{"xhttp direct", "vision direct", "router_off"}; !slices.Equal(got, want) {
		t.Fatalf("back to standard: %v", got)
	}
	// A remote node with its API open and no page gets both; one switched off is left alone.
	p.nodes[0].node.ApiLockdown, p.nodes[0].node.FrontPage = 0, ""
	if got, want := postureLines(p.secure()), []string{"api_lockdown", "front_page maintenance"}; !slices.Equal(got, want) {
		t.Fatalf("a remote node: %v", got)
	}
	p.nodes[0].node.Enabled = 0
	if got := postureLines(p.secure()); len(got) != 0 {
		t.Fatalf("a node switched off: %v", got)
	}
}

// A protocol the router cannot take holds TCP 443: the preset says so and leaves the node's
// ports as they are.
func TestSecurePresetWithPort443Held(t *testing.T) {
	ss := postureInbound(t, 1, "ss", "shadowsocks_2022", "443", "")
	p := posture{page: "cats", nodes: []postureNode{{
		node:     db.Node{ID: 1, Name: "main", Enabled: 1, FrontPage: "cats"},
		inbounds: []db.Inbound{ss, postureInbound(t, 2, "vision", "vless_reality_vision", "8443", "news.example.org:443")},
		spare:    []string{"20000"},
	}}}
	if got, want := postureLines(p.secure()), []string{"ss port_443_busy (manual)"}; !slices.Equal(got, want) {
		t.Fatalf("443 held by Shadowsocks: %v", got)
	}
	// The same when the panel itself listens on 443 of its own server.
	p.nodes[0].inbounds = p.nodes[0].inbounds[1:]
	p.panelPort = 443
	if got, want := postureLines(p.secure()), []string{"port_443_panel (manual)"}; !slices.Equal(got, want) {
		t.Fatalf("443 held by the panel: %v", got)
	}
}

// The changes the admin was shown are written through the same doors a change by hand
// goes: the inbound behind the router, the node's router and page, the panel's page. One
// that is refused says why and the rest are written; an API key takes no preset.
func TestSecurityPresetApply(t *testing.T) {
	ctx := context.Background()
	f := newHostFixture(t, false, func(d *Deps) { d.Inbounds, d.Nodes = domain.NewInbounds(d.Store, nil, d.Now), nil })
	code, body := f.do(http.MethodGet, "/api/v1/security/presets", "")
	if code != http.StatusOK || !strings.Contains(body, `"current":"standard"`) {
		t.Fatalf("a fresh panel: %d %s", code, body)
	}
	ins, err := f.st.ListNodeInbounds(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var own PostureChange
	for _, in := range ins {
		if tpl, err := proto.Parse(in.Config); err == nil && in.Enabled != 0 && proto.Reality(tpl) && own.InboundID == 0 {
			own = PostureChange{Node: 1, InboundID: in.ID, Inbound: in.Name, Code: "through_443"}
		}
	}
	if own.InboundID == 0 {
		t.Fatal("the panel's node has no REALITY inbound to start with")
	}
	req, _ := json.Marshal(map[string]any{"changes": []PostureChange{
		own,
		{Node: 1, Code: "router_on"},
		{Node: 1, Code: "front_page", Params: map[string]string{"page": "cats"}},
		{Node: 2, Code: "front_page", Params: map[string]string{"page": "nope"}},
		{Code: "panel_page", Params: map[string]string{"page": "cats"}},
		{Node: 1, Code: "ssh_open", Manual: true},
	}})
	code, body = f.send(http.MethodPost, "/api/v1/security/presets/secure/apply", string(req))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got struct{ Changes []PostureChange }
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Changes {
		if bad := c.Node == 2; (c.Error != "") != bad {
			t.Errorf("%s on node %d: error %q", c.Code, c.Node, c.Error)
		}
	}
	node, err := f.st.GetNode(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.st.GetInbound(ctx, own.InboundID)
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := proto.Parse(row.Config)
	page, _ := f.deps.Settings.String(ctx, settings.KeySiteTemplate)
	if node.SniRouter == 0 || node.FrontPage != "cats" || !proto.SNI443(tpl) || page != "cats" {
		t.Fatalf("after the preset: router %d, page %q, behind 443 %v, the panel's page %q", node.SniRouter, node.FrontPage, proto.SNI443(tpl), page)
	}
	if far, _ := f.st.GetNode(ctx, 2); far.FrontPage != "" {
		t.Fatalf("a page that does not exist was written: %q", far.FrontPage)
	}
	if f.audited("security.preset") != 1 {
		t.Fatal("the preset is not in the audit log")
	}
	// A line the panel never makes is refused whole.
	if code, body := f.send(http.MethodPost, "/api/v1/security/presets/secure/apply", `{"changes":[{"node":1,"code":"ssh_open"}]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown change: %d %s", code, body)
	}
	if code, _ := f.do(http.MethodPost, "/api/v1/security/presets/standard/plan", "full"); code != http.StatusForbidden {
		t.Fatalf("an API key asked for a plan: %d", code)
	}
	code, body = f.do(http.MethodPost, "/api/v1/security/presets/standard/plan", "")
	if code != http.StatusOK || !strings.Contains(body, `"code":"direct"`) || !strings.Contains(body, `"code":"router_off"`) {
		t.Fatalf("back to standard: %d %s", code, body)
	}
}

// The admin's own front page: saved by the admin's session alone, a page and not anything
// else, and from then on a page the panel and a node may be set to.
func TestSitePage(t *testing.T) {
	ctx := context.Background()
	f := newHostFixture(t, false, func(d *Deps) { d.Nodes = nil })
	t.Cleanup(func() { frontpage.SetCustom(nil) })
	code, body := f.do(http.MethodGet, "/api/v1/settings/site-page", "")
	if code != http.StatusOK || !strings.Contains(body, `"size":0`) || !strings.Contains(body, `"seed":"`) {
		t.Fatalf("before a page: %d %s", code, body)
	}
	if code, body := f.send(http.MethodPatch, "/api/v1/settings", `{"site_template":"custom"}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "bad_front_page") {
		t.Fatalf("a page that is not there was chosen: %d %s", code, body)
	}
	if code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"front_page":"custom"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a node took a page that is not there: %d %s", code, body)
	}
	page := "<!DOCTYPE html><html lang=\"en\"><head><title>Bakery on the corner</title></head><body><h1>Fresh bread</h1></body></html>"
	put := func(doc, html string) (int, string) {
		req, _ := json.Marshal(map[string]string{"doc": doc, "html": html})
		return f.send(http.MethodPut, "/api/v1/settings/site-page", string(req))
	}
	for name, bad := range map[string][2]string{
		"not a page":     {`{}`, strings.Repeat("just some words, no markup at all. ", 4)},
		"a broken doc":   {`{"v":`, page},
		"a page too big": {`{}`, page + strings.Repeat("<!-- padding -->", frontpage.MaxCustom/16)},
	} {
		if code, body := put(bad[0], bad[1]); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if frontpage.Page(frontpage.Custom) != nil {
		t.Fatal("a refused page was kept")
	}
	if code, body := put(`{"v":1}`, page); code != http.StatusOK || !strings.Contains(body, `"doc":"{\"v\":1}"`) {
		t.Fatalf("%d %s", code, body)
	}
	if got := string(frontpage.Page(frontpage.Custom)); got != page {
		t.Fatalf("the page the panel shows: %q", got)
	}
	if saved, _ := f.deps.Settings.String(ctx, settings.KeySiteHTML); saved != page || f.audited("settings.site_page") != 1 {
		t.Fatalf("saved %q, audited %d", saved, f.audited("settings.site_page"))
	}
	if code, body := f.send(http.MethodPatch, "/api/v1/settings", `{"site_template":"custom"}`); code != http.StatusOK {
		t.Fatalf("the panel's page: %d %s", code, body)
	}
	if code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"front_page":"custom"}`); code != http.StatusOK {
		t.Fatalf("a node's page: %d %s", code, body)
	}
	// An API key reads nothing it could not already, and writes no page.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/site-page", strings.NewReader(`{"doc":"","html":"`+strings.Repeat("x", 80)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer mk_FullKeyFullKeyFullKeyFullKey0123456789")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an API key wrote the page: %d %s", rec.Code, rec.Body.String())
	}
}
