package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"mikan/internal/netaddr"
	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/hello"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// nodeHello answers the hellos of the panel's remote nodes: it finds the node by its
// certificate, dials it back at once and keeps the outcome for the Nodes page. A node it
// cannot reach where it knows it, saying hello from a new address, is moved there when it
// answers there (readdressNode); moved hears of it.
func nodeHello(st *store.Store, nodes *nodesync.Manager, o Options, moved func(ctx context.Context, n db.Node, from, to string)) *hello.Handler {
	return hello.New(hello.Deps{
		Lookup: func(ctx context.Context, pin string) (hello.Node, bool) {
			list, err := st.Q.ListNodes(ctx)
			if err != nil {
				return hello.Node{}, false
			}
			for _, n := range list {
				if n.Address != "" && nodetls.SamePin(n.CertSha256, pin) {
					return hello.Node{ID: n.ID, Host: n.PublicHost}, true
				}
			}
			return hello.Node{}, false
		},
		Dial: func(ctx context.Context, id int64) nodehello.Result {
			hv, ok := nodes.CheckNow(ctx, id)
			switch {
			case !ok:
				// The node was added a moment ago and its syncer is not up yet.
				return nodehello.Result{Code: nodeapi.LinkUnknown, Error: "the panel is not connected to the node yet"}
			case hv.OK:
				ok, total := 0, len(hv.Listeners)
				for _, l := range hv.Listeners {
					if l.OK {
						ok++
					}
				}
				return nodehello.Result{OK: true, Params: map[string]string{"listeners_ok": strconv.Itoa(ok), "listeners": strconv.Itoa(total)}}
			}
			return nodehello.Result{Code: hv.Code, Params: hv.Params, Error: hv.Error}
		},
		Readdress: func(ctx context.Context, id int64, seen string) (nodehello.Result, bool) {
			return readdressNode(ctx, st, nodes, o, id, seen, reachNode(o), moved)
		},
		Record:   nodes.RecordHello,
		Panel:    o.PanelCert,
		ClientIP: func(r *http.Request) string { return server.ClientIP(r.Header, r.RemoteAddr, o.TrustProxy) },
		Resolve:  hello.SystemResolve,
		Now:      o.Now,
	})
}

// reachNode says whether the node answers at addr (host:port): the panel's pinned client
// reaches it there, and it shows its own certificate.
func reachNode(o Options) func(ctx context.Context, n db.Node, addr string) error {
	return func(ctx context.Context, n db.Node, addr string) error {
		if o.PanelCert == nil {
			return errors.New("no panel certificate")
		}
		panel, err := o.PanelCert()
		if err != nil {
			return err
		}
		tc, err := nodetls.ClientConfig(panel, n.CertSha256)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		_, err = nodeapi.NewTLSClient(addr, tc).Health(ctx)
		return err
	}
}

// readdressNode moves a remote node the panel cannot reach to seen, the address its hello
// came from: only a node the panel knows by an IP address that its clients use too (one
// known by a name follows its DNS), when the node answers there (reach) and its domain, if
// it has one, leads there as well. The switch AutoNodeAddress turns it off.
func readdressNode(ctx context.Context, st *store.Store, nodes *nodesync.Manager, o Options, id int64, seen string,
	reach func(ctx context.Context, n db.Node, addr string) error, moved func(ctx context.Context, n db.Node, from, to string)) (nodehello.Result, bool) {
	if on, err := settings.New(st.Q).On(ctx, settings.AutoNodeAddress); err != nil || !on {
		return nodehello.Result{}, false
	}
	n, err := st.Q.GetNode(ctx, id)
	if err != nil || n.Address == "" {
		return nodehello.Result{}, false
	}
	host, port, err := net.SplitHostPort(n.Address)
	if err != nil || n.PublicHost != host {
		return nodehello.Result{}, false
	}
	old, err := netip.ParseAddr(host)
	if err != nil {
		return nodehello.Result{}, false
	}
	to, err := netip.ParseAddr(seen)
	if err != nil || !netaddr.IsPublic(to) || to.Unmap() == old.Unmap() {
		return nodehello.Result{}, false
	}
	addr := to.Unmap().String()
	if err := reach(ctx, n, net.JoinHostPort(addr, port)); err != nil {
		return nodehello.Result{}, false
	}
	next, err := domain.UpdateNode(ctx, st, id, domain.NodePatch{Host: &addr, Rules: domain.NodeRules{DNS: o.DNS}}, o.Now())
	if err != nil {
		o.Log.Warn("node: not moved to the address its hello came from", "node", id, "to", addr, "err", err)
		return nodehello.Result{}, false
	}
	if err := audit.Write(ctx, st.Q, o.Now(), audit.Entry{Action: "auto.node_address", TargetType: "node", TargetID: strconv.FormatInt(id, 10),
		Details: map[string]string{"from": host, "to": addr}}); err != nil {
		o.Log.Warn("node: the move not written to the journal", "node", id, "err", err)
	}
	o.Log.Warn("node: moved to the address its hello came from", "node", id, "from", host, "to", addr)
	if nodes != nil {
		nodes.NodesChanged()
		nodes.SlotsChanged()
	}
	if o.NodesACME != nil {
		o.NodesACME.Wake()
	}
	if moved != nil {
		moved(ctx, next, host, addr)
	}
	return nodehello.Result{OK: true}, true
}
