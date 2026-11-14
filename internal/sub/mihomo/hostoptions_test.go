package mihomo

import (
	"strings"
	"testing"

	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// An address's own fields reach the profile as mihomo names them, and the core loads it.
func TestHostOptions(t *testing.T) {
	p := profileOf(subtest.WithHostOptions(subtest.EveryType()))
	raw, err := Render(p, subtest.EveryTypeVariant.Groups.WithDefaults("en"), routing.All)
	if err != nil {
		t.Fatal(err)
	}
	coreAccepts(t, raw)
	for _, want := range []string{"ip-version: ipv6-prefer", "protocol: h2mux", "- http/1.1"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("no %q in the profile:\n%s", want, raw)
		}
	}
}
