package webhooks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// What an address may be, by what the endpoint allows: public addresses always; this server's
// and its network's only with allow_local; link-local (where the cloud's metadata service
// lives), the other metadata addresses and what no connection reaches never, whatever the
// endpoint says, and the same whichever way the address is written (IPv4 inside IPv6, NAT64).
func TestCheckURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", MaxURLLen)
	for _, c := range []struct {
		url         string
		public      error // the verdict of an endpoint that allows nothing local
		allowsLocal error // and of one that does
	}{
		{"https://hooks.example.com/x", nil, nil},
		{"http://hooks.example.com/x", nil, nil},
		{"HTTPS://Example.COM/Hook?token=abc&x=1", nil, nil},
		{"https://hooks.example.com:8443/x", nil, nil},
		{"http://203.0.113.10:8080/hook?token=abc", nil, nil},
		{"https://[2606:4700::1111]/x", nil, nil},
		{"https://[2606:4700::1111]:444/x", nil, nil},
		{"https://example.com", nil, nil},
		{"https://example.com./x", nil, nil},
		{"https://xn--e1afmkfd.xn--p1ai/x", nil, nil},
		{"http://[64:ff9b::808:808]/", nil, nil}, // 8.8.8.8 over NAT64

		// This server and its network: refused unless the endpoint allows them.
		{"http://localhost/x", ErrLocalAddress, nil},
		{"http://LOCALHOST:8080/x", ErrLocalAddress, nil},
		{"http://localhost./x", ErrLocalAddress, nil},
		{"http://app.localhost/x", ErrLocalAddress, nil},
		{"http://127.0.0.1/x", ErrLocalAddress, nil},
		{"http://127.8.9.10:9000/x", ErrLocalAddress, nil},
		{"http://[::1]:8080/x", ErrLocalAddress, nil},
		{"http://[::ffff:127.0.0.1]/x", ErrLocalAddress, nil},
		{"http://[64:ff9b::7f00:1]/x", ErrLocalAddress, nil},
		{"http://10.0.0.5/x", ErrLocalAddress, nil},
		{"http://172.16.0.1/x", ErrLocalAddress, nil},
		{"http://192.168.1.1:5678/x", ErrLocalAddress, nil},
		{"http://[::ffff:10.0.0.5]/x", ErrLocalAddress, nil},
		{"http://[fd00::1]/x", ErrLocalAddress, nil},
		{"http://100.64.0.1/x", ErrLocalAddress, nil}, // Tailscale's range
		{"http://n8n/webhook", ErrLocalAddress, nil},  // a name of the local network
		{"http://n8n:5678/webhook", ErrLocalAddress, nil},

		// Never: the link-local range with the metadata service, the other metadata
		// addresses, and addresses that lead nowhere.
		{"http://169.254.169.254/latest/meta-data/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://169.254.169.254:80/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://169.254.170.2/v2/credentials", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[fe80::1]/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[fe80::1%25eth0]/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[::ffff:169.254.169.254]/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[64:ff9b::a9fe:a9fe]/x", ErrBlockedAddress, ErrBlockedAddress}, // 169.254.169.254 over NAT64
		{"http://[fd00:ec2::254]/latest/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[fd20:ce::254]/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://100.100.100.200/latest/meta-data/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://168.63.129.16/machine?comp=goalstate", ErrBlockedAddress, ErrBlockedAddress},
		{"http://192.0.0.192/latest/", ErrBlockedAddress, ErrBlockedAddress},
		{"http://0.0.0.0/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[::]/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://224.0.0.1/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://[ff02::1]/x", ErrBlockedAddress, ErrBlockedAddress},
		{"http://255.255.255.255/x", ErrBlockedAddress, ErrBlockedAddress},

		// Not an http or https address.
		{"ftp://files.example.com/x", ErrURLScheme, ErrURLScheme},
		{"file:///etc/passwd", ErrURLScheme, ErrURLScheme},
		{"gopher://example.com/x", ErrURLScheme, ErrURLScheme},
		{"ws://example.com/x", ErrURLScheme, ErrURLScheme},
		{"javascript:alert(1)", ErrURLScheme, ErrURLScheme},
		{"data:text/plain,hi", ErrURLScheme, ErrURLScheme},
		{"mailto:a@example.com", ErrURLScheme, ErrURLScheme},
		{"example.com/hook", ErrURLScheme, ErrURLScheme},
		{"//example.com/hook", ErrURLScheme, ErrURLScheme},
		{"/hook", ErrURLScheme, ErrURLScheme},
		{"", ErrURLScheme, ErrURLScheme},

		// Not an address at all.
		{"http://", ErrURLInvalid, ErrURLInvalid},
		{"https:///x", ErrURLInvalid, ErrURLInvalid},
		{"http://:80/x", ErrURLInvalid, ErrURLInvalid},
		{"https://user:pass@example.com/x", ErrURLInvalid, ErrURLInvalid},
		{"https://user@example.com/x", ErrURLInvalid, ErrURLInvalid},
		{"https://example.com/x#fragment", ErrURLInvalid, ErrURLInvalid},
		{"https://exa mple.com/x", ErrURLInvalid, ErrURLInvalid},
		{"https://example.com/x\n", ErrURLInvalid, ErrURLInvalid},
		{"https://example.com:abc/x", ErrURLInvalid, ErrURLInvalid},
		{"http://[::1/x", ErrURLInvalid, ErrURLInvalid},
		{"http:example.com", ErrURLInvalid, ErrURLInvalid},
		{long, ErrURLInvalid, ErrURLInvalid},
		{"https://example.com:0/x", ErrURLPort, ErrURLPort},
		{"https://example.com:65536/x", ErrURLPort, ErrURLPort},
		{"https://example.com:99999999999/x", ErrURLPort, ErrURLPort},
	} {
		for _, local := range []bool{false, true} {
			want := c.public
			if local {
				want = c.allowsLocal
			}
			label := c.url
			if len(label) > 60 {
				label = label[:60] + "..."
			}
			if got := CheckURL(c.url, local); !errors.Is(got, want) || (want == nil) != (got == nil) {
				t.Errorf("CheckURL(%q, allowLocal=%v) = %v, want %v", label, local, got, want)
			}
		}
	}
}

