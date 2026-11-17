package webhooks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"syscall"
	"time"

	"mikan/internal/netaddr"
)

// What is wrong with an address the admin gave, or one it led to. The API words each of them
// for the admin.
var (
	ErrURLInvalid = errors.New("webhooks: the address is not a URL with a host")
	ErrURLScheme  = errors.New("webhooks: the address is not http or https")
	ErrURLPort    = errors.New("webhooks: the port is not 1 to 65535")
	// ErrLocalAddress is an address of this server or its network (loopback, private, a
	// carrier's NAT such as Tailscale's) while the endpoint does not allow local targets.
	ErrLocalAddress = errors.New("webhooks: a local address")
	// ErrBlockedAddress is one a delivery never goes to, whatever the endpoint allows: the
	// link-local range that holds the cloud's metadata service, the other known metadata
	// addresses, and what no unicast connection reaches.
	ErrBlockedAddress = errors.New("webhooks: an address deliveries never go to")
)

// metadata are the cloud metadata services outside the link-local range (169.254.0.0/16 is
// refused whole): the panel's server may sit in a cloud, and an address of its own network
// that an endpoint with allow_local may reach must not be one of these.
var metadata = []netip.Addr{
	netip.MustParseAddr("fd00:ec2::254"),   // AWS, over IPv6
	netip.MustParseAddr("fd20:ce::254"),    // Google Cloud, over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"),   // Azure's WireServer
	netip.MustParseAddr("192.0.0.192"),     // Oracle Cloud, the older service
}

// checkAddr says whether a delivery may connect to a: a public address always, an address of
// this server or its network only with allowLocal, anything else never.
func checkAddr(a netip.Addr, allowLocal bool) error {
	a = netaddr.Unwrap(a).WithZone("")
	if slices.Contains(metadata, a) {
		return ErrBlockedAddress
	}
	switch netaddr.Classify(a) {
	case netaddr.Public:
		return nil
	case netaddr.Loopback, netaddr.Private, netaddr.Shared:
		if allowLocal {
			return nil
		}
		return ErrLocalAddress
	}
	return ErrBlockedAddress
}

// MaxURLLen bounds an address; longer ones are no webhook's.
const MaxURLLen = 2048

// CheckURL judges what the admin typed, before it is saved: http or https, a host and no
// credentials, a port that exists, and for a literal address or a name that can only be a
// local one (localhost, a bare name) the same verdict a connection would get. It does not
// resolve names: a name that leads to a local address is refused when the delivery connects
// (the dialer checks the address it is given, so a name cannot be turned into another meanwhile).
func CheckURL(raw string, allowLocal bool) error {
	if len(raw) > MaxURLLen {
		return ErrURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ErrURLInvalid
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrURLScheme
	}
	host := u.Hostname()
	// Credentials in the address would be logged with it by whoever shows it, and sent as a
	// Basic header; the secret is what authenticates the panel to the receiver.
	if host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return ErrURLInvalid
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return ErrURLPort
		}
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return checkAddr(a, allowLocal)
	}
	if !allowLocal && !netaddr.PublicHost(host) {
		return ErrLocalAddress
	}
	return nil
}

// control runs on the address a connection is about to be made to, after the name was looked
// up, so DNS cannot answer a public address to the check and a private one to the connection:
// the check is on the one that is dialled. It runs for every address a name leads to, so the
// connection goes only to the allowed ones.
func control(allowLocal bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
		}
		if err := checkAddr(a, allowLocal); err != nil {
			return fmt.Errorf("%w: %s", err, a)
		}
		return nil
	}
}

// Limits of one delivery: a receiver that is slow or talks on must not hold a worker or fill
// the memory.
const (
	dialTimeout    = 5 * time.Second
	requestTimeout = 10 * time.Second // the whole attempt: connecting, sending, the answer
	maxAnswer      = 4 << 10          // bytes of an answer that are read; nothing is kept
	maxHeaderBytes = 16 << 10
)

// newClient is the HTTP client for deliveries: no proxy (a proxy would be dialled instead of
// the receiver, past the check), no redirect (the answer is the redirect itself: a POST sent
// on to a place the admin did not name is no delivery), a connection each time (so every
// delivery is judged on the address it connects to), and the limits above.
func newClient(allowLocal bool, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{Timeout: dialTimeout, Control: control(allowLocal)}).DialContext,
			TLSHandshakeTimeout:    dialTimeout,
			ResponseHeaderTimeout:  timeout,
			MaxResponseHeaderBytes: maxHeaderBytes,
			DisableKeepAlives:      true,
		},
	}
}

// Why a delivery did not go through: the codes the admin panel words, kept in the log.
const (
	FailLocal    = "address_local"   // the address is local and the endpoint does not allow that
	FailBlocked  = "address_blocked" // the address is one deliveries never go to
	FailDNS      = "dns"             // the name did not resolve
	FailRefused  = "refused"         // nothing listens at the address
	FailTimeout  = "timeout"         // no answer in time
	FailTLS      = "tls"             // the certificate or the handshake
	FailRedirect = "redirect"        // the receiver answered with a redirect, which is not followed
	FailStatus   = "status"          // the receiver answered something but 2xx
	FailNetwork  = "network"         // the connection broke some other way
	FailInternal = "internal"        // the panel could not make the request: its log says why
)

// failure is the code for a request that returned an error.
func failure(err error) string {
	var dns *net.DNSError
	var verify *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var timeout net.Error
	switch {
	case errors.Is(err, ErrLocalAddress):
		return FailLocal
	case errors.Is(err, ErrBlockedAddress):
		return FailBlocked
	case errors.As(err, &dns):
		return FailDNS
	case errors.As(err, &verify), errors.As(err, &record), errors.As(err, &alert):
		return FailTLS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return FailTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		return FailRefused
	}
	return FailNetwork
}
