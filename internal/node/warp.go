package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"

	"mikan/internal/nodeapi"
	"mikan/internal/textutil"
)

// warpProxy is the WARP outbound's name in mihomo.
const warpProxy = "WARP"

// warpProxyConfig is the WireGuard outbound to Cloudflare WARP.
func warpProxyConfig(w *nodeapi.Warp) (map[string]any, error) {
	host, portStr, err := net.SplitHostPort(w.Endpoint)
	port, perr := strconv.Atoi(portStr)
	if err != nil || perr != nil || host == "" || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("warp endpoint %q", w.Endpoint)
	}
	if _, err := netip.ParseAddr(w.IPv4); err != nil {
		return nil, fmt.Errorf("warp ipv4 %q", w.IPv4)
	}
	mtu := w.MTU
	if mtu <= 0 {
		mtu = 1280
	}
	p := map[string]any{
		"name": warpProxy, "type": "wireguard", "server": host, "port": port,
		"ip": w.IPv4, "private-key": w.PrivateKey, "public-key": w.PeerPublicKey,
		"allowed-ips": []string{"0.0.0.0/0", "::/0"}, "udp": true, "mtu": mtu,
		// Names are resolved inside WARP: the server's resolver is not asked about them.
		"remote-dns-resolve": true, "dns": []string{"1.1.1.1", "1.0.0.1"},
	}
	if w.IPv6 != "" {
		if _, err := netip.ParseAddr(w.IPv6); err != nil {
			return nil, fmt.Errorf("warp ipv6 %q", w.IPv6)
		}
		p["ipv6"] = w.IPv6
	}
	if len(w.Reserved) == 3 {
		p["reserved"] = []int{int(w.Reserved[0]), int(w.Reserved[1]), int(w.Reserved[2])}
	}
	return p, nil
}

// safeRuleValue keeps a value from splitting a rule line.
func safeRuleValue(s string) bool {
	return s != "" && !strings.ContainsAny(s, ", \t\r\n")
}

// warpRules send the listed inbounds, domains and networks to WARP. They come after the
// REJECT rules, so WARP never reaches what a direct connection may not.
func warpRules(st nodeapi.DesiredState) []string {
	w := st.Warp
	if w == nil {
		return nil
	}
	present := listenerNames(st)
	viaExit := map[string]bool{} // an inbound sent to another node does not use WARP here
	for _, e := range st.Exits {
		for _, n := range e.Inbounds {
			viaExit[n] = true
		}
	}
	var r []string
	for _, name := range w.Inbounds {
		if present[name] && !viaExit[name] && safeRuleValue(name) {
			r = append(r, "IN-NAME,"+name+","+warpProxy)
		}
	}
	for _, d := range w.Domains {
		if safeRuleValue(d) {
			r = append(r, "DOMAIN-SUFFIX,"+strings.ToLower(d)+","+warpProxy)
		}
	}
	for _, c := range w.CIDRs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		kind := "IP-CIDR"
		if p.Addr().Is6() {
			kind = "IP-CIDR6"
		}
		r = append(r, kind+","+p.Masked().String()+","+warpProxy+",no-resolve")
	}
	return r
}

// probeTarget is what a probe goes through and asks. Tests swap the dialer and the URL.
type probeTarget struct {
	dial     func(ctx context.Context, addr string) (net.Conn, error)
	traceURL string
	tls      *tls.Config
	// reachAddr is dialled by IP first: a failure there means the tunnel itself is down,
	// not DNS or TLS.
	reachAddr string
	// endpoint is WARP's UDP endpoint, named in the detail; empty for other outbounds.
	endpoint    string
	dialTimeout time.Duration // the tunnel check, 6 s when zero
	httpTimeout time.Duration // the trace request, 10 s when zero
}

