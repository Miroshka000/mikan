package nodesync

import (
	"context"
	"slices"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// webPorts stay open to a node's users on the node's own address, on every node: a site or a
// reverse proxy of the subscription on the server is there for anyone, and without these it
// would open from everywhere but this node.
var webPorts = []int{80, 443}

// ownPorts is what a node's users reach on the node's own address (nodeapi.DesiredState.OwnPorts):
// webPorts and the ports the admin added; on the panel's own node also the panel's port and
// the subscription's, or a client going through it could not open the panel or refresh the
// subscription. Another node has no need of those: the panel is at another address, which
// the guard does not refuse.
func (s *Syncer) ownPorts(ctx context.Context, n db.Node) ([]int, error) {
	ports := slices.Clone(webPorts)
	for _, p := range n.OwnPorts {
		ports = append(ports, int(p))
	}
	if s.local {
		for _, key := range []string{settings.KeyPanelPort, settings.KeySubPort} {
			p, _, err := settings.Get[int](ctx, s.m.set, key)
			if err != nil {
				return nil, err
			}
			if p > 0 {
				ports = append(ports, p)
			}
		}
	}
	slices.Sort(ports)
	return slices.Compact(ports), nil
}
