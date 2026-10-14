package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What each node carried: a chart per node by range, the share of every node for the
// dashboard, the last day on the node's card and the counters for Prometheus. A read key
// sees them (they are GETs); a node that is not there is a 404.
func TestNodeTrafficEndpoints(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	// A second node, so the shares have something to compare.
	var other int64
	if err := k.st.DB.QueryRowContext(ctx, "INSERT INTO nodes(name,address,created_at,updated_at) VALUES('NL','198.51.100.9:40000',1,1) RETURNING id").Scan(&other); err != nil {
		t.Fatal(err)
	}
	hour, day := k.now.Unix()/3600, k.now.Unix()/86400
	for _, q := range []struct {
		sql  string
		args []any
	}{
		// Node 1: this hour, 5 hours ago, 3 days ago (outside 24 h, inside 7 d), 20 days ago (daily only).
		{"INSERT INTO node_traffic_hourly(node_id,hour,up,down) VALUES (1,$1,10,100),(1,$2,20,200),(1,$3,40,400)", []any{hour, hour - 5, hour - 72}},
		{"INSERT INTO node_traffic_daily(node_id,day,up,down) VALUES (1,$1,70,700),(1,$2,5,50),(1,$3,1,1)", []any{day, day - 3, day - 20}},
		{"INSERT INTO node_traffic_hourly(node_id,hour,up,down) VALUES ($1,$2,1000,2000)", []any{other, hour}},
		{"INSERT INTO node_traffic_daily(node_id,day,up,down) VALUES ($1,$2,1000,2000)", []any{other, day}},
		{"UPDATE nodes SET total_up = 77, total_down = 88 WHERE id = 1", nil},
	} {
		if _, err := k.st.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	type point struct {
		T    time.Time `json:"t"`
		Up   int64     `json:"up"`
		Down int64     `json:"down"`
	}
	chart := func(path string) []point {
		t.Helper()
		resp, body := k.asKey(k.read, http.MethodGet, path, nil)
		var out struct {
			Points []point `json:"points"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		return out.Points
	}
	if p := chart("/nodes/1/traffic?range=24h"); len(p) != 2 || p[0].Up != 20 || p[1].Down != 100 {
		t.Fatalf("24h: %+v", p)
	}
	if p := chart("/nodes/1/traffic?range=7d"); len(p) != 3 || p[0].Down != 400 {
		t.Fatalf("7d: %+v", p)
	}
	if p := chart("/nodes/1/traffic?range=30d"); len(p) != 3 || p[0].Up != 1 || p[2].Down != 700 || p[2].T.Unix() != day*86400 {
		t.Fatalf("30d (by the day): %+v", p)
	}
	if p := chart("/nodes/" + idOf(other) + "/traffic"); len(p) != 1 || p[0].Up != 1000 {
		t.Fatalf("another node: %+v", p)
	}
	if resp, _ := k.asKey(k.read, http.MethodGet, "/nodes/9999/traffic", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a node that is not there: %d", resp.StatusCode)
	}

	resp, body := k.asKey(k.read, http.MethodGet, "/stats/nodes?range=24h", nil)
	var shares struct {
		Items []struct {
			ID    int64 `json:"id"`
			Local bool  `json:"local"`
			Bytes int64 `json:"bytes"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &shares) != nil || len(shares.Items) != 2 {
		t.Fatalf("shares: %d %s", resp.StatusCode, body)
	}
	if shares.Items[0].ID != other || shares.Items[0].Bytes != 3000 || shares.Items[1].ID != 1 || !shares.Items[1].Local || shares.Items[1].Bytes != 330 || shares.Total != 3330 {
		t.Fatalf("shares of 24h: %s", body)
	}
	// The accounting began 20 days ago: inside 30 days, so that period says from when;
	// 24 hours are covered whole and say nothing.
	if strings.Contains(string(body), `"since"`) {
		t.Fatalf("24h covered by the accounting carries a since: %s", body)
	}
	_, body = k.asKey(k.read, http.MethodGet, "/stats/nodes?range=30d", nil)
	var month struct {
		Since *time.Time `json:"since"`
	}
	if json.Unmarshal(body, &month) != nil || month.Since == nil || month.Since.Unix() != (day-20)*86400 {
		t.Fatalf("30d starts before the accounting: %s", body)
	}

	// The card of each node: the last day.
	resp, body = k.asKey(k.read, http.MethodGet, "/nodes", nil)
	var nodes []struct {
		ID         int64 `json:"id"`
		Traffic24h int64 `json:"traffic_24h"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &nodes) != nil || len(nodes) != 2 {
		t.Fatalf("nodes: %d %s", resp.StatusCode, body)
	}
	for _, n := range nodes {
		if want := map[int64]int64{1: 330, other: 3000}[n.ID]; n.Traffic24h != want {
			t.Errorf("node %d carried %d in 24h, want %d", n.ID, n.Traffic24h, want)
		}
	}

	_, body = k.asKey(k.read, http.MethodGet, "/metrics", nil)
	text := string(body)
	for _, want := range []string{
		"# TYPE mikan_node_traffic_bytes_total counter",
		`mikan_node_traffic_bytes_total{node_id="1",node="`,
		`direction="up"} 77`,
		`direction="down"} 88`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in the metrics", want)
		}
	}
}
