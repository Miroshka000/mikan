package mihomo

import (
	"strconv"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	_ "github.com/metacubex/mihomo/hub/executor" // provides config's go:linkname temporaryUpdateGeneral
	"github.com/metacubex/mihomo/log"
)

// coreAccepts runs the profile through the core's own parser, the one an app loads it with:
// proxies, groups, rule-providers, rules and DNS are each built, so a missing field, a
// wrong type or a group, rule or policy naming nothing fails here. Geodata is a download,
// so a GEOSITE, GEOIP or IP-ASN rule is rewritten to a plain DOMAIN / IP-CIDR rule with the
// same target and flags, and a geosite: DNS policy to a plain domain; the core's home goes
// to a temporary directory and the geodata URLs to a closed port, so nothing the parser
// still reaches for leaves the test. Rule-providers are built, not fetched. Not safe for
// t.Parallel: it sets the core's process-wide home dir and log level.
func coreAccepts(t *testing.T, raw []byte) {
	t.Helper()
	rc, err := config.UnmarshalRawConfig(raw)
	if err != nil {
		t.Fatalf("mihomo refuses the profile: %v\n%s", err, raw)
	}
	C.SetHomeDir(t.TempDir())
	log.SetLevel(log.SILENT)
	const nowhere = "http://127.0.0.1:1/"
	rc.GeoXUrl.GeoIp, rc.GeoXUrl.Mmdb, rc.GeoXUrl.ASN, rc.GeoXUrl.GeoSite = nowhere, nowhere, nowhere, nowhere
	for i, r := range rc.Rule {
		typ, rest, _ := strings.Cut(r, ",")
		_, rest, _ = strings.Cut(rest, ",")
		switch strings.ToUpper(typ) {
		case "GEOSITE":
			rc.Rule[i] = "DOMAIN,geo.invalid," + rest
		case "GEOIP", "IP-ASN":
			rc.Rule[i] = "IP-CIDR,192.0.2.0/24," + rest
		}
	}
	if p := rc.DNS.NameServerPolicy; p != nil {
		var geo []string
		for kv := p.Oldest(); kv != nil; kv = kv.Next() {
			if strings.HasPrefix(strings.ToLower(kv.Key), "geosite:") {
				geo = append(geo, kv.Key)
			}
		}
		for i, k := range geo {
			v, _ := p.Get(k)
			p.Delete(k)
			p.Set("+.geo"+strconv.Itoa(i)+".invalid", v)
		}
	}
	// A deliberate guard: a dns fallback would load geodata through these.
	rc.DNS.FallbackFilter.GeoIP, rc.DNS.FallbackFilter.GeoSite = false, nil
	if _, err := config.ParseRawConfig(rc); err != nil {
		t.Fatalf("mihomo refuses the profile: %v\n%s", err, raw)
	}
}
