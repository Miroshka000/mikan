package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/updates"
	"mikan/internal/release"
)

type UpdatesView struct {
	Current     string              `json:"current"`
	Latest      string              `json:"latest" doc:"Последний релиз; пусто, пока проверки не было"`
	Published   string              `json:"published" doc:"Когда вышел последний релиз, RFC 3339"`
	Notes       map[string]string   `json:"notes" doc:"Что изменилось: markdown по языкам, en и ru"`
	Available   bool                `json:"available" doc:"Вышла версия новее этой"`
	CheckedAt   int64               `json:"checked_at" doc:"Unix-время последней проверки; 0 — ещё не проверяли"`
	Error       string              `json:"error" doc:"Почему последняя проверка не удалась; no_release — релизов ещё нет"`
	Auto        bool                `json:"auto" doc:"Сервер сам ставит новые релизы раз в сутки, ночью"`
	Channel     string              `json:"channel" enum:"stable,beta" doc:"Какие релизы ставить: stable — только релизы, beta — и пре-релизы (vX-rc.N)"`
	Newest      string              `json:"newest" doc:"Новейший релиз канала, если обновление идёт к нему через latest или до него отсюда не добраться; иначе пусто"`
	Unreachable bool                `json:"unreachable" doc:"Вышел newest, но с этой версии к нему не ведёт ни одно обновление"`
	RequestedAt int64               `json:"requested_at" doc:"Когда нажали «Обновить»; 0 — заявки нет или сервер её уже взял"`
	Host        *updates.HostStatus `json:"host,omitempty" doc:"Как прошло последнее обновление на сервере"`
	NodesFollow bool                `json:"nodes_follow" doc:"Удалённые ноды следуют за панелью: после её обновления панель обновляет их до своей версии, по одной"`
	// The goals come with the release index, signed: the panel asks nothing more for them.
	Goals     []release.Goal `json:"goals" doc:"На что проект собирает деньги: из подписанного индекса релизов; пусто, пока проверки не было"`
	Donate    string         `json:"donate" doc:"Куда поддержать проект без цели"`
	ShowGoals bool           `json:"show_goals" doc:"Показывать цели в карточке обновлений"`
}

type updatesOutput struct{ Body UpdatesView }

type patchUpdatesInput struct {
	Body struct {
		Auto    *bool   `json:"auto,omitempty"`
		Channel *string `json:"channel,omitempty" enum:"stable,beta"`
		// NodesFollow turning on also lets the panel try again the nodes whose update failed.
		NodesFollow *bool `json:"nodes_follow,omitempty"`
		ShowGoals   *bool `json:"show_goals,omitempty"`
	}
}

// checking allows one release check at a time.
var checking sync.Mutex

