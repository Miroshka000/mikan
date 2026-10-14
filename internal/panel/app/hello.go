package app

import (
	"context"
	"net/http"
	"strconv"

	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
	"mikan/internal/panel/hello"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store"
)

// nodeHello answers the hellos of the panel's remote nodes: it finds the node by its
// certificate, dials it back at once and keeps the outcome for the Nodes page.
func nodeHello(st *store.Store, nodes *nodesync.Manager, o Options) *hello.Handler {
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
		Record:   nodes.RecordHello,
		Panel:    o.PanelCert,
		ClientIP: func(r *http.Request) string { return server.ClientIP(r.Header, r.RemoteAddr, o.TrustProxy) },
		Resolve:  hello.SystemResolve,
		Now:      o.Now,
	})
}
