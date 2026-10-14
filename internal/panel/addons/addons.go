// Package addons runs the addons of the marketplace (getmikan/marketplace): the signed
// catalog, what the host installed, the install and remove requests to the host, and the
// adapter protocol (client.go). Payment adapters speak protocol 1 (PROTOCOL.md in the
// marketplace repository); every other kind speaks protocol 2 (PROTOCOL-v2.md).
//
// The panel has no Docker socket: containers are the host's job. They talk through files
// in the panel's data directory, as updates do:
//
//	addons/requests/<seq>-<id>.json  the panel: {"action": "install|remove", "id", "at"},
//	                     for an addon of the admin's own also "source" (custom|external)
//	                     with "image" and "digest" or "url", "kind", "mode", "permissions",
//	                     "mem_mb"; seq is one past the highest queued, and the host takes them
//	                     in that order. The panel then wakes the host with update/request
//	                     {"do": "addons"}
//	addons/state.json    the host: what runs where, with the token, and how the last
//	                     request went
package addons

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/release"
)

// CatalogURL is where the signed catalog is published, next to its .sig.
const CatalogURL = "https://github.com/getmikan/marketplace/releases/latest/download/index.json"

// Protocol is the payment adapters' protocol; Protocol2 that of every other kind.
const (
	Protocol  = 1
	Protocol2 = 2
)

// Kinds of addons. Payment adapters are protocol 1 only, the rest protocol 2 only.
const (
	KindPayment = "payment"
	KindBot     = "bot"
	KindUI      = "ui"
	KindGeneric = "generic"
)

// Modes of a protocol 2 addon: it serves /v2/* to the panel, or it is only a client of
// the panel (a bot on a framework with no HTTP server of its own).
const (
	ModeHTTP   = "http"
	ModeClient = "client"
)

// Sources of an addon: the signed catalog, an image the admin named by its digest, or a
// process of the admin's own on a loopback port. Payment adapters come from the catalog only.
const (
	SourceOfficial = "official"
	SourceCustom   = "custom"
	SourceExternal = "external"
)

// Permissions an addon of protocol 2 may ask for; the admin approves them at install.
// users:read and users:write are the bot's: the subscribers it talks to. users:all (read
// every subscriber) and users:grant (give anyone days and traffic) reach all of them, are
// implied by neither of those, and the admin is warned of both.
var Permissions = []string{"bot", "users:read", "users:write", "users:all", "users:grant", "billing:invoice", "events", "kv", "ui"}

// MinMemMB and MaxMemMB bound what an addon may ask for; 0 is the host's default.
const (
	MinMemMB = 64
	MaxMemMB = 256
)

// AdapterTimeout bounds one call to an adapter: some make two calls to their provider.
const AdapterTimeout = 40 * time.Second

var (
	ErrUnavailable  = errors.New("addons_unavailable") // no data directory (tests, the CLI)
	ErrUnknown      = errors.New("addon_unknown")
	ErrNotInstalled = errors.New("addon_not_installed")
	ErrBusy         = errors.New("addon_busy")       // a request for this addon is still with the host
	ErrBadAnswer    = errors.New("addon_bad_answer") // an addon's answer that is too large, not JSON, a redirect, or from another addon
)

// validID: adapter ids are path and provider-name safe.
var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func ValidID(id string) bool { return validID.MatchString(id) }

