package api

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/diag"
	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
	"mikan/internal/panel/checkhost"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/store/db"
	"mikan/internal/release"
)

// CheckItem is one line of "Check node" or "Check server": what was looked at, how it is,
// and what fixes it. The page words it by id, status and code; params are the facts.
type CheckItem struct {
	ID     string            `json:"id" doc:"Что проверено: link, hello, version, update, clock, listeners, listener, port, relay, cascade, node_dns, node_internet, node_github, node_ghcr, node_disk, node_memory, diagnose, local_node, dns, internet, github, ghcr, disk, memory"`
	Status string            `json:"status" enum:"ok,warn,fail,skip"`
	Code   string            `json:"code,omitempty" doc:"Почему не ok: timeout, refused, pin_mismatch, behind, skew, busy и другие"`
	Params map[string]string `json:"params,omitempty"`
	Fix    string            `json:"fix,omitempty" enum:"rekey,update_node,old_node,open_port,start_node,check_host,check_dns,free_port,sync_time,update_panel,free_disk,check_outbound,restart_panel,exit_node" doc:"Что исправит: кнопка в панели или команда на сервере"`
	Detail string            `json:"detail,omitempty" doc:"Слова ошибки как есть, для «подробнее»"`
}

type CheckView struct {
	At    time.Time   `json:"at"`
	Items []CheckItem `json:"items"`
	// Report is the check as text for the chat, with addresses, names, ports and keys masked.
	Report string `json:"report" doc:"Отчёт для чата: адреса, домены, порт API и ключи скрыты"`
}

type checkOutput struct{ Body CheckView }

type NodeHello struct {
	At     time.Time         `json:"at"`
	OK     bool              `json:"ok" doc:"Панель достучалась до ноды в ответ на её hello"`
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	SeenIP string            `json:"seen_ip,omitempty" doc:"Адрес, с которого пришёл hello"`
	Host   string            `json:"host,omitempty" doc:"Адрес ноды в панели"`
	// IPDiffers: the hello came from another address than the node's in the panel.
	IPDiffers bool `json:"ip_differs" doc:"Hello пришёл не с того IP, что указан в панели"`
}

type RussiaPort struct {
	Port int    `json:"port"`
	Name string `json:"name" doc:"api или имя подключения"`
}

type RussiaView struct {
	checkhost.Result
	Ports []RussiaPort `json:"ports"`
}

type russiaOutput struct{ Body RussiaView }

