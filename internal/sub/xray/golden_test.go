package xray

import (
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// The share links, which do not depend on the mode: one list for each user, of all its
// inbounds, since the variants' apps are mihomo's and not the ones the links go to
// (subtest has the golden files' story).
func TestGoldenURIs(t *testing.T) {
	for _, v := range subtest.CoreVariants {
		t.Run(v.Name, func(t *testing.T) {
			links, _, err := Links(v.Base().Proxies())
			if err != nil {
				t.Fatal(err)
			}
			subtest.Golden(t, v.Name+".uri.txt", []byte(links+"\n"))
		})
	}
}

// The xray JSON profile of every user in every mode, each of its configs checked by xray.
// It takes the admin's rules whole, as serve gives them: what xray cannot match is left
// out by the profile itself.
func TestGoldenXrayJSON(t *testing.T) {
	for _, v := range subtest.CoreVariants {
		for _, mode := range subtest.Modes {
			t.Run(v.Name+"/"+string(mode), func(t *testing.T) {
				t.Parallel() // xray in Docker takes a while for each config
				goldenXray(t, v.User(), v.Groups, mode, model.Template{}, v.Name+"_"+string(mode)+".xray.json")
			})
		}
	}
}

// The xray JSON profile of a user with an inbound of each kind the default presets leave
// out, in the mode All alone: the protocols xray has no word for are not in it.
func TestGoldenXrayEveryType(t *testing.T) {
	v := subtest.EveryTypeVariant
	goldenXray(t, v.User(), v.Groups, routing.All, model.Template{}, v.Name+"_"+string(routing.All)+".xray.json")
}

// The xray JSON profile made of the admin's own Clash profile on the two-node users.
func TestGoldenXrayTemplate(t *testing.T) {
	for _, v := range subtest.CoreVariants {
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			goldenXray(t, v.User(), v.Groups, routing.All, subtest.Clash(subtest.OwnTemplate), v.Name+".template.xray.json")
		})
	}
}

func goldenXray(t *testing.T, u subtest.User, groups model.GroupNames, mode routing.Mode, tpl model.Template, file string) {
	t.Helper()
	raw, err := Render(profileOf(u), groups, mode, tpl)
	if err != nil {
		t.Fatal(err)
	}
	subtest.Golden(t, file, append(raw, '\n'))
	t.Run("xray", func(t *testing.T) { corecheck.Xray(t, raw) })
}
