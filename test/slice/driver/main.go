// driver runs the vertical slice against a real panel and node:
//
//	nodes:          add a remote node through the API and hand its join key to the node2 container
//	prepare:        log in, create a user by tariff, turn its subscription into a client config
//	verify:         push traffic through every protocol of both nodes and check the accounting
//	devices:        a device that sends its id gets keys of its own; the client gets them too
//	devices-check:  the device's keys work on both nodes and count to the user; unbound, they
//	                stop at once; extra devices get the stub
//	autotune:       with the node's 443/tcp dropped for two clients on two networks (the blocker
//	                containers), keep checking every proxy like a url-test group until the
//	                panel moves XHTTP
//	autotune-check: the client on the new profile gets through XHTTP again
//	pools:          VLESS Vision counts to a 4 MiB traffic pool: its bytes go there, not to the
//	                main quota; past the limit the node cuts it while XHTTP keeps going
//	cascade:        VLESS Vision of the panel's node leaves through node2: the target sees node2's
//	                address, the user is charged once, node2 off means no way out, not a leak
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/proxy"
)

const (
	panelURL  = "https://panel:2053/slice-admin-path-0000"
	subPrefix = "https://panel:2053/slicesub0000/"
	mib       = 1 << 20
)

// protos: the four default inbounds of a fresh install, then the ones prepare adds through
// the API the way an admin would (the last local one as an own template from the editor),
// then two default inbounds of the remote node, named with its flag in the subscription.
// Shared ones have one key for everyone: they work, but no user is charged for them.
// main_test.go keeps the table to the presets and the templates.
var protos = []struct {
	name, proxy string
	shared      bool
}{
	{"vision", "VLESS Vision", false}, {"xhttp", "VLESS XHTTP", false}, {"hy2", "Hysteria2", false}, {"tuic", "TUIC", false},
	{"grpc", "VLESS gRPC", false}, {"trojan", "Trojan", false}, {"anytls", "AnyTLS", false}, {"pq", "VLESS PQ", false},
	{"trusttunnel", "TrustTunnel", false}, {"shadowquic", "ShadowQUIC", false}, {"mieru", "Mieru", false},
	{"ss", "Shadowsocks", false}, {"sudoku", "Sudoku", true}, {"snell", "Snell", true},
	{"tls-xhttp", "VLESS TLS XHTTP", false}, {"tls-vision", "VLESS TLS Vision", false},
	{"custom-ws", "Custom", false},
	{"us-xhttp", "🇺🇸 VLESS XHTTP", false}, {"us-hy2", "🇺🇸 Hysteria2", false},
}

// customTemplate is the own template of the custom inbound: VLESS over WebSocket on the
// node's TLS (what the core runs of the old vmess one).
const customTemplate = "type: vless\nws-path: /vm\nmikan:\n  tls: node\n"

// localProtos run on the panel's own node; a new node gets remoteInbounds defaults.
const (
	localProtos    = 17
	remoteInbounds = 4
	// clashOnly: local inbounds only Clash apps get (TrustTunnel, ShadowQUIC, Mieru,
	// Sudoku, Snell); they have no share link.
	clashOnly = 5
)

type panel struct {
	hc   *http.Client
	csrf string
}

