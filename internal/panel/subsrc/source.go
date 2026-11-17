package subsrc

import (
	"context"
	"errors"
	"slices"
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
	sub, err := h.subscriptionOf(ctx, u, cfg)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// subscriptionOf is u's subscription served by cfg as the clients of u's group are served
// (Config.forGroup): the group's appearance over the panel's settings.
func (h *Handler) subscriptionOf(ctx context.Context, u db.User, cfg Config) (*subscription, error) {
	group, err := domain.UserGroup(ctx, h.st.Q, u)
	if err != nil {
		return nil, err
	}
	return &subscription{h: h, u: u, cfg: cfg.forGroup(group)}, nil
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
	// dry is a preview's subscription (PreviewUser): it takes no device's place, places the
	// client in no balance group and records neither the fetch nor the request. admitted is
	// what its device would get (domain.Admit*), once Keys is asked.
	dry      bool
	admitted string
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
	if s.dry {
		return s.dryKeys(ctx, dev)
	}
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
	r, err := s.h.reach(ctx, s.u, s.cfg, s.grants, s.dry)
	if err != nil {
		return nil, err
	}
	hosts, err := s.h.hosts(ctx, r.inbounds)
	if err != nil {
		return nil, err
	}
	hidden, err := s.h.hidden(ctx, s.u, hosts, r.grants, r.group)
	if err != nil {
		return nil, err
	}
	out := inbounds(r.inbounds, hosts)
	for i, in := range r.inbounds {
		out[i].Override = s.cfg.addressOf(r.group, in.ID)
		for j := range out[i].Hosts {
			host := &out[i].Hosts[j]
			why := hidden[host.ID]
			host.Hidden = why != ""
			if why != "" {
				r.left = append(r.left, Left{Inbound: in, Host: host.ID, Address: host.Address, HostName: host.Name, Reason: why})
			}
		}
	}
	s.reach = r
	return out, nil
}

// hidden are the hosts the user is not shown, by id, with why (Left*): by the state of the
// subscription, whose packages have grants left, and the trial (domain.HostShows), and by
// group, the one the user is in (domain.HostShowsGroup). Whether the user is on the trial is
// read only when a host asks it.
func (h *Handler) hidden(ctx context.Context, u db.User, hosts map[int64][]db.InboundHost, grants domain.GrantsLeft, group int64) (map[int64]string, error) {
	state := domain.State(u, grants.Main(u.ID), h.now())
	var trial *bool
	out := map[int64]string{}
	for _, list := range hosts {
		for _, host := range list {
			if host.Trial != "" && trial == nil {
				t, err := domain.OnTrial(ctx, h.st.Q, u)
				if err != nil {
					return nil, err
				}
				trial = &t
			}
			switch {
			case !domain.HostShows(host.States, "", state, false):
				out[host.ID] = LeftHostState
			case !domain.HostShows(nil, host.Trial, state, trial != nil && *trial):
				out[host.ID] = LeftHostTrial
			case !domain.HostShowsGroup(host.GroupMode, host.Groups, group):
				out[host.ID] = LeftHostGroup
			}
		}
	}
	return out, nil
}

// Fetched: the block detector trusts a device only once it took a profile with an
// inbound's current port and target (see autotune.Detect). The handler tells it once the
// profile went out, with a context the client's hanging up does not cancel. A preview's
// fetch is no fetch of a device's.
func (s *subscription) Fetched(ctx context.Context, ip string) {
	if s.dry {
		return
	}
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
	group    int64        // the group the user is in (domain.GroupOf); 0: none
	byGroup  bool         // the user stands on its group's own servers
	inbounds []db.Inbound // the user's, in display order
	// left are the inbounds of all the user does not get, with why, and, once Inbounds read
	// them, the hosts of its inbounds it is not shown: what a preview tells.
	left []Left
}

// reach lists what u may use. grants is what is left of u's grants when the caller read it
// already; nil reads it. dry places u in no balance group: a preview's (Balancer.Peek).
func (h *Handler) reach(ctx context.Context, u db.User, cfg Config, grants domain.GrantsLeft, dry bool) (reach, error) {
	all, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		return reach{}, err
	}
	stand, err := domain.UserServers(ctx, h.st.Q, u)
	if err != nil {
		return reach{}, err
	}
	access := domain.AccessOf(u, stand.Servers)
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
	group := stand.Group
	out := reach{all: all, access: access, grants: grants, group: group, byGroup: stand.ByGroup}
	// serves: the client may use in now, and its group, if any, still serves the client.
	serves := func(in db.Inbound) bool { return access.Usable(in) && !(in.PoolID.Valid && spent[in.PoolID.Int64]) }
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
		if on[in.NodeID] && serves(in) {
			nodes[in.NodeID] = true
		}
	}
	// One node of each balance group: the client's own. A balancer that fails shows the
	// group whole rather than leave the client without servers.
	var dark map[int64]bool
	if h.balance != nil && u.ID != 0 {
		// A node where every host is hidden from the client (Host.States, Host.Trial, its
		// groups) gives it nothing: no candidate either, or the group could place the client
		// on it and leave it without the group's servers. A client placed on such a node is
		// placed anew, and stays there once the node shows it something again.
		if dark, err = h.dropHidden(ctx, u, all, serves, nodes, grants, group); err != nil {
			return reach{}, err
		}
		place := h.balance.Nodes
		if dry {
			place = h.balance.Peek
		}
		if picked, err := place(ctx, u.ID, nodes); err != nil {
			h.warn("balance", "subscriptions: load balancing", "err", err)
		} else {
			nodes = picked
		}
	}
	for _, in := range all {
		if !nodes[in.NodeID] || !serves(in) {
			out.left = append(out.left, Left{Inbound: in, Reason: leftWhy(in, out, on, spent, dark)})
			continue
		}
		out.inbounds = append(out.inbounds, in)
	}
	return out, nil
}

