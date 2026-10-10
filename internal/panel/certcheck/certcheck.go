// Package certcheck is "Check the certificate": what the panel's and the nodes' ports
// serve as seen from outside, whether the domain leads here, whether port 80 reaches the
// panel for the CA, and how the orders went. Every finding is a code the admin panel
// translates (certCheck.<code>) with what to do, and a button or a command where one fixes it.
package certcheck

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/dnscheck"
)

// The outcomes of a check: ok, info (worth knowing, nothing broken), warn (works, but not
// everywhere), fail (does not work).
const (
	OK   = "ok"
	Info = "info"
	Warn = "warn"
	Fail = "fail"
)

// The fixes the admin panel offers as a button, or a command to copy.
const (
	FixRenew      = "renew"       // the panel's certificate: «Получить сейчас» / «Повторить»
	FixRenewNode  = "renew_node"  // a node's certificate
	FixUseZeroSSL = "use_zerossl" // choose ZeroSSL
	FixUpdateNode = "update_node" // update the node from the panel
	FixCopy       = "copy"        // run Command on the server
)

// CertCheck is one finding.
type CertCheck struct {
	ID      string            `json:"id" doc:"Что проверено: served, compat, dns, port80, acme, node_cert, node_port"`
	Status  string            `json:"status" enum:"ok,info,warn,fail"`
	Code    string            `json:"code" doc:"Что именно, текст — certCheck.<code> в языке панели"`
	Params  map[string]string `json:"params,omitempty"`
	NodeID  int64             `json:"node_id,omitempty"`
	Node    string            `json:"node,omitempty" doc:"Имя ноды, если проверка про ноду"`
	Detail  string            `json:"detail,omitempty" doc:"Подробности как есть: ошибка соединения или ответ центра сертификации"`
	CertFix *CertFix          `json:"fix,omitempty"`
}

// CertFix is what repairs a finding.
type CertFix struct {
	Action  string `json:"action" enum:"renew,renew_node,use_zerossl,update_node,copy"`
	Command string `json:"command,omitempty" doc:"Команда для сервера (action copy)"`
	NodeID  int64  `json:"node_id,omitempty"`
}

// CertReport is every finding, the worst first.
type CertReport struct {
	Checks    []CertCheck `json:"checks"`
	CheckedAt time.Time   `json:"checked_at"`
}

// Input is what is checked.
type Input struct {
	Domain string // the panel's domain, "" without one
	Host   string // the panel's address: the domain, or the public IP
	Ports  []int  // the panel's port, and the subscription port when it has one
	Own    []netip.Addr
	// Expected is the leaf the panel serves now: what answers instead is someone else's.
	Expected *x509.Certificate
	Status   acme.Status
	// Challenge is the panel's HTTP-01 responder; nil skips the port 80 check.
	Challenge *acmechallenge.Server
	Nodes     []Node
}

// Node is a node to check.
type Node struct {
	ID    int64
	Name  string
	Local bool
	Host  string // where clients reach it
	SNI   string // its domain, "" on an IP
	Kind  string // custom, acme, panel, self-signed: what it serves on its protocols on TLS
	// Pinned: links pin a self-signed certificate.
	Pinned   bool
	Expected *x509.Certificate
	ACME     *acme.NodeStatus
	// Ports are its listeners on TLS over TCP, by inbound name: what can be dialled.
	Ports []Port
}

type Port struct {
	Inbound string
	Port    int
}

// Checker runs the checks; its fields are what a test replaces.
type Checker struct {
	DNS interface {
		Lookup(ctx context.Context, name string) ([]netip.Addr, error)
	}
	// Dial returns the chain a TLS server at addr presents for sni.
	Dial func(ctx context.Context, addr, sni string) ([]*x509.Certificate, error)
	// Fetch gets url as a CA does: status, the start of the body, the Server header.
	Fetch func(ctx context.Context, url string) (int, string, string, error)
	Roots *x509.CertPool // nil: the system's
	Now   func() time.Time
}

