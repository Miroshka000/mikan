package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Payment access after expiry (payment.sh), on the stack of guard.sh: the node's REJECT of
// private ranges and its egress guard act as on a real server, and the subscription page is
// the panel's own, beside the node.
const (
	// The sites of compose.yaml: pay-site is on the renewal list, pay-other is not.
	paySite, payOther         = "pay.test", "pay-other.test"
	paySiteName, payOtherName = "pay-site", "pay-other"
	// The client's mixed ports: the renewal key on guardPort, so the guard's helpers go
	// through it; the subscription's own key, taken before it expired; the key of the
	// renewal window before the current one.
	payRegularPort, payOldPort = 11002, 11003
	// The renewal limits; the quota is small enough to spend within the phase.
	payTrafficMiB, paySpeedMbps = 2, 5
	// payDeadline bounds every exchange with a site.
	payDeadline = 20 * time.Second
	// The subscription's own VLESS on the node's certificate: REALITY would borrow its
	// handshake from an outside site.
	payInbound, payRegularProxy = "vless-tls-vision", "VLESS TLS Vision"
	payStateFile                = "/work/payment.json"
)

var renewalLabels = []string{"Продлить подписку", "Renew subscription"}

// paymentState is what the phases hand on: the subscription, its own proxy taken before
// it expired, and the renewal proxies of the current window and of the one before.
type paymentState struct {
	Token    string         `json:"token"`
	User     int64          `json:"user"`
	Regular  map[string]any `json:"regular"`
	Renewal  map[string]any `json:"renewal"`
	Previous map[string]any `json:"previous,omitempty"`
}

func paymentPrepare() {
	p := login()
	const freshInstall = 4 // default inbounds
	waitNode(p, freshInstall)
	p.call("POST", "/api/v1/inbounds", map[string]any{"preset": "vless_tls_vision"}, nil)
	waitNode(p, freshInstall+1)
	p.call("PATCH", "/api/v1/settings", map[string]any{"sub_routing": "all", "auto_port": false, "auto_sni": false, "sub_inactive_server": true}, nil)
	enablePaymentAccess(p)

	u := createUser(p)
	st := paymentState{Token: u.token(), User: u.ID, Regular: profileProxy(clashProxies(u.token()), payRegularProxy)}
	if st.Regular == nil {
		log.Fatalf("payment: the active subscription has no %q", payRegularProxy)
	}
	expire(p, u.ID, time.Hour) // inside the renewal window
	st.Renewal = renewalProxy(st.Token)
	if st.Renewal["uuid"] == st.Regular["uuid"] {
		log.Fatal("payment: the renewal profile carries the subscription's own key")
	}
	log.Printf("payment: the expired subscription gets the renewal profile %q", st.Renewal["name"])
	st.save()
	log.Print("payment: client config written from the renewal profile")
}

// enablePaymentAccess turns renewal access on for the VLESS inbound, once the panel has
// refused what the renewal keys may not be.
func enablePaymentAccess(p *panel) {
	vless, other := paymentInbounds(p)
	access := func(inbound int64, days int, domains ...string) map[string]any {
		return map[string]any{"sub_payment_access": map[string]any{
			"inbound_id": inbound, "days": days, "traffic_mib": payTrafficMiB, "speed_mbps": paySpeedMbps, "domains": domains,
		}}
	}
	for what, body := range map[string]map[string]any{
		"an inbound that is not VLESS": access(other, 3, paySite),
		"a wildcard name":              access(vless, 3, "*."+paySite),
		"a window over 14 days":        access(vless, 15, paySite),
	} {
		if code := p.try("PATCH", "/api/v1/settings", body); code != 422 {
			log.Fatalf("payment: %s must be refused, got %d", what, code)
		}
	}
	p.call("PATCH", "/api/v1/settings", access(vless, 3, paySite), nil)
}

