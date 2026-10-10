package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/release"
)

const (
	KeyAdminPath  = "admin_path"
	KeySubPath    = "sub_path"
	KeyPublicHost = "public_host"
	KeyPanelPort  = "panel_port"
	// KeySubPort is a port of its own for subscriptions; 0 or unset: the panel's port.
	// The panel's port keeps serving subscriptions either way, for links handed out.
	KeySubPort   = "sub_port"
	KeyDomain    = "domain"
	KeyACMEEmail = "acme_email"
	// KeyACMECA is the certificate authority of the panel and its nodes (acme.ValidCA);
	// unset: Let's Encrypt. KeyACMEEABKID and KeyACMEEABHMAC are Google Trust Services'
	// external account key; the HMAC is a secret and never shown back.
	KeyACMECA      = "acme_ca"
	KeyACMEEABKID  = "acme_eab_kid"
	KeyACMEEABHMAC = "acme_eab_hmac"
	KeyGroupMain   = "sub_group_main" // subscription group names, see subs.Groups
	KeyGroupAuto   = "sub_group_auto"
	KeyRouting     = "sub_routing"  // subs.Routing
	KeyRules       = "sub_rules"    // the admin's own Clash rules, as typed (subs.ParseRules)
	KeyRoutes      = "sub_routes"   // services, direct apps and DNS of the profiles (subs.Routes)
	KeyTemplate    = "sub_template" // the admin's own Clash profile (subs.Template); empty: none
	// KeyFingerprint is the uTLS profile clients get where an inbound sets none
	// (proto.Fingerprints); unset means proto.DefaultFingerprint.
	KeyFingerprint = "client_fingerprint"
	// Automatic moves (internal/panel/autotune), on unless switched off.
	KeyAutoPort = "auto_port" // move an inbound whose port is blocked on the way to clients
	KeyAutoSNI  = "auto_sni"  // replace a REALITY target that stopped working
	// Devices (domain.Devices): bind subscriptions to devices, on unless switched off;
	// refuse apps that send no device id instead of seating them together, off by default.
	KeyDeviceBinding = "device_binding"
	KeyRequireHWID   = "device_require_hwid"
	// KeyDefaultLang is the language chosen at install: the admin panel and the subscription
	// page open in it until a visitor picks one, and new names (tariffs, the auto group, the
	// bot's menu) are written in it. "auto" or unset: the visitor's browser decides.
	KeyDefaultLang = "default_lang"
	// KeyAutoUpdate lets the host updater install new releases on its own, once a day;
	// off by default (internal/panel/updates).
	KeyAutoUpdate = "auto_update"
	// KeyUpdateChannel is which releases the panel and the host updater take: "stable"
	// (unset) or "beta", the pre-releases too (release.Stable, release.Beta).
	KeyUpdateChannel = "update_channel"
	// KeyNodesFollow lets the panel update its remote nodes to its own version, one at a
	// time, after it updated itself; on unless switched off (internal/panel/nodeupdate).
	KeyNodesFollow = "nodes_follow_panel"
	// KeyShowGoals shows the goals the project collects money for (release.Goals) on the
	// updates card; on unless switched off.
	KeyShowGoals = "show_goals"
	// Branding and support: the bot's and the subscription page's name and the support link.
	KeyBrand      = "brand"
	KeySupportURL = "support_url"
	// The announcement apps show over the profile (the announce and announce-url headers),
	// for maintenance or news; empty: none.
	KeyAnnounce    = "sub_announce"
	KeyAnnounceURL = "sub_announce_url"
	// KeySubTitle is the profile's name in the apps (subs.Config.Title); empty: the brand.
	KeySubTitle = "sub_title"
	// App branding: the brand, logo and accent colour go to the apps that read operator
	// headers (subs.OperatorHeaders); off by default.
	KeyAppBranding = "app_branding"
	KeyBrandAccent = "brand_accent"   // #RRGGBB, empty: the app's own
	KeyBrandLogo   = "brand_logo_url" // https, empty: the app's own
	// Happ (subs.Happ): a routing profile link, the happ-proxy.com provider id and what
	// needs it, and how the page's Happ button hides the address ("", "api", "local").
	KeyHappRouting  = "happ_routing"
	KeyHappProvider = "happ_provider_id"
	KeyHappHide     = "happ_hide_settings"
	KeyHappCrypt    = "happ_crypt"
	// KeyLegacySubPath is the path of the subscription links of the panel users were
	// imported from: "sub" for Marzban and PasarGuard, "api/sub" for Remnawave. The old
	// tokens lead to the users (legacy_sub_tokens); empty: off.
	KeyLegacySubPath = "legacy_sub_path"
	// KeyLegacySubKind and KeyLegacySubSecret check the links Marzban or PasarGuard signed
	// (panelimport.Verifier): the panel's kind and the secret from its jwt table. The
	// secret is never shown back.
	KeyLegacySubKind   = "legacy_sub_kind"
	KeyLegacySubSecret = "legacy_sub_secret"
	// KeyQuietHour is the UTC hour the slot pool is refilled, which reconnects QUIC clients.
	KeyQuietHour = "quiet_hour_utc"
	// KeySubPage is the subscription page as the admin built it (subpage.Config): its look,
	// blocks, apps and own CSS; unset: the page as it always was.
	KeySubPage = "sub_page"
)

