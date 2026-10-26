package serve

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// happRouting reads the lists of a Happ routing link.
type happRouting struct {
	Name        string   `json:"Name"`
	GlobalProxy string   `json:"GlobalProxy"`
	DirectIP    []string `json:"DirectIp"`
	DirectSites []string `json:"DirectSites"`
	ProxySites  []string `json:"ProxySites"`
	BlockSites  []string `json:"BlockSites"`
}

func decodeHapp(t *testing.T, link string) happRouting {
	t.Helper()
	payload, ok := strings.CutPrefix(link, "happ://routing/onadd/")
	if !ok {
		t.Fatalf("not a routing link: %s", link)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var p happRouting
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The rules come from the profile the settings make: the simple ones, or an own profile.
func TestHappAutoRouting(t *testing.T) {
	cfg := Config{Brand: "Mikan", Direct: []string{"203.0.113.7"}, Mode: routing.Blocked,
		Routes: routing.Routes{Services: map[string]string{"youtube": routing.TargetVPN, "ads": routing.TargetBlock, "ai": "node:2"}},
		Nodes:  []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды"}, {ID: 2, Name: "🇺🇸 США"}}}
	p := decodeHapp(t, happAutoRouting(cfg, ""))
	if p.Name != "Mikan" || p.GlobalProxy != "false" || !slices.Contains(p.ProxySites, "geosite:youtube") || !slices.Contains(p.ProxySites, "geosite:category-ai-!cn") ||
		!slices.Contains(p.ProxySites, "geosite:ru-blocked") || !slices.Contains(p.BlockSites, "geosite:category-ads-all") || !slices.Contains(p.DirectIP, "203.0.113.7/32") {
		t.Fatalf("simple: %+v", p)
	}
	own := "proxy-groups:\n  - {name: Net, type: select, include-all-proxies: true}\nrules:\n  - DOMAIN-SUFFIX,mine.example,DIRECT\n  - MATCH,Net\n"
	p = decodeHapp(t, happAutoRouting(cfg, own))
	if p.GlobalProxy != "true" || !slices.Contains(p.DirectSites, "domain:mine.example") || slices.Contains(p.ProxySites, "geosite:youtube") {
		t.Fatalf("own profile: %+v", p)
	}
	if p := decodeHapp(t, happAutoRouting(Config{}, "")); p.Name != "VPN" {
		t.Fatalf("no brand: %+v", p)
	}
}
