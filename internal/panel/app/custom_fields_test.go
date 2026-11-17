package app

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
)

type customField struct {
	ID     int64  `json:"id"`
	Scope  string `json:"scope"`
	Key    string `json:"key"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Values int    `json:"values"`
}

// The admin's own fields: named once for users or nodes, a value checked by its kind,
// shown on the card and in the list, found by the search, gone with the field.
func TestCustomFields(t *testing.T) {
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	k := newKeyHarness(t, func(o *Options) { o.PanelCert = func() (nodetls.Pair, error) { return panel, nil } })
	field := func(scope, key, label, kind string) customField {
		t.Helper()
		resp, raw := k.do(http.MethodPost, k.api+"/custom-fields", map[string]any{"scope": scope, "key": key, "label": label, "kind": kind}, k.csrf)
		var f customField
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &f) != nil {
			t.Fatalf("create field %s: %d %s", key, resp.StatusCode, raw)
		}
		return f
	}
	order := field("user", "order_no", "Заказ", "text")
	paid := field("user", "paid_until", "Оплачено до", "date")
	field("user", "vip", "VIP", "bool")
	hoster := field("node", "hoster", "Хостер", "url")
	// A key is a scope's once; nodes may have a field of the same key.
	if resp, raw := k.do(http.MethodPost, k.api+"/custom-fields", map[string]any{"scope": "user", "key": "order_no", "label": "x", "kind": "text"}, k.csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a taken key: %d %s", resp.StatusCode, raw)
	}
	field("node", "order_no", "Заказ сервера", "text")
	if resp, raw := k.do(http.MethodPost, k.api+"/custom-fields", map[string]any{"scope": "user", "key": "Bad-Key", "label": "x", "kind": "text"}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a bad key: %d %s", resp.StatusCode, raw)
	}

	tariffs, _ := k.st.Q.ListTariffs(t.Context())
	resp, raw := k.do(http.MethodPost, k.api+"/users", map[string]any{"name": "anna", "tariff_id": tariffs[1].ID, "custom_fields": map[string]string{"order_no": "A-1042", "vip": "true"}}, k.csrf)
	var u struct {
		ID           int64             `json:"id"`
		CustomFields map[string]string `json:"custom_fields"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &u) != nil || u.CustomFields["order_no"] != "A-1042" || u.CustomFields["vip"] != "true" {
		t.Fatalf("create user: %d %s", resp.StatusCode, raw)
	}
	path := k.api + "/users/" + strconv.FormatInt(u.ID, 10)
	// Everything wrong is told at once, and nothing is written.
	resp, raw = k.do(http.MethodPatch, path, map[string]any{"name": "anna2", "custom_fields": map[string]string{"paid_until": "soon", "nope": "1"}}, k.csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "body.custom_fields.paid_until") || !strings.Contains(string(raw), "body.custom_fields.nope") {
		t.Fatalf("bad values: %d %s", resp.StatusCode, raw)
	}
	if _, raw = k.do(http.MethodGet, path, nil, nil); !strings.Contains(string(raw), `"name":"anna"`) {
		t.Fatalf("a refused patch changed the user: %s", raw)
	}
	// Only the named fields change; an empty value clears one, false clears a bool.
	resp, raw = k.do(http.MethodPatch, path, map[string]any{"custom_fields": map[string]string{"paid_until": "2026-12-01", "vip": "false"}}, k.csrf)
	u.CustomFields = nil
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &u) != nil || len(u.CustomFields) != 2 || u.CustomFields["paid_until"] != "2026-12-01" || u.CustomFields["order_no"] != "A-1042" {
		t.Fatalf("patch fields: %d %s", resp.StatusCode, raw)
	}
	resp, raw = k.do(http.MethodPatch, path, map[string]any{"custom_fields": map[string]string{"order_no": ""}}, k.csrf)
	u.CustomFields = nil
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &u) != nil || len(u.CustomFields) != 1 {
		t.Fatalf("clear a field: %d %s", resp.StatusCode, raw)
	}
	k.do(http.MethodPatch, path, map[string]any{"custom_fields": map[string]string{"order_no": "A-1042"}}, k.csrf)
	k.do(http.MethodPost, k.api+"/users", map[string]any{"name": "boris", "tariff_id": tariffs[1].ID}, k.csrf)

	// The search finds a value; the list shows the values.
	type listed struct {
		Items []struct {
			Name         string            `json:"name"`
			CustomFields map[string]string `json:"custom_fields"`
		} `json:"items"`
	}
	var list listed
	resp, raw = k.do(http.MethodGet, k.api+"/users?q=a-104", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 || list.Items[0].Name != "anna" || list.Items[0].CustomFields["order_no"] != "A-1042" {
		t.Fatalf("search by a field: %d %s", resp.StatusCode, raw)
	}
	list = listed{}
	if _, raw = k.do(http.MethodGet, k.api+"/users", nil, nil); json.Unmarshal(raw, &list) != nil || len(list.Items) != 2 {
		t.Fatalf("list: %s", raw)
	}
	for _, it := range list.Items {
		if it.CustomFields == nil {
			t.Fatalf("%s has no custom_fields object: %s", it.Name, raw)
		}
	}

	// A node's fields.
	_, raw = k.do(http.MethodGet, k.api+"/nodes", nil, nil)
	var nodes []struct {
		ID           int64             `json:"id"`
		CustomFields map[string]string `json:"custom_fields"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) == 0 || nodes[0].CustomFields == nil {
		t.Fatalf("nodes: %s", raw)
	}
	npath := k.api + "/nodes/" + strconv.FormatInt(nodes[0].ID, 10)
	if resp, raw := k.do(http.MethodPatch, npath, map[string]any{"custom_fields": map[string]string{"hoster": "ftp://x"}}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a bad link: %d %s", resp.StatusCode, raw)
	}
	// A user's field is not a node's.
	if resp, raw := k.do(http.MethodPatch, npath, map[string]any{"custom_fields": map[string]string{"vip": "true"}}, k.csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a user's field on a node: %d %s", resp.StatusCode, raw)
	}
	resp, raw = k.do(http.MethodPatch, npath, map[string]any{"custom_fields": map[string]string{"hoster": "https://hoster.example/server/7"}}, k.csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"hoster":"https://hoster.example/server/7"`) {
		t.Fatalf("node field: %d %s", resp.StatusCode, raw)
	}

	// The list of fields counts the values; a rename keeps them, a delete takes them along.
	var fields []customField
	if _, raw = k.do(http.MethodGet, k.api+"/custom-fields", nil, nil); json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		t.Fatalf("fields: %s", raw)
	}
	for _, f := range fields {
		if want := map[int64]int{order.ID: 1, paid.ID: 1, hoster.ID: 1}[f.ID]; f.Values != want {
			t.Errorf("%s/%s has %d values, want %d", f.Scope, f.Key, f.Values, want)
		}
	}
	if resp, raw := k.do(http.MethodPatch, k.api+"/custom-fields/"+strconv.FormatInt(order.ID, 10), map[string]any{"label": "Номер заказа"}, k.csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"values":1`) {
		t.Fatalf("rename: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodPut, k.api+"/custom-fields/order", map[string]any{"scope": "user", "ids": []int64{paid.ID, order.ID}}, k.csrf); resp.StatusCode < 400 {
		t.Fatalf("an order without every field: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := k.do(http.MethodDelete, k.api+"/custom-fields/"+strconv.FormatInt(order.ID, 10), nil, k.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, raw)
	}
	u.CustomFields = nil
	if _, raw = k.do(http.MethodGet, path, nil, nil); json.Unmarshal(raw, &u) != nil || len(u.CustomFields) != 1 || u.CustomFields["paid_until"] == "" {
		t.Fatalf("after the delete: %s", raw)
	}
}
