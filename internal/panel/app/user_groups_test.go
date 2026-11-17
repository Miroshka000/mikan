package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// Groups of clients over HTTP (#290): a group's servers and the addresses shown by group
// decide what its clients' subscriptions hold; a client's own group wins over its tariff's;
// a group that does not exist is refused; the users' list filters and counts by group; the
// bulk action moves clients between groups; a deleted group lets its clients and tariffs go
// and leaves an address that was only for it to nobody.
func TestUserGroupsOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	q := k.st.Q
	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	ins, _ := q.ListInbounds(ctx)
	local := ins[0]
	b, err := q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "203.0.113.20:25305", PublicHost: "203.0.113.20", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	onB, err := q.CreateInbound(ctx, db.CreateInboundParams{NodeID: b.ID, Name: "b-" + local.Name, Preset: local.Preset, Port: local.Port, Config: local.Config, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	std := tariffs[1]
	clock := func() time.Time { return k.now }
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "anna", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	proxies := func() []string {
		t.Helper()
		resp, body := k.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.31"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription: %d", resp.StatusCode)
		}
		var prof struct {
			Proxies []struct {
				Server string `json:"server"`
			} `json:"proxies"`
		}
		_ = unmarshalProfile(body, &prof)
		var out []string
		for _, p := range prof.Proxies {
			out = append(out, p.Server)
		}
		slices.Sort(out)
		return out
	}
	type groupView struct {
		ID       int64 `json:"id"`
		Users    int   `json:"users"`
		OwnUsers int   `json:"own_users"`
		Tariffs  int   `json:"tariffs"`
		Servers  struct {
			AsTariff bool    `json:"as_tariff"`
			Inbounds []int64 `json:"inbounds"`
		} `json:"servers"`
		Hosts []struct {
			ID   int64  `json:"id"`
			Mode string `json:"mode"`
		} `json:"hosts"`
	}
	mkGroup := func(body map[string]any) groupView {
		t.Helper()
		resp, raw := k.do(http.MethodPost, k.api+"/groups", body, k.csrf)
		var g groupView
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &g) != nil {
			t.Fatalf("create group %v: %d %s", body, resp.StatusCode, raw)
		}
		return g
	}
	groups := func() map[int64]groupView {
		t.Helper()
		resp, raw := k.do(http.MethodGet, k.api+"/groups", nil, nil)
		var list []groupView
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil {
			t.Fatalf("list groups: %d %s", resp.StatusCode, raw)
		}
		out := map[int64]groupView{}
		for _, g := range list {
			out[g.ID] = g
		}
		return out
	}
	patchUser := func(body map[string]any) (int, string) {
		t.Helper()
		resp, raw := k.do(http.MethodPatch, k.api+"/users/"+id(u.ID), body, k.csrf)
		return resp.StatusCode, string(raw)
	}
	// left is what the preview of the client's subscription says is left out of it, and why.
	type leftOut struct {
		InboundID int64  `json:"inbound_id"`
		HostID    int64  `json:"host_id"`
		Reason    string `json:"reason"`
	}
	type stand struct {
		GroupID *int64    `json:"group_id"`
		ByGroup bool      `json:"by_group"`
		Left    []leftOut `json:"left"`
	}
	preview := func() stand {
		t.Helper()
		resp, raw := k.do(http.MethodPost, k.api+"/users/"+id(u.ID)+"/sub-preview", map[string]any{"user_agent": "mihomo/1.19.31"}, k.csrf)
		var s stand
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &s) != nil {
			t.Fatalf("preview: %d %s", resp.StatusCode, raw)
		}
		return s
	}
	leftFor := func(s stand, inbound int64, reason string) bool {
		return slices.ContainsFunc(s.Left, func(l leftOut) bool { return l.InboundID == inbound && l.Reason == reason })
	}

	// The same checks as the other endpoints of users: a read key reads, it makes nothing.
	if resp, _ := k.asKey(k.read, http.MethodPost, "/groups", map[string]any{"name": "Nope"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a read key made a group: %d", resp.StatusCode)
	}
	cdn := mkGroup(map[string]any{"name": "CDN"})
	if !cdn.Servers.AsTariff || len(cdn.Servers.Inbounds) != 0 {
		t.Fatalf("a new group leaves the servers to the tariff: %+v", cdn)
	}
	onlyB := mkGroup(map[string]any{"name": "Only B", "color": "teal", "servers": map[string]any{"as_tariff": false, "inbounds": []int64{onB.ID}}})
	if onlyB.Servers.AsTariff || !slices.Equal(onlyB.Servers.Inbounds, []int64{onB.ID}) {
		t.Fatalf("a group with its own servers: %+v", onlyB)
	}
	if resp, raw := k.do(http.MethodPost, k.api+"/groups", map[string]any{"name": "cdn"}, k.csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "group_name_taken") {
		t.Fatalf("a name taken but for its case: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodPost, k.api+"/groups", map[string]any{"name": "Empty", "servers": map[string]any{"as_tariff": false}}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "servers_empty") {
		t.Fatalf("a group's choice of nothing: %d %s", resp.StatusCode, raw)
	}

	public := proxies()
	if len(public) < 2 || !slices.Contains(public, "203.0.113.20") {
		t.Fatalf("no group: every server of the tariff: %v", public)
	}
	// An address only for the CDN group: refused without a group, or with one there is not.
	hostsPath := k.api + "/inbounds/" + id(local.ID) + "/hosts"
	host := func(mode string, groups []int64) map[string]any {
		return map[string]any{"address": "cdn.example.com", "port": 0, "sni": "", "host": "", "fingerprint": "", "group_mode": mode, "groups": groups}
	}
	if resp, raw := k.do(http.MethodPost, hostsPath, host("only", nil), k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "host_groups_empty") {
		t.Fatalf("only for no group: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodPost, hostsPath, host("only", []int64{999999}), k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "group_not_found") {
		t.Fatalf("only for a group there is not: %d %s", resp.StatusCode, raw)
	}
	resp, raw := k.do(http.MethodPost, hostsPath, host("only", []int64{cdn.ID}), k.csrf)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(raw), `"group_mode":"only","groups":[`+id(cdn.ID)+`]`) {
		t.Fatalf("an address only for the CDN group: %d %s", resp.StatusCode, raw)
	}
	if got := proxies(); slices.Contains(got, "cdn.example.com") {
		t.Fatalf("a client in no group gets an address only for a group: %v", got)
	}
	if s := preview(); s.GroupID != nil || s.ByGroup || !slices.ContainsFunc(s.Left, func(l leftOut) bool { return l.InboundID == local.ID && l.HostID != 0 && l.Reason == "host_group" }) {
		t.Fatalf("the preview tells the address is for a group: %+v", s)
	}
	if g := groups()[cdn.ID]; len(g.Hosts) != 1 || g.Hosts[0].Mode != "only" {
		t.Fatalf("the group lists the address shown by it: %+v", g)
	}

	// The client's own group: the CDN address joins its servers.
	if code, raw := patchUser(map[string]any{"group_id": 999999}); code != http.StatusUnprocessableEntity || !strings.Contains(raw, "group_not_found") {
		t.Fatalf("a group there is not: %d %s", code, raw)
	}
	if code, raw := patchUser(map[string]any{"group_id": cdn.ID}); code != http.StatusOK ||
		!strings.Contains(raw, `"group_id":`+id(cdn.ID)) || !strings.Contains(raw, `"effective_group_id":`+id(cdn.ID)) {
		t.Fatalf("into the CDN group: %d %s", code, raw)
	}
	if got := proxies(); len(got) != len(public)+1 || !slices.Contains(got, "cdn.example.com") {
		t.Fatalf("the CDN group's client gets the address: %v", got)
	}
	// A group with servers of its own: only them.
	if code, raw := patchUser(map[string]any{"group_id": onlyB.ID}); code != http.StatusOK {
		t.Fatalf("into the B group: %d %s", code, raw)
	}
	if got := proxies(); !slices.Equal(got, []string{"203.0.113.20"}) {
		t.Fatalf("the group's servers alone: %v", got)
	}
	if s := preview(); s.GroupID == nil || *s.GroupID != onlyB.ID || !s.ByGroup || !leftFor(s, local.ID, "not_in_group") || leftFor(s, onB.ID, "not_in_group") {
		t.Fatalf("the preview tells the group's servers: %+v", s)
	}

	// The tariff's group reaches the client once its own is cleared.
	resp, raw = k.do(http.MethodPut, k.api+"/tariffs/"+id(std.ID), map[string]any{"name": std.Name, "duration_days": 30, "reset_strategy": "period", "group_id": cdn.ID}, k.csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"group_id":`+id(cdn.ID)) {
		t.Fatalf("the tariff's group: %d %s", resp.StatusCode, raw)
	}
	if code, raw := patchUser(map[string]any{"group_id": 0}); code != http.StatusOK || !strings.Contains(raw, `"group_id":null`) || !strings.Contains(raw, `"effective_group_id":`+id(cdn.ID)) {
		t.Fatalf("back to the tariff's group: %d %s", code, raw)
	}
	if got := proxies(); !slices.Contains(got, "cdn.example.com") {
		t.Fatalf("the tariff's group gives the address: %v", got)
	}
	if g := groups()[cdn.ID]; g.Users != 1 || g.OwnUsers != 0 || g.Tariffs != 1 {
		t.Fatalf("the group counts the tariff's clients: %+v", g)
	}

	// The list filters by the group a client is in, and counts every group.
	var list struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		GroupCounts map[string]int `json:"group_counts"`
	}
	resp, raw = k.do(http.MethodGet, k.api+"/users?group="+id(cdn.ID), nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 || list.Items[0].ID != u.ID ||
		list.GroupCounts[id(cdn.ID)] != 1 || list.GroupCounts[id(onlyB.ID)] != 0 {
		t.Fatalf("the list by group: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodGet, k.api+"/users?group=nope", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "bad_group") {
		t.Fatalf("a bad group filter: %d %s", resp.StatusCode, raw)
	}

	// The bulk action: previewed, then done.
	bulk := map[string]any{"ids": []int64{u.ID}, "action": "group", "group_id": onlyB.ID}
	var plan struct {
		Changed int `json:"changed"`
	}
	resp, raw = k.do(http.MethodPost, k.api+"/users/bulk/preview", bulk, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &plan) != nil || plan.Changed != 1 {
		t.Fatalf("the bulk preview: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodPost, k.api+"/users/bulk", bulk, k.csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"affected":1`) {
		t.Fatalf("the bulk action: %d %s", resp.StatusCode, raw)
	}
	if got := proxies(); !slices.Equal(got, []string{"203.0.113.20"}) {
		t.Fatalf("moved in bulk: %v", got)
	}

	// Deleting a group: its clients go back to their tariffs, its address to nobody.
	if resp, raw := k.do(http.MethodDelete, k.api+"/groups/"+id(onlyB.ID), nil, k.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete the B group: %d %s", resp.StatusCode, raw)
	}
	if got := proxies(); !slices.Contains(got, "cdn.example.com") || len(got) != len(public)+1 {
		t.Fatalf("back in the tariff's CDN group: %v", got)
	}
	if resp, raw := k.do(http.MethodDelete, k.api+"/groups/"+id(cdn.ID), nil, k.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete the CDN group: %d %s", resp.StatusCode, raw)
	}
	if got := proxies(); !slices.Equal(got, public) {
		t.Fatalf("no group left: the tariff's servers, the address to nobody: %v, want %v", got, public)
	}
	hosts, _ := q.ListInboundHosts(ctx, local.ID)
	if last := hosts[len(hosts)-1]; last.GroupMode != "only" || len(last.Groups) != 0 {
		t.Fatalf("the address only for a deleted group: %+v", last)
	}
	resp, raw = k.do(http.MethodGet, k.api+"/tariffs", nil, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `"group_id":`+id(cdn.ID)) {
		t.Fatalf("the tariff lets the deleted group go: %d %s", resp.StatusCode, raw)
	}
}