// New checks with the system's resolver, roots and network.
func New(dns *dnscheck.Checker) *Checker {
	return &Checker{DNS: dns, Dial: dialChain, Fetch: fetch, Now: time.Now}
}

// Run checks everything; it takes up to some ten seconds.
func (c *Checker) Run(ctx context.Context, in Input) CertReport {
	var (
		mu  sync.Mutex
		out []CertCheck
		wg  sync.WaitGroup
	)
	add := func(cs ...CertCheck) {
		mu.Lock()
		out = append(out, cs...)
		mu.Unlock()
	}
	for _, p := range dedupe(in.Ports) {
		wg.Go(func() { add(c.served(ctx, in, p)...) })
	}
	wg.Go(func() { add(c.dns(ctx, in)) })
	wg.Go(func() {
		if ch, ok := c.port80(ctx, in); ok {
			add(ch)
		}
	})
	add(acmeCheck(in.Status)...)
	for _, n := range in.Nodes {
		add(nodeCert(n, in.Status))
		for _, p := range n.Ports {
			wg.Go(func() { add(c.nodePort(ctx, n, p)) })
		}
	}
	wg.Wait()
	sortChecks(out)
	return CertReport{Checks: out, CheckedAt: c.Now()}
}

func dedupe(ports []int) []int {
	var out []int
	for _, p := range ports {
		if p > 0 && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// rank orders findings: what fails first, what is fine last; then as they came.
var rank = map[string]int{Fail: 0, Warn: 1, Info: 2, OK: 3}

func sortChecks(cs []CertCheck) {
	order := map[string]int{"served": 0, "acme": 1, "dns": 2, "port80": 3, "compat": 4, "node_cert": 5, "node_port": 6}
	slices.SortStableFunc(cs, func(a, b CertCheck) int {
		return cmp.Or(cmp.Compare(rank[a.Status], rank[b.Status]), cmp.Compare(order[a.ID], order[b.ID]), cmp.Compare(a.NodeID, b.NodeID),
			cmp.Compare(a.Params["port"], b.Params["port"]))
	})
}

// served is what the panel's port presents to a client that comes by its address.
func (c *Checker) served(ctx context.Context, in Input, port int) []CertCheck {
	params := map[string]string{"port": strconv.Itoa(port)}
	if in.Host == "" {
		return []CertCheck{{ID: "served", Status: Warn, Code: "served_no_host", Params: params}}
	}
	dctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	chain, err := c.Dial(dctx, net.JoinHostPort(in.Host, strconv.Itoa(port)), in.Domain)
	if err != nil {
		return []CertCheck{{ID: "served", Status: Fail, Code: "served_unreachable", Params: params, Detail: err.Error()}}
	}
	v := c.verify(chain, in.Host)
	maps.Copy(params, v.params)
	ch := CertCheck{ID: "served", Params: params}
	if in.Expected != nil && !bytes.Equal(chain[0].Raw, in.Expected.Raw) {
		// Another program answers on the panel's port, or a proxy in front of it: nginx with
		// certbot's certificate, a CDN.
		ch.Status, ch.Code = Fail, "served_foreign"
		return []CertCheck{ch}
	}
	ch.Status, ch.Code = v.status, v.code
	if v.code == "served_self_signed" || v.code == "served_expired" {
		ch.CertFix = &CertFix{Action: FixRenew}
	}
	out := []CertCheck{ch}
	// Let's Encrypt is trusted by every current system, not by Android 7 and older and many
	// TV boxes: ZeroSSL chains to roots they have. Only for a domain: an IP stays with LE.
	if v.status == OK && strings.Contains(strings.ToLower(chain[0].Issuer.String()), "let's encrypt") && in.Domain != "" && port == firstPort(in.Ports) {
		out = append(out, CertCheck{ID: "compat", Status: Info, Code: "compat_le_old_devices", CertFix: &CertFix{Action: FixUseZeroSSL}})
	}
	return out
}

func firstPort(ports []int) int {
	for _, p := range ports {
		if p > 0 {
			return p
		}
	}
	return 0
}

type verdict struct {
	status, code string
	params       map[string]string
}

// verify says how a client sees chain for host: trusted, self-signed, for another name,
// expired, or missing its intermediate.
func (c *Checker) verify(chain []*x509.Certificate, host string) verdict {
	leaf := chain[0]
	now := c.Now()
	days := int(leaf.NotAfter.Sub(now).Hours() / 24)
	params := map[string]string{"issuer": issuerName(leaf), "names": strings.Join(names(leaf), ", "),
		"until": leaf.NotAfter.UTC().Format(time.RFC3339), "days": strconv.Itoa(days)}
	inter := x509.NewCertPool()
	for _, ic := range chain[1:] {
		inter.AddCert(ic)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: c.Roots, Intermediates: inter, DNSName: strings.Trim(host, "[]"), CurrentTime: now})
	var (
		hostErr    x509.HostnameError
		invalidErr x509.CertificateInvalidError
		authErr    x509.UnknownAuthorityError
	)
	switch {
	case err == nil && days < 7:
		return verdict{Warn, "served_expiring", params}
	case err == nil:
		return verdict{OK, "served_ok", params}
	case bytes.Equal(leaf.RawIssuer, leaf.RawSubject):
		return verdict{Warn, "served_self_signed", params}
	case errors.As(err, &invalidErr) && invalidErr.Reason == x509.Expired:
		return verdict{Fail, "served_expired", params}
	case errors.As(err, &hostErr):
		return verdict{Fail, "served_wrong_host", params}
	case errors.As(err, &authErr) && len(chain) == 1:
		// A public CA's leaf alone: the server sends no intermediate, which browsers fetch
		// but VPN apps and old phones do not.
		return verdict{Fail, "served_chain_incomplete", params}
	}
	params["error"] = err.Error()
	return verdict{Fail, "served_untrusted", params}
}

func issuerName(c *x509.Certificate) string {
	if c.Issuer.CommonName != "" {
		if len(c.Issuer.Organization) > 0 && !strings.Contains(c.Issuer.CommonName, c.Issuer.Organization[0]) {
			return c.Issuer.Organization[0] + " " + c.Issuer.CommonName
		}
		return c.Issuer.CommonName
	}
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return ""
}

func names(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	if len(out) == 0 && c.Subject.CommonName != "" {
		out = []string{c.Subject.CommonName}
	}
	return out
}

// dns: the domain's records lead to this server, not through Cloudflare's proxy.
func (c *Checker) dns(ctx context.Context, in Input) CertCheck {
	ch := CertCheck{ID: "dns", Params: map[string]string{"domain": in.Domain}}
	if in.Domain == "" {
		ch.Status, ch.Code = Info, "dns_ip_only"
		ch.Params["host"] = in.Host
		return ch
	}
	found, err := c.DNS.Lookup(ctx, in.Domain)
	switch {
	case errors.Is(err, dnscheck.ErrNotFound):
		ch.Status, ch.Code = Fail, "dns_none"
		return ch
	case err != nil:
		ch.Status, ch.Code, ch.Detail = Warn, "dns_unknown", err.Error()
		return ch
	}
	ch.Params["ips"] = dnscheck.Join(found)
	ch.Params["own"] = dnscheck.Join(in.Own)
	var foreign []netip.Addr
	for _, a := range found {
		if dnscheck.Cloudflare(a) {
			ch.Status, ch.Code = Fail, "dns_cloudflare"
			return ch
		}
		if !slices.Contains(in.Own, a) {
			foreign = append(foreign, a)
		}
	}
	if len(foreign) > 0 && len(in.Own) > 0 {
		ch.Status, ch.Code = Fail, "dns_elsewhere"
		ch.Params["foreign"] = dnscheck.Join(foreign)
		return ch
	}
	ch.Status, ch.Code = OK, "dns_ok"
	return ch
}

// port80: a token of the check's own on the panel's challenge responder, fetched through
// the panel's public address as the CA would.
func (c *Checker) port80(ctx context.Context, in Input) (CertCheck, bool) {
	if in.Challenge == nil || in.Host == "" || in.Status.Kind == "custom" {
		return CertCheck{}, false
	}
	ch := CertCheck{ID: "port80", Params: map[string]string{"listen": in.Challenge.Addr()}}
	if in.Status.Ordering {
		ch.Status, ch.Code = Info, "port80_ordering"
		return ch, true
	}
	token, keyAuth := testToken()
	err := in.Challenge.Present(token, keyAuth)
	var busy *acmechallenge.BusyError
	switch {
	case errors.As(err, &busy):
		ch.Status, ch.Code = Fail, "port80_busy"
		ch.Params["holder"] = busy.Holder
		ch.CertFix = &CertFix{Action: FixCopy, Command: "ss -ltnp 'sport = :80'"}
		return ch, true
	case err != nil:
		ch.Status, ch.Code, ch.Detail = Fail, "port80_error", err.Error()
		return ch, true
	}
	defer in.Challenge.CleanUp(token)
	fctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	status, body, server, err := c.Fetch(fctx, "http://"+hostPort(in.Host)+acmechallenge.Prefix+token)
	switch {
	case err != nil:
		ch.Status, ch.Code, ch.Detail = Fail, "port80_closed", err.Error()
		ch.CertFix = &CertFix{Action: FixCopy, Command: "ufw allow 80/tcp"}
	case status == 200 && body == keyAuth:
		ch.Status, ch.Code = OK, "port80_ok"
	default:
		ch.Status, ch.Code = Fail, "port80_foreign"
		ch.Params["status"], ch.Params["server"] = strconv.Itoa(status), server
	}
	return ch, true
}

func hostPort(host string) string {
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		return "[" + host + "]"
	}
	return host
}

