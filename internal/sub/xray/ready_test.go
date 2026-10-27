package xray

import (
	"testing"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/ready"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// Every ready template xray apps get configs of, each config checked by xray: the ready
// xray ones as own templates, the Clash ones (mikan's own made from the panel's settings
// among them) through the conversion.
func TestReadyTemplates(t *testing.T) {
	prof := profileOf(subtest.Multi())
	g := model.GroupNames{}.WithDefaults(prof.Lang)
	for _, r := range ready.All() {
		src := r.Body
		switch {
		case r.Core == model.CoreSingBox:
			continue
		case r.Starter && r.Core == model.CoreXray:
			src = string(Example(g))
		case r.Starter:
			b, err := mihomo.Starter(prof, g, routing.Blocked)
			if err != nil {
				t.Fatal(err)
			}
			src = string(b)
		}
		t.Run(r.ID, func(t *testing.T) {
			t.Parallel() // xray in Docker takes a while for each config
			if r.Core == model.CoreXray {
				if err := CheckTemplate(prof, src); err != nil {
					t.Fatal(err)
				}
			}
			cfgs := render(t, prof, routing.Blocked, model.Template{Core: r.Core, Body: src})
			servers := 0 // the outbounds of the user's servers, whatever their tags
			for _, c := range cfgs {
				for _, o := range c.Outbounds {
					switch o["protocol"] {
					case "freedom", "blackhole", "dns":
					default:
						servers++
					}
				}
			}
			if len(cfgs) == 0 || servers == 0 {
				t.Errorf("no servers in the configs: %v", cfgs)
			}
		})
	}
}
