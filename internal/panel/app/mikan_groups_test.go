package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/sub/serve"
)

// The X-Mikan-Groups of TestMikanGroupsHeader with the group active and exhausted, taken
// from the panel before Source.Groups stopped taking the profile's proxies: the one group
// "Премиум +" (own, 15 GiB) with 3 GiB used and its server "VLESS XHTTP", then all used and
// "proxies":[].
const (
	activeHeader    = "base64:eyJ2IjoxLCJyZXNldHNfYXQiOjE3OTU1MjE2MDAsImdyb3VwcyI6W3siaWQiOjEsIm5hbWUiOiLQn9GA0LXQvNC40YPQvCArIiwibW9kZSI6Im93biIsInVzZWQiOjMyMjEyMjU0NzIsImxpbWl0IjoxNjEwNjEyNzM2MCwibGVmdCI6MTI4ODQ5MDE4ODgsInN0YXRlIjoiYWN0aXZlIiwicHJveGllcyI6WyJWTEVTUyBYSFRUUCJdfV19"
	exhaustedHeader = "base64:eyJ2IjoxLCJyZXNldHNfYXQiOjE3OTU1MjE2MDAsImdyb3VwcyI6W3siaWQiOjEsIm5hbWUiOiLQn9GA0LXQvNC40YPQvCArIiwibW9kZSI6Im93biIsInVzZWQiOjE2MTA2MTI3MzYwLCJsaW1pdCI6MTYxMDYxMjczNjAsImxlZnQiOjAsInN0YXRlIjoiZXhoYXVzdGVkIiwicHJveGllcyI6W119XX0="
)

// The Mikan app's server groups (X-Mikan-Groups): only the app that asks gets them, with the
// group's servers named as they are in the very profile it got; every other app gets the
// same subscription as before, and a group that ran out says so with its servers gone.
func TestMikanGroupsHeader(t *testing.T) {
	h := newHarness(t)
	// A fixed clock: the header's resets_at is of it, and the header is checked byte for byte.
	h.setNow(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
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
	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	var pool struct {
		ID int64 `json:"id"`
	}
	resp, body := h.do(http.MethodPost, api+"/pools", map[string]any{"name": "Премиум +"}, csrf)
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &pool) != nil {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	ins, _ := h.st.Q.ListInbounds(ctx)
	if resp, body := h.do(http.MethodPatch, api+"/inbounds/"+id(ins[0].ID), map[string]any{"pool_id": pool.ID}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("inbound into the group: %d %s", resp.StatusCode, body)
	}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	std := tariffs[1]
	const gb = int64(1) << 30
	if resp, body := h.do(http.MethodPut, api+"/tariffs/"+id(std.ID), map[string]any{"name": std.Name, "duration_days": 30, "reset_strategy": "period",
		"pools": []map[string]any{{"pool_id": pool.ID, "mode": "own", "traffic_limit": 15 * gb}}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("tariff groups: %d %s", resp.StatusCode, body)
	}
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE user_pools SET used_down = $2 WHERE user_id = $1", u.ID, 3*gb); err != nil {
		t.Fatal(err)
	}

	fetchAs := func(query, features string) (http.Header, []byte) {
		t.Helper()
		hd := map[string]string{"User-Agent": "mihomo/1.19.32 Mikan/1.2.0"}
		if features != "" {
			hd[serve.FeaturesHeader] = features
		}
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+query, nil, hd)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("subscription: %d %s", resp.StatusCode, body)
		}
		return resp.Header, body
	}
	fetch := func(features string) (http.Header, []byte) { return fetchAs("", features) }
	groups := func(hd http.Header) serve.AppGroups {
		t.Helper()
		raw, ok := strings.CutPrefix(hd.Get(serve.GroupsHeader), "base64:")
		b, err := base64.StdEncoding.DecodeString(raw)
		var g serve.AppGroups
		if !ok || err != nil || json.Unmarshal(b, &g) != nil {
			t.Fatalf("X-Mikan-Groups: %q", hd.Get(serve.GroupsHeader))
		}
		return g
	}
	proxyNames := func(body []byte) []string {
		t.Helper()
		var prof struct {
			Proxies []struct {
				Name string `json:"name"`
			} `json:"proxies"`
		}
		if err := unmarshalProfile(body, &prof); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range prof.Proxies {
			out = append(out, p.Name)
		}
		return out
	}

	plainHd, plain := fetch("")
	if v := plainHd.Get(serve.GroupsHeader); v != "" {
		t.Fatalf("an app that did not ask got the groups: %q", v)
	}
	hd, body := fetch("groups")
	if !bytes.Equal(plain, body) {
		t.Fatal("the profile differs for the app that asks for the groups")
	}
	// The header byte for byte as the panel sent it before the source stopped naming servers.
	if v := hd.Get(serve.GroupsHeader); v != activeHeader {
		t.Fatalf("X-Mikan-Groups:\n got %s\nwant %s", v, activeHeader)
	}
	g := groups(hd)
	if g.V != 1 || len(g.Groups) != 1 {
		t.Fatalf("groups: %+v", g)
	}
	got := g.Groups[0]
	if got.ID != pool.ID || got.Name != "Премиум +" || got.Mode != "own" || got.Used != 3*gb || got.Limit == nil || *got.Limit != 15*gb ||
		got.Left == nil || *got.Left != 12*gb || got.State != "active" || len(got.Proxies) == 0 {
		t.Fatalf("group: %+v", got)
	}
	names := proxyNames(body)
	for _, n := range got.Proxies {
		if !slices.Contains(names, n) {
			t.Fatalf("%q is no server of the profile %q", n, names)
		}
	}
	if g.ResetsAt == 0 {
		t.Fatal("a plan with a period resets: resets_at is missing")
	}
	// The share links (a profile updated by hand to the links' format) carry them too.
	if hd, _ := fetchAs("?format=uri", "groups"); groups(hd).Groups[0].ID != pool.ID {
		t.Fatal("no groups with the share links")
	}

	// The group runs out: its servers leave the profile, and the app is told why.
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE user_pools SET used_down = $2 WHERE user_id = $1", u.ID, 15*gb); err != nil {
		t.Fatal(err)
	}
	hd, body = fetch("groups")
	if v := hd.Get(serve.GroupsHeader); v != exhaustedHeader {
		t.Fatalf("X-Mikan-Groups of the exhausted group:\n got %s\nwant %s", v, exhaustedHeader)
	}
	got = groups(hd).Groups[0]
	if got.State != "exhausted" || got.Left == nil || *got.Left != 0 || got.Proxies == nil || len(got.Proxies) != 0 {
		t.Fatalf("exhausted group: %+v", got)
	}
	if len(proxyNames(body)) != len(names)-len(g.Groups[0].Proxies) {
		t.Fatalf("the exhausted group's servers are still in the profile: %q", proxyNames(body))
	}
}