// The verdict on an address as the dialer sees it, written in every form: what is judged is the
// address behind the form.
func TestCheckAddr(t *testing.T) {
	for addr, c := range map[string][2]error{ // [public only, allow local]
		"8.8.8.8":                {nil, nil},
		"2606:4700::1111":        {nil, nil},
		"127.0.0.1":              {ErrLocalAddress, nil},
		"::1":                    {ErrLocalAddress, nil},
		"::ffff:127.0.0.1":       {ErrLocalAddress, nil},
		"10.1.2.3":               {ErrLocalAddress, nil},
		"::ffff:192.168.0.1":     {ErrLocalAddress, nil},
		"fd12:3456::1":           {ErrLocalAddress, nil},
		"100.64.0.1":             {ErrLocalAddress, nil},
		"64:ff9b::a00:1":         {ErrLocalAddress, nil}, // 10.0.0.1 over NAT64
		"169.254.169.254":        {ErrBlockedAddress, ErrBlockedAddress},
		"::ffff:169.254.169.254": {ErrBlockedAddress, ErrBlockedAddress},
		"64:ff9b::a9fe:a9fe":     {ErrBlockedAddress, ErrBlockedAddress},
		"fe80::1":                {ErrBlockedAddress, ErrBlockedAddress},
		"fe80::1%eth0":           {ErrBlockedAddress, ErrBlockedAddress},
		"fd00:ec2::254":          {ErrBlockedAddress, ErrBlockedAddress},
		"::ffff:100.100.100.200": {ErrBlockedAddress, ErrBlockedAddress},
		"fd00:ec2::254%eth0":     {ErrBlockedAddress, ErrBlockedAddress},
		"0.0.0.0":                {ErrBlockedAddress, ErrBlockedAddress},
		"::":                     {ErrBlockedAddress, ErrBlockedAddress},
		"224.0.0.251":            {ErrBlockedAddress, ErrBlockedAddress},
		"255.255.255.255":        {ErrBlockedAddress, ErrBlockedAddress},
	} {
		a := netip.MustParseAddr(addr)
		for i, local := range []bool{false, true} {
			if got := checkAddr(a, local); !errors.Is(got, c[i]) || (c[i] == nil) != (got == nil) {
				t.Errorf("checkAddr(%s, allowLocal=%v) = %v, want %v", addr, local, got, c[i])
			}
		}
	}
	if err := checkAddr(netip.Addr{}, true); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("the zero address: %v", err)
	}
}

