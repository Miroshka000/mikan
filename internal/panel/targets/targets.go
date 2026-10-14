// Package targets is how the panel looks for REALITY camouflage sites and checks one: the
// admin API and `mikan admin targets` both go through it, so what the panel may dial on the
// admin's word is decided in one place. The node checks and scans from its own network
// when it can, because the round trips are the ones REALITY will see; the panel does it
// itself when the node cannot (a node older than 0.3, one out of reach).
package targets

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

const (
	// ScanLimit is how many sites a scan returns, the fastest first.
	ScanLimit = 12
	// CheckTimeout is how long the panel's own check of one site may take.
	CheckTimeout = 15 * time.Second
	// ScanTimeout is how long the panel's own scan of a /24 may take; what it found by
	// then is the answer.
	ScanTimeout = 30 * time.Second
)

var (
	// ErrBadDest: the destination is not host:port.
	ErrBadDest = errors.New("bad_dest")
	// ErrPrivateDest: the destination is a host this server must not dial on the admin's
	// word: an internal address, localhost or a bare name. Only the panel's own port on
	// loopback (self-steal) is allowed.
	ErrPrivateDest = errors.New("reality_dest_private")
	// ErrNoAddress: the server's address is not set, so there is nothing to scan around.
	ErrNoAddress = errors.New("the server's address is not set")
)

// NoIPError: there is no IPv4 address to scan around. Err says why: ErrNoAddress,
// scan.ErrNotIPv4, scan.ErrNoIPv4, or the scan's refusal of a non-public address.
type NoIPError struct {
	Host string
	Err  error
}

func (e *NoIPError) Error() string {
	switch {
	case errors.Is(e.Err, ErrNoAddress):
		return "the server's address is not set: run `mikan admin bootstrap`"
	case errors.Is(e.Err, scan.ErrNotIPv4):
		return e.Host + " is not an IPv4 address"
	case errors.Is(e.Err, scan.ErrNoIPv4):
		return e.Host + " has no IPv4 address"
	}
	return fmt.Sprintf("%s: %v", e.Host, e.Err)
}

func (e *NoIPError) Unwrap() error { return e.Err }

// Deps is what the checks need from the process that runs them.
type Deps struct {
	Settings *settings.Settings
	// Resolve looks a name up for what the panel dials; nil is the system's resolver. The
	// answer is checked: a name that leads inside is refused as private.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// CheckTarget and ScanTargets ask the node to do it from its own network. Either is nil
	// when the node cannot be reached, and an error from it sends the panel to do the work
	// itself.
	CheckTarget func(ctx context.Context, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error)
	ScanTargets func(ctx context.Context, req nodeapi.TargetScanRequest) (nodeapi.TargetScan, error)
	// Neighbors is the panel's own scan; nil is scan.Neighbors. Tests put a fake here: the
	// real one opens ~250 connections to public addresses.
	Neighbors func(ctx context.Context, ip string, limit int, o scan.Options) ([]scan.Result, int, error)
}

// PanelPort is the panel's own port: the one loopback address a check may use.
func PanelPort(ctx context.Context, set *settings.Settings) int {
	p, _, _ := settings.Get[int](ctx, set, settings.KeyPanelPort)
	return p
}

// options is where the panel's own checks may connect: public addresses, and its own port
// on loopback (self-steal).
func (d Deps) options(ctx context.Context) scan.Options {
	return scan.Options{LoopbackPort: PanelPort(ctx, d.Settings), Resolve: d.Resolve}
}

// Check tests dest as a REALITY target for clients sending sni ("" = the host of dest). The
// check dials from the server, so internal addresses are not its business, except the
// panel's own port (self-steal): those are ErrPrivateDest, a malformed dest ErrBadDest.
func (d Deps) Check(ctx context.Context, dest, sni string) (scan.Result, error) {
	host, port, err := net.SplitHostPort(dest)
	if err != nil || host == "" {
		return scan.Result{}, ErrBadDest
	}
	selfSteal := (host == "127.0.0.1" || host == "localhost") && port == strconv.Itoa(PanelPort(ctx, d.Settings))
	if !selfSteal && !proto.PublicHost(host) {
		return scan.Result{}, ErrPrivateDest
	}
	if selfSteal {
		dest = net.JoinHostPort("127.0.0.1", port) // the check takes the literal address only
	}
	return d.check(ctx, dest, sni), nil
}

