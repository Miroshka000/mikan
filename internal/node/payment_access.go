package node

import (
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	N "github.com/metacubex/mihomo/common/net"
	C "github.com/metacubex/mihomo/constant"

	"mikan/internal/nodeapi"
)

const paymentRule = "payment-access"
const paymentDestinations = "payment-destinations"

// https://core.telegram.org/resources/cidr.txt
var telegramNetworks = []string{
	"91.108.56.0/22", "91.108.4.0/22", "91.108.8.0/22", "91.108.16.0/22",
	"91.108.12.0/22", "149.154.160.0/20", "91.105.192.0/23", "91.108.20.0/22",
	"185.76.151.0/24", "2001:b28:f23d::/48", "2001:b28:f23f::/48", "2001:67c:4e8::/48",
	"2001:b28:f23c::/48", "2a0a:f280::/32",
}

var telegramPrefixes = func() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(telegramNetworks))
	for _, cidr := range telegramNetworks {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}()

func paymentUser(name string) bool { return strings.HasPrefix(name, nodeapi.PaymentSlotPrefix) }

func paymentUsers(st nodeapi.DesiredState) string {
	if st.PaymentAccess == nil {
		return ""
	}
	names := make([]string, 0, len(st.PaymentAccess.Slots))
	for _, s := range st.PaymentAccess.Slots {
		names = append(names, s.Name)
	}
	return strings.Join(names, "/")
}

func paymentRules(st nodeapi.DesiredState) map[string][]string {
	guard := []string{"MATCH,REJECT"}
	out := map[string][]string{paymentRule: guard}
	if users := paymentUsers(st); users != "" {
		out[paymentRule] = append([]string{"SUB-RULE,(IN-USER," + users + ")," + paymentDestinations}, guard...)
		r := make([]string, 0, len(privateRules)+len(st.PaymentAccess.Domains)+len(telegramNetworks)+4)
		r = append(r, privateRules...)
		r = append(r, egressRules(st.Filters)...)
		r = append(r, "NETWORK,UDP,REJECT")
		if host, port := paymentPage(st.PaymentAccess); port != 0 {
			match := "DOMAIN," + host
			if ip, err := netip.ParseAddr(host); err == nil {
				kind := "IP-CIDR"
				if ip.Is6() {
					kind = "IP-CIDR6"
				}
				match = kind + "," + netip.PrefixFrom(ip, ip.BitLen()).String() + ",no-resolve"
			}
			r = append(r, "AND,(("+match+"),(DST-PORT,"+strconv.Itoa(int(port))+")),DIRECT")
		}
		r = append(r, "NOT,((DST-PORT,443)),REJECT")
		for _, raw := range st.PaymentAccess.Domains {
			if host, ok := nodeapi.PaymentDomain(raw); ok {
				r = append(r, "DOMAIN,"+host+",DIRECT")
			}
		}
		for i, prefix := range telegramPrefixes {
			kind := "IP-CIDR"
			if prefix.Addr().Is6() {
				kind = "IP-CIDR6"
			}
			r = append(r, kind+","+telegramNetworks[i]+",DIRECT,no-resolve")
		}
		out[paymentDestinations] = append(r, "MATCH,REJECT")
	}
	return out
}

type paymentTargets struct {
	hosts    map[string]bool
	pageHost string
	pagePort uint16
}

func paymentPage(p *nodeapi.PaymentAccess) (string, uint16) {
	if ip, err := netip.ParseAddr(p.PageHost); err == nil {
		return ip.Unmap().String(), p.PagePort
	}
	if host, ok := nodeapi.PaymentDomain(p.PageHost); ok {
		return host, p.PagePort
	}
	return "", 0
}

func (t *Tunnel) setPaymentHosts(p *nodeapi.PaymentAccess) {
	if p == nil || len(p.Slots) == 0 {
		t.paymentHosts.Store(nil)
		return
	}
	hosts := make(map[string]bool, len(p.Domains))
	for _, raw := range p.Domains {
		if host, ok := nodeapi.PaymentDomain(raw); ok {
			hosts[host] = true
		}
	}
	host, port := paymentPage(p)
	t.paymentHosts.Store(&paymentTargets{hosts: hosts, pageHost: host, pagePort: port})
}

// The original TLS SNI chooses the website, and the node resolves it. An address
// supplied by the client is used only for the native Telegram transport.
func (t *Tunnel) paymentTCP(conn net.Conn, m *C.Metadata) net.Conn {
	targets := t.paymentHosts.Load()
	if targets == nil || m.DstPort == 0 || m.DstPort != 443 && m.DstPort != targets.pagePort {
		_ = conn.Close()
		return nil
	}
	m.SpecialRules = paymentRule
	m.EgressGuard = C.EgressGuardOn
	ip := m.DstIP.Unmap()
	if m.Host == "" && ip.IsValid() && m.DstPort == targets.pagePort && ip.String() == targets.pageHost {
		return conn
	}
	if m.DstPort == 443 && m.Host == "" {
		for _, prefix := range telegramPrefixes {
			if prefix.Contains(ip) {
				return conn
			}
		}
	}
	hello, name := readHello(conn)
	host, ok := nodeapi.PaymentDomain(name)
	allowed := m.DstPort == 443 && targets.hosts[host] || m.DstPort == targets.pagePort && host == targets.pageHost
	if !ok || !allowed {
		_ = conn.Close()
		return nil
	}
	m.Host, m.SniffHost = host, ""
	m.DstIP, m.DstIPChecked, m.DNSMode = netip.Addr{}, false, C.DNSNormal
	return N.NewCachedConn(conn, hello)
}

// A removed key's limit disappears from Citrus. Close its tracked connections as
// well, including connections to a domain that the admin has just removed.
func (e *Engine) closePaymentConnections(next *nodeapi.PaymentAccess) {
	prev := e.applied.PaymentAccess
	if prev == nil {
		return
	}
	keep := map[string]string{}
	if next != nil && next.Inbound == prev.Inbound && next.PageHost == prev.PageHost && next.PagePort == prev.PagePort && slices.Equal(next.Domains, prev.Domains) {
		for _, s := range next.Slots {
			keep[s.Name] = s.UUID
		}
	}
	var keys []userKey
	for _, s := range prev.Slots {
		if keep[s.Name] != s.UUID {
			keys = append(keys, userKey{inbound: prev.Inbound, user: s.Name})
		}
	}
	if len(keys) > 0 {
		e.Reg.core.closeConns(keys)
	}
}
