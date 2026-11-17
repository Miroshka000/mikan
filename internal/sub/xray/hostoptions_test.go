package xray

import (
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// An address's IP version reaches the configs as xray's domainStrategy of the dial, and
// its ALPN the TLS of the protocols on the node's certificate; xray checks them.
func TestHostOptions(t *testing.T) {
	p := profileOf(subtest.WithHostOptions(subtest.EveryType()))
	raw, err := Render(p, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"UseIPv6v4"`) {
		t.Errorf("no IP version in the configs:\n%s", raw)
	}
	render(t, p, routing.All, model.Template{})
}
