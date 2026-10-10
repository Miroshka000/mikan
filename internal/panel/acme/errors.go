package acme

import (
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	legoacme "github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/acme/api"

	"mikan/internal/acmechallenge"
	"mikan/internal/nodeapi"
	"mikan/internal/panel/dnscheck"
)

// Problem is why a certificate was not issued, as the admin panel tells it: a code it
// translates (errors.acme.<code>), the CA's or the system's own words for "more", and when
// to try again if the CA said.
type Problem struct {
	Code    string
	Detail  string
	Holder  string     // the program on port 80, when the code is port80_busy and it is known
	RetryAt *time.Time // rate_limited: when the CA lets the order in again
}

// The codes of Problem. The admin panel has a text for each with what to do.
const (
	CodePort80Busy      = "port80_busy"      // another program holds port 80 here
	CodePort80Timeout   = "port80_timeout"   // the CA got no answer on port 80: a firewall
	CodePort80Refused   = "port80_refused"   // nothing listens on port 80 where the CA came
	CodePort80Foreign   = "port80_foreign"   // another web server answered the CA on port 80
	CodeCloudflare      = "cloudflare_proxy" // the domain goes through Cloudflare's proxy
	CodeDNSElsewhere    = "dns_elsewhere"    // the domain points at another server
	CodeDNSProblem      = "dns_problem"      // the domain does not resolve
	CodeCAA             = "caa_forbids"      // a CAA record allows other CAs only
	CodeRateLimited     = "rate_limited"
	CodeEABRequired     = "eab_required" // the CA wants an external account the panel lacks
	CodeEABInvalid      = "eab_invalid"  // the CA refused the external account key
	CodeCAUnreachable   = "ca_unreachable"
	CodeNodeOutdated    = "node_outdated"    // the node predates the challenge endpoint
	CodeNodeUnreachable = "node_unreachable" // the panel cannot reach the node's API
	CodeUnknown         = "acme_failed"
)

// errRegister marks a failure of the account registration: an "unauthorized" there is the
// EAB key, not the domain.
type errRegister struct{ err error }

func (e *errRegister) Error() string { return "register: " + e.err.Error() }
func (e *errRegister) Unwrap() error { return e.err }

// leadingIP is the address a CA's validation error starts with: "203.0.113.7: Fetching …".
var leadingIP = regexp.MustCompile(`(?:^|[\s"])([0-9a-fA-F:.]{7,45}): (?:Fetching|Invalid response|Timeout|Connection)`)

// retryUntil is Let's Encrypt's "retry after 2026-10-10 12:00:00 UTC".
var retryUntil = regexp.MustCompile(`retry after (\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}) ?(UTC|Z)?`)

// Classify maps a failed order to a Problem. own are the addresses of the server the
// challenge was for: a validation that reached another one says the domain points away.
func Classify(err error, own []netip.Addr, now time.Time) Problem {
	if err == nil {
		return Problem{}
	}
	p := Problem{Code: CodeUnknown, Detail: err.Error()}
	var (
		busy *acmechallenge.BusyError
		ne   *nodeapi.Error
		se   *nodeapi.StatusError
		rl   *legoacme.RateLimitedError
		pd   *legoacme.ProblemDetails
		reg  *errRegister
	)
	switch {
	case errors.As(err, &busy):
		p.Code, p.Holder = CodePort80Busy, busy.Holder
	case errors.As(err, &ne) && ne.Code == nodeapi.CodePort80Busy:
		p.Code, p.Holder = CodePort80Busy, ne.Message
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		p.Code = CodeNodeOutdated
	case errors.Is(err, nodeapi.ErrUnavailable):
		p.Code = CodeNodeUnreachable
	case errors.Is(err, ErrEmailRequired), errors.Is(err, ErrZeroSSLEAB), errors.Is(err, ErrGoogleEAB):
		for _, e := range []error{ErrEmailRequired, ErrZeroSSLEAB, ErrGoogleEAB} {
			if errors.Is(err, e) {
				p.Code = e.Error()
			}
		}
	case errors.As(err, &rl):
		p.Code = CodeRateLimited
		if d, perr := api.ParseRetryAfter(rl.RetryAfter); rl.RetryAfter != "" && perr == nil {
			at := now.Add(d)
			p.RetryAt = &at
		}
	case errors.As(err, &pd):
		p.Code = problemCode(pd, own, errors.As(err, &reg))
	case errors.As(err, &reg) && strings.Contains(err.Error(), "eab"):
		// lego could not sign the binding: the HMAC key is not one.
		p.Code = CodeEABInvalid
	default:
		s := err.Error()
		switch {
		case strings.Contains(s, "address already in use"):
			p.Code = CodePort80Busy
		case strings.Contains(s, "rateLimited") || strings.Contains(s, "too many"):
			p.Code = CodeRateLimited
		case strings.Contains(s, "get directory at") || strings.Contains(s, "no such host") || strings.Contains(s, "i/o timeout") ||
			strings.Contains(s, "connection refused") || strings.Contains(s, "connection reset"):
			// lego's own requests to the CA: the directory, the account, the order.
			p.Code = CodeCAUnreachable
		}
	}
	if p.Code == CodeRateLimited && p.RetryAt == nil {
		if m := retryUntil.FindStringSubmatch(p.Detail); m != nil {
			if t, err := time.Parse("2006-01-02 15:04:05", strings.Replace(m[1], "T", " ", 1)); err == nil {
				t = t.UTC()
				p.RetryAt = &t
			}
		}
	}
	return p
}

// problemCode reads a CA's problem document, and its subproblems, which carry the
// per-name validation errors.
func problemCode(pd *legoacme.ProblemDetails, own []netip.Addr, registering bool) string {
	types := []string{pd.Type}
	details := []string{pd.Detail}
	for _, sp := range pd.SubProblems {
		types = append(types, sp.Type)
		details = append(details, sp.Detail)
	}
	detail := strings.Join(details, " ")
	has := func(t string) bool {
		for _, x := range types {
			if strings.HasSuffix(x, ":"+t) {
				return true
			}
		}
		return false
	}
	// Where the CA went: Cloudflare's proxy, another server, or this one.
	where := ""
	if m := leadingIP.FindStringSubmatch(detail); m != nil {
		if ip, err := netip.ParseAddr(strings.Trim(m[1], "[]")); err == nil {
			ip = ip.Unmap()
			switch {
			case dnscheck.Cloudflare(ip):
				where = "cloudflare"
			case len(own) > 0 && !containsAddr(own, ip):
				where = "elsewhere"
			}
		}
	}
	switch {
	case has("rateLimited"):
		return CodeRateLimited
	case has("externalAccountRequired"):
		return CodeEABRequired
	case registering && (has("unauthorized") || has("malformed")):
		return CodeEABInvalid
	case has("caa"):
		return CodeCAA
	case where == "cloudflare":
		return CodeCloudflare
	case where == "elsewhere":
		return CodeDNSElsewhere
	case has("dns"):
		return CodeDNSProblem
	case has("connection"):
		low := strings.ToLower(detail)
		switch {
		case strings.Contains(low, "refused"):
			return CodePort80Refused
		case strings.Contains(low, "timeout"):
			return CodePort80Timeout
		}
		return CodePort80Timeout
	case has("unauthorized") && strings.Contains(detail, "Invalid response"):
		return CodePort80Foreign
	}
	return CodeUnknown
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
