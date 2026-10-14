// Package turn runs the way in through VK calls ("WL") on a node: the panel starts the
// vk-turn-proxy server there and the server hands what it unwraps to one inbound of the
// same node. A user reaches it with the vk-turn-proxy client running on their own
// machine, pointed at the node and at a VK call link; the client joins the call and the
// TURN servers of VK relay its DTLS to the node. The user's app then speaks to the
// node's inbound as if it stood on the same machine: the subscription lists a copy of
// that inbound at client_host:client_port, next to the ordinary servers.
//
// The link is the credential of a "call" and is shared by everybody the panel gives it
// to, so a panel with many users spreads them over several links (one per line) and a
// call carries only what VK's TURN relay allows through it.
package turn

import (
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"strconv"
	"strings"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

const (
	// DefaultListenPort is where the server listens on the node (UDP).
	DefaultListenPort = "56000"
	// DefaultClientHost and DefaultClientPort are where the client is told to listen,
	// on the user's own machine; the app's server address is this.
	DefaultClientHost = "127.0.0.1"
	DefaultClientPort = "9000"
	// DefaultStreams is how many TURN sessions the client keeps (its -n): VK's relay
	// limits a session, so the client spreads connections over several.
	DefaultStreams = 10
	MaxStreams     = 64
	// DefaultSuffix marks the copy in the app's server list when the admin gives no title.
	DefaultSuffix = " WL"
)

// Error is a refusal the admin sees in the drawer. Code is stable and translated by the
// UI; Field names the input it belongs to.
type Error struct {
	Code   string
	Field  string
	Detail string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return e.Code
	}
	return e.Code + " (" + e.Field + ")"
}

// Config is one node's setup, as the admin writes it.
type Config struct {
	Enabled    bool
	InboundID  int64
	ListenPort string // UDP port of the server on the node
	ClientHost string // where the client listens, on the user's machine
	ClientPort string
	Streams    int    // the client's -n
	UDP        bool   // the client's -udp: talk to the TURN server over UDP
	Title      string // the name in the app; empty: the inbound's name + " WL"
	Links      []string
	BinaryPath string
}

// FromRow is the config as the store holds it.
func FromRow(r db.NodeTurn) Config {
	return Config{
		Enabled: r.Enabled != 0, InboundID: r.InboundID, ListenPort: r.ListenPort,
		ClientHost: r.ClientHost, ClientPort: r.ClientPort, Streams: int(r.Streams), UDP: r.Udp != 0,
		Title: r.Title, Links: splitLinks(r.VkLinks), BinaryPath: r.BinaryPath,
	}
}

// Validate checks the config as the admin wrote it, without the inbound: whether the
// inbound is this node's own is the caller's business.
func (c Config) Validate() error {
	if !port(c.ListenPort) {
		return &Error{Code: "turn_listen_port", Field: "listen_port", Detail: "a port 1..65535"}
	}
	if c.ClientHost == "" || strings.ContainsAny(c.ClientHost, " \t/\\") || strings.Contains(c.ClientHost, ":") && net.ParseIP(c.ClientHost) == nil {
		return &Error{Code: "turn_client_host", Field: "client_host", Detail: "an address the phone can dial, e.g. 127.0.0.1"}
	}
	if !port(c.ClientPort) {
		return &Error{Code: "turn_client_port", Field: "client_port", Detail: "a port 1..65535"}
	}
	if c.Streams < 1 || c.Streams > MaxStreams {
		return &Error{Code: "turn_streams", Field: "streams", Detail: fmt.Sprintf("1..%d", MaxStreams)}
	}
	for _, l := range c.Links {
		u, err := url.Parse(l)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return &Error{Code: "turn_link", Field: "vk_links", Detail: "a call link, one per line"}
		}
	}
	return nil
}

