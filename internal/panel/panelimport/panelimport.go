// Package panelimport brings users over from another panel through its API: Marzban,
// PasarGuard and Remnawave. Each becomes a mikan user on a plan the admin picks, with
// the old panel's limit, traffic used, term and device limit kept.
//
// What does not come over: the old panel's traffic reset strategy, groups and inbounds
// (users get the plan's), and an on-hold term, which starts at the import.
//
// The links people already have can keep working (subs.Handler.Legacy; the bot takes them
// to sign in too, both through Verifier.User):
//
//   - Remnawave's link is the user's short UUID, the same every time: it is kept as is.
//   - Marzban and PasarGuard sign a new token for every request, so the tokens people
//     hold were never seen by the API. They are checked as the old panel checked them,
//     with its secret (Verifier); without the secret those links are not taken over,
//     since a token without a checked signature is anybody's for the asking.
//     A token signed before the user was made in the old panel is refused (not_before).
//     A revoke in the old panel (Marzban's sub_revoked_at) is not in either API, so links
//     revoked there open again here: revoke them anew in mikan (a reissue drops all of a
//     user's old links).
package panelimport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"mikan/internal/proto"
)

// Kind is the panel users come from.
type Kind string

const (
	Marzban    Kind = "marzban"
	PasarGuard Kind = "pasarguard"
	Remnawave  Kind = "remnawave"
)

// Source is how to reach the old panel. Marzban and PasarGuard take an admin's username
// and password (PasarGuard an API key instead); Remnawave an API token.
type Source struct {
	Kind     Kind
	URL      string // the panel's address, https://panel.example.com
	Username string
	Password string
	Token    string // Remnawave API token, or PasarGuard API key
}

// Status of a user in the old panel, in mikan's words.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusLimited  = "limited"
	StatusExpired  = "expired"
	StatusOnHold   = "on_hold" // Marzban and PasarGuard: the term starts at the first connection
)

// User is a user of the old panel.
type User struct {
	Name     string
	SourceID int64  // PasarGuard's user id (its tokens carry it); 0 elsewhere
	Status   string // Status*
	Note     string
	Contact  string // Remnawave's Telegram id or e-mail
	// TrafficLimit in bytes; 0: unlimited.
	TrafficLimit int64
	// Used is the traffic of the current period, Lifetime of all time, in bytes.
	Used, Lifetime int64
	// Expires is when the term ends; zero: never.
	Expires time.Time
	// OnHold is the term an on-hold user gets once it starts.
	OnHold time.Duration
	// DeviceLimit; 0: the plan's.
	DeviceLimit int64
	// Token is a subscription token that is the same every time (Remnawave's short UUID).
	Token string
	// WeakToken: the old panel's token is too short or odd to be a secret; the user comes
	// over without the old link.
	WeakToken bool
	// Created is when the old panel made the user: its signed tokens from before are not
	// the user's (a name used again).
	Created time.Time
}

// Errors the admin sees.
var (
	ErrAuth        = errors.New("import_auth")        // the old panel refused the credentials
	ErrUnreachable = errors.New("import_unreachable") // the old panel did not answer
	ErrAnswer      = errors.New("import_bad_answer")  // the answer is not what this panel gives
	ErrTLS         = errors.New("import_tls")         // the old panel's certificate is not trusted
	ErrRedirect    = errors.New("import_redirect")    // the old panel answered with a redirect
	ErrAddress     = errors.New("import_address")     // an address the import may not reach
	ErrIncomplete  = errors.New("import_incomplete")  // the panel gave fewer users than it said it has
)

// Limits on what an old panel may send: a broken or hostile one must not hold the
// request or fill the memory.
const (
	maxUsers     = 200_000
	maxPages     = 2_000
	maxPageBytes = 16 << 20
	FetchTimeout = 10 * time.Minute
	// Fields are cut a little past what mikan takes, so a too long one still fails the
	// check (Normalize) instead of coming over shortened.
	maxNameBytes = 4 * maxName
	maxNoteBytes = 4 * maxNote
)

