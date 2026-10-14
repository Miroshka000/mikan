// Package pages serves the admin pages of an addon of protocol 2 (docs/pipeline/addons.md,
// section 9): the files under the addon's /ui/, proxied at
//
//	/<admin path>/addons-ui/<id>/<sig>/<file>
//
// into a sandboxed frame of the admin panel. The frame has an opaque origin and so no
// cookie: <sig> stands for the admin's session instead. It is an HMAC over the addon, the
// admin and their session, valid ten minutes, and the session must still be alive. The
// frame holds no credential: its calls go through the admin panel (api: ui/rpc).
package pages

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
)

// Prefix is where the pages live below the admin path.
const Prefix = "/addons-ui/"

// TTL is how long a signed address lets the frame load files. Calls of the pages need only
// the admin's session.
const TTL = 10 * time.Minute

// Permission is the grant an addon needs for pages in the admin panel.
const Permission = "ui"

var (
	ErrForbidden = errors.New("addon_ui_forbidden") // the admin did not approve ui
	ErrNoPages   = errors.New("addon_no_pages")     // not installed, not running, no pages
)

// Store is what the pages read: the approvals and the admin's sessions.
type Store interface {
	addons.GrantReader
	ListAdminSessions(ctx context.Context, adminID int64) ([]db.Session, error)
}

type Deps struct {
	Addons *addons.Manager
	Store  Store
	Log    *slog.Logger
	Now    func() time.Time
}

type Pages struct {
	d Deps
	// key signs the addresses. It lives in this process alone: never stored, never sent;
	// a restart only ends the addresses given out, which last ten minutes anyway.
	key []byte
}

func New(d Deps) *Pages {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("pages: no randomness: " + err.Error())
	}
	return &Pages{d: d, key: key}
}

// Allowed: the addon runs, serves HTTP, speaks protocol 2 and the admin approved its pages.
// A payment adapter or a client of the panel is ErrNoPages. It returns the addon as the
// host runs it, for the calls that follow.
func (p *Pages) Allowed(ctx context.Context, id string) (addons.Installed, error) {
	if p == nil || p.d.Addons == nil {
		return addons.Installed{}, ErrNoPages
	}
	a, err := p.d.Addons.Served(id)
	if errors.Is(err, addons.ErrNotInstalled) {
		return a, ErrNoPages
	}
	if err != nil {
		return a, err
	}
	perms, _, err := addons.LoadGrant(ctx, p.d.Store, id)
	if errors.Is(err, addons.ErrNoGrant) || err == nil && !addons.HasPerm(perms, Permission) {
		return a, ErrForbidden
	}
	return a, err
}

// Open checks the addon as Allowed does and asks it where its pages start.
func (p *Pages) Open(ctx context.Context, id string) (addons.UI, error) {
	a, err := p.Allowed(ctx, id)
	if err != nil {
		return addons.UI{}, err
	}
	info, err := p.d.Addons.InfoV2(ctx, id, a)
	if err != nil {
		return addons.UI{}, err
	}
	if info.UI == nil {
		return addons.UI{}, ErrNoPages
	}
	return *info.UI, nil
}

// URL is the signed address of the addon's entry for the admin's session, relative to the
// admin path ("addons-ui/<id>/<sig>/index.html"), and when it stops working.
func (p *Pages) URL(id string, ui addons.UI, s db.Session) (string, time.Time) {
	exp := p.d.Now().Add(TTL).Truncate(time.Second)
	sig := p.sign(id, s.AdminID, s.IDHash, exp.Unix())
	return strings.TrimPrefix(Prefix, "/") + id + "/" + sig + "/" + strings.TrimPrefix(ui.Entry, "/ui/"), exp
}

// sig is base64url(admin id | expiry | mac), 48 bytes. The session's id hash is in the mac
// but not in the address: the address names the admin, the server finds the session.
const sigLen = 8 + 8 + sha256.Size

func (p *Pages) mac(id string, admin int64, session string, exp int64) []byte {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte("mikan-addons-ui\x00" + id + "\x00" + session + "\x00"))
	_ = binary.Write(m, binary.BigEndian, admin)
	_ = binary.Write(m, binary.BigEndian, exp)
	return m.Sum(nil)
}

func (p *Pages) sign(id string, admin int64, session string, exp int64) string {
	b := make([]byte, 16, sigLen)
	binary.BigEndian.PutUint64(b[:8], uint64(admin))
	binary.BigEndian.PutUint64(b[8:16], uint64(exp))
	return base64.RawURLEncoding.EncodeToString(append(b, p.mac(id, admin, session, exp)...))
}

// Verify: sig was made for this addon and a session of the admin that is still alive, and
// it has not expired.
func (p *Pages) Verify(ctx context.Context, id, sig string) bool {
	b, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(b) != sigLen {
		return false
	}
	admin, exp := int64(binary.BigEndian.Uint64(b[:8])), int64(binary.BigEndian.Uint64(b[8:16]))
	now := p.d.Now()
	if now.Unix() >= exp || exp > now.Add(TTL+time.Minute).Unix() {
		return false
	}
	sessions, err := p.d.Store.ListAdminSessions(ctx, admin)
	if err != nil {
		p.d.Log.Warn("addon pages: sessions", "err", err)
		return false
	}
	ok := false
	for _, s := range sessions {
		// Alive as auth.Sessions.Lookup sees it: a logout or a revoked session ends the pages.
		if now.Unix() >= s.ExpiresAt || now.Sub(time.Unix(s.LastSeenAt, 0)) > auth.IdleTTL {
			continue
		}
		if hmac.Equal(b[16:], p.mac(id, admin, s.IDHash, exp)) {
			ok = true
		}
	}
	return ok
}

// ServeHTTP serves a file of the pages; the path is below the admin path
// ("/addons-ui/<id>/<sig>/<file>"). Anything wrong is the panel's bare 404: an address
// that expired or never was looks like any unknown path.
func (p *Pages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, Prefix)
	id, rest, ok2 := strings.Cut(rest, "/")
	sig, name, ok3 := strings.Cut(rest, "/")
	if !ok || !ok2 || !ok3 || !addons.ValidID(id) {
		server.NotFound(w)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	if !p.Verify(ctx, id, sig) {
		server.NotFound(w)
		return
	}
	a, err := p.Allowed(ctx, id)
	if err != nil {
		server.NotFound(w)
		return
	}
	h := w.Header()
	server.FrameHeaders(h)
	f, err := p.d.Addons.Page(ctx, a, name, r.URL.RawQuery, r.Header)
	if err != nil {
		p.d.Log.Warn("addon pages: file", "addon", id, "err", err)
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("502 the addon does not answer\n"))
		return
	}
	for k, v := range f.Header {
		h[k] = v
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-store")
	}
	if f.Status == http.StatusNotModified {
		w.WriteHeader(f.Status)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(f.Body)))
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}
