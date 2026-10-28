// Package nodeapi is the contract between the panel and a node. It is shared by both
// binaries and must not import mihomo.
package nodeapi

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/proto"
	"mikan/internal/scan"
	"mikan/internal/textutil"
)

// DesiredState is the complete configuration of a node. Applying the same state twice
// is a no-op; listeners are recreated only when their own part changed.
type DesiredState struct {
	Revision int64     `json:"revision"`
	Epoch    string    `json:"epoch"` // counters epoch the policies' BaseSeq refers to
	Inbounds []Inbound `json:"inbounds"`
	Slots    []Slot    `json:"slots"`
	Policies []Policy  `json:"policies"`
	TLS      *TLSFiles `json:"tls,omitempty"`
	// SelfStealPort allows REALITY dest 127.0.0.1:<port> (the panel's own HTTPS).
	SelfStealPort int `json:"self_steal_port,omitempty"`
	// Warp is Cloudflare WARP as an outbound; nil: everything leaves directly.
	Warp *Warp `json:"warp,omitempty"`
	// Relay is the hidden listener other nodes send their chosen traffic out through;
	// Exits are the other nodes this one sends chosen inbounds through (a cascade).
	Relay *Relay `json:"relay,omitempty"`
	Exits []Exit `json:"exits,omitempty"`
	// Torrent turns the torrent blocker on; nil: off. Nodes older than the blocker ignore it.
	Torrent *TorrentBlock `json:"torrent,omitempty"`
	// Filters keep users' traffic from places and strangers from the node; nil: none.
	// Nodes older than the filters ignore it.
	Filters *Filters `json:"filters,omitempty"`
	// Shaping splits the node's channel evenly between the users moving traffic; nil: off.
	// Nodes older than it ignore it.
	Shaping *Shaping `json:"shaping,omitempty"`
	// DNS is how the node names the sites users reach (ExitDNS); nil: as before it, the
	// address the user's device sent and the host's resolver. Nodes older than it ignore it.
	DNS *ExitDNS `json:"dns,omitempty"`
	// HTTPRedirect: the node holds port 80 (or MIKAN_ACME_LISTEN) for good and answers plain
	// HTTP with 301 to https, the ACME challenges as before. Nodes older than it ignore it.
	HTTPRedirect bool `json:"http_redirect,omitempty"`
	// Detail: the node sums each slot's bytes by site (Counters.Sites). Off by default;
	// nodes older than it ignore it.
	Detail bool `json:"detail,omitempty"`
	// Router shares the node's TCP 443 between inbounds by the TLS name the client asks for
	// (SNIRouter); nil: none. Nodes older than it ignore it, and their routed inbounds stay
	// on their own ports.
	Router *SNIRouter `json:"sni_router,omitempty"`
	// FrontPage is the built-in page (frontpage.Templates) a stranger gets: from the inbounds
	// on an HTTP transport for a request that is not theirs (the core's fallback-page), and
	// on the names of Router.Stub over TLS with the node's certificate (TLS); "": none. Nodes
	// older than it ignore it.
	FrontPage string `json:"front_page,omitempty"`
	// APIAllow: the node's API answers only these networks ("203.0.113.5/32"), loopback and
	// the addresses the panel has reached it from; nil: anyone (the panel's certificate
	// still guards it). Empty but not nil is on too, with the panel's addresses alone. The
	// node refuses the others in its own listener and asks its server's firewall for the
	// same rule; both lift by themselves after APILockStale without the panel. Nodes older
	// than it ignore it.
	APIAllow []string `json:"api_allow,omitzero"`
}

// APILockStale is how long the panel may stay away before a node with APIAllow opens its
// API to everyone again: a panel that moved to an address the rule does not know gets in
// within this time, and the rule comes back with its new address.
const APILockStale = 15 * time.Minute

// SNIRouter: the node listens on Listen (":443" when empty) and hands each connection to
// the inbound whose Names hold the ClientHello's SNI, with a PROXY v2 header; any other
// name to Default ("": closed). The routed inbounds listen on loopback with PROXY protocol.
type SNIRouter struct {
	Listen  string     `json:"listen,omitempty"`
	Routes  []SNIRoute `json:"routes"`
	Default string     `json:"default,omitempty"`
	// Stub: the names that get the node's front page (DesiredState.FrontPage) instead, ""
	// for a ClientHello with no name; with no Default it takes any other name too. Nodes
	// older than it ignore it.
	Stub []string `json:"stub,omitempty"`
}