// dropHidden takes out of nodes those where u, of group, is shown no host of an inbound it
// may use (serves), and says which it took; an inbound without hosts is reached at its
// template's.
func (h *Handler) dropHidden(ctx context.Context, u db.User, all []db.Inbound, serves func(db.Inbound) bool, nodes map[int64]bool, grants domain.GrantsLeft, group int64) (map[int64]bool, error) {
	var ins []db.Inbound
	for _, in := range all {
		if nodes[in.NodeID] && serves(in) {
			ins = append(ins, in)
		}
	}
	hosts, err := h.hosts(ctx, ins)
	if err != nil {
		return nil, err
	}
	hidden, err := h.hidden(ctx, u, hosts, grants, group)
	if err != nil {
		return nil, err
	}
	if len(hidden) == 0 {
		return nil, nil
	}
	shows := map[int64]bool{}
	for _, in := range ins {
		list := hosts[in.ID]
		if len(list) == 0 || slices.ContainsFunc(list, func(x db.InboundHost) bool { return hidden[x.ID] == "" }) {
			shows[in.NodeID] = true
		}
	}
	dropped := map[int64]bool{}
	for id := range nodes {
		if !shows[id] {
			delete(nodes, id)
			dropped[id] = true
		}
	}
	return dropped, nil
}

// Inbound is in as the subscription module sees it, reached at hosts.
func Inbound(in db.Inbound, hosts []db.InboundHost) submodel.Inbound {
	out := submodel.Inbound{ID: in.ID, Node: in.NodeID, Pool: in.PoolID.Int64, Label: in.Name, Name: domain.ProxyName(in),
		Named: in.DisplayName != "", Port: domain.FirstPort(in.Port), PortSpec: in.Port, Config: in.Config, NoAuto: in.NoAuto != 0}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, submodel.Host{ID: h.ID, Order: h.Sort, Address: h.Address, Port: int(h.Port), SNI: h.Sni, Host: h.Host, Fingerprint: h.Fingerprint,
			ALPN: h.Alpn, IPVersion: h.IpVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, CDN: h.Cdn, Name: h.Name, Addresses: h.Addresses, SNIs: h.Snis})
	}
	return out
}

// inbounds are ins as the subscription module sees them; hosts are theirs by inbound id.
func inbounds(ins []db.Inbound, hosts map[int64][]db.InboundHost) []submodel.Inbound {
	out := make([]submodel.Inbound, 0, len(ins))
	for _, in := range ins {
		out = append(out, Inbound(in, hosts[in.ID]))
	}
	return out
}

// hosts are the hosts of ins by inbound id, each inbound's in their order.
func (h *Handler) hosts(ctx context.Context, ins []db.Inbound) (map[int64][]db.InboundHost, error) {
	ids := make([]int64, len(ins))
	for i, in := range ins {
		ids[i] = in.ID
	}
	rows, err := h.st.Q.ListHostsOfInbounds(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]db.InboundHost{}
	for _, r := range rows {
		out[r.InboundID] = append(out[r.InboundID], r)
	}
	return out, nil
}