// testToken is a token and key authorization of the right shape that no CA asked for.
func testToken() (token, keyAuth string) {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token = base64.RawURLEncoding.EncodeToString(raw[:24])
	sum := sha256.Sum256(raw)
	return token, token + "." + base64.RawURLEncoding.EncodeToString(sum[:])
}

// acmeCheck is how the panel's last order went.
func acmeCheck(st acme.Status) []CertCheck {
	switch {
	case st.Kind == "custom":
		return nil
	case st.Error != "" && !strings.HasPrefix(st.Error, "custom_"):
		ch := CertCheck{ID: "acme", Status: Fail, Code: "acme_error", Detail: st.ErrorDetail,
			Params: map[string]string{"error": st.Error, "holder": st.Holder, "ca": st.WantCA}, CertFix: &CertFix{Action: FixRenew}}
		if st.RetryAt != nil {
			ch.Params["retry_at"] = st.RetryAt.UTC().Format(time.RFC3339)
		}
		if st.Error == "no_public_host" {
			ch.CertFix = nil
		}
		return []CertCheck{ch}
	case st.Ordering:
		return []CertCheck{{ID: "acme", Status: Info, Code: "acme_ordering", Params: map[string]string{"ca": st.WantCA}}}
	case st.Kind == "acme" && st.CA != st.WantCA:
		return []CertCheck{{ID: "acme", Status: Info, Code: "acme_switching", Params: map[string]string{"ca": st.CA, "want": st.WantCA}, CertFix: &CertFix{Action: FixRenew}}}
	case st.Kind == "acme":
		return []CertCheck{{ID: "acme", Status: OK, Code: "acme_ok", Params: map[string]string{"ca": st.CA, "until": st.NotAfter.UTC().Format(time.RFC3339)}}}
	}
	return []CertCheck{{ID: "acme", Status: Warn, Code: "acme_none", CertFix: &CertFix{Action: FixRenew}}}
}

