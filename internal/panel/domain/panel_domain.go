package domain

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The panel's own node may show the panel's own HTTPS as its REALITY site (self-steal): the
// name it is shown under is the panel's domain. When the domain changes, the site follows
// it, or every check of it would find a certificate for another name and the tuner would
// move the inbounds to a site of the server's network.

// FollowPanelDomain moves the panel's own node from the panel's domain from to its new
// domain to: its REALITY inbounds that show the panel's HTTPS under from show it under to,
// from kept after it for the clients that have not fetched their subscription since; a
// relay the admin pointed at the panel's HTTPS under from takes to. It returns the inbounds
// it changed; one that cannot be changed is left as it is, its error joined to the others.
func (s *Inbounds) FollowPanelDomain(ctx context.Context, from, to string) ([]db.Inbound, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || strings.EqualFold(from, to) {
		return nil, nil
	}
	return s.renamePanelSite(ctx, func(names []string) []string {
		if !containsFold(names, from) || len(names) > 0 && strings.EqualFold(names[0], to) {
			return nil
		}
		return append([]string{to}, slices.DeleteFunc(slices.Clone(names), func(n string) bool { return strings.EqualFold(n, to) })...)
	}, func(sni string) string {
		if strings.EqualFold(sni, from) {
			return to
		}
		return ""
	})
}

// ForgetPanelName takes a former name of the panel off its own node's self-steal inbounds:
// the panel answers at it no more (acme.Formers let it go). An inbound shown under that
// name alone stays as it is: the tuner finds it broken and replaces its site.
func (s *Inbounds) ForgetPanelName(ctx context.Context, name string) ([]db.Inbound, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	return s.renamePanelSite(ctx, func(names []string) []string {
		if len(names) < 2 || strings.EqualFold(names[0], name) || !containsFold(names, name) {
			return nil
		}
		return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return strings.EqualFold(n, name) })
	}, func(string) string { return "" })
}

// renamePanelSite gives the self-steal inbounds of the panel's own node the names rename
// returns for theirs (nil: as they are), and its relay, when the admin set it to the
// panel's HTTPS, the name relay returns for its own ("": as it is).
func (s *Inbounds) renamePanelSite(ctx context.Context, rename func([]string) []string, relay func(string) string) ([]db.Inbound, error) {
	nodes, err := s.st.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(nodes, func(n db.Node) bool { return n.Address == "" })
	if i < 0 {
		return nil, nil
	}
	local := nodes[i]
	ins, err := s.st.Q.ListNodeInbounds(ctx, local.ID)
	if err != nil {
		return nil, err
	}
	var changed []db.Inbound
	var errs []error
	for _, in := range ins {
		t, err := proto.Parse(in.Config)
		if err != nil {
			continue
		}
		dest, names := presets.Dest(t)
		if !onServer(dest) {
			continue
		}
		next := rename(names)
		if next == nil {
			continue
		}
		if err := presets.SetServerNames(t, next); err != nil {
			errs = append(errs, err)
			continue
		}
		config := proto.Marshal(t)
		_, row, err := s.Update(ctx, in.ID, InboundPatch{Config: &config})
		if err != nil {
			errs = append(errs, errors.New(in.Name+": "+err.Error()))
			continue
		}
		changed = append(changed, row)
	}
	r, err := s.st.Q.GetNodeRelay(ctx, local.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		errs = append(errs, err)
	case r.AutoSni != 0:
		// The panel keeps this relay's site: it takes the site of the node's protocols.
		if _, err := SetRelayAuto(ctx, s.st.Q, local.ID); err != nil {
			errs = append(errs, err)
		}
	default:
		if dest, sni := RelayTargetOf(r); onServer(dest) {
			if to := relay(sni); to != "" {
				if _, err := SetRelayTarget(ctx, s.st.Q, local.ID, dest, to, false); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return changed, errors.Join(errs...)
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(e string) bool { return strings.EqualFold(e, s) })
}
