package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An addon keeps up to 256 MiB unless the admin sets another bound, or none on purpose.
func TestAddonStorageLimit(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/addon-storage"
	type view struct {
		Unlimited bool  `json:"unlimited"`
		LimitMiB  int64 `json:"limit_mib"`
	}
	read := func(resp *http.Response, body []byte) view {
		t.Helper()
		var v view
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil {
			t.Fatalf("storage: %d %s", resp.StatusCode, body)
		}
		return v
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); v.Unlimited || v.LimitMiB != 256 {
		t.Fatalf("default: %+v", v)
	}
	if v := read(h.do(http.MethodPut, api, map[string]any{"limit_mib": 1024}, csrf)); v.Unlimited || v.LimitMiB != 1024 {
		t.Fatalf("1 GiB: %+v", v)
	}
	if resp, body := h.do(http.MethodPut, api, map[string]any{"unlimited": false}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no limit and not unlimited: %d %s", resp.StatusCode, body)
	}
	if v := read(h.do(http.MethodPut, api, map[string]any{"unlimited": true}, csrf)); !v.Unlimited {
		t.Fatalf("unlimited: %+v", v)
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); !v.Unlimited {
		t.Fatalf("unlimited after reading back: %+v", v)
	}
}