// probe asks Cloudflare's trace page through an outbound (WARP, NODE-<id>) what it sees.
func probe(ctx context.Context, proxy, endpoint string) nodeapi.WarpStatus {
	p, ok := tunnel.Proxies()[proxy]
	if !ok {
		return nodeapi.WarpStatus{Configured: true, CheckedAt: time.Now().UTC(), Error: "not_loaded",
			Detail: "the " + proxy + " outbound is not loaded: the node has not applied its config yet"}
	}
	return runProbe(ctx, probeTarget{
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.ParseUint(portStr, 10, 16)
			if err != nil {
				return nil, err
			}
			return p.DialContext(ctx, &C.Metadata{NetWork: C.TCP, Host: host, DstPort: uint16(port)})
		},
		traceURL:  "https://www.cloudflare.com/cdn-cgi/trace",
		tls:       &tls.Config{MinVersion: tls.VersionTLS12},
		reachAddr: "1.1.1.1:443",
		endpoint:  endpoint,
	})
}

// runProbe is probe in two steps, so a failure says what is broken: a TCP connection
// by IP (the tunnel is up: for WireGuard, the handshake got an answer), then the HTTPS
// request (names, TLS, Cloudflare's answer).
func runProbe(ctx context.Context, t probeTarget) nodeapi.WarpStatus {
	st := nodeapi.WarpStatus{Configured: true, CheckedAt: time.Now().UTC()}
	if t.dialTimeout == 0 {
		t.dialTimeout = 6 * time.Second
	}
	if t.httpTimeout == 0 {
		t.httpTimeout = 10 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, t.dialTimeout)
	conn, err := t.dial(dctx, t.reachAddr)
	cancel()
	if err != nil {
		st.Error, st.Detail = classifyProbeError(err, false, t.endpoint)
		return st
	}
	_ = conn.Close()

	hc := &http.Client{Timeout: t.httpTimeout, Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, addr string) (net.Conn, error) { return t.dial(ctx, addr) },
		TLSClientConfig:   t.tls,
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.traceURL, nil)
	if err != nil {
		st.Error, st.Detail = "failed", truncDetail(err.Error())
		return st
	}
	resp, err := hc.Do(req)
	if err != nil {
		st.Error, st.Detail = classifyProbeError(err, true, t.endpoint)
		return st
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	for _, line := range strings.Split(string(body), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "ip":
			st.IP = v
		case "warp":
			st.Warp = v
		case "colo":
			st.Colo = v
		}
	}
	st.OK = resp.StatusCode == http.StatusOK && st.IP != ""
	if !st.OK {
		st.Error = "bad_answer"
		st.Detail = "the tunnel is up, but the trace page answered HTTP " + strconv.Itoa(resp.StatusCode) + " without an address"
	}
	return st
}

// classifyProbeError turns a failed probe step into a stable code and a short detail
// for the admin. https says the tunnel already carried a TCP connection. The detail
// never holds keys: it is the error text of a dial, which names addresses only.
func classifyProbeError(err error, https bool, endpoint string) (code, detail string) {
	var dnsErr *net.DNSError
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var tlsErr tls.RecordHeaderError
	msg := err.Error()
	via := "the tunnel is up, but "
	switch {
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		if https {
			return "https_timeout", via + "the HTTPS request to cloudflare.com timed out"
		}
		if endpoint != "" {
			return "timeout", "no answer from the WARP endpoint " + endpoint + " over UDP"
		}
		return "timeout", "no answer through the outbound in time"
	case errors.As(err, &dnsErr) || strings.Contains(msg, "no such host") || strings.Contains(msg, "dns resolve failed"):
		return "dns", truncDetail(msg)
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused"):
		return "refused", truncDetail(msg)
	case errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &tlsErr) || strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") || strings.Contains(msg, "gave HTTP response to HTTPS client"):
		return "tls", truncDetail(msg)
	}
	if https {
		msg = via + "HTTPS failed: " + msg
	}
	return "failed", truncDetail(msg)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// truncDetail keeps a detail on one short line.
func truncDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if clipped := textutil.ClipRunes(s, 200); clipped != s {
		s = clipped + "…"
	}
	return s
}
