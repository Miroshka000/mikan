package app

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// Cascades over HTTP: an inbound leaves through another node, a node's relay goes on
// to a third; self, loops, unknown nodes and the relay's port are refused.
func TestCascadeOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settings.New(h.st.Q), settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	b, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "C", Host: "198.51.100.30", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	ins, _ := h.st.Q.ListInbounds(ctx)
	inbound := api + "/inbounds/" + strconv.FormatInt(ins[0].ID, 10)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	for name, c := range map[string]struct {
		body map[string]any
		code string
	}{
		"itself":      {map[string]any{"outbound": "node", "exit_node_id": 1}, "exit_self"},
		"no node":     {map[string]any{"outbound": "node", "exit_node_id": 99}, "exit_not_found"},
		"no exit set": {map[string]any{"outbound": "node"}, "exit_not_found"},
	} {
		if resp, body := h.do(http.MethodPatch, inbound, c.body, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), c.code) {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	resp, body := h.do(http.MethodPatch, inbound, map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outbound":"node"`) || !strings.Contains(string(body), `"exit_node_id":`+id(b.ID)) {
		t.Fatalf("via B: %d %s", resp.StatusCode, body)
	}
	// B now has a relay with node 1 as its source.
	resp, body = h.do(http.MethodGet, api+"/nodes/"+id(b.ID)+"/cascade", nil, nil)
	var bv struct {
		Relay struct {
			Port    string `json:"port"`
			Sources []struct {
				NodeID int64 `json:"node_id"`
			} `json:"sources"`
			Outbound string `json:"outbound"`
		} `json:"relay"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &bv) != nil || bv.Relay.Port == "" || len(bv.Relay.Sources) != 1 || bv.Relay.Sources[0].NodeID != 1 || bv.Relay.Outbound != "direct" {
		t.Fatalf("B's relay: %d %s", resp.StatusCode, body)
	}
	if _, body := h.do(http.MethodGet, api+"/nodes/1/cascade", nil, nil); !strings.Contains(string(body), `"inbounds":["`+ins[0].Name+`"]`) {
		t.Fatalf("node 1 exits: %s", body)
	}
	// B → C, then C → B would loop.
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(b.ID)+"/cascade", map[string]any{"outbound": "node", "exit_node_id": c.ID}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("B → C: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "exit_cycle") {
		t.Fatalf("loop: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "warp"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("C → WARP: %d", resp.StatusCode)
	}
	// An inbound on B may not take the relay's port.
	bIns, _ := h.st.Q.ListInbounds(ctx)
	for _, in := range bIns {
		if in.NodeID == b.ID && domain.InboundNetwork(in) == "tcp" {
			if resp, body := h.do(http.MethodPatch, api+"/inbounds/"+id(in.ID), map[string]any{"port": bv.Relay.Port}, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "port_relay") {
				t.Fatalf("relay port: %d %s", resp.StatusCode, body)
			}
			break
		}
	}
	// Back to direct: the exit is gone.
	resp, body = h.do(http.MethodPatch, inbound, map[string]any{"outbound": "direct"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outbound":"direct"`) || strings.Contains(string(body), "exit_node_id") {
		t.Fatalf("direct: %d %s", resp.StatusCode, body)
	}
}

// A relay's site over HTTP: the panel's by default; the admin's own is checked as an
// inbound's (a site by IP needs a name, a name that leads to a private network is refused)
// and kept; handed back, it is the panel's again.
func TestRelaySiteOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	b, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	url := "/" + adminPath + "/api/v1/nodes/" + strconv.FormatInt(b.ID, 10) + "/cascade"
	resp, body := h.do(http.MethodPatch, url, map[string]any{"outbound": "direct"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"auto_sni":true`) || !strings.Contains(string(body), `"target":"default"`) ||
		!strings.Contains(string(body), `"server_name":"www.microsoft.com"`) {
		t.Fatalf("new relay: %d %s", resp.StatusCode, body)
	}
	for name, c := range map[string]struct {
		body           map[string]any
		location, code string
	}{
		"a site by IP without a name": {map[string]any{"outbound": "direct", "auto_sni": false, "dest": "203.0.113.5:443"}, "body.server_name", "reality_sni"},
		"a private network":           {map[string]any{"outbound": "direct", "auto_sni": false, "dest": "internal.attacker.example:443"}, "body.dest", "reality_dest_private"},
		"no site":                     {map[string]any{"outbound": "direct", "auto_sni": false}, "body.dest", "dest_format"},
	} {
		resp, body := h.do(http.MethodPatch, url, c.body, csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), c.location) || !strings.Contains(string(body), c.code) {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	resp, body = h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_sni": false, "dest": "203.0.113.5:443", "server_name": "near.example"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"dest":"203.0.113.5:443"`) || !strings.Contains(string(body), `"server_name":"near.example"`) ||
		!strings.Contains(string(body), `"auto_sni":false`) || !strings.Contains(string(body), `"target":"manual"`) {
		t.Fatalf("the admin's site: %d %s", resp.StatusCode, body)
	}
	// A route change alone keeps the site.
	if _, body := h.do(http.MethodPatch, url, map[string]any{"outbound": "warp"}, csrf); !strings.Contains(string(body), `"server_name":"near.example"`) {
		t.Fatalf("route only: %s", body)
	}
	// The admin's own port: a malformed one and B's node API port are refused, a free one kept.
	if resp, body := h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_port": false, "port": "70000"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(string(body), "body.port") || !strings.Contains(string(body), "bad_port") {
		t.Fatalf("past 65535: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_port": false, "port": "40000"}, csrf); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(string(body), "port_node_api") {
		t.Fatalf("the node API's port: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_port": false, "port": "9443"}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"port":"9443"`) || !strings.Contains(string(body), `"auto_port":false`) {
		t.Fatalf("the admin's port: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_port": true}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"port":"9443"`) || !strings.Contains(string(body), `"auto_port":true`) {
		t.Fatalf("the port back to the panel: %d %s", resp.StatusCode, body)
	}

	// Handed back on a node whose protocols show no site of their own: the default, which the
	// tuner replaces once nodes leave through B; the admin's site is not kept as the panel's.
	resp, body = h.do(http.MethodPatch, url, map[string]any{"outbound": "direct", "auto_sni": true}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"auto_sni":true`) || !strings.Contains(string(body), `"target":"default"`) ||
		strings.Contains(string(body), "near.example") {
		t.Fatalf("back to the panel: %d %s", resp.StatusCode, body)
	}
}
