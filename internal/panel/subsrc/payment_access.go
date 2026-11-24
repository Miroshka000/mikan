package subsrc

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

func (s *subscription) PaymentProfile(ctx context.Context) (proto.Slot, []model.Inbound, error) {
	c := s.cfg.PaymentAccess
	in := s.cfg.PaymentInbound
	if !c.Matches(in) || !s.u.SlotID.Valid {
		return proto.Slot{}, nil, nil
	}
	if _, ok := c.Window(s.u, s.h.now()); !ok {
		return proto.Slot{}, nil, nil
	}
	used, err := s.h.st.Q.PaymentAccessTraffic(ctx, db.PaymentAccessTrafficParams{
		UserID: s.u.ID, ExpiredAt: s.u.ExpiresAt.Int64,
	})
	if err != nil || c.Remaining(used) == 0 {
		return proto.Slot{}, nil, err
	}
	hosts, err := s.h.hosts(ctx, []db.Inbound{in})
	if err != nil {
		return proto.Slot{}, nil, err
	}
	group, err := domain.UserGroup(ctx, s.h.st.Q, s.u)
	if err != nil {
		return proto.Slot{}, nil, err
	}
	hidden, err := s.h.hidden(ctx, s.u, hosts, s.grants, group)
	if err != nil || len(hosts[in.ID]) > 0 && len(hidden) == len(hosts[in.ID]) {
		return proto.Slot{}, nil, err
	}
	label := "Renew subscription"
	if s.cfg.Lang == "ru" {
		label = "Продлить подписку"
	}
	out := Inbound(in, hosts[in.ID])
	out.Override = s.cfg.addressOf(group, in.ID)
	out.Override.Name = "" // The payment profile keeps its renewal label.
	out.Name, out.Named = label, true
	for i := range out.Hosts {
		out.Hosts[i].Name = label
		out.Hosts[i].Hidden = hidden[out.Hosts[i].ID] != ""
	}
	if s.dry {
		return proto.Slot{}, []model.Inbound{out}, nil
	}
	base, err := s.h.st.Q.GetSlot(ctx, s.u.SlotID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return proto.Slot{}, nil, nil
	}
	if err != nil || base.Secret == "" {
		return proto.Slot{}, nil, err
	}
	return domain.PaymentSlot(s.u, base.Secret), []model.Inbound{out}, nil
}