// paymentCheck: the first renewal window, the payment, then a new expiry; it ends with
// the client's config for the new window (payment-renewed).
func paymentCheck() {
	p := login()
	st := loadPaymentState()
	checkRenewalReach(st)
	checkRenewalLimits(p, st)

	pay(p, st.User)
	eventually("payment: after the payment the regular profile is back", func() bool { return regularProfile(st) })
	eventually("payment: the subscription's own key works again, wherever it goes", func() bool {
		return answers(payRegularPort, payOther+":9000", "", payOtherName) && answers(payRegularPort, payOther+":443", payOther, payOtherName)
	})
	expire(p, st.User, time.Minute)
	renewal := renewalProxy(st.Token)
	if renewal["uuid"] == st.Renewal["uuid"] {
		log.Fatal("payment: a new expiry kept the old window's key")
	}
	st.Previous, st.Renewal = st.Renewal, renewal
	st.save()
	log.Print("payment: a new expiry gets a key of its own; client config written for it")
}

// checkRenewalReach: the renewal key reaches the subscription page and the listed site,
// chosen by the name in the ClientHello, and nothing else; the subscription's own key is
// refused.
func checkRenewalReach(st paymentState) {
	eventually("payment: the renewal key opens the subscription page", func() bool { return subThroughNode(2053, st.Token) })
	eventually("payment: the renewal key reaches the listed site", func() bool { return listedSiteOpen(guardPort) })
	if !answers(guardPort, payOther+":443", paySite, paySiteName) {
		log.Fatal("payment: another address under the listed name must reach the listed site, as the node resolves the name")
	}
	log.Print("payment: the listed name chooses the site, not the address the client asks for")
	for _, c := range []struct{ what, addr, sni string }{
		{"an unlisted name at the listed site's address", paySite + ":443", payOther},
		{"an unlisted site", payOther + ":443", payOther},
		{"the listed site on another port", paySite + ":8443", paySite},
		{"the listed site without TLS", paySite + ":443", ""},
		{"the listed site's plain port", paySite + ":9000", ""},
	} {
		if name, err := siteName(guardPort, c.addr, c.sni); err == nil {
			log.Fatalf("payment: %s answered through the renewal key (%s)", c.what, name)
		}
	}
	if echoesThroughNode(net.JoinHostPort(guardHost, guardWeb)) {
		log.Fatal("payment: the server's web port answered through the renewal key")
	}
	log.Print("payment: nothing else answers through the renewal key")
	if _, err := siteName(payRegularPort, payOther+":9000", ""); err == nil {
		log.Fatal("payment: the expired subscription's own key still works")
	}
	log.Print("payment: the subscription's own key is refused")
}

// checkRenewalLimits: the renewal speed and quota hold, and the renewal traffic is not the
// subscription's own.
func checkRenewalLimits(p *panel, st paymentState) {
	const sample, over = 3 * mib / 2, 4 * mib
	got, took, err := siteDownload(guardPort, sample)
	if err != nil {
		log.Fatalf("payment: %d of %d bytes within the quota: %v", got, sample, err)
	}
	rate := float64(got*8) / took.Seconds() / 1e6
	log.Printf("payment: %.1f Mbit/s under a limit of %d", rate, paySpeedMbps)
	if rate > 2*paySpeedMbps {
		log.Fatalf("payment: %.1f Mbit/s is past the renewal speed of %d", rate, paySpeedMbps)
	}
	if got, _, err := siteDownload(guardPort, over); err == nil || got >= payTrafficMiB*mib {
		log.Fatalf("payment: the renewal quota let %d bytes through (%v)", got, err)
	}
	eventually("payment: the spent renewal key is refused", func() bool { return !listedSiteOpen(guardPort) })
	time.Sleep(storeSettle)
	var after user
	p.call("GET", "/api/v1/users/"+strconv.FormatInt(st.User, 10), nil, &after)
	if after.UsedUp != 0 || after.UsedDown != 0 {
		log.Fatalf("payment: the renewal traffic went to the subscription's quota: %d/%d", after.UsedUp, after.UsedDown)
	}
	log.Print("payment: the renewal traffic is not the subscription's")
	eventually("payment: the subscription stops handing out the spent key", func() bool {
		return !strings.Contains(string(fetchSub(st.Token, "mihomo/1.19.31")), fmt.Sprint(st.Renewal["uuid"]))
	})
}

