package xray

import (
	"strings"
	"testing"

	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The share links, which do not depend on the mode: one list for each user (subtest has
// the golden files' story).
func TestGoldenURIs(t *testing.T) {
	for _, v := range subtest.Variants {
		t.Run(v.Name, func(t *testing.T) {
			links, _, err := Links(v.User().Proxies())
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
	for _, v := range subtest.Variants {
		for _, mode := range subtest.Modes {
			t.Run(v.Name+"/"+string(mode), func(t *testing.T) {
				t.Parallel() // xray in Docker takes a while for each config
				goldenXray(t, v, mode, "", v.Name+"_"+string(mode)+".xray.json")
			})
		}
	}
}

// The xray JSON profile made of the admin's own Clash profile on the two-node users.
func TestGoldenXrayTemplate(t *testing.T) {
	for _, v := range subtest.Variants {
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			goldenXray(t, v, routing.All, subtest.OwnTemplate, v.Name+".template.xray.json")
		})
	}
}

func goldenXray(t *testing.T, v subtest.Variant, mode routing.Mode, src, file string) {
	t.Helper()
	raw, err := Render(profileOf(v.User()), v.Groups, mode, src)
	if err != nil {
		t.Fatal(err)
	}
	subtest.Golden(t, file, append(raw, '\n'))
	t.Run("xray", func(t *testing.T) { checkXray(t, raw) })
}