type SNIRoute struct {
	Inbound string   `json:"inbound"`
	Names   []string `json:"names"`
}

// Routed says whether inbound name runs behind the router.
func (r *SNIRouter) Routed(name string) bool {
	if r == nil {
		return false
	}
	if r.Default == name {
		return true
	}
	for _, x := range r.Routes {
		if x.Inbound == name {
			return true
		}
	}
	return false
}

// Shaping is the fair share of a node: ChannelMbps (each way) is split evenly between the
// groups (users) moving traffic at the moment, a user's own cap staying the ceiling.
type Shaping struct {
	ChannelMbps int `json:"channel_mbps"`
}

// Filters are the ingress and egress filters of the node.
type Filters struct {
	Egress  Egress  `json:"egress"`
	Ingress Ingress `json:"ingress"`
}

// Egress: users' traffic to these ports ("465", "1000-2000"), networks ("203.0.113.0/24")
// and domains (with their subdomains) is refused.
type Egress struct {
	Ports    []string `json:"ports,omitempty"`
	Networks []string `json:"networks,omitempty"`
	Domains  []string `json:"domains,omitempty"`
}

// Ingress: connections from Networks are refused; with Allow, connections from anywhere
// else are. Another node of the panel relaying its users is never refused.
type Ingress struct {
	Allow    bool     `json:"allow,omitempty"`
	Networks []string `json:"networks,omitempty"`
}

type Inbound struct {
	Name   string          `json:"name"`
	Listen string          `json:"listen"`
	Port   string          `json:"port"`             // "443" or a range "20000-20100"
	Config json.RawMessage `json:"config,omitempty"` // proto.Template as JSON
	// Pool is the traffic pool the inbound counts to ("" = the main quota): its own
	// limit per user, separate from the rest (GitHub issue #6).
	Pool string `json:"pool,omitempty"`
}

type Slot = proto.Slot

// ValidateRequest asks the node to parse one inbound with mihomo without applying it.
type ValidateRequest struct {
	Inbound       Inbound `json:"inbound"`
	SelfStealPort int     `json:"self_steal_port,omitempty"`
}

type Policy struct {
	Slot           string   `json:"slot"`
	Allowed        bool     `json:"allowed"`
	Inbounds       []string `json:"inbounds,omitempty"` // allowed inbound names; empty = all
	DeviceLimit    int      `json:"device_limit"`       // 0 = unlimited
	QuotaRemaining int64    `json:"quota_remaining"`    // bytes left as of BaseSeq; -1 = unlimited
	BaseSeq        int64    `json:"base_seq"`
	// OtherIPs are the slot's devices on the panel's other nodes: they count against
	// DeviceLimit here too, and may connect here without taking another device.
	OtherIPs []string `json:"other_ips,omitempty"`
	// Pools are the slot's quotas in traffic pools (server groups); a pool not listed has
	// no limit, one listed with Main counts to QuotaRemaining like the inbounds outside every
	// pool, which QuotaRemaining is the quota of.
	Pools []PoolQuota `json:"pools,omitempty"`
	// TorrentExempt: the torrent blocker leaves the slot alone.
	TorrentExempt bool `json:"torrent_exempt,omitempty"`
	// BannedUntil (unix seconds) keeps the slot out until then: the torrent blocker
	// caught it on some node of the panel. 0: no ban.
	BannedUntil int64 `json:"banned_until,omitempty"`
	// SpeedMbps caps the slot's traffic each way, in Mbit/s; 0: no cap. Slots of one Group
	// (a user's devices) share one cap. Nodes older than the caps ignore both.
	SpeedMbps int    `json:"speed_mbps,omitempty"`
	Group     string `json:"group,omitempty"`
}

type PoliciesRequest struct {
	Epoch    string   `json:"epoch"`
	Policies []Policy `json:"policies"`
}

// ResetRequest names the slots whose open connections the node closes (POST
// /v1/slots/reset); their access stays, so the clients connect again at once.
type ResetRequest struct {
	Slots []string `json:"slots"`
}

// ResetResult is how many connections the core closed.
type ResetResult struct {
	Closed int `json:"closed"`
}

type AckRequest struct {
	Epoch string `json:"epoch"`
	Seq   int64  `json:"seq"`
}