func login() *panel {
	jar, _ := cookiejar.New(nil)
	// The panel uses its self-signed certificate inside the test network.
	p := &panel{hc: &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	p.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": os.Getenv("SLICE_PW")}, &me)
	p.csrf = me.CSRF
	return p
}

func (p *panel) call(method, path string, in, out any) int {
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, panelURL+path, body)
	req.Header.Set("Content-Type", "application/json")
	if p.csrf != "" {
		req.Header.Set("X-CSRF-Token", p.csrf)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		log.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			log.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// try is call for requests that are expected to fail: it returns the status instead.
func (p *panel) try(method, path string, in any) int {
	raw, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, panelURL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", p.csrf)
	resp, err := p.hc.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type user struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	UsedUp   int64  `json:"used_up"`
	UsedDown int64  `json:"used_down"`
	SubURL   string `json:"sub_url"`
}

func main() {
	log.SetFlags(log.Ltime)
	switch os.Args[1] {
	case "nodes":
		addNode()
	case "prepare":
		prepare()
	case "verify":
		verify()
	case "devices":
		devices()
	case "devices-check":
		devicesCheck()
	case "pools":
		pools()
	case "cascade":
		cascade()
	case "autotune":
		autotune()
	case "autotune-check":
		autotuneCheck()
	}
}

func prepare() {
	p := login()
	waitNode(p, 4)
	for _, in := range []map[string]any{
		// Its default port, 2053, is the panel's here, and the panel's node shares its host.
		{"preset": "vless_reality_grpc", "port": "3053"},
		{"preset": "trojan_reality"},
		{"preset": "anytls"},
		{"preset": "vless_reality_xhttp_pq"},
		{"preset": "trusttunnel"},
		{"preset": "shadowquic"},
		{"preset": "mieru"},
		{"preset": "shadowsocks_2022"},
		{"preset": "sudoku"},
		{"preset": "snell"},
		{"preset": "vless_tls_xhttp"},
		{"preset": "vless_tls_vision"},
		{"preset": "custom", "port": "2097", "config": customTemplate},
	} {
		p.call("POST", "/api/v1/inbounds", in, nil)
	}
	// An own template that mihomo cannot run is refused before it reaches the node.
	if code := p.try("POST", "/api/v1/inbounds", map[string]any{"preset": "custom", "port": "2098", "config": "type: vless\nws-path: /plain\n"}); code != 422 {
		log.Fatalf("an unencrypted vless template must be refused, got %d", code)
	}
	waitNode(p, localProtos)
	waitRemote(p)
	// GEOSITE/GEOIP rules of the default routing would make the client download geodata
	// from GitHub on start; the slice checks the tunnel, not the geodata. The automatic
	// moves run on test timings here: off until the autotune phase blocks a port on purpose.
	p.call("PATCH", "/api/v1/settings", map[string]any{"sub_routing": "all", "auto_port": false, "auto_sni": false}, nil)
	var tariffs []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	p.call("GET", "/api/v1/tariffs", nil, &tariffs)
	var tariffID int64
	for _, t := range tariffs {
		if t.Name == "Стандарт" {
			tariffID = t.ID
		}
	}
	var u user
	p.call("POST", "/api/v1/users", map[string]any{"name": "Slice User", "tariff_id": tariffID}, &u)
	log.Printf("created user %d, subscription %s", u.ID, u.SubURL)
	if !strings.HasPrefix(u.SubURL, "https://node.test:2053/slicesub0000/") {
		log.Fatalf("unexpected sub_url %q", u.SubURL)
	}
	token := u.SubURL[strings.LastIndex(u.SubURL, "/")+1:]

	// Links for apps that read them: every inbound of both nodes with a share-link format.
	links := fetchSub(token, "Shadowrocket/2592")
	decoded, err := base64.StdEncoding.DecodeString(string(links))
	if err != nil || strings.Count(string(decoded), "\n") != localProtos-clashOnly+remoteInbounds-1 {
		log.Fatalf("uri subscription: %v %q", err, decoded)
	}
	// Happ is on Xray: no TUIC or AnyTLS for it, the rest as for everyone. It gets whole
	// Xray configs, not links: one per automatic group first (a balancer), then one per server.
	happServers := localProtos - clashOnly - 2 + remoteInbounds - 1
	var configs []struct {
		Outbounds []struct {
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
		Routing struct {
			Balancers []json.RawMessage `json:"balancers"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(fetchSub(token, "Happ/3.4.1"), &configs); err != nil {
		log.Fatalf("Happ gets Xray configs: %v", err)
	}
	groups, servers, protocols := 0, 0, map[string]bool{}
	for _, c := range configs {
		if len(c.Routing.Balancers) > 0 {
			groups++
			continue
		}
		servers++
		for _, o := range c.Outbounds {
			protocols[o.Protocol] = true
		}
	}
	if groups == 0 || servers != happServers || protocols["tuic"] || protocols["anytls"] || !protocols["shadowsocks"] {
		log.Fatalf("Happ gets only what Xray speaks: %d groups, %d servers (want %d), protocols %v", groups, servers, happServers, protocols)
	}
	// The link still asks for links: the same servers as share links.
	raw, _ := base64.StdEncoding.DecodeString(string(fetchSub(token+"?format=uri", "Happ/3.4.1")))
	happ := string(raw)
	if strings.Contains(happ, "tuic://") || strings.Contains(happ, "anytls://") || !strings.Contains(happ, "ss://") ||
		strings.Count(happ, "\n") != happServers-1 {
		log.Fatalf("Happ asking for links gets only what Xray speaks: %q", happ)
	}

	writeClient(token, nil)
	for name, v := range map[string]string{"user": strconv.FormatInt(u.ID, 10), "token": token} {
		if err := os.WriteFile("/work/"+name, []byte(v), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Print("client config written from the subscription")
}

// writeClient turns the user's mihomo profile into the client's config, with one mixed
// port per proxy so a phase can pick a protocol. A device's own profile adds its proxies
// under devicePrefix, reachable on 12001… in devProtos order.
func writeClient(token string, device map[string]any) {
	var cfg map[string]any
	if err := yaml.Unmarshal(fetchSub(token, "mihomo/1.19.31"), &cfg); err != nil {
		log.Fatalf("clash subscription is not YAML: %v", err)
	}
	cfg["allow-lan"] = true
	cfg["bind-address"] = "*"
	delete(cfg, "dns")
	// in-direct: requests from the client's own address, as its app fetches the profile.
	listeners := []map[string]any{{"name": "in-direct", "type": "mixed", "listen": "0.0.0.0", "port": directPort, "proxy": "DIRECT"}}
	for i, pr := range protos {
		listeners = append(listeners, map[string]any{"name": "in-" + pr.name, "type": "mixed", "listen": "0.0.0.0", "port": 11001 + i, "proxy": pr.proxy})
	}
	if device != nil {
		proxies, _ := cfg["proxies"].([]any)
		for _, px := range device["proxies"].([]any) {
			m := px.(map[string]any)
			m["name"] = devicePrefix + m["name"].(string)
			proxies = append(proxies, m)
		}
		cfg["proxies"] = proxies
		for i, pr := range devProtos {
			listeners = append(listeners, map[string]any{"name": "dev-" + pr.name, "type": "mixed", "listen": "0.0.0.0", "port": 12001 + i, "proxy": devicePrefix + pr.proxy})
		}
	}
	cfg["listeners"] = listeners
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile("/work/client.yaml", raw, 0o644); err != nil {
		log.Fatal(err)
	}
}

func waitNode(p *panel, want int) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var n struct {
			OK        bool `json:"ok"`
			Listeners []struct {
				Name string `json:"name"`
				OK   bool   `json:"ok"`
			} `json:"listeners"`
		}
		p.call("GET", "/api/v1/node", nil, &n)
		ok := n.OK && len(n.Listeners) == want
		for _, l := range n.Listeners {
			ok = ok && l.OK
		}
		if ok {
			log.Printf("node is up with %d listeners", want)
			return
		}
		time.Sleep(time.Second)
	}
	log.Fatal("node did not come up with healthy listeners")
}

// storeSettle is how long a phase waits before it reads a baseline: the panel stores a
// node's traffic every 10 s (nodesync.storeEvery), and a baseline read at once would count
// the tail of the previous phase as this one's.
const storeSettle = 12 * time.Second

// directPort is the client's mixed port without a proxy behind it.
const directPort = 11000

func fetchSub(token, ua string) []byte { return fetchSubVia(token, ua, "") }

// fetchSubVia fetches the subscription through a client's SOCKS port ("" = from here):
// the panel records the device that took the profile by its address.
func fetchSubVia(token, ua, socks string) []byte {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	if socks != "" {
		tr.Proxy = http.ProxyURL(&url.URL{Scheme: "socks5", Host: socks})
	}
	hc := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	req, _ := http.NewRequest("GET", subPrefix+token, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := hc.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Subscription-Userinfo") == "" {
		log.Fatalf("subscription %s: %d, userinfo %q", ua, resp.StatusCode, resp.Header.Get("Subscription-Userinfo"))
	}
	raw, _ := io.ReadAll(resp.Body)
	return raw
}

// clients are the mihomo containers of one subscription: client on the default network,
// client2 on isp2 (the autotune phase alone).
var clients = []string{"client", "client2"}

func download(port int, n int64) (int64, error) { return downloadVia("client", port, n) }

// downloadVia pulls n bytes from the target through a client's mixed port.
func downloadVia(host string, port int, n int64) (int64, error) {
	d, _ := proxy.SOCKS5("tcp", net.JoinHostPort(host, strconv.Itoa(port)), nil, &net.Dialer{Timeout: 10 * time.Second})
	c, err := d.Dial("tcp", "target:9000")
	if err != nil {
		return 0, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	hdr := make([]byte, 9)
	hdr[0] = 'D'
	binary.BigEndian.PutUint64(hdr[1:], uint64(n))
	if _, err := c.Write(hdr); err != nil {
		return 0, err
	}
	got, err := io.Copy(io.Discard, c)
	if err == nil && got != n {
		err = fmt.Errorf("short download %d/%d", got, n)
	}
	return got, err
}

func verify() {
	p := login()
	raw, _ := os.ReadFile("/work/user")
	id, _ := strconv.ParseInt(string(raw), 10, 64)
	var before user
	p.call("GET", "/api/v1/users/"+strconv.FormatInt(id, 10), nil, &before)

	const each = 16 * mib
	var want int64
	for i, pr := range protos {
		if _, err := download(11001+i, each); err != nil {
			log.Fatalf("%s: download through the subscription config failed: %v", pr.name, err)
		}
		log.Printf("%s: 16 MiB downloaded", pr.name)
		if !pr.shared {
			want += each
		}
	}
	var after user
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.call("GET", "/api/v1/users/"+strconv.FormatInt(id, 10), nil, &after)
		if after.UsedDown-before.UsedDown >= want {
			break
		}
		time.Sleep(time.Second)
	}
	got := after.UsedDown - before.UsedDown
	log.Printf("panel accounted download %d, expected %d", got, want)
	if diff := float64(got-want) / float64(want); diff < -0.01 || diff > 0.01 {
		log.Fatalf("AC-3 failed: panel counted %d, expected %d ±1%%", got, want)
	}

	p.call("PATCH", "/api/v1/users/"+strconv.FormatInt(id, 10), map[string]any{"disabled": true}, nil)
	time.Sleep(1500 * time.Millisecond)
	if _, err := download(11001, mib); err == nil {
		log.Fatal("AC-5 failed: disabled user can still download")
	}
	if _, err := download(11001+localProtos, mib); err == nil {
		log.Fatal("AC-5 failed: the remote node still lets a disabled user in")
	}
	log.Print("disabled user is cut off")
	p.call("PATCH", "/api/v1/users/"+strconv.FormatInt(id, 10), map[string]any{"disabled": false}, nil)
	time.Sleep(1500 * time.Millisecond)
	if _, err := download(11003, mib); err != nil {
		log.Fatalf("re-enabled user cannot download: %v", err)
	}
	log.Print("re-enabled user works again")
	log.Print("SLICE OK")
}

// The device of the devices phases: its proxies go into the client under devicePrefix;
// devProtos are checked through it — two local protocols and one of the remote node.
const (
	devicePrefix = "📱 "
	deviceHWID   = "slice-phone-00000001"
)

var devProtos = []struct{ name, proxy string }{
	{"vision", "VLESS Vision"}, {"hy2", "Hysteria2"}, {"us-xhttp", "🇺🇸 VLESS XHTTP"},
}

// fetchDevice fetches the subscription as a device that sends its id.
func fetchDevice(token, hwid string) (*http.Response, []byte) {
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	req, _ := http.NewRequest("GET", subPrefix+token, nil)
	req.Header.Set("User-Agent", "mihomo/1.19.31")
	req.Header.Set("X-Hwid", hwid)
	req.Header.Set("X-Device-Os", "Android")
	req.Header.Set("X-Device-Model", "Slice "+hwid[6:12])
	resp, err := hc.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Fatalf("subscription for %s: %d", hwid, resp.StatusCode)
	}
	return resp, raw
}

// proxyField is a field of the named proxy in a clash profile.
func proxyField(profile map[string]any, name, field string) string {
	for _, px := range profile["proxies"].([]any) {
		if m := px.(map[string]any); m["name"] == name {
			v, _ := m[field].(string)
			return v
		}
	}
	return ""
}

// devices: a device with an id gets keys of its own, not the user's shared ones.
func devices() {
	raw, _ := os.ReadFile("/work/token")
	token := string(raw)
	var shared, own map[string]any
	if err := yaml.Unmarshal(fetchSub(token, "mihomo/1.19.31"), &shared); err != nil {
		log.Fatal(err)
	}
	_, body := fetchDevice(token, deviceHWID)
	if err := yaml.Unmarshal(body, &own); err != nil {
		log.Fatalf("device profile: %v", err)
	}
	a, b := proxyField(shared, "VLESS Vision", "uuid"), proxyField(own, "VLESS Vision", "uuid")
	if a == "" || b == "" || a == b {
		log.Fatalf("the device must get keys of its own: shared %q, device %q", a, b)
	}
	if _, again := fetchDevice(token, deviceHWID); !bytes.Contains(again, []byte(b)) {
		log.Fatal("the device must keep its keys on the next fetch")
	}
	writeClient(token, own)
	log.Print("the device got keys of its own; client config written with them")
}

// devicesCheck: the device's keys work on both nodes and its traffic counts to the user;
// unbound by the admin, they stop at once while the shared keys keep working. Extra
// devices get the stub once the places are taken.
func devicesCheck() {
	p := login()
	raw, _ := os.ReadFile("/work/user")
	id := string(raw)
	var before, after user
	time.Sleep(storeSettle)
	p.call("GET", "/api/v1/users/"+id, nil, &before)
	const each = 4 * mib
	for i, pr := range devProtos {
		if _, err := download(12001+i, each); err != nil {
			log.Fatalf("device %s: %v", pr.name, err)
		}
	}
	want := int64(len(devProtos) * each)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.call("GET", "/api/v1/users/"+id, nil, &after)
		if after.UsedDown-before.UsedDown >= want {
			break
		}
		time.Sleep(time.Second)
	}
	if got := after.UsedDown - before.UsedDown; float64(got) < float64(want)*0.99 || float64(got) > float64(want)*1.01 {
		log.Fatalf("the device's traffic must count to the user: %d, expected %d", got, want)
	}
	log.Printf("the device's keys work on both nodes, %d bytes counted to the user", want)

	var bound []struct {
		ID   int64  `json:"id"`
		HWID string `json:"hwid"`
	}
	p.call("GET", "/api/v1/users/"+id+"/bound-devices", nil, &bound)
	var dev int64
	for _, d := range bound {
		if d.HWID == deviceHWID {
			dev = d.ID
		}
	}
	if dev == 0 || len(bound) != 2 {
		log.Fatalf("bound devices: %+v (the shared place and the phone)", bound)
	}
	p.call("DELETE", "/api/v1/users/"+id+"/bound-devices/"+strconv.FormatInt(dev, 10), nil, nil)
	time.Sleep(1500 * time.Millisecond)
	for i, pr := range devProtos {
		if _, err := download(12001+i, mib); err == nil {
			log.Fatalf("the unbound device still gets through %s", pr.name)
		}
	}
	if _, err := download(11001, mib); err != nil {
		log.Fatalf("the shared keys must keep working: %v", err)
	}
	log.Print("the unbound device is cut off on both nodes, the shared keys work")

	token, _ := os.ReadFile("/work/token")
	for _, hwid := range []string{"slice-tablet-0000002", "slice-laptop-0000003"} {
		if resp, _ := fetchDevice(string(token), hwid); resp.Header.Get("X-Hwid-Max-Devices-Reached") != "" {
			log.Fatalf("%s must get a free place", hwid)
		}
	}
	resp, body := fetchDevice(string(token), "slice-extra-00000004")
	if resp.Header.Get("X-Hwid-Max-Devices-Reached") != "true" || !bytes.Contains(body, []byte("port: 1\n")) {
		log.Fatalf("a device over the limit gets the stub: %v %.200s", resp.Header, body)
	}
	log.Print("a device over the limit gets the stub")
	log.Print("DEVICES OK")
}

// xhttpPort is the client's mixed port of the local VLESS XHTTP (protos[1]).
const xhttpPort = 11002

// autotune: the blocker containers drop the packets of two clients, each on a network of
// its own, to the node's 443/tcp, where the local XHTTP listens, like the DPI of two ISPs:
// one network alone does not move a port (GitHub issue #67). Both clients keep checking
// every local proxy the way a Clash url-test group does; the panel must move XHTTP on its
// own. The phase then writes the new profile for the client.
func autotune() {
	p := login()
	p.call("PATCH", "/api/v1/settings", map[string]any{"auto_port": true}, nil)
	raw, _ := os.ReadFile("/work/token")
	token := string(raw)
	// Each client takes its profile from its own address: now the detector trusts it.
	for _, host := range clients {
		fetchSubVia(token, "mihomo/1.19.31", net.JoinHostPort(host, strconv.Itoa(directPort)))
		if _, err := downloadVia(host, xhttpPort, 64<<10); err == nil {
			log.Fatalf("the blocker did not cut XHTTP off for %s", host)
		}
	}
	start := time.Now()
	for time.Since(start) < 150*time.Second {
		var wg sync.WaitGroup
		for _, host := range clients {
			for i := range localProtos {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = downloadVia(host, 11001+i, 16<<10)
				}()
			}
		}
		wg.Wait()
		var ins []struct {
			Name   string `json:"name"`
			NodeID int64  `json:"node_id"`
			Port   string `json:"port"`
			Auto   struct {
				CutOff bool `json:"cut_off"`
				Last   *struct {
					Kind   string `json:"kind"`
					Old    string `json:"old"`
					New    string `json:"new"`
					Reason string `json:"reason"`
				} `json:"last"`
			} `json:"auto"`
		}
		p.call("GET", "/api/v1/inbounds", nil, &ins)
		for _, in := range ins {
			if in.NodeID != 1 || in.Name != "vless-xhttp" || in.Port == "443" {
				continue
			}
			l := in.Auto.Last
			if l == nil || l.Kind != "port" || l.Old != "443" || l.New != in.Port || l.Reason != "blocked" {
				log.Fatalf("XHTTP moved to %s without a matching event: %+v", in.Port, l)
			}
			log.Printf("the panel moved the blocked XHTTP 443 → %s after %s", in.Port, time.Since(start).Round(time.Second))
			for _, other := range ins {
				if other.NodeID == 1 && other.Name != "vless-xhttp" && other.Auto.Last != nil {
					log.Fatalf("%s was moved too, but only XHTTP was blocked", other.Name)
				}
			}
			writeClient(token, nil)
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Fatal("the panel did not move the blocked inbound")
}

// autotuneCheck: the client runs the new profile; 443/tcp is still dropped for it.
func autotuneCheck() {
	var err error
	for range 10 {
		if _, err = download(xhttpPort, mib); err == nil {
			log.Print("XHTTP works again on its new port")
			log.Print("AUTOTUNE OK")
			return
		}
		time.Sleep(time.Second)
	}
	log.Fatalf("XHTTP on its new port: %v", err)
}

// addNode registers the node2 container the way an admin adds a remote node and passes
// its one-time join key on through the shared volume.
func addNode() {
	p := login()
	var out struct {
		Node struct {
			ID int64 `json:"id"`
		} `json:"node"`
		Key string `json:"key"`
	}
	p.call("POST", "/api/v1/nodes", map[string]any{"name": "🇺🇸 US", "host": "node2.slice", "api_port": 7443}, &out)
	if !strings.HasPrefix(out.Key, "mikan1.") {
		log.Fatalf("join key: %q", out.Key)
	}
	// Test key in a throwaway volume: node2 runs as the image user, not as the driver.
	if err := os.WriteFile("/work/node2.key", []byte(out.Key), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("remote node %d added, key handed over", out.Node.ID)
}

// waitRemote waits until the panel drives node2 over mTLS with healthy listeners.
func waitRemote(p *panel) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var nodes []struct {
			ID          int64  `json:"id"`
			Status      string `json:"status"`
			Error       string `json:"error"`
			Listeners   int    `json:"listeners"`
			ListenersOK int    `json:"listeners_ok"`
		}
		p.call("GET", "/api/v1/nodes", nil, &nodes)
		for _, n := range nodes {
			if n.ID != 1 && n.Status == "ok" && n.Listeners == 4 && n.ListenersOK == 4 {
				log.Printf("remote node %d is up over mTLS with 4 listeners", n.ID)
				return
			}
		}
		time.Sleep(time.Second)
	}
	log.Fatal("the remote node did not come up")
}

// whoami asks the target which address the connection through proxy port came from.
func whoami(port int) (string, error) {
	d, _ := proxy.SOCKS5("tcp", net.JoinHostPort("client", strconv.Itoa(port)), nil, &net.Dialer{Timeout: 10 * time.Second})
	c, err := d.Dial("tcp", "target:9000")
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := c.Write([]byte{'W', 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(c)
	if err == nil && len(raw) == 0 {
		err = fmt.Errorf("no answer")
	}
	return string(raw), err
}

// cascade sends the local Vision inbound out through node2 and back.
func cascade() {
	p := login()
	raw, _ := os.ReadFile("/work/user")
	uid, _ := strconv.ParseInt(string(raw), 10, 64)
	var nodes []struct {
		ID    int64 `json:"id"`
		Local bool  `json:"local"`
	}
	p.call("GET", "/api/v1/nodes", nil, &nodes)
	var remote int64
	for _, n := range nodes {
		if !n.Local {
			remote = n.ID
		}
	}
	var ins []struct {
		ID     int64  `json:"id"`
		NodeID int64  `json:"node_id"`
		Preset string `json:"preset"`
	}
	p.call("GET", "/api/v1/inbounds", nil, &ins)
	var vision int64
	for _, in := range ins {
		if in.Preset == "vless_reality_vision" && in.NodeID != remote {
			vision = in.ID
		}
	}
	addrOf := func(host string) string {
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			log.Fatalf("resolve %s: %v", host, err)
		}
		return ips[0]
	}
	nodeIP, node2IP := addrOf("node"), addrOf("node2.slice")
	const port = 11001 // VLESS Vision in the client
	waitFor := func(want string) {
		deadline := time.Now().Add(30 * time.Second)
		var got string
		var err error
		for time.Now().Before(deadline) {
			if got, err = whoami(port); err == nil && got == want {
				return
			}
			time.Sleep(time.Second)
		}
		log.Fatalf("cascade: the target saw %q (%v), want %s", got, err, want)
	}
	waitFor(nodeIP)
	log.Printf("direct: the target sees node %s", nodeIP)

	path := "/api/v1/inbounds/" + strconv.FormatInt(vision, 10)
	p.call("PATCH", path, map[string]any{"outbound": "node", "exit_node_id": remote}, nil)
	waitFor(node2IP)
	log.Printf("cascade: the target sees node2 %s", node2IP)

	var before, after user
	time.Sleep(storeSettle)
	p.call("GET", "/api/v1/users/"+strconv.FormatInt(uid, 10), nil, &before)
	const each = 8 * mib
	if _, err := download(port, each); err != nil {
		log.Fatalf("cascade download: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		p.call("GET", "/api/v1/users/"+strconv.FormatInt(uid, 10), nil, &after)
		if after.UsedDown-before.UsedDown >= each {
			break
		}
		time.Sleep(time.Second)
	}
	got := after.UsedDown - before.UsedDown
	if diff := float64(got-each) / float64(each); diff < -0.01 || diff > 0.01 {
		log.Fatalf("cascade: the user was charged %d for %d (once, at the first node)", got, each)
	}
	log.Printf("cascade: charged %d for %d", got, each)

	// node2 off: its relay stops, and the first node must not fall back to going direct.
	p.call("PATCH", "/api/v1/nodes/"+strconv.FormatInt(remote, 10), map[string]any{"enabled": false}, nil)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := whoami(port); err != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if ip, err := whoami(port); err == nil {
		log.Fatalf("cascade: with node2 off the traffic still got out, from %s", ip)
	}
	log.Print("cascade: node2 off, no way out")
	p.call("PATCH", "/api/v1/nodes/"+strconv.FormatInt(remote, 10), map[string]any{"enabled": true}, nil)
	waitFor(node2IP)
	p.call("PATCH", path, map[string]any{"outbound": "direct"}, nil)
	waitFor(nodeIP)
	log.Print("CASCADE OK")
}

// pools puts the local VLESS Vision into a traffic pool with a small limit.
func pools() {
	p := login()
	raw, _ := os.ReadFile("/work/user")
	uid, _ := strconv.ParseInt(string(raw), 10, 64)
	userPath := "/api/v1/users/" + strconv.FormatInt(uid, 10)
	var nodes []struct {
		ID    int64 `json:"id"`
		Local bool  `json:"local"`
	}
	p.call("GET", "/api/v1/nodes", nil, &nodes)
	var local int64
	for _, n := range nodes {
		if n.Local {
			local = n.ID
		}
	}
	var ins []struct {
		ID     int64  `json:"id"`
		NodeID int64  `json:"node_id"`
		Preset string `json:"preset"`
	}
	p.call("GET", "/api/v1/inbounds", nil, &ins)
	var vision int64
	for _, in := range ins {
		if in.Preset == "vless_reality_vision" && in.NodeID == local {
			vision = in.ID
		}
	}
	var pool struct {
		ID int64 `json:"id"`
	}
	p.call("POST", "/api/v1/pools", map[string]any{"name": "WL"}, &pool)
	p.call("PATCH", "/api/v1/inbounds/"+strconv.FormatInt(vision, 10), map[string]any{"pool_id": pool.ID}, nil)
	const limit = 4 * mib
	p.call("PUT", userPath+"/pools", map[string]any{"pools": []map[string]any{{"pool_id": pool.ID, "mode": "own", "traffic_limit": limit}}}, nil)

	const visionPort, xhttpPort = 11001, 11002
	usage := func() (main, inPool int64) {
		var u user
		p.call("GET", userPath, nil, &u)
		var ps []struct {
			PoolID   int64 `json:"pool_id"`
			UsedUp   int64 `json:"used_up"`
			UsedDown int64 `json:"used_down"`
		}
		p.call("GET", userPath+"/pools", nil, &ps)
		for _, x := range ps {
			if x.PoolID == pool.ID {
				inPool = x.UsedUp + x.UsedDown
			}
		}
		return u.UsedUp + u.UsedDown, inPool
	}
	settle := func(check func(main, inPool int64) bool) (int64, int64) {
		deadline := time.Now().Add(20 * time.Second)
		var m, pl int64
		for time.Now().Before(deadline) {
			if m, pl = usage(); check(m, pl) {
				break
			}
			time.Sleep(time.Second)
		}
		return m, pl
	}
	// The node learns the pool with its next state, and the panel stores what came before.
	time.Sleep(storeSettle)
	main0, pool0 := usage()
	if _, err := download(visionPort, 2*mib); err != nil {
		log.Fatalf("pools: vision download: %v", err)
	}
	main1, pool1 := settle(func(_, pl int64) bool { return pl-pool0 >= 2*mib })
	if pool1-pool0 < 2*mib || main1-main0 > 1024 {
		log.Fatalf("pools: 2 MiB through Vision went to the pool %d and the main quota %d", pool1-pool0, main1-main0)
	}
	log.Printf("pools: 2 MiB through Vision counted to the pool (%d), main +%d", pool1-pool0, main1-main0)

	// Past the limit the node cuts the pool mid-download.
	if n, err := download(visionPort, 16*mib); err == nil {
		log.Fatalf("pools: a 16 MiB download through a 4 MiB pool finished (%d bytes)", n)
	}
	if _, err := download(visionPort, mib); err == nil {
		log.Fatal("pools: the used-up pool still lets Vision in")
	}
	log.Print("pools: past 4 MiB the pool is cut")
	// The rest keeps working and counts to the main quota.
	if _, err := download(xhttpPort, 2*mib); err != nil {
		log.Fatalf("pools: XHTTP outside the pool stopped too: %v", err)
	}
	main2, _ := settle(func(m, _ int64) bool { return m-main1 >= 2*mib })
	if main2-main1 < 2*mib {
		log.Fatalf("pools: XHTTP's 2 MiB did not reach the main quota (%d)", main2-main1)
	}
	log.Printf("pools: XHTTP still works, main +%d", main2-main1)

	// Clean up for the next phases: Vision back to the main traffic.
	p.call("DELETE", "/api/v1/pools/"+strconv.FormatInt(pool.ID, 10), nil, nil)
	time.Sleep(3 * time.Second)
	if _, err := download(visionPort, mib); err != nil {
		log.Fatalf("pools: Vision without the pool: %v", err)
	}
	log.Print("POOLS OK")
}
