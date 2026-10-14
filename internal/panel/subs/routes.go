package subs

import (
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Routes is the admin's routing beyond the mode (Routing): where each service goes, the
// apps that go past the tunnel, and the DNS servers. Kept as JSON (settings.KeyRoutes);
// the zero value is the profile as it always was.
type Routes struct {
	// Services: a service id (Services) to where it goes, a Target. A service left out
	// follows the mode.
	Services map[string]string `json:"services,omitempty"`
	// Direct: ids of DirectSets whose apps and sites go past the tunnel.
	Direct []string `json:"direct,omitempty"`
	// Lists are rule lists of the admin's own, by link, each sent one way like a service.
	Lists   []RouteList `json:"lists,omitempty"`
	DNS     DNS         `json:"dns,omitzero"`
	Servers Servers     `json:"servers,omitzero"`
	Tune    Tune        `json:"tune,omitzero"`
}

// RouteList is a rule list the apps download (a mihomo rule-provider): a site of the admin's
// choice (privWL-clash, a list of their own), sent to Target.
type RouteList struct {
	Name     string `json:"name"`     // a-z, 0-9 and "-": the provider's name in the profile
	URL      string `json:"url"`      // https
	Behavior string `json:"behavior"` // domain, ipcidr or classical
	Format   string `json:"format"`   // yaml, text or mrs
	Target   string `json:"target"`   // as a service's
}

// MaxLists bounds the admin's own lists: each is a download for every app.
const MaxLists = 30

// Servers is how the apps pick a server. The zero value is the profile as it was: a group
// per country, the fastest picked every five minutes.
type Servers struct {
	NoCountries bool   `json:"no_countries,omitempty"` // no group per node
	Auto        string `json:"auto,omitempty"`         // "" the fastest (url-test), "fallback" the first that works
	Interval    int    `json:"interval,omitempty"`     // seconds between checks; 0: 300
}

// Tune holds the finer settings of the profile.
type Tune struct {
	// BlockQUIC sends UDP to port 443 nowhere: browsers and YouTube fall back to TCP, which
	// gets through the tunnel better where QUIC is throttled.
	BlockQUIC bool `json:"block_quic,omitempty"`
	// Sniffer reads the site's name from the connection: rules by domain work for apps
	// that connect by IP.
	Sniffer bool `json:"sniffer,omitempty"`
	// RealIP resolves names for real instead of fake-ip: for apps that break on fake
	// addresses, at the cost of a DNS round trip before each connection.
	RealIP bool `json:"real_ip,omitempty"`
}

// Targets of a service. "node:<id>" is one server: a group of its own with the node's
// proxies, and the main group to fall back on.
const (
	TargetVPN    = "vpn"
	TargetDirect = "direct"
	TargetBlock  = "block"
	targetNode   = "node:"
)

func nodeOf(target string) (int64, bool) {
	s, ok := strings.CutPrefix(target, targetNode)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// DNS overrides the profile's resolvers (GitHub issue #70); an empty list keeps the
// panel's own for that key. Servers are as mihomo writes them: an IP, or a URL such as
// https://dns.alidns.com/dns-query, with "#<group>" to go through a group.
type DNS struct {
	Nameserver  []string    `json:"nameserver,omitempty"`
	Default     []string    `json:"default_nameserver,omitempty"`
	ProxyServer []string    `json:"proxy_server_nameserver,omitempty"`
	Policy      []DNSPolicy `json:"policy,omitempty"`
}

// DNSPolicy resolves the names Match covers (mihomo's nameserver-policy keys: "+.cn",
// "geosite:category-ru") with Servers.
type DNSPolicy struct {
	Match   string   `json:"match"`
	Servers []string `json:"servers"`
}

func (d DNS) empty() bool {
	return len(d.Nameserver) == 0 && len(d.Default) == 0 && len(d.ProxyServer) == 0 && len(d.Policy) == 0
}

// Match is one rule without its target: "GEOSITE,youtube" goes to wherever the service goes.
type Match struct {
	Type, Value string
	NoResolve   bool // an IP rule that must not make the app resolve the name first
}

func (m Match) rule(target string) string {
	r := m.Type + "," + m.Value + "," + target
	if m.NoResolve {
		r += ",no-resolve"
	}
	return r
}

// Provider is a rule list the app downloads (a rule-provider), matched by RULE-SET.
type Provider struct {
	Name, Behavior, Format, URL string
	// Since is the core the list needs (a rule type or format of its own); zero: any.
	Since Version
}

// Service is a kind of traffic the admin sends one way. The lists are mihomo's geodata
// (MetaCubeX meta-rules-dat) and, where it has none, lists the apps download: their
// sources are linked, not copied.
type Service struct {
	ID, Icon, Name, NameEN string
	Rules                  []Match
	Providers              []Provider
}

// The lists are pinned to a commit: whoever can push to a list's repository would
// otherwise reroute every client at once (a DIRECT entry takes any site past the tunnel).
// A release moves the pins after a look at what changed.
const (
	privWL = "https://raw.githubusercontent.com/Nemu-x/privWL-clash/5d0a5924b6c87e4abed0d3c87989ce2469679828/"
	legiz  = "https://raw.githubusercontent.com/legiz-ru/mihomo-rule-sets/a80b3c1d7a70ce83b1d1a45ff113a2fe5b312154/"
)

// Services in the order the admin panel lists them and the rules go.
var Services = []Service{
	{ID: "ads", Icon: "🚫", Name: "Реклама и трекеры", NameEN: "Ads and trackers", Rules: []Match{{Type: "GEOSITE", Value: "category-ads-all"}}},
	{ID: "youtube", Icon: "📺", Name: "YouTube", NameEN: "YouTube", Rules: []Match{{Type: "GEOSITE", Value: "youtube"}}},
	{ID: "telegram", Icon: "✈️", Name: "Telegram", NameEN: "Telegram",
		Rules:     []Match{{Type: "GEOSITE", Value: "telegram"}, {Type: "GEOIP", Value: "telegram", NoResolve: true}, {Type: "RULE-SET", Value: "mikan-telegram-ip", NoResolve: true}},
		Providers: []Provider{{Name: "mikan-telegram-ip", Behavior: "classical", Format: "yaml", URL: privWL + "tgprx.yaml"}}},
	{ID: "discord", Icon: "🎧", Name: "Discord", NameEN: "Discord",
		Rules: []Match{{Type: "GEOSITE", Value: "discord"}, {Type: "PROCESS-NAME-REGEX", Value: "(?i).*discord.*"}, {Type: "RULE-SET", Value: "mikan-discord-voice", NoResolve: true}},
		Providers: []Provider{{Name: "mikan-discord-voice", Behavior: "ipcidr", Format: "mrs", Since: mrsSince,
			URL: legiz + "other/discord-voice-ip-list.mrs"}}},
	{ID: "messengers", Icon: "💬", Name: "WhatsApp и Signal", NameEN: "WhatsApp and Signal", Rules: []Match{{Type: "GEOSITE", Value: "whatsapp"}, {Type: "GEOSITE", Value: "signal"}}},
	{ID: "meta", Icon: "📷", Name: "Instagram и Facebook", NameEN: "Instagram and Facebook",
		Rules: []Match{{Type: "GEOSITE", Value: "instagram"}, {Type: "GEOSITE", Value: "facebook"}, {Type: "GEOIP", Value: "facebook", NoResolve: true}}},
	{ID: "tiktok", Icon: "🎵", Name: "TikTok", NameEN: "TikTok", Rules: []Match{{Type: "GEOSITE", Value: "tiktok"}}},
	{ID: "twitter", Icon: "🐦", Name: "X (Twitter)", NameEN: "X (Twitter)", Rules: []Match{{Type: "GEOSITE", Value: "twitter"}, {Type: "GEOIP", Value: "twitter", NoResolve: true}}},
	{ID: "ai", Icon: "🤖", Name: "ChatGPT, Claude, Gemini", NameEN: "ChatGPT, Claude, Gemini",
		Rules: []Match{{Type: "GEOSITE", Value: "category-ai-!cn"}, {Type: "RULE-SET", Value: "mikan-ai"}, {Type: "RULE-SET", Value: "mikan-gemini"}},
		Providers: []Provider{{Name: "mikan-ai", Behavior: "classical", Format: "yaml", URL: privWL + "routing/ai.yaml"},
			{Name: "mikan-gemini", Behavior: "classical", Format: "yaml", URL: privWL + "routing/gemini.yaml"}}},
	{ID: "streaming", Icon: "🎬", Name: "Netflix и Spotify", NameEN: "Netflix and Spotify",
		Rules: []Match{{Type: "GEOSITE", Value: "netflix"}, {Type: "GEOSITE", Value: "spotify"}, {Type: "GEOIP", Value: "netflix", NoResolve: true}}},
	{ID: "twitch", Icon: "📡", Name: "Twitch", NameEN: "Twitch", Rules: []Match{{Type: "GEOSITE", Value: "twitch"}}},
	{ID: "games", Icon: "🕹️", Name: "Игры (Steam, Epic, Riot…)", NameEN: "Games (Steam, Epic, Riot…)",
		Rules:     []Match{{Type: "RULE-SET", Value: "mikan-games"}, {Type: "GEOSITE", Value: "category-games"}},
		Providers: []Provider{{Name: "mikan-games", Behavior: "classical", Format: "yaml", URL: privWL + "routing/games-core.yaml"}}},
	{ID: "github", Icon: "🐙", Name: "GitHub", NameEN: "GitHub", Rules: []Match{{Type: "GEOSITE", Value: "github"}}},
	{ID: "torrents", Icon: "🧲", Name: "Торренты", NameEN: "Torrents",
		Rules: []Match{{Type: "RULE-SET", Value: "mikan-torrent-apps"}, {Type: "GEOSITE", Value: "category-public-tracker"}},
		Providers: []Provider{{Name: "mikan-torrent-apps", Behavior: "classical", Format: "yaml",
			URL: legiz + "other/torrent-clients.yaml"}}},
}

// DirectSet is a list of apps and sites that go past the tunnel, for the apps that need a
// local address: banks, state services, marketplaces.
type DirectSet struct {
	ID, Name, NameEN string
	Providers        []Provider
}

var DirectSets = []DirectSet{
	{ID: "ru", Name: "Российские приложения и сайты: банки, госуслуги, маркетплейсы", NameEN: "Russian apps and sites: banks, state services, marketplaces",
		Providers: []Provider{
			{Name: "mikan-ru-apps", Behavior: "classical", Format: "yaml", URL: privWL + "ru/process-direct-apk.yaml"},
			{Name: "mikan-ru-apps-wildcard", Behavior: "classical", Format: "yaml", URL: privWL + "ru/process-direct-apk-wildcard.yaml", Since: Version{1, 19, 12}},
			{Name: "mikan-ru-pc", Behavior: "classical", Format: "yaml", URL: privWL + "ru/process-direct-pc.yaml"},
			{Name: "mikan-ru-sites", Behavior: "classical", Format: "yaml", URL: privWL + "ru/domain-direct-suffix.yaml"},
			{Name: "mikan-ru-keywords", Behavior: "classical", Format: "yaml", URL: privWL + "ru/domain-direct-keyword.yaml"},
			{Name: "mikan-ru-core", Behavior: "classical", Format: "yaml", URL: privWL + "ru/ru-core.yaml"},
		}},
}

// blockedLists are what RoutingBlocked sends through the tunnel: sites and apps blocked
// or slowed down in Russia (privWL-clash, kept by hand).
var blockedLists = []Provider{
	{Name: "mikan-blocked", Behavior: "classical", Format: "yaml", URL: privWL + "clV2_provider.yaml"},
	{Name: "mikan-blocked-apps", Behavior: "classical", Format: "yaml", URL: privWL + "process-core.yaml"},
}

// mrsSince is the first mihomo with binary rule lists (.mrs).
var mrsSince = Version{1, 18, 7}

// listProvider is the profile's name of an own list: apart from the panel's ("mikan-").
func listProvider(name string) string { return "own-" + name }

var listName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// checkLists refuses a list that would break the profiles: a name taken or unfit for a
// provider, a link that is not https, a behavior or format mihomo does not know.
func checkLists(lists []RouteList, target func(string) bool) error {
	if len(lists) > MaxLists {
		return errors.New("routes_lists_many")
	}
	seen := map[string]bool{}
	for _, l := range lists {
		u, err := url.Parse(l.URL)
		switch {
		case !listName.MatchString(l.Name) || seen[l.Name]:
			return errors.New("routes_list_name")
		case err != nil || u.Scheme != "https" || u.Host == "" || len(l.URL) > 500:
			return errors.New("routes_list_url")
		case !slices.Contains([]string{"domain", "ipcidr", "classical"}, l.Behavior):
			return errors.New("routes_list_behavior")
		case !slices.Contains([]string{"yaml", "text", "mrs"}, l.Format) || l.Format == "mrs" && l.Behavior == "classical":
			return errors.New("routes_list_format")
		case !target(l.Target):
			return errors.New("routes_target")
		}
		seen[l.Name] = true
	}
	return nil
}

func serviceByID(id string) (Service, bool) {
	i := slices.IndexFunc(Services, func(s Service) bool { return s.ID == id })
	if i < 0 {
		return Service{}, false
	}
	return Services[i], true
}

func directSetByID(id string) (DirectSet, bool) {
	i := slices.IndexFunc(DirectSets, func(s DirectSet) bool { return s.ID == id })
	if i < 0 {
		return DirectSet{}, false
	}
	return DirectSets[i], true
}

// Check refuses what would break the profiles: an unknown service, list or target, a node
// that does not exist, a DNS server mihomo would not read. The error is a code for the
// admin panel.
func (r Routes) Check(nodeExists func(int64) bool) error {
	target := func(t string) bool {
		switch t {
		case TargetVPN, TargetDirect, TargetBlock:
			return true
		}
		n, ok := nodeOf(t)
		return ok && nodeExists(n)
	}
	for id, t := range r.Services {
		if _, ok := serviceByID(id); !ok {
			return errors.New("routes_service")
		}
		if !target(t) {
			return errors.New("routes_target")
		}
	}
	if err := checkLists(r.Lists, target); err != nil {
		return err
	}
	if s := r.Servers; s.Auto != "" && s.Auto != "fallback" || s.Interval != 0 && (s.Interval < 30 || s.Interval > 86400) {
		return errors.New("routes_servers")
	}
	for _, id := range r.Direct {
		if _, ok := directSetByID(id); !ok {
			return errors.New("routes_direct")
		}
	}
	for _, list := range [][]string{r.DNS.Nameserver, r.DNS.Default, r.DNS.ProxyServer} {
		for _, s := range list {
			if !dnsServer(s) {
				return errors.New("routes_dns")
			}
		}
	}
	for _, p := range r.DNS.Policy {
		if p.Match == "" || strings.ContainsAny(p.Match, " \t\n") || len(p.Servers) == 0 {
			return errors.New("routes_dns")
		}
		for _, s := range p.Servers {
			if !dnsServer(s) {
				return errors.New("routes_dns")
			}
		}
	}
	return nil
}

// dnsServer: an IP (with a port or not), or a URL of a scheme mihomo reads.
func dnsServer(s string) bool {
	if s == "system" {
		return true
	}
	if s == "" || strings.ContainsAny(s, " \t\n\"'") {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if _, err := netip.ParseAddrPort(s); err == nil {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "udp", "tcp", "tls", "https", "h3", "quic", "dhcp":
		return u.Host != ""
	case "system":
		return true
	}
	return false
}

// routable: the app takes rule lists and the rules of Services. Stash and apps we do not
// know get the profile without them; a zero App (the admin panel's preview) is mihomo.
func (p Profile) routable() bool {
	return p.App.Family == "" || p.App.Family == FamilyMihomo
}

// takes says whether the app's core reads a list.
func (p Profile) takes(pr Provider) bool {
	return !pr.Since.Known() || p.App.Core.Known() && p.App.Core.AtLeast(pr.Since)
}

// routed is what Routes adds to a profile.
type routed struct {
	groups    []map[string]any
	providers map[string]any
	rules     []string
	geodata   bool // a GEOSITE or GEOIP rule: the app needs mihomo's geodata
}

func (p Profile) route(g Groups, ps []proxy, taken map[string]bool, r Routing) routed {
	var out routed
	if !p.routable() {
		return out
	}
	out.providers = map[string]any{}
	use := func(pr Provider) bool {
		if !p.takes(pr) {
			return false
		}
		ext := pr.Format
		if ext == "" {
			ext = "yaml"
		}
		// Through the tunnel: GitHub is slow or cut off where the VPN is needed.
		out.providers[pr.Name] = map[string]any{"type": "http", "behavior": pr.Behavior, "format": pr.Format, "url": pr.URL,
			"path": "./ruleset/" + pr.Name + "." + ext, "interval": 86400, "proxy": g.Main}
		return true
	}
	for _, s := range Services {
		t, ok := p.Routes.Services[s.ID]
		if !ok {
			continue
		}
		target := p.serviceTarget(s, t, g, ps, taken, &out)
		have := map[string]bool{}
		for _, pr := range s.Providers {
			have[pr.Name] = use(pr)
		}
		for _, m := range s.Rules {
			if m.Type == "RULE-SET" && !have[m.Value] {
				continue
			}
			out.geodata = out.geodata || m.Type == "GEOSITE" || m.Type == "GEOIP"
			out.rules = append(out.rules, m.rule(target))
		}
	}
	for _, l := range p.Routes.Lists {
		pr := Provider{Name: listProvider(l.Name), Behavior: l.Behavior, Format: l.Format, URL: l.URL}
		if l.Format == "mrs" {
			pr.Since = mrsSince
		}
		if !use(pr) {
			continue
		}
		target := p.serviceTarget(Service{Icon: "📋", Name: l.Name, NameEN: l.Name}, l.Target, g, ps, taken, &out)
		rule := "RULE-SET," + pr.Name + "," + target
		if l.Behavior == "ipcidr" {
			rule += ",no-resolve"
		}
		out.rules = append(out.rules, rule)
	}
	for _, id := range p.Routes.Direct {
		set, _ := directSetByID(id)
		for _, pr := range set.Providers {
			if use(pr) {
				out.rules = append(out.rules, "RULE-SET,"+pr.Name+",DIRECT")
			}
		}
	}
	if r == RoutingBlocked {
		for _, pr := range blockedLists {
			if use(pr) {
				out.rules = append(out.rules, "RULE-SET,"+pr.Name+","+g.Main)
			}
		}
		// The lists name sites by GEOSITE too.
		out.geodata = true
		out.rules = rejectTwins(out.rules)
	}
	return out
}

// rejectTwins follows every rule into the tunnel with the same match to REJECT. mihomo
// skips a rule whose group cannot carry UDP (XHTTP) and here the profile ends with
// MATCH,DIRECT: QUIC to a blocked site would go out from the real address. The twin
// stops it there, and the app falls back to TCP through the tunnel.
func rejectTwins(rules []string) []string {
	out := make([]string, 0, 2*len(rules))
	for _, r := range rules {
		out = append(out, r)
		// TYPE,VALUE,TARGET[,no-resolve]: anything else (a comma in the value) stays as it is.
		parts := strings.Split(r, ",")
		if len(parts) < 3 || len(parts) > 4 || len(parts) == 4 && parts[3] != "no-resolve" {
			continue
		}
		if target := parts[2]; target == "DIRECT" || strings.HasPrefix(target, "REJECT") {
			continue
		}
		parts[2] = "REJECT"
		out = append(out, strings.Join(parts, ","))
	}
	return out
}

// serviceTarget is the policy or group a service's rules go to; a group of one node is
// added to out.
func (p Profile) serviceTarget(s Service, t string, g Groups, ps []proxy, taken map[string]bool, out *routed) string {
	switch t {
	case TargetDirect:
		return "DIRECT"
	case TargetBlock:
		return "REJECT"
	}
	id, ok := nodeOf(t)
	if !ok {
		return g.Main
	}
	var own []string
	for _, x := range ps {
		if x.node == id {
			own = append(own, x.name)
		}
	}
	name := s.Icon + " " + s.Name
	if p.Lang == "en" {
		name = s.Icon + " " + s.NameEN
	}
	// A rule is split on commas: "ChatGPT, Claude, Gemini" as a target breaks the profile.
	name = strings.NewReplacer(", ", " · ", ",", " ").Replace(name)
	// A node this app has nothing of, or a name already taken: the main group.
	if len(own) == 0 || taken[strings.ToLower(name)] {
		return g.Main
	}
	taken[strings.ToLower(name)] = true
	out.groups = append(out.groups, map[string]any{"name": name, "type": "select", "proxies": append(own, g.Main)})
	return name
}

// apply puts the admin's resolvers over the panel's own.
func (d DNS) apply(dns map[string]any) {
	if d.empty() {
		return
	}
	for key, list := range map[string][]string{"nameserver": d.Nameserver, "default-nameserver": d.Default, "proxy-server-nameserver": d.ProxyServer} {
		if len(list) > 0 {
			dns[key] = list
		}
	}
	if len(d.Policy) > 0 {
		policy := map[string]any{}
		for _, x := range d.Policy {
			policy[x.Match] = x.Servers
		}
		dns["nameserver-policy"] = policy
	}
}
