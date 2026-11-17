package api

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/frontpage"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/targets"
)

// Loading the installation a security preset is worked out for.

// postureSpare is how many loopback ports a plan looks for on a node: more than a node has
// inbounds on 443.
const postureSpare = 8

// posture loads the installation. With probe the panel's own names are checked as REALITY
// sites (a connection to the panel's own HTTPS for each); without, it only reads the
// database.
func (h *handlers) posture(ctx context.Context, probe bool) (posture, error) {
	var p posture
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return p, err
	}
	for _, n := range nodes {
		ins, err := h.d.Store.Q.ListNodeInbounds(ctx, n.ID)
		if err != nil {
			return p, err
		}
		pn := postureNode{node: n, inbounds: ins, byKey: h.routesByKey(n)}
		m, err := domain.NodePorts(ctx, h.d.Store.Q, n)
		if err != nil {
			return p, err
		}
		for port := domain.SpareLo; port <= domain.SpareHi && len(pn.spare) < postureSpare; port++ {
			if m.Free(port, "tcp") {
				pn.spare = append(pn.spare, strconv.Itoa(port))
			}
		}
		p.nodes = append(p.nodes, pn)
	}
	if p.page, err = h.d.Settings.String(ctx, settings.KeySiteTemplate); err != nil {
		return p, err
	}
	seed, err := h.d.Settings.String(ctx, settings.KeyFrontSeed)
	if err != nil {
		return p, err
	}
	p.pagePick = frontpage.Pick(seed)
	p.panelPort = targets.PanelPort(ctx, h.d.Settings)
	deps := h.targetDeps(0)
	for _, key := range []string{settings.KeyDomain, settings.KeySubDomain} {
		name, err := h.d.Settings.String(ctx, key)
		if err != nil {
			return p, err
		}
		if name = strings.ToLower(strings.TrimSuffix(name, ".")); name == "" || slices.Contains(p.own, name) {
			continue
		}
		if probe && !deps.Own(ctx, name).OK {
			continue
		}
		p.own = append(p.own, name)
	}
	return p, nil
}