type TLSFiles struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// ChallengeRequest asks the node to answer an ACME HTTP-01 challenge for its own address
// (PUT /v1/acme/challenge/{token}): the panel orders the node's certificate, the CA comes
// to the node's port 80. Nodes that predate it answer 404 and stay on a pinned
// self-signed certificate.
type ChallengeRequest struct {
	KeyAuth string `json:"key_auth"`
}

// The node's answers to a challenge it cannot take: port 80 held by another program (the
// message names it when the node can tell), or too many pending at once.
const (
	CodePort80Busy        = "port80_busy"
	CodeTooManyChallenges = "too_many_challenges"
)

// Counters is a batch of traffic deltas. The node returns the same batch until it is
// acknowledged, so the panel can apply it idempotently by (Epoch, Seq).
type Counters struct {
	Epoch string                        `json:"epoch"`
	Seq   int64                         `json:"seq"`
	Slots map[string]Traffic            `json:"slots"`           // the main traffic: outside every group, and the groups in it
	Pools map[string]map[string]Traffic `json:"pools,omitempty"` // slot → pool → traffic
	// Sites are the slots' bytes by site (DesiredState.Detail): slot → site → traffic, a
	// part of the batch like the rest. Nodes older than it send none.
	Sites map[string]map[string]Traffic `json:"sites,omitempty"`
	// Relay is what the node's relay carried for the other nodes' cascades, by the source
	// node's relay user (relay-<id>): a part of the batch like the rest, which counts for
	// this node's load alone, as the source node counted the same bytes for its users.
	// Nodes older than it send none.
	Relay  map[string]Traffic `json:"relay,omitempty"`
	Online map[string]Online  `json:"online"` // live view, not part of the batch
	// RelayOnline is the relay's open connections by source, a live view like Online.
	RelayOnline map[string]int `json:"relay_online,omitempty"`
	// Idle: no batch was cut because there was no traffic to report. There is nothing to
	// store and nothing to acknowledge; only Online is of use. Nodes before 0.4.4 cut an
	// empty batch instead, which must be acknowledged like any other.
	Idle bool `json:"idle,omitempty"`
}

type Traffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type Online struct {
	IPs   []string `json:"ips"`
	Conns int      `json:"conns"`
}

type Health struct {
	Version   string           `json:"version"`
	Core      string           `json:"core"`
	Revision  int64            `json:"revision"`
	StartedAt time.Time        `json:"started_at"`
	Listeners []ListenerStatus `json:"listeners"`
	Conns     int              `json:"conns"`
	System    System           `json:"system"`
	// Update is how the last update the panel asked for went, as the host's updater wrote
	// it; nil while there is none. Nodes before 0.5.0.2 send nothing.
	Update *UpdateStatus `json:"update,omitempty"`
	// Host is what listens on the node's server, whoever runs it; nil when the node does not
	// say (before 0.5.0.2, or it cannot read the kernel's tables).
	Host *HostPorts `json:"host,omitempty"`
	// Time is the node's clock as it answered: the panel holds it against its own and warns
	// of a skew. Older nodes send none.
	Time time.Time `json:"time,omitzero"`
	// Redirect is how DesiredState.HTTPRedirect went; nil while it is off or the node is
	// older than it.
	Redirect *PortStatus `json:"http_redirect,omitempty"`
	// Router is how DesiredState.Router went; nil while there is none or the node is older.
	Router *PortStatus `json:"sni_router,omitempty"`
	// APILock is how DesiredState.APIAllow went on the node's server: Error says why its
	// firewall has no rule (nft missing, an installer too old to know it); the node refuses
	// strangers in its own listener either way. nil while it is off or the node is older.
	APILock *PortStatus `json:"api_lock,omitempty"`
}

// PortStatus is how a port the node holds for a feature of its own went (the 301 on port
// 80, the SNI router on 443, the firewall rule on the API port): Error says why it is not
// held (another program has it, the host could not put the rule in).
type PortStatus struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// HostPorts are the ports something listens on at the node's server: TCP sockets in the
// listen state and bound UDP ports, sorted. The panel keeps its automatic picks off them,
// as a port another program holds keeps a listener from starting.
type HostPorts struct {
	TCP []int `json:"tcp"`
	UDP []int `json:"udp"`
}

// Listens says whether port is among the ones held over network (tcp or udp).
func (h *HostPorts) Listens(network string, port int) bool {
	if h == nil {
		return false
	}
	list := h.TCP
	if network == "udp" {
		list = h.UDP
	}
	_, found := slices.BinarySearch(list, port)
	return found
}

