// Package hello is the panel's side of a node's hello (see nodehello): it checks the
// hello, dials the node back and answers signed. Whatever is not a valid hello from one of
// the panel's nodes is left to the router, which answers it like any other unknown path.
package hello

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
)

// Node is a node of the panel as a hello needs it.
type Node struct {
	ID int64
	// Host is the node's address in the panel: an IP or a name.
	Host string
}

// Deps are what the handler needs from the panel.
type Deps struct {
	// Lookup finds the node whose certificate has this pin.
	Lookup func(ctx context.Context, pin string) (Node, bool)
	// Dial reaches the node now and says how it went (OK, or a link code and its params).
	Dial func(ctx context.Context, id int64) nodehello.Result
	// Record keeps the hello for the Nodes page.
	Record func(id int64, r nodehello.Result, at time.Time)
	// Panel is the client certificate the nodes pin: it signs the answers.
	Panel func() (nodetls.Pair, error)
	// ClientIP is where a request comes from (the proxy's word when it is trusted).
	ClientIP func(r *http.Request) string
	// Resolve looks a node's name up, to compare it with where its hello came from; nil
	// compares only addresses.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Now     func() time.Time
}

// Limits of the hellos the panel takes from one address: a node retries a few times in
// its first minute, nobody needs more.
const (
	perIP     = 20
	perWindow = 10 * time.Minute
	maxIPs    = 4096
	maxNonces = 20000
)

type Handler struct {
	d Deps

	mu     sync.Mutex
	hits   map[string][]time.Time
	nonces map[string]time.Time // nonce → when it may be forgotten
}

func New(d Deps) *Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handler{d: d, hits: map[string][]time.Time{}, nonces: map[string]time.Time{}}
}

// Serve answers a valid hello and returns true; for anything else it writes nothing and
// returns false, and the router serves the path as it serves any path it does not know.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != nodehello.Path {
		return false
	}
	now := h.d.Now()
	ip := h.d.ClientIP(r)
	if !h.allow(ip, now) {
		return false
	}
	var req nodehello.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, nodehello.MaxBody)).Decode(&req); err != nil {
		return false
	}
	pin, err := nodehello.Verify(req, now)
	if err != nil {
		return false
	}
	n, ok := h.d.Lookup(r.Context(), pin)
	if !ok || !h.fresh(req.Nonce, now) {
		return false
	}
	panel, err := h.d.Panel()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res := h.d.Dial(ctx, n.ID)
	res.SeenIP, res.Host = ip, n.Host
	if h.differs(ctx, n.Host, ip) {
		if res.Params == nil {
			res.Params = map[string]string{}
		}
		res.Params[nodehello.IPDiffers] = "1"
	}
	if h.d.Record != nil {
		h.d.Record(n.ID, res, now)
	}
	out, err := nodehello.Reply(panel, req.Nonce, res)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
	return true
}

// allow counts a request of ip and says whether it is within the limit. Every request
// counts, a stranger's too.
func (h *Handler) allow(ip string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := now.Add(-perWindow)
	if len(h.hits) >= maxIPs {
		for k, ts := range h.hits {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(h.hits, k)
			}
		}
		if len(h.hits) >= maxIPs {
			return false
		}
	}
	kept := h.hits[ip][:0]
	for _, t := range h.hits[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= perIP {
		h.hits[ip] = kept
		return false
	}
	h.hits[ip] = append(kept, now)
	return true
}

// fresh says whether a nonce is new, and remembers it for as long as its hello could be
// replayed.
func (h *Handler) fresh(nonce string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.nonces[nonce]; seen {
		return false
	}
	if len(h.nonces) >= maxNonces {
		for k, until := range h.nonces {
			if now.After(until) {
				delete(h.nonces, k)
			}
		}
		if len(h.nonces) >= maxNonces {
			return false
		}
	}
	h.nonces[nonce] = now.Add(2 * nodehello.Window)
	return true
}

// differs says whether the hello came from an address the node's host is not.
func (h *Handler) differs(ctx context.Context, host, seen string) bool {
	from, err := netip.ParseAddr(seen)
	if err != nil || host == "" {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap() != from.Unmap()
	}
	if h.d.Resolve == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := h.d.Resolve(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if a.Unmap() == from.Unmap() {
			return false
		}
	}
	// A node with an IPv6 too may say hello from either: only an address of the same
	// family that matches none counts.
	for _, a := range addrs {
		if a.Unmap().Is4() == from.Unmap().Is4() {
			return true
		}
	}
	return false
}

// SystemResolve looks a name up with the system's resolver.
func SystemResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}
