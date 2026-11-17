package app

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
)

// Nodes and inbounds take labels as a user does: on creation and on a change, trimmed,
// the empty ones dropped, at most 20; the lists filter by one of them in any letter case.
func TestNodeAndInboundTags(t *testing.T) {
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	k := newKeyHarness(t, func(o *Options) { o.PanelCert = func() (nodetls.Pair, error) { return panel, nil } })
	type item struct {
		ID   int64    `json:"id"`
		Tags []string `json:"tags"`
	}
	ids := func(path string) []int64 {
		t.Helper()
		resp, raw := k.do(http.MethodGet, k.api+path, nil, nil)
		var list []item
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
		}
		out := []int64{}
		for _, i := range list {
			out = append(out, i.ID)
		}
		return out
	}

	// Nodes.
	resp, raw := k.do(http.MethodPost, k.api+"/nodes", map[string]any{"name": "B", "host": "198.51.100.20", "api_port": 40000, "tags": []string{" Germany ", "", "hetzner"}}, k.csrf)
	var created struct {
		Node item `json:"node"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &created) != nil {
		t.Fatalf("create node: %d %s", resp.StatusCode, raw)
	}
	b := created.Node
	if !slices.Equal(b.Tags, []string{"Germany", "hetzner"}) {
		t.Fatalf("created node's tags: %q", b.Tags)
	}
	all := ids("/nodes")
	if len(all) != 2 {
		t.Fatalf("nodes: %v", all)
	}
	if got := ids("/nodes?tag=germany"); !slices.Equal(got, []int64{b.ID}) {
		t.Fatalf("?tag=germany: %v", got)
	}
	if got := ids("/nodes?tag=nobody"); len(got) != 0 {
		t.Fatalf("?tag=nobody: %v", got)
	}
	bPath := "/nodes/" + strconv.FormatInt(b.ID, 10)
	resp, raw = k.do(http.MethodPatch, k.api+bPath, map[string]any{"tags": []string{"whitelist"}}, k.csrf)
	var patched item
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &patched) != nil || !slices.Equal(patched.Tags, []string{"whitelist"}) {
		t.Fatalf("patch node: %d %s", resp.StatusCode, raw)
	}
	if got := ids("/nodes?tag=germany"); len(got) != 0 {
		t.Fatalf("a replaced tag still filters: %v", got)
	}
	// A change of something else keeps the tags.
	resp, raw = k.do(http.MethodPatch, k.api+bPath, map[string]any{"public_name": "B"}, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &patched) != nil || !slices.Equal(patched.Tags, []string{"whitelist"}) {
		t.Fatalf("patch without tags: %d %s", resp.StatusCode, raw)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = "t" + strconv.Itoa(i)
	}
	if resp, raw := k.do(http.MethodPatch, k.api+bPath, map[string]any{"tags": many}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "body.tags") {
		t.Fatalf("21 tags: %d %s", resp.StatusCode, raw)
	}
	resp, raw = k.do(http.MethodPatch, k.api+bPath, map[string]any{"tags": []string{}}, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &patched) != nil || patched.Tags == nil || len(patched.Tags) != 0 {
		t.Fatalf("clear node tags: %d %s", resp.StatusCode, raw)
	}

	// Inbounds.
	resp, raw = k.do(http.MethodPost, k.api+"/inbounds", map[string]any{"preset": "vless_reality_vision", "port": "24443", "tags": []string{"wl", " "}}, k.csrf)
	var in item
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &in) != nil || !slices.Equal(in.Tags, []string{"wl"}) {
		t.Fatalf("create inbound: %d %s", resp.StatusCode, raw)
	}
	if got := ids("/inbounds?tag=WL"); !slices.Equal(got, []int64{in.ID}) {
		t.Fatalf("?tag=WL: %v", got)
	}
	inPath := "/inbounds/" + strconv.FormatInt(in.ID, 10)
	resp, raw = k.do(http.MethodPatch, k.api+inPath, map[string]any{"tags": []string{"wl", "backup"}}, k.csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &in) != nil || !slices.Equal(in.Tags, []string{"wl", "backup"}) {
		t.Fatalf("patch inbound: %d %s", resp.StatusCode, raw)
	}
	// A copy keeps the labels, as the rest of the settings.
	resp, raw = k.do(http.MethodPost, k.api+inPath+"/copy", map[string]any{}, k.csrf)
	var cp item
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &cp) != nil || !slices.Equal(cp.Tags, []string{"wl", "backup"}) {
		t.Fatalf("copy: %d %s", resp.StatusCode, raw)
	}
	if got := ids("/inbounds?tag=backup"); !slices.Equal(got, []int64{in.ID, cp.ID}) {
		t.Fatalf("?tag=backup: %v", got)
	}
	if resp, raw := k.do(http.MethodPatch, k.api+inPath, map[string]any{"tags": many}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "body.tags") {
		t.Fatalf("21 tags: %d %s", resp.StatusCode, raw)
	}
	// The read-only key sees the labels and filters by them too.
	if resp, raw := k.asKey(k.read, http.MethodGet, "/inbounds?tag=wl", nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"tags":["wl","backup"]`) {
		t.Fatalf("read key: %d %s", resp.StatusCode, raw)
	}
}