// paymentRenewed: the second window's key works with a quota of its own, the first one's
// stays refused, and the payment closes what is open on the renewal key.
func paymentRenewed() {
	p := login()
	st := loadPaymentState()
	eventually("payment: the new window's key reaches the listed site", func() bool { return listedSiteOpen(guardPort) })
	if listedSiteOpen(payOldPort) {
		log.Fatal("payment: the previous window's key still works")
	}
	log.Print("payment: the previous window's key is refused")
	checkPaymentCloses(p, st)
	eventually("payment: the renewal key is refused once paid", func() bool { return !listedSiteOpen(guardPort) })
	eventually("payment: the regular profile is back", func() bool { return regularProfile(st) })
	if !answers(payRegularPort, payOther+":9000", "", payOtherName) {
		log.Fatal("payment: the subscription's own key does not work after the payment")
	}
	log.Print("PAYMENT OK")
}

// checkPaymentCloses: the payment closes a stream open on the renewal key.
func checkPaymentCloses(p *panel, st paymentState) {
	stream, err := siteTLS(guardPort, paySite+":443", paySite)
	if err != nil {
		log.Fatalf("payment: a stream through the renewal key: %v", err)
	}
	defer stream.Close()
	// 32 KiB/s: well within the quota for the phase.
	if err := request(stream, 'S', 32<<10); err != nil {
		log.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		_ = stream.SetDeadline(time.Now().Add(guardWait + payDeadline))
		_, err := io.Copy(io.Discard, stream)
		closed <- err
	}()
	time.Sleep(2 * time.Second)
	select {
	case err := <-closed:
		log.Fatalf("payment: the stream ended before the payment: %v", err)
	default:
	}
	pay(p, st.User)
	select {
	case <-closed:
		log.Print("payment: the payment closed the open renewal stream")
	case <-time.After(guardWait):
		log.Fatalf("payment: the renewal stream outlived the payment by %s", guardWait)
	}
}

// pay stands in for a paid renewal: the admin extends the subscription by a month.
func pay(p *panel, id int64) {
	p.call("POST", "/api/v1/users/"+strconv.FormatInt(id, 10)+"/extend", map[string]any{"days": 30}, nil)
}

// expire moves the user's expiry to ago before now.
func expire(p *panel, id int64, ago time.Duration) {
	at := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	p.call("PATCH", "/api/v1/users/"+strconv.FormatInt(id, 10), map[string]any{"expires_at": at}, nil)
}

// paymentInbounds are the ids of the VLESS inbound the renewal keys go to and of one that
// is not VLESS.
func paymentInbounds(p *panel) (vless, other int64) {
	var list []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	p.call("GET", "/api/v1/inbounds", nil, &list)
	for _, in := range list {
		switch {
		case in.Name == payInbound:
			vless = in.ID
		case in.Type != "vless":
			other = in.ID
		}
	}
	if vless == 0 || other == 0 {
		log.Fatalf("payment: no %s and no other protocol in %+v", payInbound, list)
	}
	return vless, other
}

// save keeps the state for the next phase and writes the client's config from it.
func (st paymentState) save() {
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(payStateFile, raw, 0o644); err != nil {
		log.Fatal(err)
	}
	writePaymentClient(st)
}

func loadPaymentState() paymentState {
	raw, err := os.ReadFile(payStateFile)
	if err != nil {
		log.Fatal(err)
	}
	var st paymentState
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Fatal(err)
	}
	return st
}

// writePaymentClient: the renewal key on guardPort, the subscription's own on
// payRegularPort, the previous window's, if there is one, on payOldPort.
func writePaymentClient(st paymentState) {
	proxies := []map[string]any{st.Renewal, st.Regular}
	listeners := []map[string]any{
		{"name": "in-renewal", "type": "mixed", "listen": "0.0.0.0", "port": guardPort, "proxy": st.Renewal["name"]},
		{"name": "in-regular", "type": "mixed", "listen": "0.0.0.0", "port": payRegularPort, "proxy": st.Regular["name"]},
	}
	if st.Previous != nil {
		previous := maps.Clone(st.Previous)
		previous["name"] = "previous renewal" // the label is the current window's too
		proxies = append(proxies, previous)
		listeners = append(listeners, map[string]any{"name": "in-previous", "type": "mixed", "listen": "0.0.0.0", "port": payOldPort, "proxy": previous["name"]})
	}
	writeClientConfig(map[string]any{"allow-lan": true, "bind-address": "*", "proxies": proxies, "rules": []string{"MATCH,DIRECT"}, "listeners": listeners})
}

