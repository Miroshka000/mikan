package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"time"
	"unicode/utf8"
)

// The admin pages of an addon of protocol 2 (docs/pipeline/addons.md, section 9): its
// /v2/info names an entry under /ui/, the panel proxies the files under /ui/ to a sandboxed
// frame and forwards the frame's calls to POST /v2/ui/rpc.

// Limits of what the panel takes from an addon's pages.
const (
	MaxPageFile  = 8 << 20 // one file of the pages
	MaxRPCAnswer = 1 << 20 // one answer to a call of the pages
)

// UI is where an addon's pages start and what the admin panel calls them.
type UI struct {
	Entry string `json:"entry"` // /ui/index.html
	Title Text   `json:"title"`
}

// InfoV2 is the part of an addon's /v2/info the panel reads: who answers, where its pages
// start and how it wants its events. The permissions come from the signed catalog entry and
// the grant, never from the addon: Push only picks the way events reach an addon that was
// granted them, and Events narrows what it is sent.
type InfoV2 struct {
	ID       string   `json:"id"`
	Protocol int      `json:"protocol"`
	UI       *UI      `json:"ui,omitempty"`
	Events   []string `json:"events,omitempty"` // the types it wants; empty: all
	Push     bool     `json:"push,omitempty"`   // it serves POST /v2/events and is sent them
}

// maxInfoEvents is how many event types of an addon's /v2/info the panel reads.
const maxInfoEvents = 64

// validEntry: a plain file path under /ui/, nothing to escape or to climb out with.
var validEntry = regexp.MustCompile(`^/ui/[A-Za-z0-9._~/-]{0,200}$`)

func (u *UI) valid() bool {
	return u != nil && validEntry.MatchString(u.Entry) && (path.Clean(u.Entry) == u.Entry || u.Entry == "/ui/")
}

// Served is a running addon of protocol 2 that serves HTTP: the only kind with pages.
func (m *Manager) Served(id string) (Installed, error) {
	return m.running(id, func(a Installed) bool { return !isPayment(a) && a.ModeOf() == ModeHTTP })
}

// InfoV2 is the /v2/info of the served addon a, asked once per installed build. Pages it
// names outside /ui/ are dropped: the panel serves nothing else of an addon.
func (m *Manager) InfoV2(ctx context.Context, id string, a Installed) (InfoV2, error) {
	info, err := describe(ctx, m, m.infos2, id, a, "/v2/info", Protocol2, func(i InfoV2) (string, int) { return i.ID, i.Protocol })
	if err != nil {
		return InfoV2{}, err
	}
	if !info.UI.valid() {
		info.UI = nil
	}
	// A name that is not an event type matches no event: it narrows the feed like any other.
	info.Events = info.Events[:min(len(info.Events), maxInfoEvents)]
	return info, nil
}

// noInfoFor is how long a /v2/info that failed is not asked again: the addon is pulled from
// meanwhile, and a hung one costs the panel one wait per this, not one per pull.
const noInfoFor = 30 * time.Second

// noInfo is a failed /v2/info of one build of an addon.
type noInfo struct {
	build string
	until time.Time
}

// Pushes is the served addon id, with its description, when it asks for its events to be
// pushed to it (push in its /v2/info); ok is false for one that is not running, does not
// serve HTTP, does not ask, or does not answer now. It pulls then.
func (m *Manager) Pushes(ctx context.Context, id string) (a Installed, info InfoV2, ok bool) {
	a, err := m.Served(id)
	if err != nil {
		return Installed{}, InfoV2{}, false
	}
	build := a.build()
	m.mu.Lock()
	f, failed := m.noInfo[id]
	m.mu.Unlock()
	if failed && f.build == build && m.now().Before(f.until) {
		return Installed{}, InfoV2{}, false
	}
	info, err = m.InfoV2(ctx, id, a)
	if err != nil {
		m.mu.Lock()
		m.noInfo[id] = noInfo{build: build, until: m.now().Add(noInfoFor)}
		m.mu.Unlock()
		return Installed{}, InfoV2{}, false
	}
	return a, info, info.Push
}

// PushEvents sends one batch (body is {"events": [...]}) to the served addon a: POST
// /v2/events. Only a 2xx takes it; anything else, a refusal, a redirect or an addon that does
// not answer, is an error and the caller sends the batch again.
func (m *Manager) PushEvents(ctx context.Context, a Installed, body []byte) error {
	return NewClient("http://"+a.Listen, a.Token, m.peer).call(ctx, http.MethodPost, "/v2/events", json.RawMessage(body), nil)
}

// PageFile is one file of an addon's pages as the panel passes it on.
type PageFile struct {
	Status int
	Header http.Header // only the headers safe to pass on
	Body   []byte
}

// pageRequestHeaders are the browser's headers an addon gets with a file: none that carry
// the admin's credentials.
var pageRequestHeaders = []string{"Accept", "Accept-Language", "If-None-Match", "If-Modified-Since"}

// pageResponseHeaders are the addon's headers the browser gets: nothing that sets cookies,
// redirects or changes the panel's own security headers.
var pageResponseHeaders = []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified"}

// Page fetches GET /ui/<name> of the served addon a with its token. hdr is the browser's
// request: only pageRequestHeaders of it go on.
func (m *Manager) Page(ctx context.Context, a Installed, name, rawQuery string, hdr http.Header) (PageFile, error) {
	u := url.URL{Scheme: "http", Host: a.Listen, Path: "/ui/" + name, RawQuery: rawQuery}
	fwd := http.Header{}
	for _, k := range pageRequestHeaders {
		if v := hdr.Get(k); v != "" {
			fwd.Set(k, v)
		}
	}
	resp, body, err := doBearer(ctx, m.peer, a.Token, http.MethodGet, u.String(), nil, fwd, MaxPageFile)
	if err != nil {
		return PageFile{}, err
	}
	if resp.StatusCode/100 == 3 && resp.StatusCode != http.StatusNotModified {
		return PageFile{}, ErrBadAnswer
	}
	f := PageFile{Status: resp.StatusCode, Header: http.Header{}, Body: body}
	for _, k := range pageResponseHeaders {
		if v := resp.Header.Get(k); v != "" {
			f.Header.Set(k, v)
		}
	}
	return f, nil
}

// RPC is a call of an addon's pages, as the panel forwards it to POST /v2/ui/rpc.
type RPC struct {
	Method string          `json:"method"` // GET | POST | PUT | DELETE
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
	Admin  RPCAdmin        `json:"admin"`
}

// RPCAdmin is what the addon learns of the admin who called.
type RPCAdmin struct {
	Lang string `json:"lang"`
}

// CallUI forwards a call of the pages to the served addon a and returns its status and its
// JSON as they are, errors included: the pages read them. Only a broken answer is an error
// here.
func (m *Manager) CallUI(ctx context.Context, a Installed, call RPC) (int, json.RawMessage, error) {
	data, err := json.Marshal(call)
	if err != nil {
		return 0, nil, err
	}
	resp, body, err := doBearer(ctx, m.peer, a.Token, http.MethodPost, "http://"+a.Listen+"/v2/ui/rpc", data, nil, MaxRPCAnswer)
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode/100 == 3 || resp.StatusCode < 200 {
		return 0, nil, ErrBadAnswer
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return resp.StatusCode, json.RawMessage("null"), nil
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return 0, nil, ErrBadAnswer
	}
	return resp.StatusCode, json.RawMessage(body), nil
}
