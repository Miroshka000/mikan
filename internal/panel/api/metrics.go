package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
)

// Metrics in the Prometheus text format, for Prometheus, VictoriaMetrics, Grafana Agent
// and the like. Scrape with a read API key:
//
//	authorization: { type: Bearer, credentials: mk_… }
//
// Labels stay few: per node and per inbound, never per user, so the series do not grow
// with the users.

type metricsOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

func (h *handlers) registerMetrics() {
	huma.Register(h.api, huma.Operation{OperationID: "metrics", Method: http.MethodGet, Path: "/api/v1/metrics", Summary: "Метрики в формате Prometheus",
		Description: "Пользователи по состояниям, онлайн, трафик, ноды и подключения. Для Prometheus и Grafana: ключ на чтение в заголовке Authorization: Bearer.",
		Tags:        []string{"stats"},
		Responses:   map[string]*huma.Response{"200": {Description: "Prometheus text format 0.0.4", Content: map[string]*huma.MediaType{"text/plain": {}}}},
	}, h.metrics)
}

// metricsTTL: scrapes closer together than this get the same text: several Prometheus
// servers scraping one panel make one set of queries.
const metricsTTL = 5 * time.Second

// metricsCache is the text of the last scrape and when it was made.
type metricsCache struct {
	mu   sync.Mutex
	at   time.Time
	body []byte
}

func (h *handlers) metrics(ctx context.Context, _ *struct{}) (*metricsOutput, error) {
	c := &h.metricsCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := h.d.Now()
	// A clock moved back does not keep an old text forever.
	if c.body == nil || now.Sub(c.at) >= metricsTTL || now.Before(c.at) {
		body, err := h.renderMetrics(ctx, now)
		if err != nil {
			return nil, err
		}
		c.at, c.body = now, body
	}
	return &metricsOutput{ContentType: "text/plain; version=0.0.4; charset=utf-8", Body: c.body}, nil
}

