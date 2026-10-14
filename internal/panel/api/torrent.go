package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/torrent"
)

// The torrent blocker (internal/panel/torrent): the nodes drop BitTorrent they recognise
// and the panel bans the user on every node.

type TorrentUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TorrentView struct {
	Enabled    bool          `json:"enabled" doc:"Ноды распознают BitTorrent (рукопожатие, DHT, uTP, трекеры) и не пропускают его. Зашифрованный торрент не распознаётся"`
	BanMinutes int64         `json:"ban_minutes" doc:"На сколько минут пойманный пользователь теряет доступ ко всем нодам; 0 — только сбросить соединение"`
	Exempt     []TorrentUser `json:"exempt" doc:"Пользователи, которых блокировщик не трогает"`
}

type torrentOutput struct{ Body TorrentView }

type patchTorrentInput struct {
	Body struct {
		Enabled    *bool   `json:"enabled,omitempty"`
		BanMinutes *int64  `json:"ban_minutes,omitempty" minimum:"0" maximum:"43200"`
		Exempt     []int64 `json:"exempt,omitempty" maxItems:"1000" doc:"Весь список id пользователей-исключений"`
	}
}

type TorrentHitView struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	UserName    string     `json:"user_name"`
	NodeID      *int64     `json:"node_id" doc:"null — нода удалена"`
	NodeName    string     `json:"node_name"`
	IP          string     `json:"ip" doc:"Адрес, с которого подключался пользователь"`
	Inbound     string     `json:"inbound"`
	Network     string     `json:"network" enum:"tcp,udp"`
	Kind        string     `json:"kind" enum:"handshake,tracker,dht,utp" doc:"Что распознано"`
	Dest        string     `json:"dest" doc:"Куда шло соединение"`
	Hits        int32      `json:"hits" doc:"Сколько попыток за раз: нода сообщает о пользователе не чаще раза в минуту"`
	At          time.Time  `json:"at"`
	BannedUntil *time.Time `json:"banned_until" doc:"До какого времени бан; null — без бана"`
	LiftedAt    *time.Time `json:"lifted_at" doc:"Когда администратор снял бан"`
}

