package subsrc

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/sub/build"
	submodel "mikan/internal/sub/model"
)

// sampleProxies are the proxies of a user given every inbound, with placeholder keys, in the
// order of the subscriptions; they keep off the names of groups (over, when set, in place of
// the saved ones).
func (h *Handler) sampleProxies(ctx context.Context, groups *submodel.GroupNames) ([]submodel.Proxy, Config, error) {
	cfg, err := h.cfg(ctx)
	if err != nil {
		return nil, cfg, err
	}
	r, err := h.reach(ctx, db.User{}, cfg, domain.GrantsLeft{})
	if err != nil {
		return nil, cfg, err
	}
	hosts, err := h.hosts(ctx, r.inbounds)
	if err != nil {
		return nil, cfg, err
	}
	keys := proto.Slot{Name: "preview", UUID: "00000000-0000-0000-0000-000000000000", Secret: "preview"}
	ps := build.Proxies(build.Profile{Keys: keys, Inbounds: inbounds(r.inbounds, hosts), Nodes: cfg.Nodes, Fingerprint: cfg.Fingerprint,
		Groups: groupsOf(cfg, groups)})
	return ps, cfg, nil
}

// ServerNames are the names a client given every server sees them by, by the id of their
// host (submodel.Proxy.Host): the order of the subscription shows them so. A host's own name
// keeps its variables as written. A server no client gets now (its protocol or node is off,
// its template does not render) has none.
func (h *Handler) ServerNames(ctx context.Context) (map[int64]string, error) {
	ps, _, err := h.sampleProxies(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(ps))
	for _, p := range ps {
		if p.Host != 0 {
			out[p.Host] = p.Name
		}
	}
	return out, nil
}
