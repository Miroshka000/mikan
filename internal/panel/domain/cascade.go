package domain

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// Cascades: an inbound of node A may leave the internet through node B. B runs a
// hidden relay listener (VLESS REALITY) with a key per source node; the relay's own
// traffic leaves B directly, through B's WARP, or goes on to a further node.

var (
	ErrExitSelf  = errors.New("exit_self")     // a node cannot be its own exit
	ErrExitCycle = errors.New("exit_cycle")    // the chain would come back to where it started
	ErrExitLong  = errors.New("exit_too_long") // the chain has more than maxChain hops
	ErrExitOff   = errors.New("exit_off")      // the exit node is disabled
	ErrNoPort    = errors.New("relay_no_port")

	// ErrRelayChanged: the relay is not on the port a move of it was decided for.
	ErrRelayChanged = errors.New("relay_changed")
)

// maxChain bounds a cascade: more hops than this only add delay.
const maxChain = 8

// CheckExit says whether traffic of node from may leave through node to: to exists, is
// on, is not from, and the relays after it never lead back to from.
func CheckExit(ctx context.Context, q *db.Queries, from, to int64) error {
	if from == to {
		return ErrExitSelf
	}
	n, err := q.GetNode(ctx, to)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if n.Enabled == 0 {
		return ErrExitOff
	}
	seen := map[int64]bool{from: true}
	cur := to
	for range maxChain {
		if seen[cur] {
			return ErrExitCycle
		}
		seen[cur] = true
		r, err := q.GetNodeRelay(ctx, cur)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !r.ExitNodeID.Valid {
			return nil
		}
		if err != nil {
			return err
		}
		cur = r.ExitNodeID.Int64
	}
	return ErrExitLong
}

// RelayPort picks the port of a cascade relay: the first free one of PortPool (installers
// open those in the firewall) that is not in skip, else a high one.
func RelayPort(ports PortMap, skip map[string]bool) (int, bool) {
	for _, p := range PortPool {
		if !skip[strconv.Itoa(p)] && ports.Free(p, "tcp") {
			return p, true
		}
	}
	for range 64 {
		if p := 30000 + rand.IntN(30000); !skip[strconv.Itoa(p)] && ports.Free(p, "tcp") {
			return p, true
		}
	}
	return 0, false
}

// EnsureRelay gives node its relay listener when it has none, on a free TCP port; host is
// what the node's server reports as listening, nil when it did not.
func EnsureRelay(ctx context.Context, q *db.Queries, node db.Node, now time.Time, host *nodeapi.HostPorts) (db.NodeRelay, error) {
	if r, err := q.GetNodeRelay(ctx, node.ID); err == nil {
		return r, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return db.NodeRelay{}, err
	}
	ports, err := NodePorts(ctx, q, node)
	if err != nil {
		return db.NodeRelay{}, err
	}
	port, ok := RelayPort(ports.WithHost(host), nil)
	if !ok {
		return db.NodeRelay{}, ErrNoPort
	}
	reality, err := presets.NewReality(presets.DefaultDest)
	if err != nil {
		return db.NodeRelay{}, err
	}
	t := proto.Template{"type": "vless", "reality-config": reality}
	// The relay shows the site the node's own protocols show (RelayTarget); without one the
	// tuner finds a site next to the server.
	ins, err := q.ListNodeInbounds(ctx, node.ID)
	if err != nil {
		return db.NodeRelay{}, err
	}
	if dest, sni, ok := RelayTarget(ins); ok {
		if err := presets.SetDest(t, dest, sni); err != nil {
			return db.NodeRelay{}, err
		}
	}
	selfSteal := 0
	if dest, _ := presets.Dest(t); onServer(dest) {
		// Only the panel's own node shows the panel's HTTPS, and only its protocols did.
		if _, p, err := net.SplitHostPort(dest); err == nil {
			selfSteal, _ = strconv.Atoi(p)
		}
	}
	if err := proto.Validate(t, proto.Options{SelfStealPort: selfSteal}); err != nil {
		return db.NodeRelay{}, err
	}
	return q.CreateNodeRelay(ctx, db.CreateNodeRelayParams{NodeID: node.ID, Port: strconv.Itoa(port), Config: proto.Marshal(t), CreatedAt: now.Unix()})
}

