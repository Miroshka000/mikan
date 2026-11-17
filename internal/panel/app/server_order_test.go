package app

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"slices"
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

// The admin orders every server of the subscriptions in one list, across the nodes: the API
// lists them as clients see them and takes every server once, the profile follows at once,
// the nodes go where their first servers are, and an order of the nodes moves their servers
// as blocks. A new address goes right after its protocol's servers.
func TestServerOrder(t *testing.T) {
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
	if resp, _ := h.do(http.MethodGet, api+"/inbounds/order", nil, nil); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous list: %d", resp.StatusCode)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	type item struct {
		HostID    int64  `json:"host_id"`
		InboundID int64  `json:"inbound_id"`
		NodeID    int64  `json:"node_id"`
		Default   bool   `json:"default"`
		Name      string `json:"name"`
		Served    bool   `json:"served"`
	}
	list := func() []item {
		t.Helper()
		resp, body := h.do(http.MethodGet, api+"/inbounds/order", nil, nil)
		var out []item
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("list: %d %s", resp.StatusCode, body)
		}
		return out
	}
	ids := func(items []item) []int64 {
		out := make([]int64, len(items))
		for i, x := range items {
			out[i] = x.HostID
		}
		return out
	}
	names := func(items []item) []string {
		out := make([]string, len(items))
		for i, x := range items {
			out[i] = x.Name
		}
		return out
	}
	nodesOf := func(items []item) []int64 {
		var out []int64
		for _, x := range items {
			if len(out) == 0 || out[len(out)-1] != x.NodeID {
				out = append(out, x.NodeID)
			}
		}
		return out
	}
	// proxies are the names of the Clash profile's proxies, in their order.
	proxies := func() []string {
		t.Helper()
		// The panel keeps the server list for a few seconds; here the clock moves on.
		h.advance(time.Minute)
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.32"})
		var cfg struct {
			Proxies []struct {
				Name string `yaml:"name"`
			} `yaml:"proxies"`
		}
		if resp.StatusCode != http.StatusOK || yaml.Unmarshal(body, &cfg) != nil {
			t.Fatalf("clash profile: %d %s", resp.StatusCode, body)
		}
		out := make([]string, len(cfg.Proxies))
		for i, p := range cfg.Proxies {
			out[i] = p.Name
		}
		return out
	}
	nodeList := func() []int64 {
		t.Helper()
		resp, body := h.do(http.MethodGet, api+"/nodes", nil, nil)
		var out []struct {
			ID int64 `json:"id"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("nodes: %d %s", resp.StatusCode, body)
		}
		got := make([]int64, len(out))
		for i, n := range out {
			got[i] = n.ID
		}
		return got
	}
	put := func(list []int64) (*http.Response, []byte) {
		return h.do(http.MethodPut, api+"/inbounds/order", map[string]any{"host_ids": list}, csrf)
	}

	before := list()
	if len(before) < 4 {
		t.Fatalf("every node has its servers: %+v", before)
	}
	for _, x := range before {
		if !x.Served || !x.Default || x.Name == "" {
			t.Fatalf("a default server of an inbound that is on: %+v", x)
		}
	}
	if got := nodesOf(before); !slices.Equal(got, []int64{1, nl.ID, us.ID}) {
		t.Fatalf("before any order the nodes go as before: %v", got)
	}
	if got := proxies(); !slices.Equal(got, names(before)) {
		t.Fatalf("the profile has the servers as the list:\n%q\n%q", got, names(before))
	}

	// The admin moves the last server (one of us) first, and the first one (the panel's own)
	// after the first one of nl.
	last, first := before[len(before)-1], before[0]
	order := []item{last}
	for _, x := range before[1 : len(before)-1] {
		order = append(order, x)
		if x.NodeID == nl.ID && !slices.ContainsFunc(order, func(y item) bool { return y.HostID == first.HostID }) {
			order = append(order, first)
		}
	}
	for _, bad := range []struct {
		name string
		ids  []int64
	}{
		{"missing", ids(order[1:])},
		{"extra", append(ids(order), 1<<40)},
		{"duplicate", append(ids(order), order[0].HostID)},
		{"duplicate with the right count", append(ids(order[1:]), order[1].HostID)},
	} {
		resp, body := put(bad.ids)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "host_order_mismatch") {
			t.Fatalf("%s: %d %s", bad.name, resp.StatusCode, body)
		}
	}
	if resp, body := put([]int64{}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty: %d %s", resp.StatusCode, body)
	}
	if got := ids(list()); !slices.Equal(got, ids(before)) {
		t.Fatalf("a refused order changed something: %v", got)
	}

	if resp, body := put(ids(order)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("order: %d %s", resp.StatusCode, body)
	}
	after := list()
	if !slices.Equal(ids(after), ids(order)) {
		t.Fatalf("the list follows the order: %v, want %v", ids(after), ids(order))
	}
	if got := proxies(); !slices.Equal(got, names(after)) {
		t.Fatalf("the profile follows the order:\n%q\n%q", got, names(after))
	}
	// The nodes go where their first servers are: us (its server is first), then the panel's
	// own, then nl.
	if got := nodeList(); !slices.Equal(got, []int64{us.ID, 1, nl.ID}) {
		t.Fatalf("nodes follow their first servers: %v", got)
	}

	// An order of the nodes moves their servers as blocks, a node's as they were.
	if resp, body := h.do(http.MethodPut, api+"/nodes/order", map[string]any{"ids": []int64{nl.ID, 1, us.ID}}, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("node order: %d %s", resp.StatusCode, body)
	}
	blocks := list()
	if got := nodesOf(blocks); !slices.Equal(got, []int64{nl.ID, 1, us.ID}) {
		t.Fatalf("servers in blocks of the nodes: %v", got)
	}
	for _, node := range []int64{1, nl.ID, us.ID} {
		of := func(items []item) []int64 {
			var out []int64
			for _, x := range items {
				if x.NodeID == node {
					out = append(out, x.HostID)
				}
			}
			return out
		}
		if !slices.Equal(of(blocks), of(after)) {
			t.Fatalf("node %d keeps its servers' order: %v, was %v", node, of(blocks), of(after))
		}
	}
	if got := proxies(); !slices.Equal(got, names(blocks)) {
		t.Fatalf("the profile follows the nodes:\n%q\n%q", got, names(blocks))
	}

	// A new address of nl's first protocol goes right after that protocol's servers.
	in := blocks[0].InboundID
	resp, body := h.do(http.MethodPost, api+"/inbounds/"+strconv.FormatInt(in, 10)+"/hosts", map[string]any{"address": "cdn.example.com", "port": 0, "sni": "", "host": "", "fingerprint": ""}, csrf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add host: %d %s", resp.StatusCode, body)
	}
	var added struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &added); err != nil || added.ID == 0 {
		t.Fatalf("added host: %s", body)
	}
	withHost := list()
	at := slices.IndexFunc(withHost, func(x item) bool { return x.HostID == added.ID })
	if at != 1 || withHost[at].Default || withHost[at].InboundID != in || !withHost[at].Served {
		t.Fatalf("the new address after its protocol's: %d %+v", at, withHost)
	}
	if got := proxies(); !slices.Equal(got, names(withHost)) {
		t.Fatalf("the profile has the new address in its place:\n%q\n%q", got, names(withHost))
	}

	// A protocol that is off: its servers keep their places, named as the admin knows them.
	off := withHost[len(withHost)-1]
	if resp, body := h.do(http.MethodPatch, api+"/inbounds/"+strconv.FormatInt(off.InboundID, 10), map[string]any{"enabled": false}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("turn off: %d %s", resp.StatusCode, body)
	}
	offList := list()
	if x := offList[len(offList)-1]; x.HostID != off.HostID || x.Served || x.Name == "" {
		t.Fatalf("a server that is off keeps its place: %+v", x)
	}

	// A key that only reads may look at the order, not change it.
	resp, body = h.do(http.MethodPost, api+"/api-keys", map[string]any{"name": "r", "scope": "read", "password": password}, csrf)
	var key struct {
		Key string `json:"key"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &key) != nil || key.Key == "" {
		t.Fatalf("read key: %d %s", resp.StatusCode, body)
	}
	// The key goes alone, without the session's cookie.
	asKey := func(method string, body any) (*http.Response, []byte) {
		jar := h.client.Jar
		h.client.Jar = nil
		defer func() { h.client.Jar = jar }()
		return h.do(method, api+"/inbounds/order", body, map[string]string{"Authorization": "Bearer " + key.Key})
	}
	if resp, body := asKey(http.MethodGet, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("read key, list: %d %s", resp.StatusCode, body)
	}
	if resp, body := asKey(http.MethodPut, map[string]any{"host_ids": ids(offList)}); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "read_only_key") {
		t.Fatalf("read key, order: %d %s", resp.StatusCode, body)
	}

	audit, err := h.st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 40, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, a := range audit {
		count[a.Action]++
	}
	if count["inbound.order"] != 1 || count["node.order"] != 1 {
		t.Fatalf("one audit entry for each order: %v", count)
	}
}