var (
	validHost      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*(?::[0-9]{1,5})?$`)
	validComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
)

// ValidImage: an image the admin named, in docker's plain grammar and nothing more,
// lowercase and without a tag (the digest goes apart): [host[:port]/]path/name. The host
// checks the same (valid_reference in installer/src/addon.rs).
func ValidImage(r string) bool {
	if r == "" || len(r) > 255 {
		return false
	}
	parts := strings.Split(r, "/")
	// The first part is a registry when it looks like a host, as docker reads it.
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		if !validHost.MatchString(parts[0]) {
			return false
		}
		parts = parts[1:]
	}
	for _, p := range parts {
		if !validComponent.MatchString(p) {
			return false
		}
	}
	return true
}

// ValidURL: an external addon's address, http://127.0.0.1:<unprivileged port> and nothing more.
func ValidURL(u string) bool {
	port, ok := strings.CutPrefix(strings.TrimSuffix(u, "/"), "http://127.0.0.1:")
	n, err := strconv.Atoi(port)
	return ok && err == nil && n >= 1024 && n <= 65535 && strconv.Itoa(n) == port
}

// Text is a string per language: {"ru": …, "en": …}.
type Text map[string]string

// In picks the text of lang, else English, else any.
func (t Text) In(lang string) string {
	if s := t[lang]; s != "" {
		return s
	}
	if s := t["en"]; s != "" {
		return s
	}
	for _, s := range t {
		return s
	}
	return ""
}

// Entry is one addon of the catalog. Kind, Mode, Permissions and Resources are protocol 2;
// a protocol 1 entry is a payment adapter serving HTTP.
type Entry struct {
	ID          string   `json:"id"`
	Name        Text     `json:"name"`
	Description Text     `json:"description"`
	Version     string   `json:"version"`
	Protocol    int      `json:"protocol"`
	Kind        string   `json:"kind,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	// How its events reach it (push, and the types it wants) is not here: an addon of the
	// admin's own has no entry, so the panel reads it from the addon's /v2/info (InfoV2). That
	// is a choice of delivery only; what an addon may receive is the events permission.
	Resources Resources `json:"resources,omitzero"`
	Image     string    `json:"image"`
	Digest    string    `json:"digest"`
	MinPanel  string    `json:"min_panel"`
	Homepage  string    `json:"homepage"`
}

// Resources an addon asks the host for.
type Resources struct {
	MemMB int `json:"mem_mb,omitempty"`
}

type Catalog struct {
	Version  int       `json:"version"`
	Updated  time.Time `json:"updated"`
	Adapters []Entry   `json:"adapters"`
}

