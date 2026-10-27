package subsrc

import (
	"context"
	"errors"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// Subscription is the subscription of token over the panel's database (serve.Source).
func (h *Handler) Subscription(ctx context.Context, token string) (serve.Subscription, error) {
	u, err := h.st.Q.GetUserBySubToken(ctx, token)
	if err != nil {
		return nil, serve.ErrNotFound
	}
	cfg, err := h.cfg(ctx)
	if err != nil {
		return nil, err
	}
	return &subscription{h: h, u: u, cfg: cfg}, nil
}

// subscription is one user's subscription as one request reads it (serve.Subscription).
type subscription struct {
	h   *Handler
	u   db.User
	cfg Config
	// grants is what is left of the user's traffic packages, read by User, which a fetch
	// asks for first; reach is what the user may use, read by Inbounds, before Groups.
	grants domain.GrantsLeft
	reach  reach
}

func (s *subscription) Config() serve.Config { return s.cfg.Config }

func (s *subscription) User(ctx context.Context) (serve.User, error) {
	grants, err := domain.UserGrantsLeft(ctx, s.h.st.Q, s.u.ID, s.h.now())
	if err != nil {
		return serve.User{}, err
	}
	s.grants = grants
	return userOf(s.u, grants.Main(s.u.ID), s.cfg, s.h.now()), nil
}

func (s *subscription) Bound(ctx context.Context) (int64, error) {
	return s.h.st.Q.CountBoundDevices(ctx, s.u.ID)
}

func (s *subscription) Bot(ctx context.Context) string {
	if s.h.tg == nil {
		return ""
	}
	return s.h.tg.LinkURL(ctx, s.u.ID)
}

func (s *subscription) Keys(ctx context.Context, dev serve.Device) (proto.Slot, error) {
	slot, err := s.slot(ctx, dev)
	if err != nil {
		return proto.Slot{}, deviceError(err)
	}
	return proto.Slot{Name: slot.Name, UUID: slot.Uuid, Secret: slot.Secret}, nil
}

// slot is whose keys dev gets: its own with binding on, the user's otherwise.
func (s *subscription) slot(ctx context.Context, dev serve.Device) (db.Slot, error) {
	devices, u := s.h.devices, s.u
	if !s.cfg.Binding || devices == nil {
		// Without binding every device shares the user's keys, yet a device the admin banned
		// while binding was on still gets none.
		if devices != nil {
			if banned, err := devices.Banned(ctx, u.ID, dev.HWID); err != nil {
				return db.Slot{}, err
			} else if banned {
				return db.Slot{}, domain.ErrDeviceBanned
			}
		}
		if !u.SlotID.Valid {
			return db.Slot{}, errors.New("user has no slot")
		}
		return s.h.st.Q.GetSlot(ctx, u.SlotID.Int64)
	}
	// A response rule may waive the device id for an app that cannot send one.
	return devices.Bind(ctx, u, domain.DeviceInfo{HWID: dev.HWID, OS: dev.OS, OSVersion: dev.OSVersion, Model: dev.Model, App: dev.App, IP: dev.IP}, s.cfg.RequireHWID && !dev.SkipHWID)
}

// deviceError is why a device gets no keys in serve's words; any other error as it is.
func deviceError(err error) error {
	var ue *domain.UnboundError
	switch {
	case errors.Is(err, domain.ErrDeviceLimit):
		return serve.ErrDeviceLimit
	case errors.Is(err, domain.ErrNoHWID):
		return serve.ErrNoHWID
	case errors.Is(err, domain.ErrDeviceBanned):
		return serve.ErrDeviceBanned
	case errors.As(err, &ue):
		return &serve.UnboundError{Until: ue.Until, Forever: domain.Forever(ue.Until)}
	case errors.Is(err, domain.ErrDeviceUnbound):
		// Unbound without a moment to come back: the notice says the shortest wait.
		return &serve.UnboundError{}
	}
	return err
}

func (s *subscription) Inbounds(ctx context.Context) ([]submodel.Inbound, error) {
	r, err := s.h.reach(ctx, s.u, s.cfg, s.grants)
	if err != nil {
		return nil, err
	}
	s.reach = r
	return inbounds(r.inbounds), nil
}

// Fetched: the block detector trusts a device only once it took a profile with an
// inbound's current port and target (see autotune.Detect). The handler tells it once the
// profile went out, with a context the client's hanging up does not cancel.
func (s *subscription) Fetched(ctx context.Context, ip string) {
	_ = s.h.st.Q.RecordSubFetch(ctx, db.RecordSubFetchParams{UserID: s.u.ID, Ip: ip, FetchedAt: s.h.now().Unix()})
}

// userOf is what the headers and texts of a subscription say of u, whose main traffic's
// packages have grants bytes left.
func userOf(u db.User, grants int64, cfg Config, now time.Time) serve.User {
	out := serve.User{Token: u.SubToken, Name: u.Name, State: serve.State(domain.State(u, grants, now)), UsedUp: u.UsedUp, UsedDown: u.UsedDown,
		Grants: grants, Left: -1, Places: model.BindsDevices(u.DeviceMode)}
	if u.TariffID.Valid {
		out.Tariff = cfg.Tariffs[u.TariffID.Int64]
	}
	if u.TrafficLimit.Valid {
		limit := u.TrafficLimit.Int64
		out.Limit, out.Left = &limit, model.TrafficLeft(u.TrafficLimit, u.UsedUp+u.UsedDown, grants)
	}
	if u.ExpiresAt.Valid {
		out.Expires = store.Time(u.ExpiresAt.Int64)
	}
	if u.DeviceLimit.Valid {
		n := u.DeviceLimit.Int64
		out.Devices = &n
	}
	return out
}

// reach is what of the panel's inbounds a user may use now.
type reach struct {
	all      []db.Inbound // every inbound of the panel
	access   domain.Access
	grants   domain.GrantsLeft
	inbounds []db.Inbound // the user's, in display order
}

// reach lists what u may use. grants is what is left of u's grants when the caller read it
// already; nil reads it.
func (h *Handler) reach(ctx context.Context, u db.User, cfg Config, grants domain.GrantsLeft) (reach, error) {
	all, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		return reach{}, err
	}
	servers, err := domain.TariffServers(ctx, h.st.Q, u.TariffID)
	if err != nil {
		return reach{}, err
	}
	access := domain.AccessOf(u, servers)
	if grants == nil {
		if grants, err = domain.UserGrantsLeft(ctx, h.st.Q, u.ID, h.now()); err != nil {
			return reach{}, err
		}
	}
	// A traffic pool that ran out leaves the subscription; the node already turns it away.
	spent, err := domain.ExhaustedPools(ctx, h.st.Q, u.ID, grants)
	if err != nil {
		return reach{}, err
	}
	out := reach{all: all, access: access, grants: grants}
	on := map[int64]bool{}
	for _, n := range cfg.Nodes {
		on[n.ID] = true
	}
	// The nodes the client may use: a balance group places the client among these only, so
	// a node the tariff leaves out is never the one the client gets.
	nodes := map[int64]bool{}
	for _, in := range all {
		// A node whose only protocols are in a group closed or spent for the client serves it
		// nothing: it is no candidate of a balance group either.
		if on[in.NodeID] && access.Usable(in) && !(in.PoolID.Valid && spent[in.PoolID.Int64]) {
			nodes[in.NodeID] = true
		}
	}
	// One node of each balance group: the client's own. A balancer that fails shows the
	// group whole rather than leave the client without servers.
	if h.balance != nil && u.ID != 0 {
		if picked, err := h.balance.Nodes(ctx, u.ID, nodes); err != nil {
			h.warn("balance", "subscriptions: load balancing", "err", err)
		} else {
			nodes = picked
		}
	}
	for _, in := range all {
		if in.Enabled == 0 || !nodes[in.NodeID] || !access.Allows(in) || in.PoolID.Valid && spent[in.PoolID.Int64] {
			continue
		}
		out.inbounds = append(out.inbounds, in)
	}
	return out, nil
}

// Inbound is in as the subscription module sees it.
func Inbound(in db.Inbound) submodel.Inbound {
	return submodel.Inbound{ID: in.ID, Node: in.NodeID, Pool: in.PoolID.Int64, Label: in.Name, Name: domain.ProxyName(in),
		Named: in.DisplayName != "", Port: domain.FirstPort(in.Port), PortSpec: in.Port, Config: in.Config, NoAuto: in.NoAuto != 0}
}

func inbounds(ins []db.Inbound) []submodel.Inbound {
	out := make([]submodel.Inbound, 0, len(ins))
	for _, in := range ins {
		out = append(out, Inbound(in))
	}
	return out
}
