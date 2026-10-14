package pages

import (
	"context"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/store/db"
)

type fakeStore struct {
	sessions map[int64][]db.Session
}

func (f *fakeStore) GetAddonGrant(context.Context, string) (db.AddonGrant, error) {
	return db.AddonGrant{}, sql.ErrNoRows
}

func (f *fakeStore) ListAdminSessions(_ context.Context, admin int64) ([]db.Session, error) {
	return f.sessions[admin], nil
}

// The signed address works for its addon, its admin and a live session of theirs, for ten
// minutes; anything changed in it, an ended session or another admin's does not.
func TestSignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	alive := func(hash string, admin int64) db.Session {
		return db.Session{IDHash: hash, AdminID: admin, ExpiresAt: now.Add(auth.MaxTTL).Unix(), LastSeenAt: now.Unix()}
	}
	st := &fakeStore{sessions: map[int64][]db.Session{1: {alive("other", 1), alive("mine", 1)}, 2: {alive("theirs", 2)}}}
	p := New(Deps{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	ctx := context.Background()

	url, exp := p.URL("pager", addons.UI{Entry: "/ui/app/index.html"}, st.sessions[1][1])
	parts := strings.Split(url, "/")
	if len(parts) != 5 || parts[0] != "addons-ui" || parts[1] != "pager" || parts[3] != "app" || parts[4] != "index.html" {
		t.Fatalf("url: %s", url)
	}
	if !exp.Equal(now.Add(TTL)) {
		t.Fatalf("expires %v, want %v", exp, now.Add(TTL))
	}
	sig := parts[2]
	if !p.Verify(ctx, "pager", sig) {
		t.Fatal("a fresh signature is refused")
	}
	if p.Verify(ctx, "other", sig) {
		t.Fatal("the signature opens another addon")
	}

	raw, _ := base64.RawURLEncoding.DecodeString(sig)
	tamper := func(i int, b byte) string {
		c := append([]byte{}, raw...)
		c[i] = b
		return base64.RawURLEncoding.EncodeToString(c)
	}
	for name, s := range map[string]string{
		"another admin":  tamper(7, 2),
		"a later expiry": tamper(15, raw[15]+1),
		"another mac":    tamper(20, raw[20]^1),
		"short":          sig[:len(sig)-4],
		"not base64":     sig[:len(sig)-1] + "*",
		"empty":          "",
	} {
		if p.Verify(ctx, "pager", s) {
			t.Errorf("%s: accepted", name)
		}
	}
	// Another key (a restarted panel) knows nothing of it.
	if New(p.d).Verify(ctx, "pager", sig) {
		t.Fatal("another key accepts the signature")
	}

	// The session ends: the address ends with it.
	st.sessions[1] = st.sessions[1][:1]
	if p.Verify(ctx, "pager", sig) {
		t.Fatal("a logged out session still opens the pages")
	}
	st.sessions[1] = append(st.sessions[1], alive("mine", 1))
	st.sessions[1][1].LastSeenAt = now.Add(-auth.IdleTTL - time.Minute).Unix()
	if p.Verify(ctx, "pager", sig) {
		t.Fatal("an idle session still opens the pages")
	}
	st.sessions[1][1].LastSeenAt = now.Unix()
	if !p.Verify(ctx, "pager", sig) {
		t.Fatal("the session is back, the signature is not")
	}

	// Ten minutes later it is over.
	now = now.Add(TTL)
	if p.Verify(ctx, "pager", sig) {
		t.Fatal("an expired signature is accepted")
	}
}
