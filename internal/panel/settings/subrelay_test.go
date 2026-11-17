package settings

import (
	"testing"

	"mikan/internal/panel/store/storetest"
)

func TestWebhookEndpointPreservesLocalSubscriptionDomain(t *testing.T) {
	st, err := storetest.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := New(st.Q)
	for key, value := range map[string]any{KeyDomain: "panel.example", KeyPublicHost: "203.0.113.1", KeyPanelPort: 2053, KeySubPort: 2054, KeySubDomain: "sub.example"} {
		if err := Set(t.Context(), set, key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, external := range []bool{false, true} {
		if err := Set(t.Context(), set, KeySubExternal, external); err != nil {
			t.Fatal(err)
		}
		ep, err := set.PanelSubEndpoint(t.Context())
		want := "sub.example"
		if external {
			want = "panel.example"
		}
		if err != nil || ep.Host != want || ep.Port != 2054 {
			t.Fatalf("external=%v: %+v %v", external, ep, err)
		}
	}
}
func TestEndpointOmitsOnlyStandardHTTPSPort(t *testing.T) {
	for _, tc := range []struct {
		ep  Endpoint
		url string
	}{
		{Endpoint{Host: "sub.example", Port: 443}, "https://sub.example"},
		{Endpoint{Host: "2001:db8::1", Port: 443}, "https://[2001:db8::1]"},
		{Endpoint{Host: "sub.example", Port: 2053}, "https://sub.example:2053"},
	} {
		if got := tc.ep.URL(); got != tc.url {
			t.Errorf("%+v: %s", tc.ep, got)
		}
	}
}
