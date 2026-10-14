// Package scan checks sites as REALITY targets. The node borrows the target's TLS
// handshake for every probe of its port, so a usable target speaks TLS 1.3 with X25519
// and HTTP/2 and has a valid certificate for the name clients send. Neighbors looks for
// such sites next to the server's IP (the XTLS RealiTLScanner approach): the node's
// traffic then looks like traffic to a site in the same network, not like a Microsoft
// server in a random data center.
package scan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/proto"
)

type Result struct {
	Dest      string `json:"dest" doc:"host:port, который будет набирать нода"`
	SNI       string `json:"sni" doc:"Имя сервера, которое отправят клиенты"`
	IP        string `json:"ip"`
	RTTms     int    `json:"rtt_ms"`
	TLS13     bool   `json:"tls13"`
	H2        bool   `json:"h2"`
	X25519    bool   `json:"x25519"`
	CertValid bool   `json:"cert_valid"`
	Issuer    string `json:"issuer,omitempty"`
	Error     string `json:"error,omitempty"`
	DNSMatch  bool   `json:"dns_match" doc:"Имя резолвится в этот IP: сайт настоящий, а не чужая маскировка"`
	OK        bool   `json:"ok" doc:"Подходит как цель REALITY"`
}

const (
	dialTimeout      = 3 * time.Second
	handshakeTimeout = 5 * time.Second
)

// Options say where a check may connect. The server dials what the admin names, so a
// name that leads to this host or its network (127.0.0.1.nip.io, a rebinding name) must
// not be dialed, whatever its text looks like.
type Options struct {
	// LoopbackPort allows 127.0.0.1:<port> alone: the panel's own HTTPS (self-steal).
	LoopbackPort int
	// Any allows every address: tests and test setups.
	Any bool
	// Wide lets Neighbors go past its own /24: to the other /24s of the /16, the nearest
	// first, while it finds fewer sites than asked and has time. The admin turns it on: that
	// many connections may look like a scan of the network to the hoster.
	Wide bool
	// Resolve looks a name up; nil asks the system's resolver for IPv4.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (o Options) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if o.Resolve != nil {
		return o.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// allowed: may the check connect to ip on port?
func (o Options) allowed(ip netip.Addr, port string) bool {
	if o.Any || proto.PublicAddr(ip) {
		return true
	}
	return o.LoopbackPort > 0 && ip == netip.MustParseAddr("127.0.0.1") && port == strconv.Itoa(o.LoopbackPort)
}

// Check tests dest as a REALITY target for clients sending sni ("" = the host of dest). It
// connects only to addresses o allows, and to the very address it checked: the name is
// resolved once.
func Check(ctx context.Context, dest, sni string, o Options) Result {
	r := Result{Dest: dest, SNI: sni}
	host, port, err := net.SplitHostPort(dest)
	if err != nil {
		r.Error = "bad_dest"
		return r
	}
	if r.SNI == "" {
		if _, err := netip.ParseAddr(host); err == nil {
			r.Error = "sni_required"
			return r
		}
		r.SNI = host
	}
	var ip netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		ip = a
	} else {
		addrs, err := o.lookup(ctx, host)
		if err != nil || len(addrs) == 0 {
			r.Error = "dns"
			return r
		}
		// The first address the check may use: a name with a private address among its
		// answers is still dialed on a public one, never on the private one.
		// A name never leads to loopback, not even to the panel's own port: that is for the literal address.
		i := slices.IndexFunc(addrs, func(a netip.Addr) bool { return o.Any || proto.PublicAddr(a) })
		if i < 0 {
			r.Error = "private"
			return r
		}
		ip = addrs[i]
	}
	if !o.allowed(ip, port) {
		r.Error = "private"
		return r
	}
	r.IP = ip.String()
	addr := net.JoinHostPort(ip.String(), port)

	// First with X25519 only: REALITY clients offer it, a target without it breaks them.
	cs, rtt, err := handshake(ctx, addr, r.SNI, []tls.CurveID{tls.X25519})
	if err == nil {
		r.X25519 = true
	} else if cs, rtt, err = handshake(ctx, addr, r.SNI, nil); err != nil {
		r.Error = classify(err)
		return r
	}
	r.RTTms = int(rtt.Milliseconds())
	r.TLS13 = cs.Version == tls.VersionTLS13
	r.H2 = cs.NegotiatedProtocol == "h2"
	if len(cs.PeerCertificates) > 0 {
		leaf := cs.PeerCertificates[0]
		r.Issuer = leaf.Issuer.CommonName
		pool := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			pool.AddCert(c)
		}
		_, verr := leaf.Verify(x509.VerifyOptions{DNSName: r.SNI, Intermediates: pool})
		r.CertValid = verr == nil
	}
	r.DNSMatch = resolvesTo(ctx, r.SNI, r.IP, o)
	r.OK = r.TLS13 && r.H2 && r.X25519 && r.CertValid
	return r
}

// handshake returns the negotiated state and the TCP connect time. Certificates are
// checked by the caller: a target with a bad certificate still tells what it supports.
func handshake(ctx context.Context, addr, sni string, curves []tls.CurveID) (tls.ConnectionState, time.Duration, error) {
	start := time.Now()
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return tls.ConnectionState{}, 0, err
	}
	rtt := time.Since(start)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(handshakeTimeout))
	t := tls.Client(c, &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: curves})
	if err := t.HandshakeContext(ctx); err != nil {
		return tls.ConnectionState{}, rtt, err
	}
	return t.ConnectionState(), rtt, nil
}