// check asks the node to test the site, or tests it from here when the node cannot tell.
func (d Deps) check(ctx context.Context, dest, sni string) scan.Result {
	if d.CheckTarget != nil {
		if r, err := d.CheckTarget(ctx, nodeapi.TargetCheckRequest{Dest: dest, SNI: sni}); err == nil {
			return r
		}
	}
	cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	return scan.Check(cctx, dest, sni, d.options(ctx))
}

// ScanRequest says where to look.
type ScanRequest struct {
	Node db.Node
	// SelfSteal asks for the panel's own domain to be checked too: the panel with its
	// Let's Encrypt certificate is a target for the panel's own node. The caller says
	// whether that is worth offering.
	SelfSteal bool
}

// Scan is what a scan found.
type Scan struct {
	IP        string
	Scanned   int
	FromNode  bool
	SelfSteal *scan.Result
	// Results are the sites by RTT, the fastest first; never nil.
	Results []scan.Result
}

// Scan looks for sites in the /24 around the node's public address. The panel's own node
// is looked up by the panel's address (the domain when there is no address); a remote node
// by its own.
func (d Deps) Scan(ctx context.Context, req ScanRequest) (Scan, error) {
	n := req.Node
	host, domain := n.PublicHost, ""
	local := n.Address == ""
	if local {
		var err error
		if host, err = d.Settings.String(ctx, settings.KeyPublicHost); err != nil {
			return Scan{}, err
		}
		if domain, err = d.Settings.String(ctx, settings.KeyDomain); err != nil {
			return Scan{}, err
		}
		if host == "" {
			host = domain
		}
	}
	res := Scan{Results: []scan.Result{}}
	if host == "" {
		return res, &NoIPError{Err: ErrNoAddress}
	}
	ip, err := scan.ResolveIPv4(ctx, host)
	if err != nil {
		return res, &NoIPError{Host: host, Err: err}
	}
	res.IP = ip
	// Self-steal: the SNI is the server's own domain and matches its IP, the target is the
	// panel with its Let's Encrypt certificate.
	if local && domain != "" && req.SelfSteal {
		r := d.check(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(PanelPort(ctx, d.Settings))), domain)
		res.SelfSteal = &r
	}
	// Past the /24 only when the admin allowed it: many more connections, and up to
	// scan.WideEnough sites to choose from.
	wide, err := d.Settings.On(ctx, settings.WideScan)
	if err != nil {
		return res, err
	}
	limit := ScanLimit
	if wide {
		limit = scan.WideEnough
	}
	// The node scans its own network, so RTTs are what REALITY will see. A node older than
	// 0.3 cannot: then the panel scans for it.
	if d.ScanTargets != nil {
		if r, err := d.ScanTargets(ctx, nodeapi.TargetScanRequest{IP: ip, Limit: limit, Wide: wide}); err == nil {
			res.Scanned, res.FromNode = r.Scanned, true
			if r.Results != nil {
				res.Results = r.Results
			}
			return res, nil
		}
	}
	neighbors := d.Neighbors
	if neighbors == nil {
		neighbors = scan.Neighbors
	}
	took := ScanTimeout
	if wide {
		took = nodeapi.WideScanTime
	}
	sctx, cancel := context.WithTimeout(ctx, took)
	defer cancel()
	// Public addresses only: never the neighborhood of this host's own network.
	found, scanned, err := neighbors(sctx, ip, limit, scan.Options{Wide: wide})
	if err != nil && sctx.Err() == nil {
		return res, &NoIPError{Host: ip, Err: err}
	}
	res.Scanned = scanned
	if found != nil {
		res.Results = found
	}
	return res, nil
}
