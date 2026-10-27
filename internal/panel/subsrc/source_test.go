package subsrc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// Why a device gets no keys reaches serve in its own words, wrapped or not; an unbound
// device keeps its moment to come back, and anything else goes through as it is.
func TestDeviceError(t *testing.T) {
	until := time.Unix(1_800_000_000, 0).UTC()
	forever := time.Unix(253402300799, 0).UTC()
	for _, c := range []struct {
		name string
		in   error
		want error
	}{
		{"limit", fmt.Errorf("bind: %w", domain.ErrDeviceLimit), serve.ErrDeviceLimit},
		{"no hwid", domain.ErrNoHWID, serve.ErrNoHWID},
		{"banned", domain.ErrDeviceBanned, serve.ErrDeviceBanned},
		{"unbound until", fmt.Errorf("bind: %w", &domain.UnboundError{Until: until}), &serve.UnboundError{Until: until}},
		{"unbound forever", &domain.UnboundError{Until: forever}, &serve.UnboundError{Until: forever, Forever: true}},
		{"unbound, no moment", domain.ErrDeviceUnbound, &serve.UnboundError{}},
	} {
		if got := deviceError(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %#v, want %#v", c.name, got, c.want)
		}
	}
	other := errors.New("connection reset")
	if got := deviceError(other); got != other {
		t.Errorf("another error: %v", got)
	}
}

// What the headers and texts say of a user: unlimited is Left -1 whatever the grants, a
// limit counts the grants in, the tariff is named by the panel's settings.
func TestUserOf(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := Config{Tariffs: map[int64]string{7: "Pro"}}
	base := db.User{SubToken: "tok", Name: "alice", Status: "active", UsedUp: 30, UsedDown: 70, DeviceMode: domain.DeviceModeOnline}

	got := userOf(base, 500, cfg, now)
	want := serve.User{Token: "tok", Name: "alice", State: serve.StateActive, UsedUp: 30, UsedDown: 70, Grants: 500, Left: -1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unlimited:\n%+v\nwant %+v", got, want)
	}

	u := base
	u.TrafficLimit = sql.NullInt64{Int64: 1000, Valid: true}
	u.TariffID = sql.NullInt64{Int64: 7, Valid: true}
	u.ExpiresAt = sql.NullInt64{Int64: now.Add(30 * 24 * time.Hour).Unix(), Valid: true}
	u.DeviceLimit = sql.NullInt64{Int64: 3, Valid: true}
	u.DeviceMode = "bound"
	limit, devices := int64(1000), int64(3)
	got = userOf(u, 200, cfg, now)
	want = serve.User{Token: "tok", Name: "alice", Tariff: "Pro", State: serve.StateActive, UsedUp: 30, UsedDown: 70, Limit: &limit, Grants: 200,
		Left: 900 + 200, Expires: time.Unix(u.ExpiresAt.Int64, 0).UTC(), Devices: &devices, Places: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("limited:\n%+v\nwant %+v", got, want)
	}

	// The limit used up: the grants keep the user going, and without them the user is limited.
	u.UsedUp, u.UsedDown = 600, 400
	if got := userOf(u, 200, cfg, now); got.Left != 200 || got.State != serve.StateActive {
		t.Errorf("past the limit with grants: left %d, %s", got.Left, got.State)
	}
	if got := userOf(u, 0, cfg, now); got.Left != 0 || got.State != serve.StateLimited {
		t.Errorf("past the limit without grants: left %d, %s", got.Left, got.State)
	}
	// A tariff the settings do not name is no name.
	u.TariffID.Int64 = 8
	if got := userOf(u, 0, cfg, now); got.Tariff != "" {
		t.Errorf("unknown tariff: %q", got.Tariff)
	}
}

// sourceEnv is a panel with nodes and inbounds for reach.
type sourceEnv struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	h   *Handler
	cfg Config // the nodes the subscriptions know of
}

func newSourceEnv(t *testing.T) *sourceEnv {
	st := openStore(t)
	now := time.Unix(1_800_000_000, 0)
	h := NewHandler(st, func(context.Context) (Config, error) { return Config{}, nil }, nil, func() time.Time { return now }, nil, false)
	return &sourceEnv{t: t, ctx: context.Background(), st: st, h: h}
}