// Switch is an on/off setting with its default: read it with On, so the default lives
// here and nowhere else.
type Switch struct {
	Key string
	Def bool
}

// The panel's switches.
var (
	AutoPort      = Switch{KeyAutoPort, true}
	AutoSNI       = Switch{KeyAutoSNI, true}
	DeviceBinding = Switch{KeyDeviceBinding, true}
	RequireHWID   = Switch{KeyRequireHWID, false}
	AutoUpdate    = Switch{KeyAutoUpdate, false}
	NodesFollow   = Switch{KeyNodesFollow, true}
	ShowGoals     = Switch{KeyShowGoals, true}
	AppBranding   = Switch{KeyAppBranding, false}
	HappHide      = Switch{KeyHappHide, false}
)

// ValidLang says whether s is a language of the panel.
func ValidLang(s string) bool { return s == "ru" || s == "en" }

type Settings struct {
	q     *db.Queries
	cache *cache // nil: every read goes to the database
}

func New(q *db.Queries) *Settings { return &Settings{q: q} }

// Cached is a Settings for a loop that reads the same settings every few seconds: a value
// is kept for ttl and dropped as soon as this process writes any setting. What another
// process writes (the CLI) is seen within ttl. Not for a transaction's queries, and not
// where a value must be read right after it is written.
func Cached(q *db.Queries, ttl time.Duration) *Settings {
	return &Settings{q: q, cache: &cache{ttl: ttl, items: map[string]cachedValue{}}}
}

type cache struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]cachedValue
}

type cachedValue struct {
	raw   string
	found bool
	gen   uint64
	at    time.Time
}

// writeGrace: a value read this soon after a write in this process is not kept. Set moves
// the generation before its transaction commits, so such a read may still see the old value
// under the new generation.
const writeGrace = 2 * time.Second

// raw is the stored JSON under key; found is false when the key is absent.
func (s *Settings) raw(ctx context.Context, key string) (string, bool, error) {
	if s.cache == nil {
		return s.read(ctx, key)
	}
	gen, now := generation.Load(), time.Now()
	s.cache.mu.Lock()
	v, ok := s.cache.items[key]
	s.cache.mu.Unlock()
	if ok && v.gen == gen && now.Sub(v.at) < s.cache.ttl {
		return v.raw, v.found, nil
	}
	raw, found, err := s.read(ctx, key)
	if err != nil {
		return "", false, err
	}
	if now.Sub(time.Unix(0, writtenAt.Load())) >= writeGrace {
		s.cache.mu.Lock()
		s.cache.items[key] = cachedValue{raw: raw, found: found, gen: gen, at: now}
		s.cache.mu.Unlock()
	}
	return raw, found, nil
}

