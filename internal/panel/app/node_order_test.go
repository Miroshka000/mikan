package app

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The admin orders the servers: the API takes every node once, a new node goes last, and
// the subscription (share links and the Clash profile) follows the order at once.
func TestNodeOrder(t *testing.T) {
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
	add := func(name, host string) db.Node {
		n, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: name, Host: host, APIPort: 40000}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	nl, us := add("🇳🇱 Нидерланды", "198.51.100.20"), add("🇺🇸 США", "198.51.100.30")
	if us.Sort <= nl.Sort || nl.Sort == 0 {
		t.Fatalf("new nodes go last: %d %d", nl.Sort, us.Sort)
	}
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	api := "/" + adminPath + "/api/v1"
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	ids := func(list ...int64) map[string]any { return map[string]any{"ids": list} }

	if resp, _ := h.do(http.MethodPut, api+"/nodes/order", ids(1, nl.ID, us.ID), nil); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous order: %d", resp.StatusCode)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	// hosts lists the servers of the subscription in order, once each, from the links.
	hosts := func() []string {
		// The panel keeps the server list for a few seconds; a reorder drops it at once
		// when nodes run, and here the clock moves on instead.
		h.now = h.now.Add(time.Minute)
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"?format=uri", nil, map[string]string{"User-Agent": "Happ/3.4.1"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription: %d", resp.StatusCode)
		}
		raw, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			link, err := url.Parse(strings.TrimSpace(l))
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 || out[len(out)-1] != link.Hostname() {
				out = append(out, link.Hostname())
			}
		}
		return out
	}
	// groups lists the country groups of the Clash profile in order (the panel's own node
	// has no name, so no group).
	groups := func() string {
		h.now = h.now.Add(time.Minute)
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "clash.meta/1.19"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("clash profile: %d", resp.StatusCode)
		}
		var cfg struct {
			Groups []struct {
				Name    string   `yaml:"name"`
				Proxies []string `yaml:"proxies"`
			} `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal(body, &cfg); err != nil || len(cfg.Groups) == 0 {
			t.Fatalf("clash profile: %v %s", err, body)
		}
		// The main group: the auto group, then the country groups.
		return strings.Join(cfg.Groups[0].Proxies[1:3], "|")
	}
	byID := "203.0.113.10 198.51.100.20 198.51.100.30"
	if got := strings.Join(hosts(), " "); got != byID {
		t.Fatalf("before any reorder the order is by id: %s", got)
	}

	for _, bad := range []struct {
		name string
		ids  []int64
	}{
		{"missing", []int64{us.ID, 1}},
		{"extra", []int64{us.ID, nl.ID, 1, 999}},
		{"duplicate", []int64{us.ID, us.ID, 1}},
		{"duplicate with the right count", []int64{us.ID, 1, 1}},
		{"empty", []int64{}},
	} {
		resp, body := h.do(http.MethodPut, api+"/nodes/order", ids(bad.ids...), csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", bad.name, resp.StatusCode, body)
		}
	}
	if resp, body := h.do(http.MethodPut, api+"/nodes/order", map[string]any{}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no ids: %d %s", resp.StatusCode, body)
	}
	if got := strings.Join(hosts(), " "); got != byID {
		t.Fatalf("a refused order changed something: %s", got)
	}

	if resp, body := h.do(http.MethodPut, api+"/nodes/order", ids(us.ID, 1, nl.ID), csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("order: %d %s", resp.StatusCode, body)
	}
	if got := strings.Join(hosts(), " "); got != "198.51.100.30 203.0.113.10 198.51.100.20" {
		t.Fatalf("links follow the order: %s", got)
	}
	if got := groups(); got != "🇺🇸 США|🇳🇱 Нидерланды" {
		t.Fatalf("the Clash profile follows the order: %s", got)
	}
	// The nodes page lists them in the same order.
	resp, body := h.do(http.MethodGet, api+"/nodes", nil, nil)
	var list []struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list) != 3 || list[0].ID != us.ID || list[1].ID != 1 || list[2].ID != nl.ID {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	// A node added now goes last, after a reorder too.
	de := add("🇩🇪 Германия", "198.51.100.40")
	if got := strings.Join(hosts(), " "); got != "198.51.100.30 203.0.113.10 198.51.100.20 198.51.100.40" {
		t.Fatalf("a new node is last: %s (node %s)", got, id(de.ID))
	}
	// The old list is stale now: it misses the new node.
	if resp, body := h.do(http.MethodPut, api+"/nodes/order", ids(us.ID, 1, nl.ID), csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("stale list: %d %s", resp.StatusCode, body)
	}

	audit, err := h.st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 40, Lim: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, a := range audit {
		if a.Action == "node.order" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("one audit entry for the one reorder, got %d", found)
	}
}
