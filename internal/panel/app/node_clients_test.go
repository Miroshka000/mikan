package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// Who went through a node, and through which nodes a client went: two views of one table.
// The node's list carries each client's traffic and last-seen time; the client's list the
// same by node. A node or a client that is not there is a 404.
func TestNodeClientEndpoints(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	ids := map[string]int64{}
	for _, name := range []string{"anna", "boris"} {
		resp, body := k.do(http.MethodPost, k.api+"/users", map[string]any{"name": name, "tariff_id": tariffs[1].ID}, k.csrf)
		var u struct {
			ID int64 `json:"id"`
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &u) != nil {
			t.Fatalf("create %s: %d %s", name, resp.StatusCode, body)
		}
		ids[name] = u.ID
	}
	// A second node, and the rows both views read: anna's traffic on node 1 and node 2,
	// boris's on node 1 alone.
	var other int64
	if err := k.st.DB.QueryRowContext(ctx, "INSERT INTO nodes(name,address,created_at,updated_at) VALUES('NL','198.51.100.9:40000',1,1) RETURNING id").Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO node_clients(user_id,node_id,up,down,first_seen,last_seen) VALUES ($1,1,10,100,1000,2000)", []any{ids["anna"]}},
		{"INSERT INTO node_clients(user_id,node_id,up,down,first_seen,last_seen) VALUES ($1,$2,30,300,1000,2500)", []any{ids["anna"], other}},
		{"INSERT INTO node_clients(user_id,node_id,up,down,first_seen,last_seen) VALUES ($1,1,1,2,1000,1500)", []any{ids["boris"]}},
	} {
		if _, err := k.st.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	type client struct {
		UserID   int64  `json:"user_id"`
		Name     string `json:"name"`
		Up       int64  `json:"up"`
		Down     int64  `json:"down"`
		Bytes    int64  `json:"bytes"`
		Online   bool   `json:"online"`
		LastSeen string `json:"last_seen"`
	}
	resp, body := k.asKey(k.read, http.MethodGet, "/nodes/1/clients", nil)
	var out struct {
		Items []client `json:"items"`
		Total int64    `json:"total"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
		t.Fatalf("node clients: %d %s", resp.StatusCode, body)
	}
	// Sorted by traffic: anna (110) before boris (3).
	if len(out.Items) != 2 || out.Total != 2 || out.Items[0].Name != "anna" || out.Items[0].Up != 10 || out.Items[0].Down != 100 || out.Items[0].Bytes != 110 || out.Items[0].LastSeen == "" {
		t.Fatalf("node 1 clients: %+v", out)
	}
	if out.Items[1].Name != "boris" || out.Items[1].Bytes != 3 {
		t.Fatalf("node 1 clients, second row: %+v", out.Items[1])
	}
	if resp, _ := k.asKey(k.read, http.MethodGet, "/nodes/9999/clients", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a node that is not there: %d", resp.StatusCode)
	}

	type node struct {
		NodeID     int64  `json:"node_id"`
		Name       string `json:"name"`
		PublicName string `json:"public_name"`
		Up         int64  `json:"up"`
		Down       int64  `json:"down"`
		Bytes      int64  `json:"bytes"`
		Online     bool   `json:"online"`
	}
	resp, body = k.asKey(k.read, http.MethodGet, "/users/"+strconv.FormatInt(ids["anna"], 10)+"/nodes", nil)
	var un struct {
		Items []node `json:"items"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &un) != nil {
		t.Fatalf("user nodes: %d %s", resp.StatusCode, body)
	}
	if len(un.Items) != 2 || un.Items[0].NodeID != other || un.Items[0].Bytes != 330 || un.Items[1].NodeID != 1 || un.Items[1].Bytes != 110 {
		t.Fatalf("anna's nodes: %+v", un.Items)
	}
	if resp, _ := k.asKey(k.read, http.MethodGet, "/users/99999/nodes", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a user that is not there: %d", resp.StatusCode)
	}
}
