package node

import (
	"net"
	"net/netip"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

const payUser = "payment-12-1800000000"

func TestPaymentAccessCitrusRules(t *testing.T) {
	st := nodeapi.DesiredState{PaymentAccess: &nodeapi.PaymentAccess{
		Inbound: "test", Domains: []string{"yoomoney.ru"}, PageHost: "sub.example", PagePort: 8443,
		Slots: []nodeapi.Slot{{Name: payUser}},
	}}
	raw, _, err := buildConfig(st, proto.Cert{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, user, host, ip string
		port                 uint16
		network              C.NetWork
		want                 string
	}{
		{"checkout", payUser, "yoomoney.ru", "", 443, C.TCP, "DIRECT"},
		{"telegram IPv4", payUser, "", "149.154.160.1", 443, C.TCP, "DIRECT"},
		{"telegram IPv6", payUser, "", "2001:b28:f23d::1", 443, C.TCP, "DIRECT"},
		{"arbitrary host", payUser, "example.org", "", 443, C.TCP, "REJECT"},
		{"unlisted subdomain", payUser, "other.yoomoney.ru", "", 443, C.TCP, "REJECT"},
		{"wrong user", "regular", "yoomoney.ru", "", 443, C.TCP, "REJECT"},
		{"wrong port", payUser, "yoomoney.ru", "", 8443, C.TCP, "REJECT"},
		{"subscription page on its own port", payUser, "sub.example", "", 8443, C.TCP, "DIRECT"},
		{"UDP", payUser, "yoomoney.ru", "", 443, C.UDP, "REJECT"},
		{"private address", payUser, "yoomoney.ru", "127.0.0.1", 443, C.TCP, "REJECT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip, _ := netip.ParseAddr(tc.ip)
			m := &C.Metadata{InUser: tc.user, Host: tc.host, DstIP: ip, DstPort: tc.port, NetWork: tc.network}
			for _, rule := range cfg.SubRules[paymentRule] {
				if ok, got := rule.Match(m, C.RuleMatchHelper{ResolveIP: func() {}}); ok {
					if got != tc.want {
						t.Errorf("rule chose %q, want %q", got, tc.want)
					}
					return
				}
			}
			t.Fatal("no rule matched")
		})
	}
}

func TestPaymentAccessSubscriptionAddress(t *testing.T) {
	st := nodeapi.DesiredState{PaymentAccess: &nodeapi.PaymentAccess{
		Inbound: "test", PageHost: "198.51.100.12", PagePort: 8443,
		Slots: []nodeapi.Slot{{Name: payUser}},
	}}
	raw, _, err := buildConfig(st, proto.Cert{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ip, want string }{{"198.51.100.12", "DIRECT"}, {"198.51.100.13", "REJECT"}} {
		m := &C.Metadata{InUser: payUser, DstIP: netip.MustParseAddr(tc.ip), DstPort: 8443, NetWork: C.TCP}
		got := ""
		for _, r := range cfg.SubRules[paymentRule] {
			if ok, adapter := r.Match(m, C.RuleMatchHelper{}); ok {
				got = adapter
				break
			}
		}
		if got != tc.want {
			t.Errorf("%s: adapter %q, want %q", tc.ip, got, tc.want)
		}
	}
}

func TestPaymentAccessTakesDestinationFromTLS(t *testing.T) {
	tun := &Tunnel{}
	tun.setPaymentHosts(&nodeapi.PaymentAccess{Domains: []string{"yoomoney.ru"}, Slots: []nodeapi.Slot{{Name: payUser}}})
	for _, name := range []string{"yoomoney.ru", "unlisted.example"} {
		t.Run(name, func(t *testing.T) {
			hello := plainHello(t, name)
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			go b.Write(hello)
			md := &C.Metadata{Host: "different.example", DstIP: netip.MustParseAddr("192.0.2.1"), DstPort: 443}
			got := tun.paymentTCP(a, md)
			if name != "yoomoney.ru" {
				if got != nil {
					t.Fatal("an unlisted SNI was accepted")
				}
				return
			}
			if got == nil || md.Host != name || md.DstIP.IsValid() || md.SpecialRules != paymentRule {
				t.Fatalf("connection=%v metadata=%+v", got, md)
			}
		})
	}
}

func TestPaymentAccessExpiryAndQuota(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	core := &fakeCore{}
	reg := NewRegistry("test", 0, time.Minute, func() time.Time { return now }, core)
	reg.SetInbounds(map[string]bool{"pay": false, "other": false})
	reg.SetSlots([]nodeapi.Slot{{Name: payUser}})
	reg.SetPolicies("test", []nodeapi.Policy{{
		Slot: payUser, Allowed: true, Inbounds: []string{"pay"}, ExpiresAt: now.Add(time.Minute).Unix(),
		QuotaRemaining: 1024, SpeedMbps: 1,
	}})
	if reg.admitIn(payUser, "pay", "127.0.0.1") == nil || reg.admitIn(payUser, "other", "127.0.0.1") != nil {
		t.Fatal("payment policy must allow exactly one inbound")
	}
	if lim := core.held[userKey{"pay", payUser}]; lim.budget != 1024 || lim.up != mbit || lim.down != mbit {
		t.Fatalf("incorrect core limit: %+v", lim)
	}
	core.moved("pay", payUser, 512, 512)
	reg.drain()
	if reg.admitIn(payUser, "pay", "127.0.0.1") != nil {
		t.Fatal("an exhausted key was accepted")
	}
	reg.SetPolicies("test", []nodeapi.Policy{{Slot: payUser, Allowed: true, QuotaRemaining: 2048, ExpiresAt: now.Add(time.Minute).Unix()}})
	now = now.Add(time.Minute)
	reg.reconcile()
	if reg.admitIn(payUser, "pay", "127.0.0.1") != nil || !core.held[userKey{"pay", payUser}].out {
		t.Fatal("the deadline was not enforced at the node")
	}
}