// The dialer's check runs on what it is given, "ip:port" in any form, and refuses what it
// cannot read as an address.
func TestControl(t *testing.T) {
	for address, c := range map[string][2]error{
		"8.8.8.8:443":                 {nil, nil},
		"[2606:4700::1111]:443":       {nil, nil},
		"127.0.0.1:8080":              {ErrLocalAddress, nil},
		"[::1]:8080":                  {ErrLocalAddress, nil},
		"10.0.0.5:80":                 {ErrLocalAddress, nil},
		"169.254.169.254:80":          {ErrBlockedAddress, ErrBlockedAddress},
		"[fe80::1%eth0]:80":           {ErrBlockedAddress, ErrBlockedAddress},
		"[fd00:ec2::254]:80":          {ErrBlockedAddress, ErrBlockedAddress},
		"not-an-address:80":           {ErrBlockedAddress, ErrBlockedAddress},
		"example.com:80":              {ErrBlockedAddress, ErrBlockedAddress}, // a name never reaches the dialer's check
		"[::ffff:169.254.169.254]:80": {ErrBlockedAddress, ErrBlockedAddress},
	} {
		for i, local := range []bool{false, true} {
			got := control(local)("tcp", address, nil)
			if !errors.Is(got, c[i]) || (c[i] == nil) != (got == nil) {
				t.Errorf("control(allowLocal=%v)(%q) = %v, want %v", local, address, got, c[i])
			}
		}
	}
	if err := control(true)("tcp", "no port", nil); err == nil {
		t.Error("an address with no port was accepted")
	}
}

// The check is on the address a name leads to, when the connection is made: a name that gets
// past the address check of the form (a name with a dot, a public look) is refused when it
// leads to a local address, and on the address, not on the name.
func TestClientChecksTheAddressANameLeadsTo(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// localhost is what a name leads to 127.0.0.1 through; any name would do the same.
	target := "http://localhost:" + port + "/"
	do := func(c *http.Client) error {
		req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader("{}"))
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	err := do(newClient(false, requestTimeout))
	if failure(err) != FailLocal || hits.Load() != 0 {
		t.Fatalf("a name that leads to 127.0.0.1 went through: %v (%d hits)", err, hits.Load())
	}
	// The literal address is refused the same, and the refusal names the address.
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/", nil)
	if _, err := newClient(false, requestTimeout).Do(req); failure(err) != FailLocal || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the literal 127.0.0.1: %v", err)
	}
	if err := do(newClient(true, requestTimeout)); err != nil || hits.Load() != 1 {
		t.Fatalf("an endpoint that allows local targets: %v (%d hits)", err, hits.Load())
	}
	// Even those never reach the metadata service: the dialer refuses the address before it
	// connects (nothing is listening there; the answer is a refusal, not a timeout).
	req, _ = http.NewRequest(http.MethodPost, "http://169.254.169.254/latest/meta-data/", nil)
	start := time.Now()
	if _, err := newClient(true, requestTimeout).Do(req); failure(err) != FailBlocked || time.Since(start) > 2*time.Second {
		t.Fatalf("169.254.169.254 with allow_local: %v after %v", err, time.Since(start))
	}
}

// A redirect is the answer, not something to follow: the POST would be sent on to a place the
// admin did not name, and a redirect to a local address would not be judged by its address.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/landed", status)
		}))
		resp, err := newClient(true, requestTimeout).Post(srv.URL, "application/json", strings.NewReader("{}"))
		srv.Close()
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("answer %d, want the redirect itself (%d)", resp.StatusCode, status)
		}
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("a redirect was followed %d times", n)
	}
}

