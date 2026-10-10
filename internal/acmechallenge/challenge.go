// Package acmechallenge answers ACME HTTP-01 challenges: the token's key authorization at
// /.well-known/acme-challenge/<token>, on port 80 (or MIKAN_ACME_LISTEN). The port is taken
// only while a challenge is pending and given back after it, so a web server of the admin's
// may use it the rest of the time. The panel answers its own challenges with it; a node
// answers the ones the panel orders for the node's address.
package acmechallenge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Prefix is where a CA asks for the token.
const Prefix = "/.well-known/acme-challenge/"

// DefaultListen is where challenges are answered unless MIKAN_ACME_LISTEN says otherwise:
// port 80 on every address, where the CA comes.
const DefaultListen = ":80"

// TTL is how long a token is answered when nobody takes it back: a panel that went away in
// the middle of a challenge must not keep port 80 for ever.
const TTL = 10 * time.Minute

// MaxPending bounds the tokens at once: a certificate has one per name, and the panel
// orders one certificate at a time.
const MaxPending = 32

// ErrPortBusy: another program holds the challenge's port.
var ErrPortBusy = errors.New("port80_busy")

// ErrTooMany: MaxPending tokens are pending already.
var ErrTooMany = errors.New("too_many_challenges")

// BusyError is a port held by another program; Holder is its name when it can be told.
type BusyError struct {
	Addr   string
	Holder string
}

func (e *BusyError) Error() string {
	if e.Holder != "" {
		return fmt.Sprintf("port80_busy: %s is held by %s", e.Addr, e.Holder)
	}
	return "port80_busy: " + e.Addr + " is held by another program"
}

func (e *BusyError) Is(target error) bool { return target == ErrPortBusy }

// ParseListen reads MIKAN_ACME_LISTEN: ":80" when empty. When nginx or Caddy holds port 80
// the installer sets a loopback port, e.g. 127.0.0.1:18080, and the proxy forwards
// /.well-known/acme-challenge/ there. The host is an IP address, localhost or empty (every
// address); the port is 1..65535.
func ParseListen(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultListen, nil
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", err
	}
	if host != "" && host != "localhost" && net.ParseIP(host) == nil {
		return "", fmt.Errorf("host %q is not an IP address", host)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("port %q is not 1..65535", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), nil
}

// ValidToken: an ACME token is base64url, 22 characters or more (RFC 8555 §8.1); a long
// one is no token either.
func ValidToken(token string) bool {
	if len(token) < 16 || len(token) > 256 {
		return false
	}
	for _, c := range []byte(token) {
		if !base64url(c) {
			return false
		}
	}
	return true
}

// ValidKeyAuth: the key authorization is the token, a dot and the account key's
// thumbprint (base64url).
func ValidKeyAuth(token, keyAuth string) bool {
	thumb, ok := strings.CutPrefix(keyAuth, token+".")
	if !ok || len(thumb) < 16 || len(thumb) > 128 {
		return false
	}
	for _, c := range []byte(thumb) {
		if !base64url(c) {
			return false
		}
	}
	return true
}

func base64url(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

type entry struct {
	keyAuth string
	expires time.Time
}

// Server answers the pending challenges. The zero value is not usable: see New.
type Server struct {
	addr   string
	now    func() time.Time
	listen func(network, addr string) (net.Listener, error)
	// holder names the program on a port, "" when it cannot be told.
	holder func(port int) string

	mu     sync.Mutex
	tokens map[string]entry
	srv    *http.Server
	ln     net.Listener
	timer  *time.Timer
}

// New answers challenges on addr (see ParseListen).
func New(addr string) *Server {
	return &Server{addr: addr, now: time.Now, listen: net.Listen, holder: Holder, tokens: map[string]entry{}}
}

// Addr is where challenges are answered.
func (s *Server) Addr() string { return s.addr }

// Present answers token with keyAuth until CleanUp or TTL; the port is taken now if it is
// not yet. A *BusyError when another program holds it.
func (s *Server) Present(token, keyAuth string) error {
	if !ValidToken(token) || !ValidKeyAuth(token, keyAuth) {
		return errors.New("not an ACME token and key authorization")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	if _, ok := s.tokens[token]; !ok && len(s.tokens) >= MaxPending {
		return ErrTooMany
	}
	if s.srv == nil {
		if err := s.start(); err != nil {
			return err
		}
	}
	s.tokens[token] = entry{keyAuth: keyAuth, expires: s.now().Add(TTL)}
	s.schedule()
	return nil
}

// CleanUp stops answering token; the port is given back once no token is left.
func (s *Server) CleanUp(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	s.expire()
}

// Pending is how many tokens are answered now.
func (s *Server) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	return len(s.tokens)
}

// Listening says whether the port is held now.
func (s *Server) Listening() bool { return s.Bound() != "" }

// Bound is the address held now, "" while none is.
func (s *Server) Bound() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// expire drops the tokens past their time and stops the server without any. Under s.mu.
func (s *Server) expire() {
	now := s.now()
	for t, e := range s.tokens {
		if !now.Before(e.expires) {
			delete(s.tokens, t)
		}
	}
	if len(s.tokens) == 0 && s.srv != nil {
		srv := s.srv
		s.srv, s.ln = nil, nil
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		// Close, not Shutdown: a CA's request in flight is over by now, and the port must be
		// free when this returns.
		_ = srv.Close()
	}
}

// schedule wakes expire when the earliest token runs out. Under s.mu.
func (s *Server) schedule() {
	var first time.Time
	for _, e := range s.tokens {
		if first.IsZero() || e.expires.Before(first) {
			first = e.expires
		}
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(first.Sub(s.now())+time.Second, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.expire()
	})
}

func (s *Server) start() error {
	ln, err := s.listen("tcp", s.addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") {
			be := &BusyError{Addr: s.addr}
			if _, p, perr := net.SplitHostPort(s.addr); perr == nil && s.holder != nil {
				if port, aerr := strconv.Atoi(p); aerr == nil {
					be.Holder = s.holder(port)
				}
			}
			return be
		}
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	s.srv, s.ln = srv, ln
	go func() { _ = srv.Serve(ln) }()
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.URL.Path, Prefix)
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	e, found := s.tokens[token]
	live := found && s.now().Before(e.expires)
	s.mu.Unlock()
	if !live {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(e.keyAuth))
}

// Close gives the port back and forgets every token.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
	s.expire()
}

// Fetch asks url for the key authorization as a CA would: a plain GET, redirects followed
// (at most ten, as Let's Encrypt), a short body. It returns what came back.
func Fetch(ctx context.Context, client *http.Client, url string) (status int, body string, server string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("User-Agent", "mikan-acme-check")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode, strings.TrimSpace(string(raw)), resp.Header.Get("Server"), nil
}
