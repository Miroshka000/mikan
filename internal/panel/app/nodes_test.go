package app

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
)

// A node something still goes through is not deleted: the API names the inbounds and
// the nodes that leave through it and the bot's route, and nothing changes until they
// are switched away.
func TestDeleteNodeInUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
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
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	x, err := domain.NewInbounds(h.st, nil, time.Now).Find(ctx, 1, "vless-xhttp")
	if err != nil {
		t.Fatal(err)
	}
	inbound := api + "/inbounds/" + id(x.ID)
	if resp, body := h.do(http.MethodPatch, inbound, map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("via B: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/nodes/"+id(c.ID)+"/cascade", map[string]any{"outbound": "node", "exit_node_id": b.ID}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("C → B: %d %s", resp.StatusCode, body)
	}
	if err := settings.Set(ctx, set, tgate.KeyRoute, tgate.Route{Mode: tgate.RouteNode, NodeID: b.ID}); err != nil {
		t.Fatal(err)
	}

	resp, body := h.do(http.MethodDelete, api+"/nodes/"+id(b.ID), nil, csrf)
	for _, want := range []string{`"node_in_use"`, `"node_in_use_inbounds"`, `vless-xhttp (203.0.113.10)`, `"node_in_use_relays"`, `"value":"C"`, `"node_in_use_telegram"`} {
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), want) {
			t.Fatalf("B in use, no %s: %d %s", want, resp.StatusCode, body)
		}
	}
	if _, err := h.st.Q.GetNode(ctx, b.ID); err != nil {
		t.Fatalf("B is gone: %v", err)
	}
	if after, _ := h.st.Q.GetInbound(ctx, x.ID); after.ExitNodeID.Int64 != b.ID {
		t.Fatalf("the inbound lost its exit: %+v", after)
	}

	// One left is still one too many.
	for _, step := range []struct {
		path string
		body map[string]any
	}{
		{inbound, map[string]any{"outbound": "direct"}},
		{api + "/nodes/" + id(c.ID) + "/cascade", map[string]any{"outbound": "direct"}},
	} {
		if resp, body := h.do(http.MethodPatch, step.path, step.body, csrf); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", step.path, resp.StatusCode, body)
		}
	}
	resp, body = h.do(http.MethodDelete, api+"/nodes/"+id(b.ID), nil, csrf)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "node_in_use_telegram") || strings.Contains(string(body), "node_in_use_inbounds") {
		t.Fatalf("only the bot left: %d %s", resp.StatusCode, body)
	}
	if err := settings.Set(ctx, set, tgate.KeyRoute, tgate.Route{Mode: tgate.RouteDirect}); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodDelete, api+"/nodes/"+id(b.ID), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if _, err := h.st.Q.GetNode(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("B is still there: %v", err)
	}
	if resp, _ := h.do(http.MethodDelete, api+"/nodes/1", nil, csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("the panel's own node: %d", resp.StatusCode)
	}
}
