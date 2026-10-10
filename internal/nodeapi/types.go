// Package nodeapi is the contract between the panel and a node. It is shared by both
// binaries and must not import mihomo.
package nodeapi

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/proto"
	"mikan/internal/scan"
)

const (
	PresetVlessVision = "vless_reality_vision"
	PresetVlessXHTTP  = "vless_reality_xhttp"
	PresetHysteria2   = "hysteria2"
	PresetTUIC        = "tuic_v5"
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
	// Preset and Settings are the format of mikan ≤ 0.1.2; the node still reads them
	// from a saved state, the panel no longer sends them.
	Preset   string          `json:"preset,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
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
	// Pools are the slot's quotas in traffic pools; a pool not listed has no limit.
	// QuotaRemaining is then the quota of the inbounds outside every pool.
	Pools []PoolQuota `json:"pools,omitempty"`
	// TorrentExempt: the torrent blocker leaves the slot alone.
	TorrentExempt bool `json:"torrent_exempt,omitempty"`
	// BannedUntil (unix seconds) keeps the slot out until then: the torrent blocker
	// caught it on some node of the panel. 0: no ban.
	BannedUntil int64 `json:"banned_until,omitempty"`
}

type PoliciesRequest struct {
	Epoch    string   `json:"epoch"`
	Policies []Policy `json:"policies"`
}

type AckRequest struct {
	Epoch string `json:"epoch"`
	Seq   int64  `json:"seq"`
}

type TLSFiles struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// Counters is a batch of traffic deltas. The node returns the same batch until it is
// acknowledged, so the panel can apply it idempotently by (Epoch, Seq).
type Counters struct {
	Epoch  string                        `json:"epoch"`
	Seq    int64                         `json:"seq"`
	Slots  map[string]Traffic            `json:"slots"`           // outside every pool
	Pools  map[string]map[string]Traffic `json:"pools,omitempty"` // slot → pool → traffic
	Online map[string]Online             `json:"online"`          // live view, not part of the batch
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
	u.Version, u.From, u.At = clip(u.Version, MaxUpdateField), clip(u.From, MaxUpdateField), clip(u.At, MaxUpdateField)
	u.Error = clip(u.Error, MaxUpdateError)
	return u, true
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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
// address.
type TargetScanRequest struct {
	IP    string `json:"ip"`
	Limit int    `json:"limit,omitempty"`
}

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

// Preset settings stored in inbounds.settings and passed to the node as-is.

type RealitySettings struct {
	PrivateKey  string   `json:"private_key"`
	PublicKey   string   `json:"public_key"`
	ShortIDs    []string `json:"short_ids"`
	Dest        string   `json:"dest"`
	ServerNames []string `json:"server_names"`
}

type VlessVisionSettings struct {
	Reality RealitySettings `json:"reality"`
}

type VlessXHTTPSettings struct {
	Reality RealitySettings `json:"reality"`
	Path    string          `json:"path"`
	Mode    string          `json:"mode"` // "stream-one": "auto" hangs on mihomo v1.19.31 (S-01a)
}

type Hysteria2Settings struct {
	ObfsPassword string `json:"obfs_password,omitempty"`
	UpMbps       int    `json:"up_mbps,omitempty"`
	DownMbps     int    `json:"down_mbps,omitempty"`
	Masquerade   string `json:"masquerade,omitempty"`
}

type TUICSettings struct {
	CongestionControl string `json:"congestion_control"`
}

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

// PoolQuota is what is left of one traffic pool for a slot, as of the policy's BaseSeq.
type PoolQuota struct {
	Pool      string `json:"pool"`
	Remaining int64  `json:"remaining"` // bytes; -1 = unlimited
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
