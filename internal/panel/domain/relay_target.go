package domain

import (
	"context"
	"net"
	"strings"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// A relay's REALITY site is what the source nodes' connections show on the way to its node.
// One that shows www.microsoft.com from a VPS stands out next to the node's own protocols,
// which show a site of the server's network or the server's own domain. A relay the panel
// keeps by itself (auto_sni) takes theirs; on a node without them, the tuner finds a site
// next to the server. The admin may set a site of their own instead.

// The sources of a relay's site, as the Cascade window names them.
const (
	RelayTargetOwn      = "own"      // the server's own domain, a site on the server itself
	RelayTargetProtocol = "protocol" // the site the node's protocols show
	RelayTargetPicked   = "picked"   // a site next to the server the panel found
	RelayTargetDefault  = "default"  // presets.DefaultDest: nothing better is known yet
	RelayTargetManual   = "manual"   // the admin's
)

// RelayTarget is the REALITY site the relay of a node takes from the node's inbounds: the
// server's own domain when an enabled inbound shows it, else the site the first enabled
// REALITY inbound shows. A site left at presets.DefaultDest says nothing about the server and
// is passed over. ok is false when there is none to take.
func RelayTarget(inbounds []db.Inbound) (dest, sni string, ok bool) {
	type site struct{ dest, sni string }
	var own, first *site
	for _, in := range inbounds {
		if in.Enabled == 0 {
			continue
		}
		t, err := proto.Parse(in.Config)
		if err != nil {
			continue
		}
		d, names := presets.Dest(t)
		if d == "" || len(names) == 0 || strings.EqualFold(d, presets.DefaultDest) {
			continue
		}
		s := &site{d, names[0]}
		if own == nil && onServer(d) {
			own = s
		}
		if first == nil {
			first = s
		}
	}
	switch {
	case own != nil:
		return own.dest, own.sni, true
	case first != nil:
		return first.dest, first.sni, true
	}
	return "", "", false
}

// onServer: the site is on the server itself (the panel's own HTTPS for its own node).
func onServer(dest string) bool {
	host, _, err := net.SplitHostPort(dest)
	return err == nil && (host == "127.0.0.1" || host == "localhost" || host == "::1")
}

// RelayTargetOf is the site a relay shows now: its dest and the name clients send.
func RelayTargetOf(r db.NodeRelay) (dest, sni string) {
	t, err := proto.Parse(r.Config)
	if err != nil {
		return "", ""
	}
	dest, names := presets.Dest(t)
	if len(names) > 0 {
		sni = names[0]
	}
	return dest, sni
}

// RelayTargetSource says where the site of a relay comes from (RelayTarget* constants);
// inbounds are its node's.
func RelayTargetSource(r db.NodeRelay, inbounds []db.Inbound) string {
	dest, sni := RelayTargetOf(r)
	switch want, wantSNI, ok := RelayTarget(inbounds); {
	case r.AutoSni == 0:
		return RelayTargetManual
	case onServer(dest):
		return RelayTargetOwn
	case ok && sameTarget(dest, sni, want, wantSNI):
		return RelayTargetProtocol
	case strings.EqualFold(dest, presets.DefaultDest):
		return RelayTargetDefault
	}
	return RelayTargetPicked
}

func sameTarget(dest, sni, otherDest, otherSNI string) bool {
	return strings.EqualFold(dest, otherDest) && strings.EqualFold(sni, otherSNI)
}

// SetRelayTarget points node's relay at a REALITY site, dest as host:port and sni the name
// clients send ("" for the host of dest), and says whether the panel keeps the site by
// itself from now on. The relay keeps its keys, so the source nodes take the new name with
// their next sync. changed is false when the relay was so already.
func SetRelayTarget(ctx context.Context, q *db.Queries, node int64, dest, sni string, auto bool) (changed bool, err error) {
	r, err := q.GetNodeRelay(ctx, node)
	if err != nil {
		return false, err
	}
	t, err := proto.Parse(r.Config)
	if err != nil {
		return false, err
	}
	oldDest, oldSNI := RelayTargetOf(r)
	if err := presets.SetDest(t, dest, sni); err != nil {
		return false, err
	}
	newDest, newNames := presets.Dest(t)
	flag := Flag(auto)
	if r.AutoSni == flag && len(newNames) > 0 && sameTarget(oldDest, oldSNI, newDest, newNames[0]) {
		return false, nil
	}
	return true, q.SetNodeRelayTarget(ctx, db.SetNodeRelayTargetParams{Config: proto.Marshal(t), AutoSni: flag, NodeID: node})
}

// SetRelayAuto hands node's relay site back to the panel: it takes the site of the node's
// protocols at once when they show one; otherwise the default, which the tuner replaces with
// a site next to the server as soon as a node leaves through this one. The admin's former
// site stays out of it: the panel did not pick it.
func SetRelayAuto(ctx context.Context, q *db.Queries, node int64) (bool, error) {
	ins, err := q.ListNodeInbounds(ctx, node)
	if err != nil {
		return false, err
	}
	dest, sni, ok := RelayTarget(ins)
	if !ok {
		dest, sni = presets.DefaultDest, ""
	}
	return SetRelayTarget(ctx, q, node, dest, sni, true)
}

// CheckRelayTarget refuses a site the relay of node may not show, as for an inbound: one that
// is malformed or private, or a name that leads to a private network. Only the panel's own
// node may show the panel's own HTTPS (self-steal).
func (s *Inbounds) CheckRelayTarget(ctx context.Context, node db.Node, dest, sni string) error {
	reality, err := presets.NewReality(presets.DefaultDest)
	if err != nil {
		return err
	}
	t := proto.Template{"type": "vless", "reality-config": reality}
	if err := presets.SetDest(t, strings.TrimSpace(dest), strings.TrimSpace(sni)); err != nil {
		return err
	}
	selfSteal, err := s.selfStealPort(ctx, node)
	if err != nil {
		return err
	}
	if err := proto.Validate(t, proto.Options{SelfStealPort: selfSteal}); err != nil {
		return err
	}
	return s.checkDestResolves(ctx, t)
}