// The states of an update on the node's server.
const (
	UpdateRunning = "running"
	UpdateOK      = "ok"
	UpdateFailed  = "failed"
)

// UpdateRequest asks the node to be updated to a release (POST /v1/update). The node only
// hands the version to the updater on its server, which updates to a signed release of that
// version or refuses: no image or address comes from here.
type UpdateRequest struct {
	Version string `json:"version"`
}

// UpdateStatus is what the updater on the node's server wrote about its last update:
// {"state", "version", "from", "error", "at"}, the same file the panel's own updater writes.
type UpdateStatus struct {
	State   string `json:"state" enum:"running,ok,failed"`
	Version string `json:"version"`
	From    string `json:"from"`
	Error   string `json:"error,omitempty"`
	At      string `json:"at" doc:"RFC 3339"`
}

// The most a status may say: the updater's words are short, and the panel keeps and shows
// whatever a node sends, so a damaged file or a node of somebody else's making must not
// fill its database and pages.
const (
	MaxUpdateField = 64
	MaxUpdateError = 1000
)

// Clean returns the status with a known state and fields of a sane length; false when
// the state is none of the three.
func (u UpdateStatus) Clean() (UpdateStatus, bool) {
	switch u.State {
	case UpdateRunning, UpdateOK, UpdateFailed:
	default:
		return UpdateStatus{}, false
	}
	u.Version, u.From, u.At = textutil.Clip(u.Version, MaxUpdateField), textutil.Clip(u.From, MaxUpdateField), textutil.Clip(u.At, MaxUpdateField)
	u.Error = textutil.Clip(u.Error, MaxUpdateError)
	return u, true
}

type System struct {
	// CPUPercent is the whole server's: with network_mode: host the panel, its database and
	// anything else on the host count too. ProcCPUPercent is the node process's own share
	// on the same scale (all cores = 100); nodes before 0.5.0.4 send none.
	CPUPercent     float64 `json:"cpu_percent"`
	ProcCPUPercent float64 `json:"proc_cpu_percent,omitempty"`
	MemTotal       uint64  `json:"mem_total"`
	MemUsed        uint64  `json:"mem_used"`
	ProcRSS        uint64  `json:"proc_rss"`
	NetRxBps       uint64  `json:"net_rx_bps"`
	NetTxBps       uint64  `json:"net_tx_bps"`
}

type ListenerStatus struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Code names a failure the panel acts on: ListenerAddrInUse. Nodes before 0.5 send none;
	// AddrInUse reads their Error instead.
	Code string `json:"code,omitempty"`
}

// ListenerAddrInUse: the listener's address is already taken on the node's server.
const ListenerAddrInUse = "addr_in_use"

// AddrInUse says whether a listen error is "address already in use": the text Go gives
// EADDRINUSE on Linux, the BSDs and macOS, and WSAEADDRINUSE on Windows.
func AddrInUse(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "only one usage of each socket address")
}

// Busy says whether the listener failed because something already listens on its port.
func (l ListenerStatus) Busy() bool {
	if l.OK {
		return false
	}
	if l.Code != "" {
		return l.Code == ListenerAddrInUse
	}
	return AddrInUse(l.Error)
}

// Activity tells the panel which inbounds each device reached lately. A device that
// keeps reaching the node's other inbounds but never one of them is cut off from that
// one on the way, e.g. its port is blocked by DPI.
type Activity struct {
	Clients []ClientActivity `json:"clients"`
}

type ClientActivity struct {
	Slot string           `json:"slot"`
	IP   string           `json:"ip"`
	Seen map[string]int64 `json:"seen"` // inbound name → unix time of the last admitted connection
}

// TargetCheckRequest asks the node to test a REALITY target from its own network: the
// node is the one that dials it for every client handshake.
type TargetCheckRequest struct {
	Dest string `json:"dest"`
	SNI  string `json:"sni,omitempty"`
}

// TargetScanRequest asks the node for REALITY targets in the /24 around IP, its public
// address; Wide lets it go on through the rest of the /16 (scan.Options.Wide). A node
// older than 0.5.1.0 ignores Wide and scans the /24.
type TargetScanRequest struct {
	IP    string `json:"ip"`
	Limit int    `json:"limit,omitempty"`
	Wide  bool   `json:"wide,omitempty"`
}

// Scan times: the /24 is quick; a wide scan stops itself at WideScanTime, and the panel
// waits a little longer than that for its answer.
const (
	ScanTime     = 30 * time.Second
	WideScanTime = 2 * time.Minute
)