// Nothing the receiver does holds the worker or fills the memory: an answer that never ends is
// read up to the cap and no further, one that never comes is given up on, a connection that
// is refused or a certificate that is not trusted is told apart.
func TestClientLimits(t *testing.T) {
	t.Run("an endless answer is read to the cap", func(t *testing.T) {
		src := &endless{}
		n, err := io.Copy(io.Discard, io.LimitReader(src, maxAnswer))
		if err != nil || n != maxAnswer || src.read > maxAnswer+4096 {
			t.Fatalf("read %d (%d asked of the source), err %v", n, src.read, err)
		}
	})
	t.Run("an answer that never comes", func(t *testing.T) {
		hold := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hold }))
		t.Cleanup(func() { close(hold); srv.Close() })
		start := time.Now()
		_, err := newClient(true, 200*time.Millisecond).Post(srv.URL, "application/json", strings.NewReader("{}"))
		if failure(err) != FailTimeout || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("an answer that stops in the middle", func(t *testing.T) {
		hold := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000000")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-hold
		}))
		t.Cleanup(func() { close(hold); srv.Close() })
		start := time.Now()
		resp, err := newClient(true, 300*time.Millisecond).Post(srv.URL, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		// The body is read to the cap or the deadline: the client's own timeout covers it.
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("a stalled body: %v after %v", err, time.Since(start))
		}
	})
	t.Run("headers that never end", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Long", strings.Repeat("a", 2*maxHeaderBytes))
		}))
		t.Cleanup(srv.Close)
		if _, err := newClient(true, requestTimeout).Post(srv.URL, "application/json", strings.NewReader("{}")); err == nil {
			t.Fatal("an answer with 32 KiB of headers was taken")
		}
	})
	t.Run("a connection that is refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		_, err = newClient(true, requestTimeout).Post("http://"+addr+"/", "application/json", strings.NewReader("{}"))
		if failure(err) != FailRefused {
			t.Fatalf("%v is %q", err, failure(err))
		}
	})
	t.Run("a certificate nobody trusts", func(t *testing.T) {
		srv := tlsServer(t)
		_, err := newClient(true, requestTimeout).Post(srv.URL, "application/json", strings.NewReader("{}"))
		if failure(err) != FailTLS {
			t.Fatalf("%v is %q", err, failure(err))
		}
	})
	t.Run("http to a port that speaks TLS", func(t *testing.T) {
		srv := tlsServer(t)
		resp, err := newClient(true, requestTimeout).Post("http"+strings.TrimPrefix(srv.URL, "https"), "application/json", strings.NewReader("{}"))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				t.Fatalf("answered %d", resp.StatusCode)
			}
		}
	})
	t.Run("https to a port that speaks plain http", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(srv.Close)
		_, err := newClient(true, requestTimeout).Post("https"+strings.TrimPrefix(srv.URL, "http"), "application/json", strings.NewReader("{}"))
		if err == nil {
			t.Fatal("a handshake with a plain http server went through")
		}
	})
}

// tlsServer is a TLS server with a certificate nobody trusts; the failed handshakes it sees
// are the tests' doing, not worth a line of the log.
func tlsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// endless is a body that never ends; read counts what was asked of it.
type endless struct{ read int }

func (e *endless) Read(p []byte) (int, error) {
	e.read += len(p)
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// A request's error is sorted into the codes the log keeps, whatever wraps it.
func TestFailure(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://x.example/hook", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.OpError{Op: "connect", Err: syscall.ECONNREFUSED}}}
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"local":                 {&url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: fmt.Errorf("%w: 10.0.0.5", ErrLocalAddress)}}, FailLocal},
		"blocked":               {&url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: fmt.Errorf("%w: 169.254.169.254", ErrBlockedAddress)}}, FailBlocked},
		"no such name":          {&url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}}}, FailDNS},
		"a dns that timed out":  {&url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "i/o timeout", Name: "x.example", IsTimeout: true}}}, FailDNS},
		"refused":               {refused, FailRefused},
		"deadline":              {context.DeadlineExceeded, FailTimeout},
		"the client's timer":    {&url.Error{Op: "Post", Err: context.DeadlineExceeded}, FailTimeout},
		"a read that timed out": {&net.OpError{Op: "read", Err: timeoutError{}}, FailTimeout},
		"certificate":           {&tls.CertificateVerificationError{Err: errors.New("x509: certificate signed by unknown authority")}, FailTLS},
		"a handshake alert":     {&net.OpError{Op: "remote error", Err: tls.AlertError(42)}, FailTLS},
		"not TLS at all":        {tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, FailTLS},
		"reset":                 {&url.Error{Op: "Post", Err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}}, FailNetwork},
		"eof":                   {&url.Error{Op: "Post", Err: io.EOF}, FailNetwork},
		"anything else":         {errors.New("boom"), FailNetwork},
	} {
		if got := failure(c.err); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