// nodeCert is the certificate a node's protocols on TLS use, and its order.
func nodeCert(n Node, panel acme.Status) CertCheck {
	ch := CertCheck{ID: "node_cert", NodeID: n.ID, Node: n.Name, Params: map[string]string{"host": n.Host}}
	switch {
	case n.Kind == "custom":
		ch.Status, ch.Code = OK, "node_own"
		if n.Pinned {
			ch.Status, ch.Code = Info, "node_own_pinned"
		}
		return ch
	case n.Kind == "panel":
		ch.Status, ch.Code = OK, "node_panel"
		return ch
	case n.Local:
		// The panel's own node takes the panel's certificate once it is public.
		ch.Status, ch.Code = Warn, "node_local_self_signed"
		ch.CertFix = &CertFix{Action: FixRenew}
		if panel.Kind == "custom" || panel.Error == "no_public_host" {
			ch.CertFix = nil
		}
		return ch
	}
	st := n.ACME
	if st != nil && st.Error != "" {
		ch.Detail = st.ErrorDetail
		ch.Params["error"], ch.Params["holder"] = st.Error, st.Holder
		if st.RetryAt != nil {
			ch.Params["retry_at"] = st.RetryAt.UTC().Format(time.RFC3339)
		}
		switch st.Error {
		case acme.CodeNodeOutdated:
			ch.Status, ch.Code = Warn, "node_outdated"
			ch.CertFix = &CertFix{Action: FixUpdateNode, NodeID: n.ID}
			return ch
		case acme.CodePort80Timeout, acme.CodePort80Refused:
			ch.CertFix = &CertFix{Action: FixCopy, Command: "ufw allow 80/tcp", NodeID: n.ID}
		case acme.CodePort80Busy:
			ch.CertFix = &CertFix{Action: FixCopy, Command: "ss -ltnp 'sport = :80'", NodeID: n.ID}
		case "no_public_host":
		default:
			ch.CertFix = &CertFix{Action: FixRenewNode, NodeID: n.ID}
		}
		ch.Status, ch.Code = Fail, "node_acme_error"
		if n.Kind == "acme" {
			// It still has a valid one: the renewal failed.
			ch.Status = Warn
		}
		return ch
	}
	if n.Kind == "acme" {
		ch.Status, ch.Code = OK, "node_acme_ok"
		if st != nil {
			ch.Params["ca"] = st.CA
			if st.NotAfter != nil {
				ch.Params["until"] = st.NotAfter.UTC().Format(time.RFC3339)
			}
		}
		return ch
	}
	ch.Status, ch.Code = Warn, "node_self_signed"
	ch.CertFix = &CertFix{Action: FixRenewNode, NodeID: n.ID}
	if st != nil && st.Ordering {
		ch.Status, ch.Code, ch.CertFix = Info, "node_ordering", nil
	}
	return ch
}

