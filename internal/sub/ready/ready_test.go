package ready

import (
	"regexp"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// The cores' own test packages render each ready template and have the core check it
// (mihomo, singbox and xray, ready_test.go); this one checks the catalog.
func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	starters := map[model.Core]int{}
	for _, r := range All() {
		if r.ID == "" || seen[r.ID] {
			t.Errorf("id %q empty or twice", r.ID)
		}
		seen[r.ID] = true
		if got, ok := Get(r.ID); !ok || got.ID != r.ID {
			t.Errorf("Get(%q) = %v, %v", r.ID, got.ID, ok)
		}
		switch r.Core {
		case model.CoreMihomo, model.CoreSingBox, model.CoreXray:
		default:
			t.Errorf("%s: core %q", r.ID, r.Core)
		}
		if r.Name == "" || r.Summary == "" || r.Author == "" || r.License == "" || !strings.HasPrefix(r.Source, "https://") {
			t.Errorf("%s: name, summary, author, license and source are all needed: %+v", r.ID, r)
		}
		if r.Starter {
			starters[r.Core]++
			if r.Body != "" || r.Author != mikanAuthor {
				t.Errorf("%s: a starter is mikan's own and has no text of its own", r.ID)
			}
		} else if strings.TrimSpace(r.Body) == "" {
			t.Errorf("%s: no text", r.ID)
		}
	}
	for _, c := range []model.Core{model.CoreMihomo, model.CoreSingBox, model.CoreXray} {
		if starters[c] != 1 {
			t.Errorf("core %s has %d of mikan's own, want 1", c, starters[c])
		}
	}
	if _, ok := Get("nope"); ok {
		t.Error("Get of an unknown id")
	}
}

// Every list a ready template links is pinned to a commit: privWL-clash's to the one the
// panel's own routing links (routing.BlockedLists), Davoyan's to one commit for them all. A
// push to the list's repository must not reroute the apps.
func TestListsPinned(t *testing.T) {
	pin, _, _ := strings.Cut(strings.TrimPrefix(routing.BlockedLists[0].URL, "https://raw.githubusercontent.com/Nemu-x/privWL-clash/"), "/")
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	if !sha.MatchString(pin) {
		t.Fatalf("routing's privWL pin %q", pin)
	}
	const davoyan = "https://raw.githubusercontent.com/Davoyan/mihomo-rule-sets/"
	davoyanPin := ""
	url := regexp.MustCompile(`url: (\S+)`)
	for _, r := range All() {
		for _, m := range url.FindAllStringSubmatch(r.Body, -1) {
			u := m[1]
			switch {
			case strings.Contains(u, "gstatic.com/generate_204"):
			case strings.HasPrefix(u, davoyan):
				p, _, _ := strings.Cut(strings.TrimPrefix(u, davoyan), "/")
				if davoyanPin == "" {
					davoyanPin = p
				}
				if !sha.MatchString(p) || p != davoyanPin || r.Source != davoyanSource {
					t.Errorf("%s: %s is not Davoyan's lists at one commit, or the source is not his", r.ID, u)
				}
			case !strings.HasPrefix(u, "https://raw.githubusercontent.com/Nemu-x/privWL-clash/"+pin+"/"):
				t.Errorf("%s: %s is not privWL-clash at %s", r.ID, u, pin)
			}
		}
	}
}
