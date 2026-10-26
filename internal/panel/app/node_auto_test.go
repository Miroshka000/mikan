package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"

	"go.yaml.in/yaml/v3"
)

// A node switched out of the automatic choice (a server for whitelist-only networks) keeps
// its servers in the subscription, to pick by hand, and leaves the "Auto" group.
func TestNodeOutOfAuto(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	// A second node with one of the first one's inbounds: the whitelist server.
	var wl int64
	if err := h.st.DB.QueryRowContext(ctx, "INSERT INTO nodes(name,address,created_at,updated_at) VALUES('🇳🇱 Нидерланды [WL]','198.51.100.9:40000',1,1) RETURNING id").Scan(&wl); err != nil {
		t.Fatal(err)
	}
	ins, _ := h.st.Q.ListInbounds(ctx)
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE inbounds SET node_id = $1 WHERE id = $2", wl, ins[0].ID); err != nil {
		t.Fatal(err)
	}
	resp, body := h.do(http.MethodPatch, api+"/nodes/"+strconv.FormatInt(wl, 10), map[string]any{"in_auto": false}, csrf)
	var node struct {
		InAuto bool `json:"in_auto"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &node) != nil || node.InAuto {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "clash.meta/1.19"})
	var cfg struct {
		Groups []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if resp.StatusCode != http.StatusOK || yaml.Unmarshal(body, &cfg) != nil {
		t.Fatalf("subscription: %d %s", resp.StatusCode, body)
	}
	groups := map[string][]string{}
	for _, g := range cfg.Groups {
		groups[g.Name] = g.Proxies
	}
	auto, country := groups["Авто"], groups["🇳🇱 Нидерланды [WL]"]
	if len(country) != 1 || len(auto) == 0 || slices.Contains(auto, country[0]) {
		t.Fatalf("auto %v, the node's group %v (groups %v)", auto, country, groups)
	}
}

// The admin's word keeps a node out at once; a node that does not answer the panel, only
// after nodeDownForAuto; one never checked, or answering, stays in.
func TestOutOfAuto(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	down := func(d time.Duration) nodesync.HealthView { return nodesync.HealthView{Since: now.Add(-d)} }
	for _, c := range []struct {
		name   string
		noAuto int64
		hv     nodesync.HealthView
		known  bool
		want   bool
	}{
		{"the admin's word", 1, nodesync.HealthView{OK: true}, true, true},
		{"answering", 0, nodesync.HealthView{OK: true}, true, false},
		{"not checked yet", 0, nodesync.HealthView{}, false, false},
		{"silent for a minute", 0, down(time.Minute), true, false},
		{"silent long enough", 0, down(nodeDownForAuto), true, true},
	} {
		if got := outOfAuto(c.noAuto, c.hv, c.known, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