func resolvesTo(ctx context.Context, name, ip string, o Options) bool {
	if _, err := netip.ParseAddr(name); err == nil {
		return name == ip
	}
	addrs, err := o.lookup(ctx, name)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.String() == ip {
			return true
		}
	}
	return false
}

func classify(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "refused"):
		return "refused"
	case strings.Contains(err.Error(), "protocol version"):
		return "no_tls13"
	}
	return "handshake"
}

// WideEnough is where a wide search stops: this many sites found is plenty to choose from,
// and every further connection is one more the hoster may count.
const WideEnough = 20

// Neighbors scans the /24 around ip for usable targets and returns the best ones by RTT.
// With o.Wide it goes on through the /24s next to it in the /16 (wideBlocks) until it has
// enough sites (limit, at most WideEnough) or ctx ends.
func Neighbors(ctx context.Context, ip string, limit int, o Options) (found []Result, scanned int, err error) {
	self, err := netip.ParseAddr(ip)
	if err != nil || !self.Is4() {
		return nil, 0, errors.New("need an IPv4 address")
	}
	// It connects to a /24 around ip: never the neighborhood of this host's own network.
	if !o.Any && !proto.PublicAddr(self) {
		return nil, 0, errors.New("need a public IPv4 address")
	}
	own, _ := self.Prefix(24)
	blocks := []netip.Prefix{own}
	if o.Wide {
		blocks = append(blocks, wideBlocks(self)...)
	}
	enough := 0 // the own /24 is scanned whole, as it always was
	if o.Wide {
		enough = WideEnough
		if limit > 0 && limit < enough {
			enough = limit
		}
	}
	seen := map[string]bool{}
	for i, block := range blocks {
		if i > 0 && (ctx.Err() != nil || len(found) >= enough) {
			break
		}
		// Past the own /24 a block stops as soon as the search has enough.
		want := 0
		if i > 0 {
			want = enough - len(found)
		}
		f, n := scanBlock(ctx, block, self, o, seen, want)
		found, scanned = append(found, f...), scanned+n
	}
	sort.Slice(found, func(i, j int) bool { return found[i].RTTms < found[j].RTTms })
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	// A wide scan ends on its deadline by design; what it found by then is the answer.
	if o.Wide && len(blocks) > 1 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return found, scanned, nil
	}
	return found, scanned, ctx.Err()
}

// wideBlocks are the other /24s of self's /16, the nearest third octet first: c+1, c-1,
// c+2, c-2 and so on.
func wideBlocks(self netip.Addr) []netip.Prefix {
	a := self.As4()
	c := int(a[2])
	var out []netip.Prefix
	for d := 1; d <= 255; d++ {
		for _, x := range []int{c + d, c - d} {
			if x < 0 || x > 255 {
				continue
			}
			out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{a[0], a[1], byte(x), 0}), 24))
		}
	}
	return out
}

// scanBlock checks every host of one /24 for a usable target; seen keeps a name found in
// another block from coming twice. With want > 0 it starts no more connections once it
// has found that many.
func scanBlock(ctx context.Context, prefix netip.Prefix, self netip.Addr, o Options, seen map[string]bool, want int) (found []Result, scanned int) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 64)
	)
	for a := prefix.Addr(); prefix.Contains(a); a = a.Next() {
		last := a.As4()[3]
		// A /16 may hold addresses that are no one's site (a private or reserved range).
		if a == self || last == 0 || last == 255 || !o.Any && !proto.PublicAddr(a) {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		scanned++
		wg.Add(1)
		go func(a netip.Addr) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			name := certName(ctx, a.String())
			if name == "" {
				return
			}
			r := Check(ctx, net.JoinHostPort(a.String(), "443"), name, Options{Any: o.Any})
			// Shared VPS networks are full of other people's REALITY servers: probed without
			// a valid client they relay www.samsung.com or yahoo.com. Such a name does not
			// resolve to that address; a real neighbor site does.
			if !r.OK || !r.DNSMatch {
				return
			}
			mu.Lock()
			if !seen[r.SNI] && (want == 0 || len(found) < want) {
				seen[r.SNI] = true
				found = append(found, r)
				if want > 0 && len(found) >= want {
					stop() // enough: the connections still open end, no new ones start
				}
			}
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	return found, scanned
}

// certName asks an IP for its default certificate (no SNI) and returns a concrete name
// from it; wildcards cannot be sent as SNI.
func certName(ctx context.Context, ip string) string {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return ""
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	t := tls.Client(c, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}})
	if t.HandshakeContext(ctx) != nil {
		return ""
	}
	certs := t.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	names := append([]string{}, certs[0].DNSNames...)
	if cn := certs[0].Subject.CommonName; cn != "" {
		names = append(names, cn)
	}
	for _, n := range names {
		if !strings.Contains(n, "*") && strings.Contains(n, ".") && net.ParseIP(n) == nil {
			return strings.ToLower(n)
		}
	}
	return ""
}
