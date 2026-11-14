package singbox

import (
	"context"
	"strings"
	"testing"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// goldenProfile is the variant's user in its app: the admin's rules the app's core takes.
func goldenProfile(v subtest.Variant) profile.Profile {
	p := profileOf(v.User())
	p.App, p.Rules = v.App, mihomo.RulesFor(p.Rules, v.App)
	return p
}

// The sing-box config of every user in every mode, with the lists it converts as small as
// the tests' stand-in for GitHub makes them (subtest has the golden files' story).
func TestGoldenSingBox(t *testing.T) {
	lists(t)
	for _, v := range subtest.CoreVariants {
		for _, mode := range subtest.Modes {
			t.Run(v.Name+"/"+string(mode), func(t *testing.T) {
				raw, err := Render(context.Background(), goldenProfile(v), v.Groups, mode, model.Template{})
				if err != nil {
					t.Fatal(err)
				}
				subtest.Golden(t, v.Name+"_"+string(mode)+".singbox.json", raw)
				t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, raw) })
			})
		}
	}
}

// The sing-box config of a user with an inbound of each kind the default presets leave out,
// in the mode All alone: the protocols sing-box has no word for are not in it.
func TestGoldenEveryType(t *testing.T) {
	lists(t)
	v := subtest.EveryTypeVariant
	raw, err := Render(context.Background(), goldenProfile(v), v.Groups, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	subtest.Golden(t, v.Name+"_"+string(routing.All)+".singbox.json", raw)
	t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, raw) })
}

// The sing-box config made of the admin's own Clash profile on the two-node user.
func TestGoldenTemplate(t *testing.T) {
	lists(t)
	for _, v := range subtest.CoreVariants {
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.Name, func(t *testing.T) {
			sb, err := Render(context.Background(), goldenProfile(v), v.Groups, routing.All, subtest.Clash(subtest.OwnTemplate))
			if err != nil {
				t.Fatal(err)
			}
			subtest.Golden(t, v.Name+".template.singbox.json", sb)
			t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, sb) })
		})
	}
}
