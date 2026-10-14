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
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/proto"
)

// A copy over the API: the same template and keys on the port and node the request
// names, under a name of its own; a refused copy is 4xx and nothing is written.
func TestCopyInboundEndpoint(t *testing.T) {
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
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"

	ins, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		ID     int64
		Name   string
		Port   string
		Config string
	}
	for _, in := range ins {
		if in.NodeID == 1 && in.Name == "vless-xhttp" {
			source = struct {
				ID     int64
				Name   string
				Port   string
				Config string
			}{in.ID, in.Name, in.Port, in.Config}
		}
	}
	url := api + "/inbounds/" + strconv.FormatInt(source.ID, 10) + "/copy"
	copyTo := func(body map[string]any) (*http.Response, []byte) {
		return h.do(http.MethodPost, url, body, csrf)
	}
	var view struct {
		ID         int64  `json:"id"`
		NodeID     int64  `json:"node_id"`
		Name       string `json:"name"`
		Port       string `json:"port"`
		Config     string `json:"config"`
		AutoPort   bool   `json:"auto_port"`
		ExitNodeID *int64 `json:"exit_node_id"`
	}

	// The panel's port is held even in a copy.
	if resp, body := copyTo(map[string]any{"port": "21355"}); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "port_panel") {
		t.Fatalf("the panel's port: %d %s", resp.StatusCode, body)
	}
	if resp, body := copyTo(map[string]any{"port": "2543"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("copy: %d %s", resp.StatusCode, body)
	} else if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("copy body: %s", body)
	}
	if view.NodeID != 1 || view.Name != "vless-xhttp-2" || view.Port != "2543" || view.Config != source.Config || !view.AutoPort || view.ExitNodeID != nil {
		t.Fatalf("copy: %+v", view)
	}
	var audit int
	if err := h.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'inbound.copy'`).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("audit: %d %v", audit, err)
	}
	if resp, _ := copyTo(map[string]any{"port": "2543"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("the copy's own port is taken now: %d", resp.StatusCode)
	}

	// Another node, under another site.
	if resp, body := copyTo(map[string]any{"node_id": b.ID, "port": "2544", "dest": "203.0.113.20:443", "server_name": "www.example.org"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("remote copy: %d %s", resp.StatusCode, body)
	} else if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("remote copy body: %s", body)
	}
	if view.NodeID != b.ID || view.Port != "2544" {
		t.Fatalf("remote copy: %+v", view)
	}
	tpl, err := proto.Parse(view.Config)
	if err != nil {
		t.Fatalf("remote copy template: %v", err)
	}
	dest, names := presets.Dest(tpl)
	if dest != "203.0.113.20:443" || len(names) != 1 || names[0] != "www.example.org" {
		t.Fatalf("remote copy target: %s %v", dest, names)
	}

	for name, c := range map[string]struct {
		id     int64
		body   map[string]any
		status int
		code   string
	}{
		"no source":                 {999, map[string]any{}, http.StatusNotFound, "not_found"},
		"unknown node":              {source.ID, map[string]any{"node_id": 9}, http.StatusUnprocessableEntity, "unknown_node"},
		"bad port":                  {source.ID, map[string]any{"port": "70000"}, http.StatusUnprocessableEntity, "bad_port"},
		"busy port":                 {source.ID, map[string]any{"port": "8443"}, http.StatusConflict, "port_in_use"},
		"target with no site name":  {source.ID, map[string]any{"dest": "203.0.113.20:443"}, http.StatusUnprocessableEntity, "bad_dest"},
		"target of no REALITY form": {source.ID, map[string]any{"dest": "nope"}, http.StatusUnprocessableEntity, "bad_dest"},
	} {
		resp, body := h.do(http.MethodPost, api+"/inbounds/"+strconv.FormatInt(c.id, 10)+"/copy", c.body, csrf)
		if resp.StatusCode != c.status || !strings.Contains(string(body), c.code) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	// A mutation needs the session's CSRF token.
	if resp, _ := h.do(http.MethodPost, url, map[string]any{"port": "2545"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
}