func (h *handlers) registerUpdates() {
	tags := []string{"settings"}
	huma.Register(h.api, huma.Operation{OperationID: "get-updates", Method: http.MethodGet, Path: "/api/v1/updates", Summary: "Обновления", Tags: tags}, h.getUpdates)
	huma.Register(h.api, huma.Operation{OperationID: "update-updates", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/updates", Summary: "Автообновление и канал релизов", Tags: tags}, h.patchUpdates)
	huma.Register(h.api, huma.Operation{OperationID: "check-updates", Method: http.MethodPost, Path: "/api/v1/updates/check", Summary: "Проверить обновления сейчас", Tags: tags}, h.checkUpdates)
	huma.Register(h.api, huma.Operation{OperationID: "request-update", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/updates/request", Summary: "Обновить сейчас: заявка серверу", Tags: tags, DefaultStatus: http.StatusAccepted}, h.requestUpdate)
}

func (h *handlers) updatesView(ctx context.Context) (UpdatesView, error) {
	v := UpdatesView{Current: h.d.Version, Notes: map[string]string{}}
	var err error
	if v.Auto, err = h.d.Settings.On(ctx, settings.AutoUpdate); err != nil {
		return v, err
	}
	if v.Channel, err = h.d.Settings.UpdateChannel(ctx); err != nil {
		return v, err
	}
	if v.NodesFollow, err = h.d.Settings.On(ctx, settings.NodesFollow); err != nil {
		return v, err
	}
	if v.ShowGoals, err = h.d.Settings.On(ctx, settings.ShowGoals); err != nil {
		return v, err
	}
	v.Goals = []release.Goal{}
	u := h.d.Updates
	if u == nil {
		return v, nil
	}
	s := u.State()
	if s.Latest != nil {
		v.Latest, v.Published, v.Notes = s.Latest.Version, s.Latest.Published.UTC().Format(time.RFC3339), s.Latest.Notes
		if v.Notes == nil {
			v.Notes = map[string]string{}
		}
	}
	v.Available, v.Error = s.Available(), s.Error
	v.Newest, v.Unreachable = s.Found.Newest, s.Found.Unreachable
	if s.Found.Goals.Items != nil {
		v.Goals, v.Donate = s.Found.Goals.Items, s.Found.Goals.Donate
	}
	if !s.CheckedAt.IsZero() {
		v.CheckedAt = s.CheckedAt.Unix()
	}
	if at, ok := u.Requested(); ok {
		v.RequestedAt = at.Unix()
	}
	if st, ok := u.Host(); ok {
		v.Host = &st
	}
	return v, nil
}

func (h *handlers) getUpdates(ctx context.Context, _ *struct{}) (*updatesOutput, error) {
	v, err := h.updatesView(ctx)
	if err != nil {
		return nil, err
	}
	return &updatesOutput{Body: v}, nil
}

func (h *handlers) patchUpdates(ctx context.Context, in *patchUpdatesInput) (*updatesOutput, error) {
	if in.Body.NodesFollow != nil {
		on, err := h.d.Settings.On(ctx, settings.NodesFollow)
		if err != nil {
			return nil, err
		}
		if *in.Body.NodesFollow != on {
			if h.d.NodeUpdates != nil {
				err = h.d.NodeUpdates.SetFollowing(ctx, *in.Body.NodesFollow)
			} else {
				err = settings.Set(ctx, h.d.Settings, settings.KeyNodesFollow, *in.Body.NodesFollow)
			}
			if err != nil {
				return nil, err
			}
			h.audit(ctx, sessionOf(ctx).AdminID, "updates.nodes_follow", "", "", map[string]any{"nodes_follow": *in.Body.NodesFollow})
		}
	}
	if in.Body.ShowGoals != nil {
		if err := settings.Set(ctx, h.d.Settings, settings.KeyShowGoals, *in.Body.ShowGoals); err != nil {
			return nil, err
		}
	}
	if in.Body.Auto == nil && in.Body.Channel == nil {
		return h.getUpdates(ctx, nil)
	}
	if h.d.Updates == nil {
		return nil, huma.Error409Conflict("updates_unavailable")
	}
	if in.Body.Channel != nil && !release.ValidChannel(*in.Body.Channel) {
		return nil, fieldErr("body.channel", "bad_channel")
	}
	auto, err := h.d.Settings.On(ctx, settings.AutoUpdate)
	if err != nil {
		return nil, err
	}
	channel, err := h.d.Settings.UpdateChannel(ctx)
	if err != nil {
		return nil, err
	}
	was := channel
	if in.Body.Auto != nil {
		auto = *in.Body.Auto
	}
	if in.Body.Channel != nil {
		channel = *in.Body.Channel
	}
	// The host reads the policy file; the settings are what the admin chose.
	if err := h.d.Updates.SetPolicy(updates.Policy{Auto: auto, Channel: channel}); err != nil {
		if errors.Is(err, updates.ErrUnavailable) {
			return nil, huma.Error409Conflict("updates_unavailable")
		}
		return nil, err
	}
	if in.Body.Auto != nil {
		if err := settings.Set(ctx, h.d.Settings, settings.KeyAutoUpdate, auto); err != nil {
			return nil, err
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "updates.auto", "", "", map[string]any{"auto": auto})
	}
	if channel != was {
		if err := settings.Set(ctx, h.d.Settings, settings.KeyUpdateChannel, channel); err != nil {
			return nil, err
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "updates.channel", "", "", map[string]any{"channel": channel})
		// What was found is of the other channel: look again, unless a check runs already.
		if checking.TryLock() {
			cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			h.d.Updates.Check(cctx)
			cancel()
			checking.Unlock()
		}
	}
	return h.getUpdates(ctx, nil)
}

func (h *handlers) checkUpdates(ctx context.Context, _ *struct{}) (*updatesOutput, error) {
	if h.d.Updates == nil {
		return nil, huma.Error409Conflict("updates_unavailable")
	}
	if !checking.TryLock() {
		return nil, huma.Error409Conflict("check_busy")
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	h.d.Updates.Check(cctx)
	cancel()
	checking.Unlock()
	return h.getUpdates(ctx, nil)
}

func (h *handlers) requestUpdate(ctx context.Context, _ *struct{}) (*updatesOutput, error) {
	if h.d.Updates == nil {
		return nil, huma.Error409Conflict("updates_unavailable")
	}
	if !h.d.Updates.State().Available() {
		return nil, huma.Error409Conflict("update_none")
	}
	if err := h.d.Updates.Request(); err != nil {
		if errors.Is(err, updates.ErrUnavailable) {
			return nil, huma.Error409Conflict("updates_unavailable")
		}
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "updates.request", "", "", map[string]any{"version": h.d.Updates.State().Latest.Version})
	return h.getUpdates(ctx, nil)
}
