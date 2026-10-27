package mihomo

import (
	"strings"
	"testing"

	"mikan/internal/sub/subtest"
)

// profileOf is the mihomo profile of a test user: its proxies and the panel's settings
// around them.
func profileOf(u subtest.User) Profile {
	return Profile{Proxies: u.Proxies(), Nodes: u.Build.Nodes, Direct: u.Direct, Rules: u.Rules, Routes: u.Routes, Lang: u.Lang}
}

// goldenProfile is the variant's user in its app.
func goldenProfile(v subtest.Variant) Profile {
	p := profileOf(v.User())
	p.App, p.Rules = v.App, RulesFor(p.Rules, v.App)
	return p
}

// The mihomo profile of every user in every mode (subtest has the golden files' story).
func TestGoldenClash(t *testing.T) {
	for _, v := range subtest.Variants {
		for _, mode := range subtest.Modes {
			t.Run(v.Name+"/"+string(mode), func(t *testing.T) {
				raw, err := Render(goldenProfile(v), v.Groups.WithDefaults("en"), mode)
				if err != nil {
					t.Fatal(err)
				}
				coreAccepts(t, raw)
				subtest.Golden(t, v.Name+"_"+string(mode)+".clash.yaml", raw)
			})
		}
	}
}

// The admin's own Clash profile on the two-node user, and the starter the admin edits from.
func TestGoldenTemplate(t *testing.T) {
	for _, v := range subtest.Variants {
		prof := goldenProfile(v)
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		if !prof.Routable() {
			continue // serve gives the app the built-in profile
		}
		t.Run(v.Name, func(t *testing.T) {
			raw, _, err := Template(prof, subtest.OwnTemplate)
			if err != nil {
				t.Fatal(err)
			}
			coreAccepts(t, raw)
			subtest.Golden(t, v.Name+".template.clash.yaml", raw)
			for _, mode := range subtest.Modes {
				starter, err := Starter(prof, v.Groups, mode)
				if err != nil {
					t.Fatal(err)
				}
				subtest.Golden(t, v.Name+"_"+string(mode)+".starter.yaml", starter)
				// The starter is a template, its groups asking for nodes the core has no
				// word for: the profile it renders is what an app loads.
				own, _, err := Template(prof, string(starter))
				if err != nil {
					t.Fatal(err)
				}
				coreAccepts(t, own)
			}
		})
	}
}
