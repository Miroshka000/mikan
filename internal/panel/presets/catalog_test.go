package presets_test

import (
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
)

// A preset's catalog entry says what its generated template already says: its type, its
// network, whether everyone shares one key, and whether only Clash apps (mihomo, Stash) can
// use it, which the UI marks "Clash only". The entry is written by hand for the UI; this
// keeps the two from drifting apart.
func TestPresetsMatchTheirTemplates(t *testing.T) {
	others := []apps.App{{Family: apps.FamilyXray}, {Family: apps.FamilySingBox}, {Family: apps.FamilyOther}}
	for _, p := range presets.All {
		if p.ID == presets.Custom {
			continue
		}
		src, err := presets.NewConfig(p.ID, "")
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		tpl, err := proto.Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if tpl.Type() != p.Type || tpl.Network() != p.Network || proto.Shared(tpl) != p.Shared {
			t.Errorf("%s: catalog says %s/%s shared=%v, the template is %s/%s shared=%v", p.ID, p.Type, p.Network, p.Shared, tpl.Type(), tpl.Network(), proto.Shared(tpl))
		}
		needs := proto.NeedsOf(tpl)
		if !(apps.App{Family: apps.FamilyMihomo, Core: apps.Version{9, 9, 9}}).Supports(needs) {
			t.Errorf("%s: a current mihomo app cannot use it", p.ID)
		}
		clashOnly := true
		for _, a := range others {
			if a.Supports(needs) {
				clashOnly = false
			}
		}
		if clashOnly != (p.Apps == "mihomo") {
			t.Errorf("%s: catalog says apps=%q, but only Clash apps can use it: %v", p.ID, p.Apps, clashOnly)
		}
	}
}