func (h *handlers) renderMetrics(ctx context.Context, now time.Time) ([]byte, error) {
	var m promWriter
	m.gauge("mikan_info", "The panel's version.", 1, "version", h.d.Version)

	// Counted by the database, as on the dashboard: a scrape does not load every user.
	counts, err := domain.CountStates(ctx, h.d.Store.Q, now)
	if err != nil {
		return nil, err
	}
	m.help("mikan_users", "Users by state.", "gauge")
	for _, s := range []struct {
		state string
		n     int64
	}{{domain.StateActive, counts.Active}, {domain.StateExpiring, counts.Expiring}, {domain.StateLimited, counts.Limited},
		{domain.StateExpired, counts.Expired}, {domain.StateDisabled, counts.Disabled}} {
		m.sample("mikan_users", float64(s.n), "state", s.state)
	}
	traffic, err := h.d.Store.Q.SumUserTraffic(ctx)
	if err != nil {
		return nil, err
	}
	m.help("mikan_traffic_bytes_total", "Traffic of all users, all time. Drops when a user is deleted.", "counter")
	m.sample("mikan_traffic_bytes_total", float64(traffic.Up), "direction", "up")
	m.sample("mikan_traffic_bytes_total", float64(traffic.Down), "direction", "down")

	if h.d.Online != nil {
		n, err := h.onlineUsers(ctx)
		if err != nil {
			return nil, err
		}
		m.gauge("mikan_users_online", "Users with a connection now.", float64(n))
	}
	if h.d.Pool != nil {
		ps, err := h.d.Pool.Stats(ctx)
		if err != nil {
			return nil, err
		}
		m.help("mikan_slots", "Key slots by state.", "gauge")
		m.sample("mikan_slots", float64(ps.Free), "state", "free")
		m.sample("mikan_slots", float64(ps.Assigned), "state", "assigned")
	}

	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	type liveNode struct {
		id, name string
		hv       nodesync.HealthView
	}
	var live []liveNode
	m.help("mikan_node_up", "1 when the panel reaches the node and its core runs.", "gauge")
	for _, n := range nodes {
		if n.Enabled == 0 {
			continue
		}
		id := strconv.FormatInt(n.ID, 10)
		ok := false
		if h.d.Nodes != nil {
			if hv, found := h.d.Nodes.Health(n.ID); found && hv.OK {
				ok = true
				live = append(live, liveNode{id, n.Name, hv})
			}
		}
		m.sample("mikan_node_up", b2f(ok), "node_id", id, "node", n.Name)
	}
	// Counted from the day the panel started to count by node, so a counter and not the
	// rows of node_traffic_*, which are cut when old.
	m.help("mikan_node_traffic_bytes_total", "Traffic the node carried for users since it was counted by node. Drops when the node is deleted.", "counter")
	for _, n := range nodes {
		id := strconv.FormatInt(n.ID, 10)
		m.sample("mikan_node_traffic_bytes_total", float64(n.TotalUp), "node_id", id, "node", n.Name, "direction", "up")
		m.sample("mikan_node_traffic_bytes_total", float64(n.TotalDown), "node_id", id, "node", n.Name, "direction", "down")
	}
	if len(live) > 0 {
		for _, f := range []struct {
			name, help string
			v          func(nodesync.HealthView) float64
		}{
			{"mikan_node_connections", "Open connections on the node.", func(hv nodesync.HealthView) float64 { return float64(hv.Health.Conns) }},
			{"mikan_node_cpu_percent", "CPU use of the node's server.", func(hv nodesync.HealthView) float64 { return hv.Health.System.CPUPercent }},
			{"mikan_node_process_cpu_percent", "CPU use of the node process itself, on the server's scale.", func(hv nodesync.HealthView) float64 { return hv.Health.System.ProcCPUPercent }},
			{"mikan_node_memory_used_bytes", "Memory used on the node's server.", func(hv nodesync.HealthView) float64 { return float64(hv.Health.System.MemUsed) }},
			{"mikan_node_memory_total_bytes", "Memory of the node's server.", func(hv nodesync.HealthView) float64 { return float64(hv.Health.System.MemTotal) }},
			{"mikan_node_receive_bytes_per_second", "Network in on the node's server.", func(hv nodesync.HealthView) float64 { return float64(hv.Health.System.NetRxBps) }},
			{"mikan_node_transmit_bytes_per_second", "Network out on the node's server.", func(hv nodesync.HealthView) float64 { return float64(hv.Health.System.NetTxBps) }},
		} {
			m.help(f.name, f.help, "gauge")
			for _, n := range live {
				m.sample(f.name, f.v(n.hv), "node_id", n.id, "node", n.name)
			}
		}
		// A listener is named after its inbound, and inbound names are unique; the id goes
		// along all the same, and a name seen twice on a node is written once: one duplicate
		// series makes Prometheus drop the whole scrape.
		inbounds, err := h.d.Store.Q.ListInbounds(ctx)
		if err != nil {
			return nil, err
		}
		ids := make(map[string]string, len(inbounds))
		for _, in := range inbounds {
			ids[in.Name] = strconv.FormatInt(in.ID, 10)
		}
		m.help("mikan_inbound_up", "1 when the inbound listens on its node.", "gauge")
		for _, n := range live {
			seen := map[string]bool{}
			for _, l := range n.hv.Listeners {
				if seen[l.Name] {
					continue
				}
				seen[l.Name] = true
				m.sample("mikan_inbound_up", b2f(l.OK), "node_id", n.id, "node", n.name, "inbound_id", ids[l.Name], "inbound", l.Name)
			}
		}
	}

	// Retries are harmless one by one; a steady climb means writers keep colliding.
	m.help("mikan_db_serialization_retries_total", "Database transactions retried after a serialization conflict or a deadlock since the panel started.", "counter")
	m.sample("mikan_db_serialization_retries_total", float64(h.d.Store.Conflicts()))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.gauge("go_goroutines", "Goroutines of the panel.", float64(runtime.NumGoroutine()))
	m.gauge("go_memstats_heap_alloc_bytes", "Heap in use by the panel.", float64(ms.HeapAlloc))
	return m.Bytes(), nil
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// promWriter writes the Prometheus text exposition format.
type promWriter struct{ bytes.Buffer }

func (w *promWriter) help(name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (w *promWriter) gauge(name, help string, v float64, labels ...string) {
	w.help(name, help, "gauge")
	w.sample(name, v, labels...)
}

// sample writes one line; labels come in name, value pairs.
func (w *promWriter) sample(name string, v float64, labels ...string) {
	w.WriteString(name)
	if len(labels) > 0 {
		w.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				w.WriteByte(',')
			}
			w.WriteString(labels[i])
			w.WriteString(`="`)
			w.WriteString(labelEscaper.Replace(labels[i+1]))
			w.WriteByte('"')
		}
		w.WriteByte('}')
	}
	w.WriteByte(' ')
	w.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	w.WriteByte('\n')
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