// Client is the HTTP client for old panels: it follows no redirect (a 307 would carry the
// admin's password to another host), and Dial checks the address actually dialled, after
// the name was looked up, so a name cannot be turned into another address meanwhile.
// Loopback and the LAN are allowed (the old panel often runs on this very server); the
// link-local range (cloud metadata), unspecified and multicast addresses are not, and
// plain http only to loopback or a private address.
func Client() *http.Client {
	return &http.Client{
		Timeout:       2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 15 * time.Second, Control: dialControl}).DialContext,
			TLSHandshakeTimeout: 15 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
}

func dialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrAddress, host)
	}
	if !AddressOK(a) {
		return fmt.Errorf("%w: %s", ErrAddress, a)
	}
	return nil
}

// imdsV6 is the cloud metadata service's IPv6 address (AWS), outside the link-local range.
var imdsV6 = netip.MustParseAddr("fd00:ec2::254")

// AddressOK says whether the import may connect to a. Loopback, private and CGNAT
// (100.64.0.0/10, where Tailscale and similar overlays put their peers) are allowed: the
// old panel often runs on this server or next to it. Link-local (the metadata service),
// the AWS IPv6 metadata address, unspecified, multicast and broadcast are not.
func AddressOK(a netip.Addr) bool {
	a = proto.Unwrap4(a)
	return !(a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() ||
		a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || a == imdsV6)
}

// privateOK says whether plain http may go to a: loopback or a private address.
func privateOK(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate()
}

// Fetch lists the old panel's users, within FetchTimeout.
func Fetch(ctx context.Context, hc *http.Client, src Source) ([]User, error) {
	src.URL = strings.TrimRight(strings.TrimSpace(src.URL), "/")
	u, err := url.Parse(src.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: the address must be https://host", ErrAnswer)
	}
	// A literal address is judged at once; a name, on the address it leads to (dialControl).
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil {
		if !AddressOK(a) || (u.Scheme == "http" && !privateOK(a)) {
			return nil, fmt.Errorf("%w: %s", ErrAddress, a)
		}
	}
	if hc == nil {
		hc = Client()
	}
	if u.Scheme == "http" {
		hc = plainOnly(hc)
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	switch src.Kind {
	case Marzban, PasarGuard:
		return fetchMarzban(ctx, hc, src)
	case Remnawave:
		return fetchRemnawave(ctx, hc, src)
	}
	return nil, fmt.Errorf("unknown panel %q", src.Kind)
}

// plainOnly is hc for an http:// address: the password goes unencrypted, so only to this
// server or the LAN, checked on the address dialled.
func plainOnly(hc *http.Client) *http.Client {
	t, ok := hc.Transport.(*http.Transport)
	if !ok {
		return hc
	}
	t = t.Clone()
	d := &net.Dialer{Timeout: 15 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
		if err := dialControl(network, address, c); err != nil {
			return err
		}
		host, _, _ := net.SplitHostPort(address)
		if a, err := netip.ParseAddr(host); err != nil || !privateOK(a) {
			return fmt.Errorf("%w: plain http only to this server or the LAN, not %s", ErrAddress, host)
		}
		return nil
	}}
	t.DialContext = d.DialContext
	c := *hc
	c.Transport = t
	return &c
}

// getJSON does a request and decodes a JSON answer into out.
func getJSON(ctx context.Context, hc *http.Client, req *http.Request, out any) error {
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fmt.Errorf("%w: %s answered %d to %s", ErrRedirect, req.URL.Host, resp.StatusCode, resp.Header.Get("Location"))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuth
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: HTTP %d from %s", ErrAnswer, resp.StatusCode, req.URL.Path)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPageBytes)).Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrAnswer, err)
	}
	return nil
}

// errNotFound: an endpoint the old panel does not have (an older version).
var errNotFound = fmt.Errorf("%w: not found", ErrAnswer)

func transportError(ctx context.Context, err error) error {
	var verr *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.Is(err, ErrAddress):
		return err
	case errors.As(err, &verr), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalid):
		return fmt.Errorf("%w: %v", ErrTLS, err)
	case ctx.Err() != nil:
		return fmt.Errorf("%w: no answer in time", ErrUnreachable)
	}
	return ErrUnreachable
}

// cut keeps at most n bytes of s, on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
