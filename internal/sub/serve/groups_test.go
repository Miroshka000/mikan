package serve

import (
	"reflect"
	"testing"

	"mikan/internal/sub/model"
)

// The handler names the servers of each group in the profile, and drops the groups left
// with nothing to show: one on the main traffic with no servers, one apart without a limit
// with no servers and nothing used. An own limit stays, with no servers as [], never null.
func TestAppGroupsNamed(t *testing.T) {
	g := AppGroups{V: 1, ResetsAt: 1700000000, Groups: []AppGroup{
		{ID: 1, Name: "Европа", Mode: GroupMain, State: "active", Proxies: []string{"stale"}},
		{ID: 2, Name: "Пустая", Mode: GroupMain, State: "active"},
		{ID: 3, Name: "Белые списки", Mode: GroupUnlimited, Used: 10, State: "active"},
		{ID: 4, Name: "Новая", Mode: GroupUnlimited, State: "active"},
		{ID: 5, Name: "Игры", Mode: GroupOwn, Used: 5, Limit: ptr(5), Left: ptr(0), State: "exhausted"},
		{ID: 6, Name: "Премиум +", Mode: GroupOwn, Limit: ptr(15), Left: ptr(15), State: "active"},
		{ID: 7, Name: "Видео", Mode: GroupUnlimited, State: "active"},
	}}
	ps := []model.Proxy{{Name: "🇳🇱 VLESS"}, {Name: "🇩🇪 Trojan", Pool: 1}, {Name: "🇺🇸 Reality", Pool: 6}, {Name: "🇺🇸 Hysteria2", Pool: 6},
		{Name: "🇷🇺 VLESS", Pool: 7}, {Name: "🇫🇮 VLESS", Pool: 99}}
	want := AppGroups{V: 1, ResetsAt: 1700000000, Groups: []AppGroup{
		{ID: 1, Name: "Европа", Mode: GroupMain, State: "active", Proxies: []string{"🇩🇪 Trojan"}},
		{ID: 3, Name: "Белые списки", Mode: GroupUnlimited, Used: 10, State: "active", Proxies: []string{}},
		{ID: 5, Name: "Игры", Mode: GroupOwn, Used: 5, Limit: ptr(5), Left: ptr(0), State: "exhausted", Proxies: []string{}},
		{ID: 6, Name: "Премиум +", Mode: GroupOwn, Limit: ptr(15), Left: ptr(15), State: "active", Proxies: []string{"🇺🇸 Reality", "🇺🇸 Hysteria2"}},
		{ID: 7, Name: "Видео", Mode: GroupUnlimited, State: "active", Proxies: []string{"🇷🇺 VLESS"}},
	}}
	if got := g.named(ps); !reflect.DeepEqual(got, want) {
		t.Fatalf("named:\n got %+v\nwant %+v", got, want)
	}
	// The groups are the source's: naming them leaves the source's own untouched.
	if g.Groups[0].Proxies[0] != "stale" || len(g.Groups) != 7 {
		t.Errorf("the source's groups changed: %+v", g.Groups)
	}
	// Nothing to show at all is no header.
	if v := (AppGroups{V: 1, Groups: []AppGroup{{ID: 2, Mode: GroupMain}}}).named(ps).header(); v != "" {
		t.Errorf("header of nothing: %q", v)
	}
}
