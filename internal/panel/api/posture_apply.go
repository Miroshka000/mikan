package api

import (
	"context"
	"errors"
	"slices"

	"mikan/internal/frontpage"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// Applying a security preset: the changes the admin was shown, written one inbound and one node at a time.

// applyPosture writes the changes the admin was shown: each inbound and each node in one
// write of its own, checked as a change made by hand is. A change that is refused is
// answered with its reason and the rest go on; a node whose protocol could not leave port
// 443 does not get the router that needs that port.
func (h *handlers) applyPosture(ctx context.Context, in *postureApplyInput) (*posturePlanOutput, error) {
	if err := requireSession(ctx, "preset"); err != nil {
		return nil, err
	}
	changes := slices.Clone(in.Body.Changes)
	on, off := true, false

	// The inbounds: everything a preset changes in one of them is one patch.
	var order []int64
	patches := map[int64]*domain.InboundPatch{}
	rows := map[int64][]int{}
	for i, c := range changes {
		if c.Manual || c.InboundID == 0 {
			continue
		}
		p := patches[c.InboundID]
		if p == nil {
			p = &domain.InboundPatch{}
			patches[c.InboundID] = p
			order = append(order, c.InboundID)
		}
		switch c.Code {
		case "site":
			dest, sni := c.Params["dest"], c.Params["sni"]
			p.Dest, p.ServerName = &dest, &sni
		case "port":
			port := c.Params["to"]
			p.Port = &port
		case "through_443":
			p.SNI443 = &on
		case "direct":
			p.SNI443 = &off
		case "auto_off":
			p.AutoPort, p.AutoSNI = &off, &off
		case "disable":
			p.Enabled = &off
		default:
			return nil, fieldErr("body.changes", "bad_change")
		}
		rows[c.InboundID] = append(rows[c.InboundID], i)
	}
	stuck := map[int64]bool{} // nodes with an inbound left on port 443
	applied := 0
	for _, id := range order {
		if _, _, err := h.d.Inbounds.Update(ctx, id, *patches[id]); err != nil {
			for _, i := range rows[id] {
				changes[i].Error = errCode(err)
				stuck[changes[i].Node] = stuck[changes[i].Node] || changes[i].Code == "port"
			}
			continue
		}
		applied++
	}

	// The nodes.
	var nodeOrder []int64
	nodePatches := map[int64]*domain.NodePatch{}
	nodeRows := map[int64][]int{}
	for i, c := range changes {
		if c.Manual || c.InboundID != 0 || c.Code == "panel_page" {
			continue
		}
		p := nodePatches[c.Node]
		if p == nil {
			p = &domain.NodePatch{}
			nodePatches[c.Node] = p
			nodeOrder = append(nodeOrder, c.Node)
		}
		switch c.Code {
		case "router_on":
			if stuck[c.Node] {
				changes[i].Error = "port_443_busy"
				continue
			}
			p.SNIRouter = &on
		case "router_by_key":
			p.SNIByKey = &on
		case "router_off":
			p.SNIRouter = &off
		case "api_lockdown":
			p.APILockdown = &on
		case "front_page":
			page := c.Params["page"]
			p.FrontPage = &page
		default:
			return nil, fieldErr("body.changes", "bad_change")
		}
		nodeRows[c.Node] = append(nodeRows[c.Node], i)
	}
	for _, id := range nodeOrder {
		if len(nodeRows[id]) == 0 {
			continue
		}
		if _, err := domain.UpdateNode(ctx, h.d.Store, id, *nodePatches[id], h.d.Now()); err != nil {
			for _, i := range nodeRows[id] {
				changes[i].Error = errCode(err)
			}
			continue
		}
		applied++
	}

	// The panel's own page.
	for i, c := range changes {
		if c.Manual || c.Code != "panel_page" {
			continue
		}
		page := c.Params["page"]
		if frontpage.Page(page) == nil {
			changes[i].Error = "bad_front_page"
			continue
		}
		if err := settings.Set(ctx, h.d.Settings, settings.KeySiteTemplate, page); err != nil {
			return nil, err
		}
		applied++
	}
	if applied > 0 {
		h.nodesChanged()
		h.d.Changes.SlotsChanged()
	}
	failed := 0
	for _, c := range changes {
		if c.Error != "" {
			failed++
		}
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "security.preset", "settings", in.Preset, map[string]any{"changes": len(changes), "failed": failed})
	out := &posturePlanOutput{}
	out.Body.Changes = changesOrNone(changes)
	return out, nil
}

// errCode is a refusal's word for a line of the plan: the domain's own where it has one,
// the error's text otherwise.
func errCode(err error) string {
	var edit *domain.EditError
	if errors.As(err, &edit) {
		return edit.Err.Error()
	}
	return err.Error()
}
