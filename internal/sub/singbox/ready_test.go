package singbox

import (
	"context"
	"encoding/json"
	"testing"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/ready"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// Every ready template sing-box apps get a config of, checked by sing-box: the ready
// sing-box ones as own templates, the Clash ones (mikan's own made from the panel's
// settings among them) through the conversion.
func TestReadyTemplates(t *testing.T) {
	lists(t)
	prof := profileOf(subtest.Multi())
	g := model.GroupNames{}.WithDefaults(prof.Lang)
	for _, r := range ready.All() {
		src := r.Body
		switch {
		case r.Core == model.CoreXray:
			continue
		case r.Starter && r.Core == model.CoreSingBox:
			src = string(Example(g))
		case r.Starter:
			b, err := mihomo.Starter(prof, g, routing.Blocked)
			if err != nil {
				t.Fatal(err)
			}
			src = string(b)
		}
		t.Run(r.ID, func(t *testing.T) {
			if r.Core == model.CoreSingBox {
				if err := CheckTemplate(prof, src); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := Render(context.Background(), prof, g, routing.Blocked, model.Template{Core: r.Core, Body: src})
			if err != nil {
				t.Fatal(err)
			}
			var c sbConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			servers := 0
			for _, x := range prof.Proxies {
				if outbound(c, x.Name) != nil {
					servers++
				}
			}
			if servers == 0 {
				t.Errorf("no servers in the config:\n%s", raw)
			}
			corecheck.SingBox(t, raw)
		})
	}
}