type TargetResult = scan.Result

type TargetScan struct {
	Scanned int            `json:"scanned"`
	Results []TargetResult `json:"results"`
}

type ApplyResult struct {
	Revision  int64            `json:"revision"`
	Recreated []string         `json:"recreated"`
	Listeners []ListenerStatus `json:"listeners"`
}

type Error struct {
	Code    string `json:"code"` // invalid_state | apply_failed | not_ready | bad_request
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Warp is a WireGuard tunnel to Cloudflare WARP on the node. The listed inbounds leave
// through it whole, and so do the listed domains and networks for every inbound; the
// rest goes out directly. When WARP is down its traffic fails instead of leaving from
// the server's own address.
type Warp struct {
	PrivateKey    string   `json:"private_key"`
	PeerPublicKey string   `json:"peer_public_key"`
	Endpoint      string   `json:"endpoint"`       // host:port
	IPv4          string   `json:"ipv4"`           // the tunnel's address, without a mask
	IPv6          string   `json:"ipv6,omitempty"` // likewise
	Reserved      []uint8  `json:"reserved,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	Inbounds      []string `json:"inbounds"`
	Domains       []string `json:"domains,omitempty"` // suffixes: example.com covers its subdomains
	CIDRs         []string `json:"cidrs,omitempty"`
}

// WarpStatus is the node's last look at the internet through WARP.
type WarpStatus struct {
	Configured bool   `json:"configured"`
	OK         bool   `json:"ok"`
	IP         string `json:"ip,omitempty"`   // the address sites see
	Warp       string `json:"warp,omitempty"` // on | plus | off, as Cloudflare says
	Colo       string `json:"colo,omitempty"` // Cloudflare's data center
	// Error is a stable code: timeout | dns | tls | refused | not_loaded | bad_answer |
	// https_timeout | failed. Detail says the same in words for the admin (no secrets).
	// Older nodes send the code "unreachable" and no detail.
	Error     string    `json:"error,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// RelayListener names the relay's listener. It cannot clash with an inbound: their
// names are [a-z0-9-].
const RelayListener = "mikan~relay"

// Relay is a node's door for other nodes of the panel: a VLESS REALITY listener with a
// key per source node. Its connections carry no subscriber, so they pass the per-user
// accounting and limits (the source node already applied them); the REJECT rules still
// hold. Where the relay's traffic leaves is decided like any inbound's: IN-NAME rules of
// Warp.Inbounds or an Exit's Inbounds may name RelayListener.
type Relay struct {
	Port   string          `json:"port"`
	Config json.RawMessage `json:"config"` // proto.Template as JSON
	Users  []Slot          `json:"users"`  // one per source node
}

// Exit is another node as an outbound: Proxy is the mihomo proxy reaching its relay,
// Inbounds the local listeners (RelayListener included) whose traffic goes there.
type Exit struct {
	Name     string          `json:"name"` // the proxy's name in rules, NODE-<id>
	Proxy    json.RawMessage `json:"proxy"`
	Inbounds []string        `json:"inbounds"`
}

// ExitName is the proxy name of node id as an exit.
func ExitName(id int64) string { return "NODE-" + strconv.FormatInt(id, 10) }

// ProbeResult is the internet as seen through one outbound of the node.
type ProbeResult = WarpStatus

// PoolQuota is what is left of one traffic pool (a server group) for a slot, as of the
// policy's BaseSeq. With Main the group counts to the slot's main quota instead: the
// node counts its traffic there and turns the slot away from it when the main quota is
// out; Remaining then repeats QuotaRemaining for nodes that do not know Main.
type PoolQuota struct {
	Pool      string `json:"pool"`
	Remaining int64  `json:"remaining"` // bytes; -1 = unlimited
	Main      bool   `json:"main,omitempty"`
}

// TorrentBlock is the torrent blocker: the node looks at the first bytes a user sends
// on each connection and UDP packet, and drops BitTorrent it recognises (the handshake,
// DHT, uTP, tracker requests). Encrypted BitTorrent (MSE/PE) looks like noise and goes
// through: the blocker is a deterrent, not a wall.
type TorrentBlock struct {
	// BanSeconds keeps a caught user out of the node for so long, every connection cut;
	// 0: only what was caught is dropped.
	BanSeconds int64 `json:"ban_seconds"`
}

// Kinds of BitTorrent traffic the node recognises.
const (
	TorrentHandshake = "handshake" // the peer wire protocol over TCP
	TorrentTracker   = "tracker"   // an HTTP or UDP tracker announce
	TorrentDHT       = "dht"
	TorrentUTP       = "utp"
)

// TorrentHit is one catch of the torrent blocker. A slot is reported at most once a
// minute; Count says how many catches the hit stands for.
type TorrentHit struct {
	Seq     int64  `json:"seq"`
	Slot    string `json:"slot"`
	IP      string `json:"ip"`
	Inbound string `json:"inbound"`
	Network string `json:"network" enum:"tcp,udp"`
	Kind    string `json:"kind" enum:"handshake,tracker,dht,utp"`
	Dest    string `json:"dest"`
	At      int64  `json:"at"` // unix seconds
	Count   int    `json:"count"`
	// BannedUntil is when the node lets the slot in again (unix seconds); 0: no ban.
	BannedUntil int64 `json:"banned_until,omitempty"`
}

// TorrentHits are the catches after a sequence number (GET /v1/torrents?after=N). The
// node keeps the last few hundred in memory; Epoch changes when it restarts, and the
// sequence starts over.
type TorrentHits struct {
	Epoch string       `json:"epoch"`
	Hits  []TorrentHit `json:"hits"`
}

// ServiceRegions is what the sites themselves make of the node's address (POST
// /v1/regions): the country each one serves it, or whether it lets it in, over IPv4 and
// IPv6 apart. Unlike a geo base, it is asked from the node, as its clients' apps come.
type ServiceRegions struct {
	At       time.Time       `json:"at"`
	IPv4     string          `json:"ipv4,omitempty" doc:"Адрес ноды, как его видит Cloudflare"`
	IPv6     string          `json:"ipv6,omitempty"`
	Services []ServiceRegion `json:"services"`
}

// ServiceRegion is one site.
type ServiceRegion struct {
	Service string       `json:"service" doc:"google, google_search, youtube, youtube_premium, youtube_music, netflix, chatgpt, cloudflare, prime_video, deezer, apple, steam, playstation, tiktok, ookla, jetbrains, bing, reddit"`
	Access  bool         `json:"access" doc:"Ответ — пускает ли сайт адрес (open), а не страна"`
	V4      RegionAnswer `json:"v4"`
	V6      RegionAnswer `json:"v6"`
}

// RegionAnswer is a country, or open for an access check, or the reason there is none.
type RegionAnswer struct {
	Country string `json:"country,omitempty"`
	Open    *bool  `json:"open,omitempty"`
	Error   string `json:"error,omitempty" enum:"unreachable,unknown,no_ip" doc:"unreachable — сайт не ответил, unknown — ответ не разобрать, no_ip — у ноды нет адреса этого семейства"`
}

// SpeedTest is the node's own way to the internet (POST /v1/speedtest): latency, jitter
// and loss of small UDP DNS queries, then download and upload against a speed test
// server. A test that broke off keeps what it measured and says where in Error.
type SpeedTest struct {
	At       time.Time `json:"at"`
	PingMs   float64   `json:"ping_ms" doc:"Медиана задержки, мс; -1 если ответов не было"`
	JitterMs float64   `json:"jitter_ms" doc:"Средний разброс задержки, мс"`
	LossPct  float64   `json:"loss_pct" doc:"Потери, %"`
	DownBps  int64     `json:"down_bps" doc:"Загрузка, бит/с"`
	UpBps    int64     `json:"up_bps" doc:"Отдача, бит/с"`
	Error    string    `json:"error,omitempty"`
}

// Diagnosis is a server's look at what it needs to work (POST /v1/diagnose on a node; the
// panel runs the same on its own server): names, the internet, the places updates come
// from, its clock, disk and memory. The targets are fixed: nothing in the request says
// where to connect.
type Diagnosis struct {
	At    time.Time  `json:"at"`
	Items []DiagItem `json:"items"`
}

// DiagItem is one check of a Diagnosis.
type DiagItem struct {
	ID     string            `json:"id" enum:"dns,internet,github,ghcr,clock,disk,memory"`
	Status string            `json:"status" enum:"ok,warn,fail,skip"`
	Code   string            `json:"code,omitempty" doc:"Почему не ok: timeout, refused, dns, tls, skew, low и другие"`
	Params map[string]string `json:"params,omitempty"`
	Detail string            `json:"detail,omitempty" doc:"Слова ошибки, без секретов"`
}

// The states of a check.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
	CheckSkip = "skip"
)