// nodePort is what a node's listener on TLS over TCP presents from outside.
func (c *Checker) nodePort(ctx context.Context, n Node, p Port) CertCheck {
	ch := CertCheck{ID: "node_port", NodeID: n.ID, Node: n.Name, Params: map[string]string{"inbound": p.Inbound, "port": strconv.Itoa(p.Port)}}
	dctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	chain, err := c.Dial(dctx, net.JoinHostPort(n.Host, strconv.Itoa(p.Port)), n.SNI)
	if err != nil {
		ch.Status, ch.Code, ch.Detail = Warn, "node_port_unreachable", err.Error()
		return ch
	}
	v := c.verify(chain, n.Host)
	maps.Copy(ch.Params, v.params)
	switch {
	case n.Expected != nil && !bytes.Equal(chain[0].Raw, n.Expected.Raw):
		// The node has not taken the certificate yet, or another program holds the port.
		ch.Status, ch.Code = Warn, "node_port_other"
	case n.Pinned:
		ch.Status, ch.Code = Info, "node_port_pinned"
	case v.status == OK:
		ch.Status, ch.Code = OK, "node_port_ok"
	default:
		ch.Status, ch.Code = v.status, v.code
	}
	return ch
}

// dialChain completes a TLS handshake with addr and returns what the server presented,
// trusted or not: the check judges it itself.
func dialChain(ctx context.Context, addr, sni string) ([]*x509.Certificate, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // the chain is inspected, not trusted
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	chain := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return nil, errors.New("no certificate")
	}
	return chain, nil
}

// httpClient fetches the test token: a CA follows up to ten redirects and gives up soon.
var httpClient = &http.Client{Timeout: 8 * time.Second}

func fetch(ctx context.Context, url string) (int, string, string, error) {
	return acmechallenge.Fetch(ctx, httpClient, url)
}