func clashProxies(token string) []map[string]any {
	raw, _ := clashProfile(token)["proxies"].([]any)
	proxies := make([]map[string]any, 0, len(raw))
	for _, px := range raw {
		if m, ok := px.(map[string]any); ok {
			proxies = append(proxies, m)
		}
	}
	return proxies
}

func profileProxy(proxies []map[string]any, name string) map[string]any {
	for _, px := range proxies {
		if px["name"] == name {
			return px
		}
	}
	return nil
}

func isRenewal(px map[string]any) bool { return slices.Contains(renewalLabels, fmt.Sprint(px["name"])) }

// renewalProxy is the one proxy of the renewal profile, which must be VLESS.
func renewalProxy(token string) map[string]any {
	proxies := clashProxies(token)
	if len(proxies) != 1 || proxies[0]["type"] != "vless" || !isRenewal(proxies[0]) {
		log.Fatalf("payment: the expired subscription must get one renewal VLESS, got %v", proxies)
	}
	return proxies[0]
}

// regularProfile says whether the subscription is back to its own servers.
func regularProfile(st paymentState) bool {
	proxies := clashProxies(st.Token)
	own := profileProxy(proxies, payRegularProxy)
	return own != nil && own["uuid"] == st.Regular["uuid"] && !slices.ContainsFunc(proxies, isRenewal)
}

// listedSiteOpen says whether the listed site answers over TLS through the client's port.
func listedSiteOpen(port int) bool { return answers(port, paySite+":443", paySite, paySiteName) }

// payRoots trusts the certificates the two sites write as they start (compose.yaml).
var payRoots = sync.OnceValue(func() *x509.CertPool {
	var roots *x509.CertPool
	eventually("payment: the sites wrote their certificates", func() bool {
		roots = x509.NewCertPool()
		for _, name := range []string{paySiteName, payOtherName} {
			raw, err := os.ReadFile("/work/" + name + ".crt")
			if err != nil || !roots.AppendCertsFromPEM(raw) {
				return false
			}
		}
		return true
	})
	return roots
})

// viaClient opens addr through one of the guard client's mixed ports.
func viaClient(port int, addr string) (net.Conn, error) {
	c, err := dialVia("guard-client", port, addr)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(payDeadline))
	return c, nil
}

// siteTLS opens addr through port and speaks TLS to it for sni.
func siteTLS(port int, addr, sni string) (net.Conn, error) {
	c, err := viaClient(port, addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(c, &tls.Config{ServerName: sni, RootCAs: payRoots(), MinVersion: tls.VersionTLS12})
	if err := tc.Handshake(); err != nil {
		c.Close()
		return nil, err
	}
	return tc, nil
}

// siteName is the host name of the site that answers on addr through port: over TLS for
// sni, in the clear without one.
func siteName(port int, addr, sni string) (string, error) {
	open := viaClient
	if sni != "" {
		open = func(port int, addr string) (net.Conn, error) { return siteTLS(port, addr, sni) }
	}
	c, err := open(port, addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := request(c, 'N', 0); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(c)
	if err == nil && len(raw) == 0 {
		err = io.ErrUnexpectedEOF // closed without an answer: refused past the handshake
	}
	return string(raw), err
}

func answers(port int, addr, sni, want string) bool {
	name, err := siteName(port, addr, sni)
	return err == nil && name == want
}

// siteDownload pulls n bytes from the listed site over TLS through port.
func siteDownload(port int, n int64) (int64, time.Duration, error) {
	c, err := siteTLS(port, paySite+":443", paySite)
	if err != nil {
		return 0, 0, err
	}
	defer c.Close()
	start := time.Now()
	if err := request(c, 'D', n); err != nil {
		return 0, 0, err
	}
	got, err := readDownload(c, n)
	return got, time.Since(start), err
}
