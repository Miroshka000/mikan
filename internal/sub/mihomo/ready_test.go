package mihomo

import (
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/ready"
	"mikan/internal/sub/subtest"
)

// Every ready Clash template, and mikan's own made in each mode, is one the panel saves and
// whose profile the core loads, with the user's servers in its groups.
func TestReadyTemplates(t *testing.T) {
	prof := profileOf(subtest.Multi())
	for _, r := range ready.All() {
		if r.Core != model.CoreMihomo {
			continue
		}
		srcs := map[string]string{"": r.Body}
		if r.Starter {
			srcs = map[string]string{}
			for _, mode := range subtest.Modes {
				b, err := Starter(prof, model.GroupNames{}, mode)
				if err != nil {
					t.Fatal(err)
				}
				srcs[string(mode)] = string(b)
			}
		}
		for mode, src := range srcs {
			name := r.ID
			if mode != "" {
				name += "/" + mode
			}
			t.Run(name, func(t *testing.T) {
				if err := CheckTemplate(prof, src); err != nil {
					t.Fatal(err)
				}
				raw, in, err := Template(prof, src)
				if err != nil {
					t.Fatal(err)
				}
				if len(in) == 0 {
					t.Fatal("no servers in the profile")
				}
				v := readProfile(t, raw)
				for _, g := range v.Groups {
					if len(g.Proxies) == 0 || g.Proxies[0] == "REJECT" && len(g.Proxies) == 1 {
						t.Errorf("group %s has no servers: %v", g.Name, g.Proxies)
					}
				}
			})
		}
	}
}