// Server is the node's TURN state: the server listens on ListenPort and hands what it
// unwraps to the host and port of in. TCP inbounds (VLESS, XHTTP, REALITY) are carried
// as whole connections, the UDP ones (Hysteria2, TUIC) as datagrams.
func (c Config) Server(in db.Inbound) (*nodeapi.Turn, error) {
	port, err := firstPort(in.Port)
	if err != nil {
		return nil, &Error{Code: "turn_inbound", Field: "inbound_id", Detail: err.Error()}
	}
	udp, err := UDPOnly(in)
	if err != nil {
		return nil, err
	}
	return &nodeapi.Turn{
		Listen: net.JoinHostPort("0.0.0.0", c.ListenPort),
		Target: net.JoinHostPort(reachHost(in.Listen), strconv.Itoa(port)),
		VLESS:  !udp,
		Binary: c.BinaryPath,
	}, nil
}

// Name is what the entry is called in the app: the admin's title, or the inbound's own
// name with " WL" - the way in for a whitelist, next to the same server's usual entry.
func (c Config) Name(base string) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return base + DefaultSuffix
}

// Listen is where the client listens on the user's machine; the app's server address.
func (c Config) Listen() string { return net.JoinHostPort(c.ClientHost, c.ClientPort) }

// Port is the client's listening port as a number.
func (c Config) Port() int {
	p, _ := strconv.Atoi(c.ClientPort)
	return p
}

// Peer is the node's TURN server as the client names it.
func (c Config) Peer(host string) string { return net.JoinHostPort(host, c.ListenPort) }

// Mode is what the client is told to carry: whole connections for a TCP inbound, single
// datagrams for a UDP one. It decides the client's -vless flag, not the node's.
func (c Config) Mode(in db.Inbound) (string, error) {
	udp, err := UDPOnly(in)
	if err != nil {
		return "", err
	}
	if udp {
		return "udp", nil
	}
	return "vless", nil
}

// Link picks the call link the user is given: the same one every time, spread over the
// list, so a panel with many users does not put them all in one call (a call carries
// only what VK's TURN relay allows through it).
func (c Config) Link(userID int64) string {
	if len(c.Links) == 0 {
		return ""
	}
	h := fnv.New32a()
	fmt.Fprintf(h, "mikan-turn:%d", userID)
	return c.Links[int(h.Sum32())%len(c.Links)]
}

// UDPOnly says whether what stands on in is a datagram protocol (Hysteria2, TUIC,
// ShadowQUIC) rather than one carrying connections.
func UDPOnly(in db.Inbound) (bool, error) {
	t, err := proto.Parse(in.Config)
	if err != nil {
		return false, &Error{Code: "turn_inbound", Field: "inbound_id", Detail: "the inbound's config does not parse"}
	}
	return t.Network() == "udp", nil
}

// Direct is the inbound's template as the WL entry needs it: the copy reaches this very
// node, through the client running next to the app, so an override that points clients at
// something in front of the node (a CDN) is dropped. The fingerprint stays.
func Direct(t proto.Template) (proto.Template, error) {
	out := t.Clone()
	if err := proto.SetClientEndpoint(out, "", 0, ""); err != nil {
		return nil, &Error{Code: "turn_inbound", Field: "inbound_id", Detail: "the inbound's client override does not clear"}
	}
	return out, nil
}

// splitLinks is the links of the store: one per line, blank lines and #-comments skipped.
func splitLinks(raw string) []string {
	out := []string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// JoinLinks is splitLinks the other way, for the store.
func JoinLinks(links []string) string {
	clean := make([]string, 0, len(links))
	for _, l := range links {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			clean = append(clean, l)
		}
	}
	return strings.Join(clean, "\n")
}

// reachHost is the address of in to hand traffic to from the node itself: its own listen
// address when it has one, the loopback when it accepts on every interface.
func reachHost(listen string) string {
	switch strings.TrimSpace(listen) {
	case "", "0.0.0.0", "::", "*":
		return "127.0.0.1"
	}
	return strings.TrimSpace(listen)
}

func port(spec string) bool {
	p, err := strconv.Atoi(strings.TrimSpace(spec))
	return err == nil && p > 0 && p <= 65535
}

// firstPort is the head of a port spec: "443" or the low end of a range or list.
func firstPort(spec string) (int, error) {
	head, _, _ := strings.Cut(spec, ",")
	head, _, _ = strings.Cut(head, "-")
	p, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("bad port %q", spec)
	}
	return p, nil
}
