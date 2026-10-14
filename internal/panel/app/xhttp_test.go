package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The masking tab over the API: the inbound shows its XHTTP tuning, a save writes it into
// the template, a bad value names its setting, and other transports have no such field.
func TestXHTTPTuningOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ins, err := k.st.Q.ListInbounds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, in := range ins {
		ids[in.Name] = in.ID
	}
	type view struct {
		ID     int64          `json:"id"`
		Config string         `json:"config"`
		XHTTP  map[string]any `json:"xhttp"`
	}
	get := func(name string) view {
		t.Helper()
		resp, raw := k.do(http.MethodGet, k.api+"/inbounds", nil, nil)
		var list []view
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil {
			t.Fatalf("list: %d %s", resp.StatusCode, raw)
		}
		for _, v := range list {
			if v.ID == ids[name] {
				return v
			}
		}
		t.Fatalf("no %s", name)
		return view{}
	}
	if v := get("vless-xhttp"); v.XHTTP == nil || v.XHTTP["x_padding_bytes"] != "" {
		t.Fatalf("an XHTTP inbound shows its tuning: %+v", v.XHTTP)
	}
	if v := get("vless-vision"); v.XHTTP != nil {
		t.Fatalf("Vision has no XHTTP: %+v", v.XHTTP)
	}

	tune := map[string]any{"x_padding_bytes": "500-3000", "x_padding_obfs_mode": true, "x_padding_placement": "cookie", "x_padding_key": "sid",
		"uplink_http_method": "PUT", "xmux_max_connections": "2-4"}
	resp, raw := k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-xhttp"]), map[string]any{"xhttp": tune}, k.csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %s", resp.StatusCode, raw)
	}
	v := get("vless-xhttp")
	if v.XHTTP["x_padding_bytes"] != "500-3000" || v.XHTTP["uplink_http_method"] != "PUT" || v.XHTTP["xmux_max_connections"] != "2-4" ||
		!strings.Contains(v.Config, "x-padding-obfs-mode: true") || !strings.Contains(v.Config, "max-connections: 2-4") {
		t.Fatalf("saved: %+v\n%s", v.XHTTP, v.Config)
	}

	resp, raw = k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-xhttp"]), map[string]any{"xhttp": map[string]any{"x_padding_bytes": "3000-500"}}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "body.xhttp") || !strings.Contains(string(raw), "xhttp_range") || !strings.Contains(string(raw), "x-padding-bytes") {
		t.Fatalf("a bad range: %d %s", resp.StatusCode, raw)
	}
	// The new settings name themselves: GET is not an upload method of the node.
	for key, bad := range map[string]map[string]any{
		"uplink_http_method":    {"uplink_http_method": "GET"},
		"session_placement":     {"session_placement": "body"},
		"uplink-data-key":       {"mode": "packet-up", "uplink_data_placement": "header"},
		"sc-max-buffered-posts": {"sc_max_buffered_posts": "0"},
		"session-table":         {"session_table": "абвгд"},
	} {
		resp, raw = k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-xhttp"]), map[string]any{"xhttp": bad}, k.csrf)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), key) {
			t.Fatalf("%s: %d %s", key, resp.StatusCode, raw)
		}
	}
	resp, raw = k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-xhttp"]), map[string]any{"xhttp": map[string]any{"mode": "packet-up", "session_placement": "cookie",
		"uplink_data_placement": "header", "uplink_data_key": "X-D", "sc_max_buffered_posts": "40", "no_sse_header": true, "session_table": "hex", "session_length": "20"}}, k.csrf)
	if v := get("vless-xhttp"); resp.StatusCode != http.StatusOK || v.XHTTP["session_placement"] != "cookie" || v.XHTTP["no_sse_header"] != true ||
		v.XHTTP["session_table"] != "hex" || !strings.Contains(v.Config, "sc-max-buffered-posts: \"40\"") && !strings.Contains(v.Config, "sc-max-buffered-posts: 40") {
		t.Fatalf("session settings: %d %s\n%s", resp.StatusCode, raw, v.Config)
	}
	resp, raw = k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-vision"]), map[string]any{"xhttp": tune}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "xhttp_no_xhttp") {
		t.Fatalf("not XHTTP: %d %s", resp.StatusCode, raw)
	}

	// Empty fields go back to the defaults: the template is as before.
	resp, raw = k.do(http.MethodPatch, k.api+"/inbounds/"+idOf(ids["vless-xhttp"]), map[string]any{"xhttp": map[string]any{}}, k.csrf)
	if v := get("vless-xhttp"); resp.StatusCode != http.StatusOK || strings.Contains(v.Config, "x-padding") || strings.Contains(v.Config, "max-connections") {
		t.Fatalf("defaults again: %d %s\n%s", resp.StatusCode, raw, v.Config)
	}
}