// node makes a node; known puts it among the nodes the subscriptions know of.
func (e *sourceEnv) node(name string, known bool) db.Node {
	e.t.Helper()
	n, err := e.st.Q.CreateNode(e.ctx, db.CreateNodeParams{Name: name, Address: name + ":1", PublicHost: "203.0.113.1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		e.t.Fatal(err)
	}
	if known {
		e.cfg.Nodes = append(e.cfg.Nodes, submodel.Node{ID: n.ID, Name: name})
	}
	return n
}

func (e *sourceEnv) inbound(n db.Node, name string, enabled bool) db.Inbound {
	e.t.Helper()
	in, err := e.st.Q.CreateInbound(e.ctx, db.CreateInboundParams{NodeID: n.ID, Name: name, Preset: "vless_reality_xhttp", Port: "443", Config: "{}", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		e.t.Fatal(err)
	}
	if !enabled {
		if in, err = e.st.Q.UpdateInbound(e.ctx, db.UpdateInboundParams{Port: in.Port, Enabled: 0, Config: in.Config, UpdatedAt: 1, ID: in.ID}); err != nil {
			e.t.Fatal(err)
		}
	}
	return in
}

// pooled puts in into a group of its own, which u has in mode with limit.
func (e *sourceEnv) pooled(in db.Inbound, u db.User, mode string, limit sql.NullInt64) {
	e.t.Helper()
	p, err := e.st.Q.CreateTrafficPool(e.ctx, db.CreateTrafficPoolParams{Name: in.Name, CreatedAt: 1})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.Q.SetInboundPool(e.ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: p.ID, Valid: true}, ID: in.ID}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.Q.SetUserPool(e.ctx, db.SetUserPoolParams{UserID: u.ID, PoolID: p.ID, Mode: mode, TrafficLimit: limit}); err != nil {
		e.t.Fatal(err)
	}
}

// names are the names of what u reaches, in order.
func (e *sourceEnv) names(u db.User) []string {
	e.t.Helper()
	r, err := e.h.reach(e.ctx, u, e.cfg, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	out := []string{}
	for _, in := range r.inbounds {
		out = append(out, in.Name)
	}
	return out
}

// A subscription leaves out the inbounds of a node it does not know of, the ones turned
// off, the ones of a group closed or spent for the user, and those the user's card does
// not list.
func TestReachLeavesOutWhatDoesNotServe(t *testing.T) {
	e := newSourceEnv(t)
	u := newUser(t, e.st, 1)
	a, b, gone := e.node("a", true), e.node("b", true), e.node("gone", false)
	e.inbound(a, "a1", true)
	e.inbound(a, "a-off", false)
	e.inbound(gone, "gone1", true)
	e.pooled(e.inbound(b, "b-closed", true), u, domain.PoolClosed, sql.NullInt64{})
	e.pooled(e.inbound(b, "b-spent", true), u, domain.PoolOwn, sql.NullInt64{Int64: 0, Valid: true})
	b1 := e.inbound(b, "b1", true)
	e.pooled(e.inbound(b, "b-own", true), u, domain.PoolOwn, sql.NullInt64{Int64: 1 << 30, Valid: true})

	if got, want := e.names(u), []string{"a1", "b1", "b-own"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reach: %v, want %v", got, want)
	}
	u.Inbounds = sql.NullString{String: fmt.Sprintf("[%d]", b1.ID), Valid: true}
	if got, want := e.names(u), []string{"b1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the card's protocols: %v, want %v", got, want)
	}
}

// balanceEnv is a balance group of three nodes, the first of which serves nothing (its only
// inbound is off), and a node outside the group.
func balanceEnv(t *testing.T) (e *sourceEnv, u db.User, dark, x, y, solo db.Node) {
	e = newSourceEnv(t)
	u = newUser(t, e.st, 1)
	b := domain.NewBalancer(e.st, e.h.now, slog.New(slog.DiscardHandler))
	// dark has the lowest id and the most room: the balancer would place the client there
	// first, were it among the candidates.
	dark, x, y = e.node("dark", true), e.node("x", true), e.node("y", true)
	solo = e.node("solo", true)
	for id, capacity := range map[int64]int64{dark.ID: 100, x.ID: 1, y.ID: 1} {
		if err := b.SetNode(e.ctx, id, "eu", sql.NullInt64{Int64: capacity, Valid: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.inbound(dark, "dark1", false)
	e.inbound(x, "x1", true)
	e.inbound(y, "y1", true)
	e.inbound(solo, "solo1", true)
	e.h.SetBalancer(b)
	return e, u, dark, x, y, solo
}

// The balancer keeps the client on one node of the group, chosen among the nodes that
// serve the client: never one whose protocols the client cannot use.
func TestReachBalancesAmongTheNodesThatServe(t *testing.T) {
	e, u, dark, x, _, _ := balanceEnv(t)
	if got, want := e.names(u), []string{"x1", "solo1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reach: %v, want %v", got, want)
	}
	held, err := e.st.Q.ListUserAssignments(e.ctx, u.ID)
	if err != nil || len(held) != 1 || held[0].NodeID != x.ID {
		t.Errorf("placed on %+v (dark is %d): %v", held, dark.ID, err)
	}
}

// failingBalancer fails, after noting what it was asked to narrow.
type failingBalancer struct{ asked map[int64]bool }

func (f *failingBalancer) Nodes(_ context.Context, _ int64, on map[int64]bool) (map[int64]bool, error) {
	f.asked = on
	return nil, errors.New("balancer down")
}

// A balancer that fails leaves the group whole rather than the client without servers.
// It is asked about the nodes that serve the client, not every node the panel knows of.
func TestReachKeepsEveryNodeWhenTheBalancerFails(t *testing.T) {
	e, u, _, x, y, solo := balanceEnv(t)
	f := &failingBalancer{}
	e.h.balance = f
	if got, want := e.names(u), []string{"x1", "y1", "solo1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reach: %v, want %v", got, want)
	}
	if want := map[int64]bool{x.ID: true, y.ID: true, solo.ID: true}; !reflect.DeepEqual(f.asked, want) {
		t.Errorf("asked about %v, want %v", f.asked, want)
	}
}

// The admin's preview is of no client (sample): the balancer is not asked, every node shows.
func TestReachDoesNotBalanceThePreview(t *testing.T) {
	e, _, _, _, _, _ := balanceEnv(t)
	f := &failingBalancer{}
	e.h.balance = f
	if got, want := e.names(db.User{}), []string{"x1", "y1", "solo1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("preview: %v, want %v", got, want)
	}
	if f.asked != nil {
		t.Errorf("the balancer was asked about the preview: %v", f.asked)
	}
}

// A panel without a balancer shows every node, SetBalancer(nil) included.
func TestReachWithoutABalancer(t *testing.T) {
	e, u, _, _, _, _ := balanceEnv(t)
	e.h.balance = nil
	e.h.SetBalancer(nil)
	if got, want := e.names(u), []string{"x1", "y1", "solo1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("reach: %v, want %v", got, want)
	}
}