type torrentHitsInput struct {
	UserID int64 `query:"user_id" doc:"Только этого пользователя"`
	Before int64 `query:"before" doc:"Страница: записи с id меньше этого"`
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type torrentHitsOutput struct{ Body []TorrentHitView }

type liftTorrentOutput struct {
	Body struct {
		Lifted int64 `json:"lifted" doc:"Сколько банов снято"`
	}
}

func (h *handlers) registerTorrent() {
	tags := []string{"torrent"}
	huma.Register(h.api, huma.Operation{OperationID: "get-torrent", Method: http.MethodGet, Path: "/api/v1/torrent", Summary: "Блокировка торрентов", Tags: tags}, h.getTorrent)
	huma.Register(h.api, huma.Operation{OperationID: "update-torrent", Method: http.MethodPatch, Path: "/api/v1/torrent", Summary: "Настроить блокировку торрентов", Tags: tags}, h.updateTorrent)
	huma.Register(h.api, huma.Operation{OperationID: "list-torrent-hits", Method: http.MethodGet, Path: "/api/v1/torrent/hits", Summary: "Пойманные торренты", Description: "Новые сверху; следующая страница — before=id последней записи.", Tags: tags}, h.listTorrentHits)
	huma.Register(h.api, huma.Operation{OperationID: "lift-torrent-ban", Method: http.MethodPost, Path: "/api/v1/users/{id}/torrent-ban/lift", Summary: "Снять торрент-бан пользователя", Tags: []string{"users"}}, h.liftTorrentBan)
}

func (h *handlers) torrentView(ctx context.Context) (TorrentView, error) {
	c, err := torrent.Load(ctx, h.d.Settings)
	if err != nil {
		return TorrentView{}, err
	}
	v := TorrentView{Enabled: c.Enabled, BanMinutes: c.BanMinutes, Exempt: []TorrentUser{}}
	if len(c.Exempt) == 0 {
		return v, nil
	}
	users, err := h.d.Store.Q.ListUsers(ctx)
	if err != nil {
		return v, err
	}
	for _, u := range users {
		if c.IsExempt(u.ID) {
			v.Exempt = append(v.Exempt, TorrentUser{ID: u.ID, Name: u.Name})
		}
	}
	return v, nil
}

func (h *handlers) getTorrent(ctx context.Context, _ *struct{}) (*torrentOutput, error) {
	v, err := h.torrentView(ctx)
	if err != nil {
		return nil, err
	}
	return &torrentOutput{Body: v}, nil
}

func (h *handlers) updateTorrent(ctx context.Context, in *patchTorrentInput) (*torrentOutput, error) {
	b := in.Body
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		c, err := torrent.Load(ctx, set)
		if err != nil {
			return err
		}
		if b.Enabled != nil {
			c.Enabled = *b.Enabled
		}
		if b.BanMinutes != nil {
			c.BanMinutes = *b.BanMinutes
		}
		if b.Exempt != nil {
			for _, id := range b.Exempt {
				if _, err := q.GetUser(ctx, id); errors.Is(err, sql.ErrNoRows) {
					return fieldErrValue("body.exempt", "user_not_found", id)
				} else if err != nil {
					return err
				}
			}
			c.Exempt = b.Exempt
		}
		if err := c.Validate(); err != nil {
			return fieldErr("body", "torrent_config")
		}
		return settings.Set(ctx, set, torrent.KeyConfig, c)
	})
	if err != nil {
		return nil, err
	}
	// The nodes get the blocker with their state, the exemptions and bans with the policies.
	h.d.Changes.SlotsChanged()
	details := map[string]any{}
	if b.Enabled != nil {
		details["enabled"] = *b.Enabled
	}
	if b.BanMinutes != nil {
		details["ban_minutes"] = *b.BanMinutes
	}
	if b.Exempt != nil {
		details["exempt"] = len(b.Exempt)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "torrent.update", "settings", torrent.KeyConfig, details)
	return h.getTorrent(ctx, nil)
}

func (h *handlers) listTorrentHits(ctx context.Context, in *torrentHitsInput) (*torrentHitsOutput, error) {
	p := db.ListTorrentHitsParams{Limit: int32(in.Limit)}
	if in.UserID > 0 {
		p.UserID = sql.NullInt64{Int64: in.UserID, Valid: true}
	}
	if in.Before > 0 {
		p.Before = sql.NullInt64{Int64: in.Before, Valid: true}
	}
	rows, err := h.d.Store.Q.ListTorrentHits(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &torrentHitsOutput{Body: make([]TorrentHitView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, TorrentHitView{
			ID: r.ID, UserID: r.UserID, UserName: r.UserName, NodeID: store.Int64Ptr(r.NodeID), NodeName: r.NodeName,
			IP: r.Ip, Inbound: r.Inbound, Network: r.Network, Kind: r.Kind, Dest: r.Dest, Hits: r.Hits,
			At: time.Unix(r.At, 0).UTC(), BannedUntil: store.TimePtr(sql.NullInt64{Int64: r.BannedUntil, Valid: r.BannedUntil > 0}),
			LiftedAt: store.TimePtr(r.LiftedAt),
		})
	}
	return out, nil
}

func (h *handlers) liftTorrentBan(ctx context.Context, in *userIDInput) (*liftTorrentOutput, error) {
	if err := requireUser(ctx, h.d.Store.Q, in.ID); err != nil {
		return nil, err
	}
	n, err := h.d.Store.Q.LiftTorrentBans(ctx, db.LiftTorrentBansParams{UserID: in.ID, Now: h.d.Now().Unix()})
	if err != nil {
		return nil, err
	}
	out := &liftTorrentOutput{}
	out.Body.Lifted = n
	if n > 0 {
		h.d.Changes.PoliciesChanged()
		h.audit(ctx, sessionOf(ctx).AdminID, "user.torrent_lift", "user", strconv.FormatInt(in.ID, 10), nil)
	}
	return out, nil
}
