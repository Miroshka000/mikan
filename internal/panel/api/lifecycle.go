package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
)

// The lifecycle of the subscriptions (domain/lifecycle.go): the panel's rules of the
// carry-over and of the removal of inactive subscriptions, who the removal reaches now, and
// a subscription's next tariff.

type lifecycleOutput struct{ Body domain.Lifecycle }

type lifecycleInput struct{ Body domain.Lifecycle }

// InactiveView is a subscription the removal rules reach: removed by the next round of the
// housekeeping, or kept for keep.
type InactiveView struct {
	UserID    int64      `json:"user_id"`
	UserName  string     `json:"user_name"`
	TariffID  *int64     `json:"tariff_id"`
	ExpiresAt time.Time  `json:"expires_at" doc:"Когда кончился срок"`
	OnlineAt  *time.Time `json:"online_at" doc:"Последнее подключение; null — не подключался"`
	Keep      string     `json:"keep" enum:",balance,payment,grant" doc:"Почему подписка останется: balance — деньги на балансе привязанного аккаунта, payment — платёж ещё не применён или пополнение ждёт проверки, grant — действуют пакеты трафика; пусто — удалится"`
}

type inactiveInput struct {
	Days int `query:"days" minimum:"0" maximum:"3650" doc:"Посмотреть, кого достанет правило с этим числом дней; 0 — с сохранённым"`
}

// inactiveShown is how many subscriptions the list of the inactive names one by one.
const inactiveShown = 200

type inactiveOutput struct {
	Body struct {
		On    bool           `json:"on" doc:"Правило включено: следующий проход удалит тех, кого ничто не держит"`
		Days  int            `json:"days" doc:"Число дней, с которым составлен список"`
		Due   int            `json:"due" doc:"Сколько подписок удалится"`
		Kept  int            `json:"kept" doc:"Сколько правило достаёт, но оставит"`
		Items []InactiveView `json:"items" doc:"Первые 200 по сроку, дольше всех кончившиеся первыми"`
	}
}

// NextTariffView is the tariff a subscription moves to once its term ends.
type NextTariffView struct {
	TariffID int64  `json:"tariff_id"`
	TermDays *int64 `json:"term_days" doc:"Срок на нём; null — первый срок тарифа"`
}

type nextTariffInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		TariffID int64  `json:"tariff_id" minimum:"1"`
		TermDays *int64 `json:"term_days,omitempty" minimum:"0" maximum:"3650" doc:"Срок на нём в днях (0 — бессрочно); не задан — первый срок тарифа"`
	}
}

type nextTariffOutput struct {
	Body struct {
		User     UserView `json:"user"`
		Switched bool     `json:"switched" doc:"Срок уже кончился или кончится до следующего прохода: тариф применён сразу"`
	}
}

// registerLifecycle is called by registerUsers: these are the users' endpoints too.
func (h *handlers) registerLifecycle() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "get-lifecycle", Method: http.MethodGet, Path: "/api/v1/lifecycle",
		Summary: "Правила подписок: перенос остатка трафика и удаление неактивных", Tags: tags}, h.getLifecycle)
	huma.Register(h.api, huma.Operation{OperationID: "update-lifecycle", Method: http.MethodPut, Path: "/api/v1/lifecycle",
		Summary: "Изменить правила подписок", Description: "Удаление неактивных меняет только сессия админа: ключу API и поддержке — 403 session_only / support_extended.", Tags: tags}, h.updateLifecycle)
	huma.Register(h.api, huma.Operation{OperationID: "list-inactive-users", Method: http.MethodGet, Path: "/api/v1/lifecycle/inactive",
		Summary: "Кого достаёт удаление неактивных: пробный прогон, ничего не удаляет", Tags: tags}, h.listInactive)
	huma.Register(h.api, huma.Operation{OperationID: "set-next-tariff", Method: http.MethodPut, Path: "/api/v1/users/{id}/next-tariff",
		Summary: "Следующий тариф: включится сам, когда кончится срок", Tags: tags}, h.setNextTariff)
	huma.Register(h.api, huma.Operation{OperationID: "clear-next-tariff", Method: http.MethodDelete, Path: "/api/v1/users/{id}/next-tariff",
		Summary: "Убрать следующий тариф", Tags: tags, DefaultStatus: http.StatusNoContent}, h.clearNextTariff)
}

func (h *handlers) getLifecycle(ctx context.Context, _ *struct{}) (*lifecycleOutput, error) {
	l, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	return &lifecycleOutput{Body: l}, nil
}

func (h *handlers) updateLifecycle(ctx context.Context, in *lifecycleInput) (*lifecycleOutput, error) {
	l := in.Body
	if err := l.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	// A standing rule that deletes subscribers is the admin's own call: a leaked key, or a
	// helper let in for a while, may tune the carry-over but not who loses a subscription.
	if l.Removal != cur.Removal {
		if err := requireSession(ctx, "removal"); err != nil {
			return nil, err
		}
	}
	if err := settings.Set(ctx, h.d.Settings, domain.KeyLifecycle, l); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "lifecycle.update", "", "", map[string]any{"carry": l.Carry, "removal": l.Removal})
	return &lifecycleOutput{Body: l}, nil
}

func (h *handlers) listInactive(ctx context.Context, in *inactiveInput) (*inactiveOutput, error) {
	rules, err := domain.LoadLifecycle(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	r := rules.Removal
	if in.Days > 0 {
		if in.Days < domain.MinRemovalDays || in.Days > domain.MaxRemovalDays {
			return nil, fieldErr("query.days", "bad_removal_days")
		}
		r.Days = in.Days
	}
	list, err := h.d.Users.Inactive(ctx, r)
	if err != nil {
		return nil, err
	}
	out := &inactiveOutput{}
	b := &out.Body
	b.On, b.Days, b.Items = rules.Removal.On, r.Days, []InactiveView{}
	for _, u := range list {
		if u.Keep == "" {
			b.Due++
		} else {
			b.Kept++
		}
		if len(b.Items) < inactiveShown {
			b.Items = append(b.Items, InactiveView{UserID: u.ID, UserName: u.Name, TariffID: store.Int64Ptr(u.TariffID), ExpiresAt: u.ExpiresAt,
				OnlineAt: store.TimePtr(u.OnlineAt), Keep: u.Keep})
		}
	}
	return out, nil
}

func (h *handlers) setNextTariff(ctx context.Context, in *nextTariffInput) (*nextTariffOutput, error) {
	next := domain.NextTariff{TariffID: in.Body.TariffID, TermDays: domain.NullInt(in.Body.TermDays)}
	u, switched, err := h.d.Users.SetNextTariff(ctx, in.ID, next)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.next_tariff", "user", strconv.FormatInt(in.ID, 10),
		map[string]any{"tariff_id": next.TariffID, "term_days": in.Body.TermDays, "switched": switched})
	res, err := h.userResult(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	out := &nextTariffOutput{}
	out.Body.User, out.Body.Switched = res.Body, switched
	return out, nil
}

func (h *handlers) clearNextTariff(ctx context.Context, in *userIDInput) (*struct{}, error) {
	if err := h.d.Users.ClearNextTariff(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.next_tariff_clear", "user", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

// nextView is the view of a next tariff; nil for none.
func nextView(n domain.NextTariff, ok bool) *NextTariffView {
	if !ok {
		return nil
	}
	return &NextTariffView{TariffID: n.TariffID, TermDays: store.Int64Ptr(n.TermDays)}
}
