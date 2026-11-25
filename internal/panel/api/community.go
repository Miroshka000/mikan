package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// The registry of community addons (internal/panel/addons, community.go) as the marketplace
// offers it: addons of third-party authors that mikan lists and does not vouch for. They
// install as unofficial addons, by digest, as a source's do; the admin confirms the risk on
// every install and every update, and an addon mikan revoked is shown as such and installs no
// more.

// community is the registry's index, or nil when it cannot be read: the marketplace shows the
// rest without it.
func (h *handlers) community(ctx context.Context) *addons.Community {
	if h.d.Addons == nil {
		return nil
	}
	c, err := h.d.Addons.Community(ctx)
	if err != nil {
		// A request the admin left tells nothing of GitHub.
		if ctx.Err() == nil {
			h.d.Log.Warn("addons: community index", "err", err)
		}
		return nil
	}
	return &c
}

// communityIDs is the ids the registry lists: no source takes one of them up.
func communityIDs(c *addons.Community) map[string]bool {
	ids := map[string]bool{}
	if c != nil {
		for _, a := range c.Addons {
			ids[a.ID] = true
		}
	}
	return ids
}

// communityRows is the addons installed from the registry, by id.
func (h *handlers) communityRows(ctx context.Context) (map[string]db.AddonCommunity, error) {
	rows, err := h.d.Store.Q.ListAddonCommunity(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]db.AddonCommunity, len(rows))
	for _, r := range rows {
		out[r.AddonID] = r
	}
	return out, nil
}

// communityMark is how the marketplace marks an addon of the registry.
func communityMark(repo, key string, c *addons.Community, id string) *MarketOrigin {
	o := &MarketOrigin{Kind: originCommunity, Title: shortRepo(repo), Repo: "https://" + repo, Signed: key != ""}
	if c != nil {
		if r, out := c.RevokedFor(id, repo); out {
			o.Revoked = revokedText(r)
		}
	}
	return o
}

func revokedText(r addons.CommunityRevoked) string {
	if r.Reason == "" {
		return "revoked"
	}
	return r.Reason
}

// shortRepo is owner/repo of github.com/owner/repo.
func shortRepo(repo string) string {
	const prefix = "github.com/"
	if len(repo) > len(prefix) && repo[:len(prefix)] == prefix {
		return repo[len(prefix):]
	}
	return repo
}

// installCommunityEntry is the registry's snapshot of id, read again now, refused as the API
// has it: the registry down, no such addon or a revoked one, an id another addon holds, the
// very build running, an author's key the admin has not taken (accept is the fingerprint they
// looked at).
func (h *handlers) installCommunityEntry(ctx context.Context, st addons.State, board sourceBoard, id, accept string) (addons.CommunityAddon, error) {
	rows, err := h.communityRows(ctx)
	if err != nil {
		return addons.CommunityAddon{}, err
	}
	row, ours := rows[id]
	c := h.community(ctx)
	if c == nil {
		// The registry is down: an id it is known to list (or one installed from it) waits for
		// it; any other is no addon at all.
		listed := false
		if k := h.d.Addons.CommunityKnown(); k != nil {
			_, listed = k.Addon(id)
		}
		if ours || listed {
			return addons.CommunityAddon{}, huma.Error502BadGateway("community_unavailable")
		}
		return addons.CommunityAddon{}, huma.Error404NotFound("addon_unknown")
	}
	a, ok := c.Addon(id)
	if !ok {
		// Revoked by its id, or with the whole repository it was installed from.
		if _, out := c.RevokedFor(id, row.Repo); out {
			return addons.CommunityAddon{}, huma.Error409Conflict("addon_revoked")
		}
		return addons.CommunityAddon{}, huma.Error404NotFound("addon_unknown")
	}
	inst, have := st.Adapters[id]
	switch {
	case have && (inst.SourceOf() != addons.SourceCustom || !ours):
		// Another addon under this id: the registry's would take over its tokens and grant.
		return addons.CommunityAddon{}, huma.Error409Conflict("addon_id_taken")
	case !have && board.owners != nil && board.owners[id] != 0:
		return addons.CommunityAddon{}, huma.Error409Conflict("addon_id_taken")
	case have && inst.Status == "running" && inst.Digest == a.Digest:
		return addons.CommunityAddon{}, huma.Error409Conflict("addon_current")
	case ours && row.Key != a.Key && (a.Key == "" || accept != addons.Fingerprint(a.Key)):
		// Another key, or a release no longer signed: an update by whoever holds the
		// repository now. A key the admin compared is taken; no signature at all never is.
		return addons.CommunityAddon{}, huma.Error409Conflict("author_key_changed")
	}
	return a, nil
}
