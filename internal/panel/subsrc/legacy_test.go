package subsrc

import (
	"testing"

	"mikan/internal/sub/serve"
)

// The client types of the old panels' links are mikan's formats: the JSON ones are the xray
// JSON profile, a type mikan has no format for is left to the User-Agent.
func TestLegacyFormat(t *testing.T) {
	for typ, want := range map[string]string{
		"clash": serve.FormatClash, "Clash-Meta": serve.FormatClash, "v2ray": serve.FormatURI, "links": serve.FormatURI,
		"v2ray-json": serve.FormatXray, "xray-json": serve.FormatXray, "json": serve.FormatXray,
		"sing-box": "", "outline": "",
	} {
		if got := legacyFormat(typ); got != want {
			t.Errorf("legacyFormat(%q) = %q, want %q", typ, got, want)
		}
	}
}