func (s *Settings) read(ctx context.Context, key string) (string, bool, error) {
	raw, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

// Get decodes the JSON value stored under key. ok is false when the key is absent.
func Get[T any](ctx context.Context, s *Settings, key string) (v T, ok bool, err error) {
	raw, found, err := s.raw(ctx, key)
	if err != nil || !found {
		return v, false, err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, false, fmt.Errorf("setting %s: %w", key, err)
	}
	return v, true, nil
}

// GetOver decodes the value stored under key over def: what the stored JSON lacks, such
// as a field added after it was saved, keeps def's value.
func GetOver[T any](ctx context.Context, s *Settings, key string, def T) (T, bool, error) {
	raw, found, err := s.raw(ctx, key)
	if err != nil || !found {
		return def, false, err
	}
	v := def
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return def, false, fmt.Errorf("setting %s: %w", key, err)
	}
	return v, true, nil
}

func Set[T any](ctx context.Context, s *Settings, key string, v T) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := s.q.SetSetting(ctx, db.SetSettingParams{Key: key, Value: string(raw)}); err != nil {
		return err
	}
	moved()
	return nil
}

// generation counts the settings this process has written. Whoever keeps something built
// from settings checks it, and builds again when it moved. Changes made by another
// process (the CLI) do not move it: they are seen when what is kept expires.
var generation atomic.Uint64

// writtenAt is when generation last moved (unix nanoseconds).
var writtenAt atomic.Int64

func moved() {
	writtenAt.Store(time.Now().UnixNano())
	generation.Add(1)
}

// Generation is how many settings have been written by this process so far.
func Generation() uint64 { return generation.Load() }

// Touch moves the generation for a change kept outside the settings table that what is
// built from settings reads too (the subscription page's uploaded logo).
func Touch() { moved() }

func (s *Settings) String(ctx context.Context, key string) (string, error) {
	v, _, err := Get[string](ctx, s, key)
	return v, err
}

// On reads a switch; its default when it was never set.
func (s *Settings) On(ctx context.Context, sw Switch) (bool, error) {
	v, ok, err := Get[bool](ctx, s, sw.Key)
	if err != nil || !ok {
		return sw.Def, err
	}
	return v, nil
}

// Lang is the default language, "ru" or "en"; "" when the browser decides.
func (s *Settings) Lang(ctx context.Context) (string, error) {
	v, err := s.String(ctx, KeyDefaultLang)
	if err != nil || !ValidLang(v) {
		return "", err
	}
	return v, nil
}

// UpdateChannel is the channel of updates: release.Beta when chosen, release.Stable otherwise.
func (s *Settings) UpdateChannel(ctx context.Context) (string, error) {
	v, err := s.String(ctx, KeyUpdateChannel)
	if err != nil || !release.ValidChannel(v) {
		return release.Stable, err
	}
	return v, nil
}

type Paths struct {
	Admin string
	Sub   string
	// Legacy is the old panel's subscription path, one to three segments; "" when unset.
	Legacy string
}

func (s *Settings) Paths(ctx context.Context) (Paths, error) {
	a, err := s.String(ctx, KeyAdminPath)
	if err != nil {
		return Paths{}, err
	}
	sub, err := s.String(ctx, KeySubPath)
	if err != nil {
		return Paths{}, err
	}
	legacy, err := s.String(ctx, KeyLegacySubPath)
	if err != nil {
		return Paths{}, err
	}
	return Paths{Admin: a, Sub: sub, Legacy: legacy}, nil
}

// Endpoint is how clients reach the panel: host is an IP or a domain.
type Endpoint struct {
	Host string
	Port int
}

// URL is https://host:port, the panel's base address; "" while it has no host or port.
func (e Endpoint) URL() string {
	if e.Host == "" || e.Port <= 0 {
		return ""
	}
	return "https://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// SubEndpoint is where subscription links point: the subscription port when one is set.
func (s *Settings) SubEndpoint(ctx context.Context) (Endpoint, error) {
	ep, err := s.Endpoint(ctx)
	if err != nil {
		return ep, err
	}
	if p, _, err := Get[int](ctx, s, KeySubPort); err != nil {
		return ep, err
	} else if p > 0 {
		ep.Port = p
	}
	return ep, nil
}

func (s *Settings) Endpoint(ctx context.Context) (Endpoint, error) {
	host, err := s.String(ctx, KeyDomain)
	if err != nil {
		return Endpoint{}, err
	}
	if host == "" {
		if host, err = s.String(ctx, KeyPublicHost); err != nil {
			return Endpoint{}, err
		}
	}
	port, _, err := Get[int](ctx, s, KeyPanelPort)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: host, Port: port}, nil
}
