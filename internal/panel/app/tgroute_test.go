package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bot's way to Telegram over the admin API: a bad proxy or node is refused before
// it is saved, a route that does not reach Telegram too, and a proxy password never
// comes back or lands in the audit log.
func TestTelegramRouteOverHTTP(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Mikan","username":"mikan_bot"}}`)
	}))
	t.Cleanup(tg.Close)
	// A plain HTTP proxy in front of the fake Bot API.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(r.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	h := newHarness(t, func(o *Options) { o.TelegramAPI = tg.URL })
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/telegram"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	// The panel's own node shares its server: it is no way around a block.
	var own int64
	if err := h.st.DB.QueryRow("SELECT id FROM nodes WHERE address = '' ORDER BY id LIMIT 1").Scan(&own); err != nil {
		if err := h.st.DB.QueryRow("INSERT INTO nodes(name,address,created_at,updated_at) VALUES('own','',1,1) RETURNING id").Scan(&own); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct {
		route map[string]any
		code  string
	}{
		"no scheme":    {map[string]any{"mode": "proxy", "proxy": "203.0.113.5:1080"}, "tg_proxy_invalid"},
		"socks4":       {map[string]any{"mode": "proxy", "proxy": "socks4://203.0.113.5:1080"}, "tg_proxy_invalid"},
		"no proxy yet": {map[string]any{"mode": "proxy"}, "tg_proxy_invalid"},
		"no such node": {map[string]any{"mode": "node", "node_id": 999}, "tg_route_node_missing"},
		"own node":     {map[string]any{"mode": "node", "node_id": own}, "tg_route_node"},
		"dead proxy":   {map[string]any{"mode": "proxy", "proxy": "http://" + dead}, "tg_route_unreachable"},
		// A proxy next to the panel is an explicit address; a name must not lead to the host itself.
		"name to loopback":   {map[string]any{"mode": "proxy", "proxy": "socks5://127.0.0.1.nip.io:1080"}, "tg_proxy_private"},
		"name to metadata":   {map[string]any{"mode": "proxy", "proxy": "socks5://metadata.attacker.example:1080"}, "tg_proxy_private"},
		"metadata literal":   {map[string]any{"mode": "proxy", "proxy": "http://169.254.169.254:80"}, "tg_proxy_private"},
		"unspecified listen": {map[string]any{"mode": "proxy", "proxy": "http://0.0.0.0:80"}, "tg_proxy_private"},
		"unknown mode":       {map[string]any{"mode": "vpn"}, "mode"},
		"proxy too big":      {map[string]any{"mode": "proxy", "proxy": "http://" + strings.Repeat("a", 600) + ":80"}, "proxy"},
	} {
		resp, body := h.do(http.MethodPatch, api, map[string]any{"route": c.route}, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), c.code) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}

	addr := proxy.Listener.Addr().String()
	resp, body := h.do(http.MethodPatch, api, map[string]any{"route": map[string]any{"mode": "proxy", "proxy": "http://bot:s3cret@" + addr}}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"proxy":"http://bot:•••@`+addr+`"`) || strings.Contains(string(body), "s3cret") {
		t.Fatalf("proxy route: %d %s", resp.StatusCode, body)
	}
	// Switching away and back keeps the proxy: no need to type the password again.
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"route": map[string]any{"mode": "direct"}}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"direct"`) {
		t.Fatalf("direct: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"route": map[string]any{"mode": "proxy"}}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"proxy"`) {
		t.Fatalf("back to the proxy: %d %s", resp.StatusCode, body)
	}
	var leaked int
	if err := h.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE details LIKE '%s3cret%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("proxy password in the audit log: %d %v", leaked, err)
	}

	// A remote node of the panel is taken: the route is refused only when Telegram cannot
	// be reached through it (this node does not run), never as "not a node of the panel".
	var id int64
	if err := h.st.DB.QueryRow("INSERT INTO nodes(name,address,created_at,updated_at) VALUES('SE','198.51.100.9:40000',1,1) RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	resp, body = h.do(http.MethodPatch, api, map[string]any{"route": map[string]any{"mode": "node", "node_id": id}}, csrf)
	if strings.Contains(string(body), "tg_route_node") {
		t.Fatalf("a remote node refused as none: %d %s", resp.StatusCode, body)
	}
}