// Of is the catalog's addons of one kind.
func (c Catalog) Of(kind string) []Entry {
	var out []Entry
	for _, e := range c.Adapters {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// normalize fills what protocol 1 leaves out and reports whether the entry is one this
// panel can run: the right kind for its protocol, a known mode, known permissions, sane
// resources. The host checks the same (installer/src/addon.rs).
func (e *Entry) normalize() bool {
	switch e.Protocol {
	case Protocol:
		if e.Kind != "" && e.Kind != KindPayment || e.Mode != "" && e.Mode != ModeHTTP || len(e.Permissions) > 0 || e.Resources != (Resources{}) {
			return false
		}
		e.Kind, e.Mode = KindPayment, ModeHTTP
		return true
	case Protocol2:
		if e.Mode == "" {
			e.Mode = ModeHTTP
		}
		if e.Kind != KindBot && e.Kind != KindUI && e.Kind != KindGeneric || e.Mode != ModeHTTP && e.Mode != ModeClient {
			return false
		}
		seen := map[string]bool{}
		for _, p := range e.Permissions {
			if seen[p] || !slices.Contains(Permissions, p) {
				return false
			}
			seen[p] = true
		}
		return e.Resources.MemMB == 0 || e.Resources.MemMB >= MinMemMB && e.Resources.MemMB <= MaxMemMB
	}
	return false
}

// ParseCatalog checks the signature over the exact bytes, then the entries: what this
// panel cannot run (another protocol, a newer panel, a malformed entry) is left out.
func ParseCatalog(data []byte, sig string, pub ed25519.PublicKey, panelVersion string) (Catalog, error) {
	if err := release.Verify(data, sig, pub); err != nil {
		return Catalog{}, fmt.Errorf("addons: the catalog signature does not match: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, fmt.Errorf("addons: catalog: %w", err)
	}
	out := c.Adapters[:0]
	for _, e := range c.Adapters {
		if !ValidID(e.ID) || !e.normalize() || e.Image == "" || !release.ValidDigest(e.Digest) {
			continue
		}
		if e.MinPanel != "" && panelVersion != "dev" && release.Newer(e.MinPanel, panelVersion) {
			continue
		}
		out = append(out, e)
	}
	c.Adapters = out
	return c, nil
}

// Installed is an addon the host runs. Token is what the panel calls the addon with;
// PanelToken (protocol 2) what the addon calls the panel with.
type Installed struct {
	Version     string   `json:"version"`
	Image       string   `json:"image"` // "" for an external addon
	Digest      string   `json:"digest"`
	Listen      string   `json:"listen"`
	Token       string   `json:"token"`
	Protocol    int      `json:"protocol,omitempty"`    // the host writes it for every addon; 0 is 1, as older states have it
	Source      string   `json:"source,omitempty"`      // "" is official
	Kind        string   `json:"kind,omitempty"`        // payment for an adapter; "" is payment, as older states have it
	Mode        string   `json:"mode,omitempty"`        // http for an adapter; "" is http, as older states have it
	Permissions []string `json:"permissions,omitempty"` // approved for an addon of the admin's own
	PanelToken  string   `json:"panel_token,omitempty"`
	MemMB       int      `json:"mem_mb,omitempty"` // 0 is the host's default
	Status      string   `json:"status"`           // running
	At          string   `json:"at"`
}

// KindOf is the addon's kind; states before protocol 2 hold payment adapters only.
func (a Installed) KindOf() string {
	if a.Kind == "" {
		return KindPayment
	}
	return a.Kind
}

// ModeOf is how the addon talks to the panel; the host writes nothing for http.
func (a Installed) ModeOf() string {
	if a.Mode == "" {
		return ModeHTTP
	}
	return a.Mode
}

// SourceOf is where the addon comes from; the host writes nothing for the catalog.
func (a Installed) SourceOf() string {
	if a.Source == "" {
		return SourceOfficial
	}
	return a.Source
}

// LastRequest is how the host handled the panel's last request.
type LastRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	State  string `json:"state"` // done | failed
	Error  string `json:"error,omitempty"`
	At     string `json:"at"`
}

// State is the host's report. PanelAPI is the loopback address the host gave the panel's
// API for addons (protocol 2); PanelAPIBridge the same port on the gateway of the bridge
// custom addons run on, set while one runs; HostVersion the version of the host that wrote it.
type State struct {
	Adapters       map[string]Installed `json:"adapters"`
	Request        *LastRequest         `json:"request,omitempty"`
	PanelAPI       string               `json:"panel_api,omitempty"`
	PanelAPIBridge string               `json:"panel_api_bridge,omitempty"`
	HostVersion    string               `json:"host_version,omitempty"`
}

// Of is the installed addons of one kind.
func (s State) Of(kind string) map[string]Installed {
	out := map[string]Installed{}
	for id, a := range s.Adapters {
		if a.KindOf() == kind {
			out[id] = a
		}
	}
	return out
}

// Request is what the panel asks the host to do. Source and what follows it come only with
// an addon of the admin's own: Image and Digest for a custom one, URL for an external one.
type Request struct {
	Action      string   `json:"action"` // install | remove
	ID          string   `json:"id"`
	Source      string   `json:"source,omitempty"` // "" is official
	Image       string   `json:"image,omitempty"`
	Digest      string   `json:"digest,omitempty"`
	URL         string   `json:"url,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	MemMB       int      `json:"mem_mb,omitempty"`
	At          string   `json:"at"`
}

// Valid checks a request as the host will (sound in installer/src/addon.rs): one for the
// catalog carries nothing else; an addon of the admin's own is protocol 2, with an image by
// its digest or a loopback URL (none for a client of the panel, which serves nothing).
func (r Request) Valid() bool {
	if !ValidID(r.ID) || r.Action != "install" && r.Action != "remove" {
		return false
	}
	own := Entry{Protocol: Protocol2, Kind: r.Kind, Mode: r.Mode, Permissions: r.Permissions, Resources: Resources{MemMB: r.MemMB}}
	switch {
	case r.Source == "":
		return r.Image == "" && r.Digest == "" && r.URL == "" && r.Kind == "" && r.Mode == "" && len(r.Permissions) == 0 && r.MemMB == 0
	case r.Action != "install":
		return false
	case r.Source == SourceCustom:
		return ValidImage(r.Image) && release.ValidDigest(r.Digest) && r.URL == "" && own.normalize()
	case r.Source == SourceExternal:
		return r.Image == "" && r.Digest == "" && (ValidURL(r.URL) || r.URL == "" && r.Mode == ModeClient) && own.normalize()
	}
	return false
}

// Manager reads the catalog and the host's state, and talks to the adapters.
type Manager struct {
	dir, updateDir string // "" without a data directory
	catalogURL     string
	pub            ed25519.PublicKey
	version        string
	hc, peer       *http.Client // peer talks to the addons: no redirects followed
	log            *slog.Logger
	now            func() time.Time

	mu      sync.Mutex
	catalog *Catalog
	fetched time.Time
	infos   map[string]described[Info]
	infos2  map[string]described[InfoV2]
	src     *sourceNet              // the way out to third-party sources (sources.go)
	sources map[int64]SourceState   // the last fetch of each source
	flights map[int64]*sourceFlight // fetches under way
	noInfo  map[string]noInfo       // a /v2/info that failed, by addon

	ask sync.Mutex // one request queued at a time: the number and the busy check
}

// New: dataDir is the panel's data directory ("" turns the host side off); catalogURL ""
// is the marketplace's own.
func New(dataDir, catalogURL, panelVersion string, log *slog.Logger, now func() time.Time) *Manager {
	if catalogURL == "" {
		catalogURL = CatalogURL
	}
	pub, _ := release.Key(release.PublicKey)
	m := &Manager{catalogURL: catalogURL, pub: pub, version: panelVersion, log: log, now: now,
		hc:    &http.Client{Timeout: 15 * time.Second},
		infos: map[string]described[Info]{}, infos2: map[string]described[InfoV2]{}, noInfo: map[string]noInfo{},
		src: newSourceNet(false), sources: map[int64]SourceState{}, flights: map[int64]*sourceFlight{},
		// The panel fetches what the addon names and nothing it points elsewhere to.
		peer: &http.Client{Timeout: AdapterTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if dataDir != "" {
		m.dir, m.updateDir = filepath.Join(dataDir, "addons"), filepath.Join(dataDir, "update")
	}
	return m
}

// Supported: the panel sees the server's data directory, so the host can run adapters.
func (m *Manager) Supported() bool { return m.dir != "" }

// SetKey replaces the catalog key (tests sign with their own).
func (m *Manager) SetKey(pub ed25519.PublicKey) { m.pub = pub }

// Catalog is the signed catalog, fetched at most every ten minutes.
func (m *Manager) Catalog(ctx context.Context) (Catalog, error) {
	m.mu.Lock()
	if m.catalog != nil && m.now().Sub(m.fetched) < 10*time.Minute {
		c := *m.catalog
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()
	var c Catalog
	err := release.FetchSigned(ctx, m.get, m.catalogURL, 1<<20, func(data []byte, sig string) (err error) {
		c, err = ParseCatalog(data, sig, m.pub, m.version)
		return err
	})
	if err != nil {
		return Catalog{}, err
	}
	m.mu.Lock()
	m.catalog, m.fetched = &c, m.now()
	m.mu.Unlock()
	return c, nil
}

func (m *Manager) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("addons: catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("addons: catalog: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// State is what the host reports; nothing installed when it has not written anything.
func (m *Manager) State() (State, error) {
	s := State{Adapters: map[string]Installed{}}
	if m.dir == "" {
		return s, nil
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("addons: state.json: %w", err)
	}
	if s.Adapters == nil {
		s.Adapters = map[string]Installed{}
	}
	return s, nil
}

// requestLimit is the largest request the host reads.
const requestLimit = 4096

// requestName parses <seq>-<id>.json as the host does.
func requestName(name string) (seq uint64, id string, ok bool) {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return 0, "", false
	}
	n, id, ok := strings.Cut(base, "-")
	if !ok || n == "" || len(n) > 19 || strings.Trim(n, "0123456789") != "" || !ValidID(id) {
		return 0, "", false
	}
	seq, err := strconv.ParseUint(n, 10, 64)
	return seq, id, err == nil
}

// queue reads what the host has not taken yet, oldest first, by number; next is one past
// the highest number queued.
func (m *Manager) queue() (q []Request, next uint64) {
	next = 1
	entries, _ := os.ReadDir(filepath.Join(m.dir, "requests"))
	type numbered struct {
		seq      uint64
		name, id string
	}
	var queued []numbered
	for _, e := range entries {
		if seq, id, ok := requestName(e.Name()); ok {
			next = max(next, seq+1)
			queued = append(queued, numbered{seq, e.Name(), id})
		}
	}
	slices.SortFunc(queued, func(a, b numbered) int { return cmp.Or(cmp.Compare(a.seq, b.seq), cmp.Compare(a.name, b.name)) })
	for _, n := range queued {
		data, err := os.ReadFile(filepath.Join(m.dir, "requests", n.name))
		var r Request
		if err == nil && len(data) <= requestLimit && json.Unmarshal(data, &r) == nil && r.ID == n.id {
			q = append(q, r)
		}
	}
	return q, next
}

// Pending is what the host has not taken yet, oldest first.
func (m *Manager) Pending() []Request {
	if m.dir == "" {
		return nil
	}
	q, _ := m.queue()
	return q
}

// Ask queues a request for the host: install or remove an addon. Requests for different
// addons wait in line; a second one for an addon still in line is refused.
func (m *Manager) Ask(r Request) error {
	if m.dir == "" {
		return ErrUnavailable
	}
	if !r.Valid() {
		return ErrUnknown
	}
	m.ask.Lock()
	defer m.ask.Unlock()
	q, next := m.queue()
	if slices.ContainsFunc(q, func(p Request) bool { return p.ID == r.ID }) {
		return ErrBusy
	}
	at := m.now().UTC().Format(time.RFC3339)
	r.At = at
	if err := writeFile(filepath.Join(m.dir, "requests"), fmt.Sprintf("%010d-%s.json", next, r.ID), r); err != nil {
		return err
	}
	// The host's update request unit wakes up on this file: no unit of its own needed. An
	// update the admin asked for stays asked: the host takes the adapters after it.
	if _, err := os.Stat(filepath.Join(m.updateDir, "request")); err == nil {
		return nil
	}
	return writeFile(m.updateDir, "request", map[string]string{"at": at, "do": "addons"})
}

func writeFile(dir, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, name), data, 0o644)
}

// running is the addon the host runs and listens for, when accept takes it: the one place
// that tells a running addon from the rest of state.json.
func (m *Manager) running(id string, accept func(Installed) bool) (Installed, error) {
	s, err := m.State()
	if err != nil {
		return Installed{}, err
	}
	a, ok := s.Adapters[id]
	if !ok || a.Status != "running" || a.Listen == "" || !accept(a) {
		return Installed{}, ErrNotInstalled
	}
	return a, nil
}

func isPayment(a Installed) bool { return a.KindOf() == KindPayment }

// Client talks to an installed, running adapter.
func (m *Manager) Client(id string) (*Client, error) {
	a, err := m.running(id, isPayment)
	if err != nil {
		return nil, err
	}
	return NewClient("http://"+a.Listen, a.Token, m.peer), nil
}

// Info is the payment adapter's own description, asked once per installed build.
func (m *Manager) Info(ctx context.Context, id string) (Info, error) {
	a, err := m.running(id, isPayment)
	if err != nil {
		return Info{}, err
	}
	return describe(ctx, m, m.infos, id, a, "/v1/info", Protocol, func(i Info) (string, int) { return i.ID, i.Protocol })
}

// described is what an addon said of itself, with the build it said it for.
type described[T any] struct {
	build string
	info  T
}

// build tells one run of an installed build from another: an external addon has no digest,
// so a new run of it is told by the host's time.
func (a Installed) build() string { return a.Digest + "\x00" + a.Version + "\x00" + a.At }

// describe asks a running addon for its description at path, once per installed build, and
// takes it only from the addon it should be: who reads the id and the protocol out of it.
func describe[T any](ctx context.Context, m *Manager, cache map[string]described[T], id string, a Installed, path string, protocol int, who func(T) (string, int)) (T, error) {
	build := a.build()
	m.mu.Lock()
	c, ok := cache[id]
	m.mu.Unlock()
	if ok && c.build == build {
		return c.info, nil
	}
	var zero, info T
	if err := NewClient("http://"+a.Listen, a.Token, m.peer).call(ctx, http.MethodGet, path, nil, &info); err != nil {
		return zero, err
	}
	if gotID, gotProtocol := who(info); gotID != id || gotProtocol != protocol {
		return zero, fmt.Errorf("%w: %s answers as %q, protocol %d", ErrBadAnswer, id, gotID, gotProtocol)
	}
	m.mu.Lock()
	cache[id] = described[T]{build: build, info: info}
	m.mu.Unlock()
	return info, nil
}