// MoveRelay moves node's relay from port from to port to, the way another program that
// holds from calls for. It is ErrRelayChanged when the relay is no longer on from, and a
// *PortInUseError when something of mikan's holds to. Sources of the relay follow with the
// next sync of the nodes: they read the port from the row.
func MoveRelay(ctx context.Context, st *store.Store, node db.Node, from, to string) error {
	return st.Tx(ctx, func(q *db.Queries) error {
		r, err := q.GetNodeRelay(ctx, node.ID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && r.Port != from {
			return ErrRelayChanged
		}
		if err != nil {
			return err
		}
		if err := CheckPort(ctx, q, node, to, "tcp", PortHolder{Kind: PortRelay}); err != nil {
			return err
		}
		return q.SetNodeRelayPort(ctx, db.SetNodeRelayPortParams{Port: to, NodeID: node.ID})
	})
}

// UseExit lets node src leave through node exit: the chain is checked, exit gets its
// relay and src a key there. It runs on the caller's transaction; host tells what listens
// on exit's server, nil when nobody knows.
func UseExit(ctx context.Context, q *db.Queries, src, exit int64, now time.Time, host HostLookup) error {
	if err := CheckExit(ctx, q, src, exit); err != nil {
		return err
	}
	x, err := q.GetNode(ctx, exit)
	if err != nil {
		return err
	}
	if _, err := EnsureRelay(ctx, q, x, now, host.of(exit)); err != nil {
		return err
	}
	_, err = RelayUser(ctx, q, exit, src)
	return err
}

// ExitUse is what leaves the internet through a node: inbounds of other nodes, enabled
// or not, and nodes whose relay goes on through it. Deleting the node would quietly send
// their traffic straight out.
type ExitUse struct {
	Inbounds []db.Inbound
	Relays   []int64 // the nodes whose relay leaves through it
}

// ExitUsesOf lists what leaves through node id.
func ExitUsesOf(ctx context.Context, q *db.Queries, id int64) (ExitUse, error) {
	var u ExitUse
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return u, err
	}
	for _, in := range ins {
		if in.ExitNodeID.Valid && in.ExitNodeID.Int64 == id {
			u.Inbounds = append(u.Inbounds, in)
		}
	}
	relays, err := q.ListNodeRelays(ctx)
	if err != nil {
		return u, err
	}
	for _, r := range relays {
		if r.ExitNodeID.Valid && r.ExitNodeID.Int64 == id {
			u.Relays = append(u.Relays, r.NodeID)
		}
	}
	return u, nil
}

// RelayUser is the key node src uses at exit's relay, made on first use.
func RelayUser(ctx context.Context, q *db.Queries, exit, src int64) (string, error) {
	id, err := q.GetRelayUser(ctx, db.GetRelayUserParams{ExitNodeID: exit, SrcNodeID: src})
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = newUUID()
	if err := q.AddRelayUser(ctx, db.AddRelayUserParams{ExitNodeID: exit, SrcNodeID: src, Uuid: id}); err != nil {
		return "", err
	}
	return q.GetRelayUser(ctx, db.GetRelayUserParams{ExitNodeID: exit, SrcNodeID: src})
}

// RelayUserName is how a source node shows in the relay's users.
func RelayUserName(src int64) string { return "relay-" + strconv.FormatInt(src, 10) }

// RelaySource is the source node a relay user (RelayUserName) stands for; false for any
// other name.
func RelaySource(name string) (int64, bool) {
	rest, ok := strings.CutPrefix(name, "relay-")
	id, err := strconv.ParseInt(rest, 10, 64)
	if !ok || err != nil || id <= 0 || RelayUserName(id) != name {
		return 0, false
	}
	return id, true
}

// MovedExit is an inbound or a relay that went out through a node being deleted, and where
// it goes now: another node (To), or straight out (To 0) when none is left.
type MovedExit struct {
	Inbound string `json:"inbound,omitempty"` // the inbound's name; "" for a relay
	Node    int64  `json:"node"`              // the node the inbound or the relay is on
	To      int64  `json:"to"`
}

// MoveExits sends what went out through node from out through another node instead: the
// first remote node on, other than from, that the chain allows (CheckExit). The panel's own
// node is never picked: it is the one the traffic came to leave. With none, straight out.
// The new exit gets its relay and key as with UseExit.
func MoveExits(ctx context.Context, q *db.Queries, from int64, now time.Time, host HostLookup) ([]MovedExit, error) {
	uses, err := ExitUsesOf(ctx, q, from)
	if err != nil || len(uses.Inbounds) == 0 && len(uses.Relays) == 0 {
		return nil, err
	}
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	pick := func(src int64) (int64, error) {
		for _, n := range nodes {
			if n.ID == from || n.ID == src || n.Address == "" || n.Enabled == 0 {
				continue
			}
			switch err := CheckExit(ctx, q, src, n.ID); {
			case err == nil:
				return n.ID, UseExit(ctx, q, src, n.ID, now, host)
			case errors.Is(err, ErrExitCycle), errors.Is(err, ErrExitLong), errors.Is(err, ErrExitOff), errors.Is(err, ErrExitSelf):
			default:
				return 0, err
			}
		}
		return 0, nil
	}
	var out []MovedExit
	for _, in := range uses.Inbounds {
		to, err := pick(in.NodeID)
		if err != nil {
			return nil, err
		}
		if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: sql.NullInt64{Int64: to, Valid: to != 0}, Outbound: "direct", ID: in.ID}); err != nil {
			return nil, err
		}
		out = append(out, MovedExit{Inbound: in.Name, Node: in.NodeID, To: to})
	}
	for _, src := range uses.Relays {
		to, err := pick(src)
		if err != nil {
			return nil, err
		}
		if err := q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: sql.NullInt64{Int64: to, Valid: to != 0}, NodeID: src}); err != nil {
			return nil, err
		}
		out = append(out, MovedExit{Node: src, To: to})
	}
	return out, nil
}
