package main

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/proxy"
)

// The core's egress guard on the panel's own node (guard.sh). The server's name resolves to an
// address on its network interface, one the core takes for public, so what the guard does
// here is what it does on a real server.
const (
	guardHost = "guard.test"
	// guardProxy is the client's mixed port, behind the one proxy of the node.
	guardPort  = 11001
	guardProxy = "guard-client:11001"
	// What else listens on the server (guard-server in compose.yaml), each answering with
	// what it gets: a web server, and two services a firewall would close from outside.
	guardWeb, guardDB, guardOther = "80", "5432", "5433"
	// guardSubPort is off the panel's own port picks, so no inbound takes it first.
	guardSubPort = 18443
	// guardWait is how long a change of the panel may take to reach the node.
	guardWait = 30 * time.Second
)

func guardPrepare() {
	p := login()
	const freshInstall = 4 // default inbounds
	waitNode(p, freshInstall)
	// One key per user and no outside site to borrow a handshake from, as REALITY has.
	p.call("POST", "/api/v1/inbounds", map[string]any{"preset": "shadowsocks_2022"}, nil)
	waitNode(p, freshInstall+1)
	p.call("PATCH", "/api/v1/settings", map[string]any{"sub_routing": "all", "auto_port": false, "auto_sni": false}, nil)
	token := createUser(p).token()

	var cfg map[string]any
	if err := yaml.Unmarshal(fetchSub(token, "mihomo/1.19.31"), &cfg); err != nil {
		log.Fatalf("clash subscription is not YAML: %v", err)
	}
	cfg["allow-lan"] = true
	cfg["bind-address"] = "*"
	delete(cfg, "dns")
	cfg["listeners"] = []map[string]any{{"name": "in-ss", "type": "mixed", "listen": "0.0.0.0", "port": guardPort, "proxy": "Shadowsocks"}}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile("/work/client.yaml", raw, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("/work/token", []byte(token), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Print("client config written from the subscription")
}

func guardCheck() {
	p := login()
	raw, err := os.ReadFile("/work/token")
	if err != nil {
		log.Fatal(err)
	}
	token := string(raw)
	eventually := func(what string, ok func() bool) {
		deadline := time.Now().Add(guardWait)
		for !ok() {
			if time.Now().After(deadline) {
				log.Fatalf("guard: %s: not in %s", what, guardWait)
			}
			time.Sleep(time.Second)
		}
		log.Printf("guard: %s", what)
	}
	refreshes := func(port int) func() bool {
		return func() bool { return subThroughNode(port, token) }
	}

	// What the guard must leave open. The node may not know the user yet at the very start.
	eventually("the client opens the panel through the node", panelThroughNode)
	eventually("the client refreshes its subscription through the node", refreshes(2053))
	if !echoesThroughNode(net.JoinHostPort(guardHost, guardWeb)) {
		log.Fatal("guard: the server's web port does not answer through the node")
	}
	log.Print("guard: the server's web port answers through the node")

	// What it must close: both ports answer from outside here, with no firewall in the way,
	// so it is the guard that stops them, by name and by address alike.
	addrs, err := net.LookupHost(guardHost)
	if err != nil || len(addrs) == 0 {
		log.Fatalf("guard: %s does not resolve: %v %v", guardHost, addrs, err)
	}
	for _, port := range []string{guardDB, guardOther} {
		if !echoesFromOutside(net.JoinHostPort(guardHost, port)) {
			log.Fatalf("guard: port %s does not answer at all, so this says nothing", port)
		}
		for _, host := range []string{guardHost, addrs[0]} {
			if echoesThroughNode(net.JoinHostPort(host, port)) {
				log.Fatalf("guard: %s:%s answered through the node", host, port)
			}
		}
	}
	log.Print("guard: the server's other ports are closed through the node")

	// A new subscription port opens on the node as soon as the panel listens on it, and the
	// links handed out before keep working on the panel's port.
	p.call("PATCH", "/api/v1/settings", map[string]any{"sub_port": guardSubPort}, nil)
	eventually("the new subscription port opens through the node", refreshes(guardSubPort))
	if !subThroughNode(2053, token) {
		log.Fatal("guard: the panel's port stopped serving the subscription")
	}

	// The admin's own port opens that port alone, and goes away with the list.
	node := "/api/v1/nodes/1"
	db := net.JoinHostPort(guardHost, guardDB)
	p.call("PATCH", node, map[string]any{"own_ports": []int{5432}}, nil)
	eventually("the admin's port opens through the node", func() bool { return echoesThroughNode(db) })
	if echoesThroughNode(net.JoinHostPort(guardHost, guardOther)) {
		log.Fatal("guard: the admin's port opened another one too")
	}
	p.call("PATCH", node, map[string]any{"own_ports": []int{}}, nil)
	eventually("the admin's port closes again", func() bool { return !echoesThroughNode(db) })
	log.Print("GUARD OK")
}

// panelThroughNode says whether the panel's login page opens through the node.
func panelThroughNode() bool {
	resp, err := getThroughNode("https://"+net.JoinHostPort(guardHost, "2053")+adminPath+"/", "")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// subThroughNode says whether the subscription comes through the node, as an app refreshes it.
func subThroughNode(port int, token string) bool {
	resp, err := getThroughNode("https://"+net.JoinHostPort(guardHost, strconv.Itoa(port))+subPath+token, "mihomo/1.19.31")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && resp.Header.Get("Subscription-Userinfo") != ""
}

func getThroughNode(link, ua string) (*http.Response, error) {
	// A connection of its own each time: one kept open would outlive a change of the guard.
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   panelTLS(),
		Proxy:             http.ProxyURL(&url.URL{Scheme: "socks5", Host: guardProxy}),
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	return hc.Do(req)
}

func echoesThroughNode(addr string) bool {
	d, err := proxy.SOCKS5("tcp", guardProxy, nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	return echoes(c)
}

func echoesFromOutside(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false
	}
	return echoes(c)
}

// echoes says whether what is sent on c comes back; it closes c.
func echoes(c net.Conn) bool {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	const hello = "through the node\n"
	if _, err := c.Write([]byte(hello)); err != nil {
		return false
	}
	got := make([]byte, len(hello))
	_, err := io.ReadFull(c, got)
	return err == nil && string(got) == hello
}
