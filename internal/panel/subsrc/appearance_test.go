package subsrc

import (
	"reflect"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// A group's appearance goes over the panel's config for its clients alone: the config every
// request shares is left as it was, and what the group leaves unset stays the panel's.
func TestForGroup(t *testing.T) {
	title, none, support, hours, on := "VIP {name}", "", "https://t.me/vip", 6, true
	base := Config{Config: serve.Config{Title: "Mikan", Announce: "news", AnnounceURL: "https://t.me/news", SupportURL: "https://t.me/all",
		Templates: []submodel.Template{
			{ID: 1, Core: submodel.CoreMihomo, Default: true},
			{ID: 2, Core: submodel.CoreMihomo},
			{ID: 3, Core: submodel.CoreXray, Default: true},
		}}}
	base.Appearance = map[int64]domain.GroupAppearance{
		7: {Templates: map[submodel.Core]int64{submodel.CoreMihomo: 2, submodel.CoreXray: 99}, Title: &title, Announce: &none, SupportURL: &support,
			UpdateHours: &hours, Headers: []apps.Header{{Name: "X-Group", Value: "vip"}}, RequireHWID: &on, Page: true},
		8: {Templates: map[submodel.Core]int64{}},
	}
	before := reflect.ValueOf(base.Templates).Pointer()
	got := base.forGroup(7)
	if got.Title != title || got.Announce != "" || got.AnnounceURL != "https://t.me/news" || got.SupportURL != support || got.UpdateHours != 6 ||
		len(got.Headers) != 1 || !got.RequireHWID || got.Page != 7 {
		t.Fatalf("the group's: %+v", got.Config)
	}
	// The group's Clash template is the core's default; a template gone since leaves Xray's.
	want := []bool{false, true, true}
	for i, x := range got.Templates {
		if x.Default != want[i] {
			t.Errorf("template %d default %v, want %v", x.ID, x.Default, want[i])
		}
	}
	if !base.Templates[0].Default || base.Templates[1].Default || reflect.ValueOf(base.Templates).Pointer() != before {
		t.Errorf("the shared config changed: %+v", base.Templates)
	}
	for _, g := range []int64{0, 8, 9} {
		if c := base.forGroup(g); !reflect.DeepEqual(c.Config, base.Config) {
			t.Errorf("group %d: %+v", g, c.Config)
		}
	}
}

// A group's address is found by group and inbound; group 0, and another group, get none.
func TestAddressOf(t *testing.T) {
	c := Config{Addresses: map[int64]map[int64]submodel.AddressOverride{7: {3: {Address: "edge.example.com"}}}}
	if a := c.addressOf(7, 3); a.Address != "edge.example.com" {
		t.Errorf("the group's: %+v", a)
	}
	for _, x := range [][2]int64{{7, 4}, {8, 3}, {0, 3}} {
		if a := c.addressOf(x[0], x[1]); a != (submodel.AddressOverride{}) {
			t.Errorf("%v: %+v", x, a)
		}
	}
}
