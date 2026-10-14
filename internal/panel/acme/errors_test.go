package acme

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"testing"
	"time"

	legoacme "github.com/go-acme/lego/v4/acme"

	"mikan/internal/acmechallenge"
	"mikan/internal/nodeapi"
)

func validation(typ, detail string) error {
	pd := &legoacme.ProblemDetails{Type: "urn:ietf:params:acme:error:" + typ, Detail: detail, HTTPStatus: 403}
	// As lego wraps it: the per-name errors of an order, joined.
	return fmt.Errorf("error: one or more domains had a problem:\n%w", errors.Join(fmt.Errorf("[vpn.example.com] invalid authorization: %w", pd)))
}

// Every way an order fails that the admin can fix gets its own code and text.
func TestClassify(t *testing.T) {
	own := []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		err  error
		code string
	}{
		{"busy here", fmt.Errorf("[x] acme: error presenting token: %w", &acmechallenge.BusyError{Addr: ":80", Holder: "nginx"}), CodePort80Busy},
		{"busy on the node", fmt.Errorf("present: %w", &nodeapi.Error{Code: nodeapi.CodePort80Busy, Message: "caddy"}), CodePort80Busy},
		{"old node", fmt.Errorf("present: %w", &nodeapi.StatusError{Status: http.StatusNotFound}), CodeNodeOutdated},
		{"node down", fmt.Errorf("%w: dial tcp: i/o timeout", nodeapi.ErrUnavailable), CodeNodeUnreachable},
		{"firewall", validation("connection", "198.51.100.7: Fetching http://vpn.example.com/.well-known/acme-challenge/abc: Timeout during connect (likely firewall problem)"), CodePort80Timeout},
		{"nothing on 80", validation("connection", "198.51.100.7: Fetching http://vpn.example.com/.well-known/acme-challenge/abc: Connection refused"), CodePort80Refused},
		{"nginx answers", validation("unauthorized", "198.51.100.7: Invalid response from http://vpn.example.com/.well-known/acme-challenge/abc: 404"), CodePort80Foreign},
		{"cloudflare", validation("unauthorized", "104.21.32.1: Invalid response from http://vpn.example.com/.well-known/acme-challenge/abc: 404"), CodeCloudflare},
		{"elsewhere", validation("connection", "203.0.113.50: Fetching http://vpn.example.com/.well-known/acme-challenge/abc: Timeout during connect"), CodeDNSElsewhere},
		{"no record", validation("dns", "DNS problem: NXDOMAIN looking up A for vpn.example.com - check that a DNS record exists for this domain"), CodeDNSProblem},
		{"caa", validation("caa", "CAA record for vpn.example.com prevents issuance"), CodeCAA},
		{"eab required", &errRegister{err: &legoacme.ProblemDetails{Type: "urn:ietf:params:acme:error:externalAccountRequired", HTTPStatus: 400}}, CodeEABRequired},
		{"eab refused", &errRegister{err: &legoacme.ProblemDetails{Type: "urn:ietf:params:acme:error:unauthorized", HTTPStatus: 401}}, CodeEABInvalid},
		{"eab key unusable", &errRegister{err: errors.New("acme: error signing eab content: failed to External Account Binding sign content: go-jose/go-jose: invalid key size for algorithm")}, CodeEABInvalid},
		{"zerossl email", &errRegister{err: ErrEmailRequired}, "zerossl_email_required"},
		{"zerossl api", &errRegister{err: fmt.Errorf("%w: HTTP 500", ErrZeroSSLEAB)}, "zerossl_eab_failed"},
		{"google", &errRegister{err: ErrGoogleEAB}, "google_eab_required"},
		{"ca down", errors.New(`get directory at 'https://acme-v02.api.letsencrypt.org/directory': Get "...": dial tcp: lookup acme-v02.api.letsencrypt.org: no such host`), CodeCAUnreachable},
		{"bind", errors.New("listen tcp :80: bind: address already in use"), CodePort80Busy},
		{"other", errors.New("acme: something new"), CodeUnknown},
	} {
		if p := Classify(c.err, own, now); p.Code != c.code {
			t.Errorf("%s: %q, want %q (%s)", c.name, p.Code, c.code, p.Detail)
		} else if p.Detail == "" {
			t.Errorf("%s: no detail", c.name)
		}
	}
	if p := Classify(fmt.Errorf("x: %w", &acmechallenge.BusyError{Addr: ":80", Holder: "nginx"}), own, now); p.Holder != "nginx" {
		t.Errorf("holder %q", p.Holder)
	}
}

// A rate limit says when to try again: from Retry-After, or from Let's Encrypt's text.
func TestClassifyRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	rl := &legoacme.RateLimitedError{ProblemDetails: &legoacme.ProblemDetails{Type: legoacme.RateLimitedErr, HTTPStatus: 429, Detail: "too many certificates"}, RetryAfter: "3600"}
	// lego's sender returns the pointer, as an error.
	var err error = rl
	p := Classify(errors.Join(errors.New("obtain"), err), nil, now)
	if p.Code != CodeRateLimited || p.RetryAt == nil || !p.RetryAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("retry-after: %+v", p)
	}
	text := &legoacme.ProblemDetails{Type: legoacme.RateLimitedErr, HTTPStatus: 429,
		Detail: "too many certificates (5) already issued for this exact set of identifiers in the last 168h0m0s, retry after 2026-10-12 08:30:00 UTC"}
	p = Classify(text, nil, now)
	if p.Code != CodeRateLimited || p.RetryAt == nil || !p.RetryAt.Equal(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("text: %+v", p)
	}
}