func (h *handlers) registerNodeChecks() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "check-node", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/check", Summary: "Проверить ноду: связь, версия, часы, протоколы, порты, интернет, каскад", Tags: tags}, h.checkNode)
	huma.Register(h.api, huma.Operation{OperationID: "check-node-russia", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/check-russia", Summary: "Проверить порты ноды из России через check-host.net", Tags: tags}, h.checkNodeRussia)
	huma.Register(h.api, huma.Operation{OperationID: "check-server", Method: http.MethodPost, Path: "/api/v1/system/check", Summary: "Проверить сервер панели", Tags: []string{"settings"}}, h.checkServer)
}

// linkFix is what fixes a link failure.
func linkFix(code string) string {
	switch code {
	case nodeapi.LinkTimeout:
		return "open_port"
	case nodeapi.LinkRefused, nodeapi.LinkHTTPStatus, nodeapi.LinkUnknown:
		return "start_node"
	case nodeapi.LinkUnreachable:
		return "check_host"
	case nodeapi.LinkDNS:
		return "check_dns"
	case nodeapi.LinkPinMismatch:
		return "rekey"
	case nodeapi.LinkTLS:
		return "free_port"
	}
	return ""
}

func helloView(v nodesync.HelloView) *NodeHello {
	r := v.Result
	return &NodeHello{At: v.At, OK: r.OK, Code: r.Code, Params: r.Params, SeenIP: r.SeenIP, Host: r.Host, IPDiffers: r.Params[nodehello.IPDiffers] == "1"}
}

func (h *handlers) checkNode(ctx context.Context, in *nodeIDInput) (*checkOutput, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if h.d.Nodes == nil {
		return nil, huma.Error409Conflict("nodes_disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	local := n.Address == ""
	var items []CheckItem
	add := func(it ...CheckItem) { items = append(items, it...) }

	started := time.Now()
	hv, _ := h.d.Nodes.CheckNow(ctx, n.ID)
	rtt := time.Since(started)
	if hv.OK {
		add(CheckItem{ID: "link", Status: nodeapi.CheckOK, Params: map[string]string{"ms": strconv.FormatInt(rtt.Milliseconds(), 10)}})
	} else {
		code := hv.Code
		if code == "" {
			code = nodeapi.LinkUnknown
		}
		fix := linkFix(code)
		if local {
			fix = "restart_panel"
		}
		add(CheckItem{ID: "link", Status: nodeapi.CheckFail, Code: code, Params: hv.Params, Fix: fix, Detail: hv.Error})
	}
	if !local {
		add(h.helloItem(n.ID))
	}
	if !hv.OK {
		// Nothing more is known of a node that does not answer, but whether its ports open.
		if !local {
			add(h.portItems(ctx, n)...)
		}
		return h.checkResult(ctx, n, items, hv)
	}
	add(h.versionItem(n, hv))
	if it, ok := updateItem(hv.Health.Update, hv.Health.Version); ok {
		add(it)
	}
	add(clockItem(hv.Skew))
	add(listenerItems(hv)...)

	var (
		wg               sync.WaitGroup
		ports, nodeDiag  []CheckItem
		cascade, relayIt []CheckItem
	)
	if !local {
		wg.Go(func() { ports = h.portItems(ctx, n) })
	}
	wg.Go(func() { nodeDiag = h.nodeDiagItems(ctx, n.ID) })
	wg.Go(func() { cascade, relayIt = h.cascadeItems(ctx, n, hv) })
	wg.Wait()
	add(ports...)
	add(relayIt...)
	add(cascade...)
	add(nodeDiag...)
	return h.checkResult(ctx, n, items, hv)
}

func (h *handlers) checkResult(ctx context.Context, n db.Node, items []CheckItem, hv nodesync.HealthView) (*checkOutput, error) {
	at := h.d.Now().UTC()
	secrets := []string{n.PublicHost, n.Domain}
	if host, port, err := net.SplitHostPort(n.Address); err == nil {
		secrets = append(secrets, host, port)
	}
	if ep, err := h.d.Settings.Endpoint(ctx); err == nil {
		secrets = append(secrets, ep.Host)
	}
	if v, ok := h.d.Nodes.Hello(n.ID); ok {
		secrets = append(secrets, v.Result.SeenIP)
	}
	kind := "remote"
	if n.Address == "" {
		kind = "local"
	}
	nodeVersion := "?"
	if hv.OK && hv.Health.Version != "" {
		nodeVersion = "v" + hv.Health.Version
	}
	versions := "panel v" + h.d.Version + ", node " + nodeVersion + " (" + kind + ")"
	return &checkOutput{Body: CheckView{At: at, Items: items, Report: reportText("mikan node check", versions, at, items, secrets)}}, nil
}

func (h *handlers) helloItem(id int64) CheckItem {
	v, ok := h.d.Nodes.Hello(id)
	if !ok {
		return CheckItem{ID: "hello", Status: nodeapi.CheckSkip, Code: "none"}
	}
	r := v.Result
	params := map[string]string{"at": v.At.UTC().Format(time.RFC3339), "seen_ip": r.SeenIP, "host": r.Host}
	for k, val := range r.Params {
		params[k] = val
	}
	switch {
	case r.OK && r.Params[nodehello.IPDiffers] == "1":
		return CheckItem{ID: "hello", Status: nodeapi.CheckWarn, Code: nodehello.IPDiffers, Params: params, Fix: "check_host"}
	case r.OK:
		return CheckItem{ID: "hello", Status: nodeapi.CheckOK, Params: params}
	}
	return CheckItem{ID: "hello", Status: nodeapi.CheckFail, Code: r.Code, Params: params, Fix: linkFix(r.Code), Detail: r.Error}
}

func (h *handlers) versionItem(n db.Node, hv nodesync.HealthView) CheckItem {
	v := hv.Health.Version
	params := map[string]string{"version": v, "panel": h.d.Version}
	switch {
	case v == "" || !release.Valid(h.d.Version) || !release.Valid(v):
		return CheckItem{ID: "version", Status: nodeapi.CheckOK, Params: params}
	case n.Address == "" && v != h.d.Version:
		// The panel's own node comes with the panel: a difference is a restart away.
		return CheckItem{ID: "version", Status: nodeapi.CheckWarn, Code: "local_differs", Params: params, Fix: "restart_panel"}
	case nodeupdate.Behind(h.d.Version, v):
		fix := "old_node"
		if nodeupdate.CanUpdate(v) {
			fix = "update_node"
		}
		return CheckItem{ID: "version", Status: nodeapi.CheckWarn, Code: "behind", Params: params, Fix: fix}
	case release.Newer(v, h.d.Version):
		return CheckItem{ID: "version", Status: nodeapi.CheckWarn, Code: "newer", Params: params, Fix: "update_panel"}
	}
	return CheckItem{ID: "version", Status: nodeapi.CheckOK, Params: params}
}

// updateItem words the node's last update as its updater reported it.
func updateItem(u *nodeapi.UpdateStatus, version string) (CheckItem, bool) {
	r := nodeupdate.Reported(u)
	if r == nil {
		return CheckItem{}, false
	}
	params := map[string]string{"version": r.Version, "from": r.From, "at": r.At}
	switch r.State {
	case nodeapi.UpdateFailed:
		if release.AtLeast(version, r.Version) {
			return CheckItem{}, false // a later try got there
		}
		return CheckItem{ID: "update", Status: nodeapi.CheckFail, Code: "update_failed", Params: params, Fix: "update_node", Detail: r.Error}, true
	case nodeapi.UpdateRunning:
		return CheckItem{ID: "update", Status: nodeapi.CheckWarn, Code: "update_running", Params: params}, true
	}
	return CheckItem{ID: "update", Status: nodeapi.CheckOK, Params: params}, true
}

func clockItem(skew *time.Duration) CheckItem {
	if skew == nil {
		return CheckItem{ID: "clock", Status: nodeapi.CheckSkip, Code: "old_node"}
	}
	it := CheckItem{ID: "clock", Status: nodeapi.CheckOK, Params: map[string]string{"skew": strconv.FormatInt(int64(math.Round(skew.Seconds())), 10)}}
	if *skew >= diag.SkewWarn || *skew <= -diag.SkewWarn {
		it.Status, it.Code, it.Fix = nodeapi.CheckWarn, "skew", "sync_time"
	}
	return it
}

// listenerItems: the protocols that run, and one line for each that does not.
func listenerItems(hv nodesync.HealthView) []CheckItem {
	total, ok := 0, 0
	var bad []CheckItem
	for _, l := range hv.Listeners {
		total++
		if l.OK {
			ok++
			continue
		}
		params := map[string]string{"name": l.Name}
		if p := hv.Ports[l.Name]; p != "" {
			params["port"] = p
		}
		if l.Name == nodeapi.RelayListener {
			params["name"] = "relay"
		}
		it := CheckItem{ID: "listener", Status: nodeapi.CheckFail, Code: "failed", Params: params, Fix: "start_node", Detail: l.Error}
		if l.Busy() {
			it.Code, it.Fix = "busy", "free_port"
		}
		bad = append(bad, it)
	}
	params := map[string]string{"ok": strconv.Itoa(ok), "total": strconv.Itoa(total)}
	head := CheckItem{ID: "listeners", Status: nodeapi.CheckOK, Params: params}
	switch {
	case total == 0:
		head.Status, head.Code = nodeapi.CheckWarn, "none"
	case ok < total:
		head.Status, head.Code = nodeapi.CheckFail, "some_down"
	}
	return append([]CheckItem{head}, bad...)
}

// maxPortChecks bounds the ports the panel dials on one check.
const maxPortChecks = 10

type tcpPort struct {
	name string
	port int
}

// tcpPorts are the node's TCP ports clients and other nodes connect to: its enabled TCP
// protocols and its relay. A range is checked at its first port.
func (h *handlers) tcpPorts(ctx context.Context, id int64) []tcpPort {
	var out []tcpPort
	ins, err := h.d.Store.Q.ListNodeInbounds(ctx, id)
	if err != nil {
		return nil
	}
	for _, in := range ins {
		if in.Enabled == 0 || domain.InboundNetwork(in) != "tcp" {
			continue
		}
		if p := firstPort(in.Port); p > 0 {
			out = append(out, tcpPort{in.Name, p})
		}
	}
	if r, err := h.d.Store.Q.GetNodeRelay(ctx, id); err == nil {
		if p := firstPort(r.Port); p > 0 {
			out = append(out, tcpPort{"relay", p})
		}
	}
	if len(out) > maxPortChecks {
		out = out[:maxPortChecks]
	}
	return out
}

func firstPort(s string) int {
	first, _, _ := strings.Cut(s, "-")
	p, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}

// portItems dial each TCP port of a remote node from the panel: a port closed by a
// firewall shows here even while the node itself is fine.
func (h *handlers) portItems(ctx context.Context, n db.Node) []CheckItem {
	host, _, err := net.SplitHostPort(n.Address)
	if err != nil {
		return nil
	}
	ports := h.tcpPorts(ctx, n.ID)
	out := make([]CheckItem, len(ports))
	var wg sync.WaitGroup
	for i, p := range ports {
		wg.Go(func() {
			params := map[string]string{"name": p.name, "port": strconv.Itoa(p.port)}
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			conn, err := (&net.Dialer{}).DialContext(c, "tcp", net.JoinHostPort(host, strconv.Itoa(p.port)))
			if err == nil {
				conn.Close()
				out[i] = CheckItem{ID: "port", Status: nodeapi.CheckOK, Params: params}
				return
			}
			code := nodeapi.Classify(err)
			fix := "open_port"
			if code == nodeapi.LinkRefused {
				fix = "start_node"
			}
			out[i] = CheckItem{ID: "port", Status: nodeapi.CheckFail, Code: code, Params: params, Fix: fix, Detail: err.Error()}
		})
	}
	wg.Wait()
	return out
}

// nodeDiagItems are the node's own look at its server.
func (h *handlers) nodeDiagItems(ctx context.Context, id int64) []CheckItem {
	d, err := h.d.Nodes.Diagnose(ctx, id)
	if err != nil {
		var se *nodeapi.StatusError
		if errors.As(err, &se) && se.Status == http.StatusNotFound {
			return []CheckItem{{ID: "diagnose", Status: nodeapi.CheckSkip, Code: "old_node", Fix: "update_node"}}
		}
		return []CheckItem{{ID: "diagnose", Status: nodeapi.CheckFail, Code: nodeapi.Classify(err), Detail: err.Error()}}
	}
	var out []CheckItem
	for _, it := range d.Items {
		if it.ID == "clock" {
			continue // the panel measures the node's clock itself
		}
		out = append(out, diagItem("node_", it))
	}
	return out
}

func diagItem(prefix string, it nodeapi.DiagItem) CheckItem {
	c := CheckItem{ID: prefix + it.ID, Status: it.Status, Code: it.Code, Params: it.Params, Detail: it.Detail}
	if it.Status == nodeapi.CheckOK || it.Status == nodeapi.CheckSkip {
		return c
	}
	switch it.ID {
	case "dns":
		c.Fix = "check_dns"
	case "internet", "github", "ghcr":
		c.Fix = "check_outbound"
	case "clock":
		c.Fix = "sync_time"
	case "disk":
		c.Fix = "free_disk"
	}
	return c
}

// cascadeItems: the node's relay when other nodes go out through it, and each exit it sends
// traffic to, with where the way breaks.
func (h *handlers) cascadeItems(ctx context.Context, n db.Node, hv nodesync.HealthView) (exits, relay []CheckItem) {
	q := h.d.Store.Q
	names := map[int64]string{}
	if nodes, err := q.ListNodes(ctx); err == nil {
		for _, x := range nodes {
			names[x.ID] = x.Name
		}
	}
	if r, err := q.GetNodeRelay(ctx, n.ID); err == nil {
		state, why := relayListener(hv, r.Port)
		it := CheckItem{ID: "relay", Status: nodeapi.CheckOK, Params: map[string]string{"port": r.Port}}
		switch state {
		case relayBusy:
			it.Status, it.Code, it.Fix, it.Detail = nodeapi.CheckFail, "busy", "free_port", why
		case relayFailed:
			it.Status, it.Code, it.Fix, it.Detail = nodeapi.CheckFail, "failed", "start_node", why
		case relayUnknown:
			it.Status, it.Code = nodeapi.CheckSkip, "unknown"
		}
		relay = append(relay, it)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	var ids []int64
	if ins, err := q.ListNodeInbounds(ctx, n.ID); err == nil {
		for _, in := range ins {
			if in.Enabled != 0 && in.ExitNodeID.Valid && !slices.Contains(ids, in.ExitNodeID.Int64) {
				ids = append(ids, in.ExitNodeID.Int64)
			}
		}
	}
	if r, err := q.GetNodeRelay(ctx, n.ID); err == nil && r.ExitNodeID.Valid && !slices.Contains(ids, r.ExitNodeID.Int64) {
		ids = append(ids, r.ExitNodeID.Int64)
	}
	exits = make([]CheckItem, len(ids))
	var wg sync.WaitGroup
	for i, exit := range ids {
		wg.Go(func() { exits[i] = h.exitItem(ctx, n.ID, exit, names[exit]) })
	}
	wg.Wait()
	return exits, relay
}

// exitItem follows the way entry → exit → internet and says where it breaks.
func (h *handlers) exitItem(ctx context.Context, entry, exit int64, name string) CheckItem {
	params := map[string]string{"exit": name, "exit_id": strconv.FormatInt(exit, 10)}
	p, err := h.d.Nodes.Probe(ctx, entry, nodeapi.ExitName(exit))
	if err != nil {
		return CheckItem{ID: "cascade", Status: nodeapi.CheckSkip, Code: "old_node", Params: params, Detail: err.Error()}
	}
	if p.OK {
		params["ip"] = p.IP
		return CheckItem{ID: "cascade", Status: nodeapi.CheckOK, Params: params}
	}
	it := CheckItem{ID: "cascade", Status: nodeapi.CheckFail, Params: params, Detail: strings.TrimSpace(p.Error + " " + p.Detail)}
	xhv, ok := h.d.Nodes.Health(exit)
	r, rerr := h.d.Store.Q.GetNodeRelay(ctx, exit)
	switch {
	case !ok || !xhv.OK:
		it.Code, it.Fix = "exit_down", "exit_node"
	case rerr != nil:
		it.Code = "exit_no_relay"
	default:
		params["port"] = r.Port
		switch state, why := relayListener(xhv, r.Port); state {
		case relayBusy, relayFailed:
			it.Code, it.Fix = "exit_relay", "exit_node"
			if why != "" {
				it.Detail = why
			}
		default:
			// The exit runs and its relay listens: the way between them is closed, or the
			// exit has no internet itself.
			it.Code, it.Fix = "exit_blocked", "open_port"
		}
	}
	return it
}

// checkNodeRussia asks check-host.net whether the node's ports open from Russian cities:
// the API port and up to two TCP protocols.
func (h *handlers) checkNodeRussia(ctx context.Context, in *nodeIDInput) (*russiaOutput, error) {
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if h.d.CheckHost == nil {
		return nil, huma.Error409Conflict("checkhost_unavailable")
	}
	host := n.PublicHost
	var ports []RussiaPort
	if n.Address == "" {
		ep, err := h.d.Settings.Endpoint(ctx)
		if err != nil {
			return nil, err
		}
		host = ep.Host
	} else if _, p, err := net.SplitHostPort(n.Address); err == nil {
		if port, err := strconv.Atoi(p); err == nil {
			ports = append(ports, RussiaPort{Port: port, Name: "api"})
		}
	}
	for _, p := range h.tcpPorts(ctx, n.ID) {
		if len(ports) >= 3 {
			break
		}
		if p.name != "relay" && !slices.ContainsFunc(ports, func(x RussiaPort) bool { return x.Port == p.port }) {
			ports = append(ports, RussiaPort{Port: p.port, Name: p.name})
		}
	}
	if host == "" || len(ports) == 0 {
		return nil, huma.Error409Conflict("checkhost_nothing")
	}
	nums := make([]int, len(ports))
	for i, p := range ports {
		nums[i] = p.Port
	}
	res, err := h.d.CheckHost.CheckTCP(ctx, host, nums)
	switch {
	case errors.Is(err, checkhost.ErrBusy):
		return nil, huma.Error409Conflict("checkhost_busy")
	case err != nil:
		h.d.Log.Warn("check-host.net", "node", n.ID, "err", err)
		return nil, huma.Error502BadGateway("checkhost_unavailable")
	}
	return &russiaOutput{Body: RussiaView{Result: res, Ports: ports}}, nil
}

// checkServer looks at the panel's own server: its node, names, the internet, GitHub and
// GHCR for updates, the clock, the disk and memory, and how the last update went.
func (h *handlers) checkServer(ctx context.Context, _ *struct{}) (*checkOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var items []CheckItem
	var hv nodesync.HealthView
	if h.d.Nodes != nil {
		var ok bool
		if hv, ok = h.d.Nodes.CheckNow(ctx, nodesync.LocalNode); ok {
			if hv.OK {
				items = append(items, CheckItem{ID: "local_node", Status: nodeapi.CheckOK, Params: map[string]string{"version": hv.Health.Version}})
				items = append(items, listenerItems(hv)...)
			} else {
				items = append(items, CheckItem{ID: "local_node", Status: nodeapi.CheckFail, Code: hv.Code, Fix: "restart_panel", Detail: hv.Error})
			}
		}
	}
	d := diag.Run(ctx, diag.Options{DataDir: h.d.DataDir})
	for _, it := range d.Items {
		items = append(items, diagItem("", it))
	}
	if u := h.d.Updates; u != nil {
		st := u.State()
		switch {
		case st.Error != "":
			items = append(items, CheckItem{ID: "updates", Status: nodeapi.CheckWarn, Code: "check_failed", Fix: "check_outbound", Detail: st.Error})
		case st.Available():
			items = append(items, CheckItem{ID: "updates", Status: nodeapi.CheckWarn, Code: "available", Params: map[string]string{"version": st.Latest.Version}, Fix: "update_panel"})
		default:
			items = append(items, CheckItem{ID: "updates", Status: nodeapi.CheckOK, Params: map[string]string{"version": st.Current}})
		}
		if hs, ok := u.Host(); ok && hs.State == nodeapi.UpdateFailed && !release.AtLeast(h.d.Version, hs.Version) {
			items = append(items, CheckItem{ID: "update", Status: nodeapi.CheckFail, Code: "update_failed", Params: map[string]string{"version": hs.Version, "from": hs.From, "at": hs.At}, Fix: "update_panel", Detail: hs.Error})
		}
	}
	at := h.d.Now().UTC()
	var secrets []string
	if ep, err := h.d.Settings.Endpoint(ctx); err == nil {
		secrets = append(secrets, ep.Host, strconv.Itoa(ep.Port))
	}
	if paths, err := h.d.Settings.Paths(ctx); err == nil {
		secrets = append(secrets, paths.Admin, paths.Sub)
	}
	return &checkOutput{Body: CheckView{At: at, Items: items, Report: reportText("mikan server check", "panel v"+h.d.Version, at, items, secrets)}}, nil
}
