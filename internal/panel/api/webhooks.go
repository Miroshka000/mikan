package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/webhooks"
)

// Outgoing webhooks (internal/panel/webhooks): the addresses the panel POSTs its events to,
// each with the events it is told of and a secret that signs every request. Every operation is
// the session's, reading too: an address decides where the events of the panel go (payments,
// subscribers, devices, nodes), and addresses of services such as Discord or Slack carry their
// token in the path, so a leaked key must neither point the events at its own server nor read
// the addresses.

// WebhookDeliveryView is a line of an address's log: one event and how its delivery went.
type WebhookDeliveryView struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type" doc:"Тип события; webhook.test — проверка кнопкой"`
	EventID      *int64    `json:"event_id,omitempty" doc:"Номер события в ленте; у проверки его нет"`
	Status       string    `json:"status" enum:"retrying,delivered,failed" doc:"retrying — будет отправлено снова; delivered — получатель ответил 2xx; failed — больше не пробуем (проверка не прошла, событие ушло из ленты или адрес его больше не просит)"`
	Attempts     int64     `json:"attempts" doc:"Сколько раз пробовали"`
	ResponseCode int64     `json:"response_code" doc:"HTTP-статус последнего ответа; 0 — ответа не было"`
	Error        string    `json:"error,omitempty" doc:"Почему не вышло в последний раз: address_local, address_blocked, dns, refused, timeout, tls, redirect, status, network, internal; после 2xx пусто"`
	CreatedAt    time.Time `json:"created_at" doc:"Первая попытка"`
	UpdatedAt    time.Time `json:"updated_at" doc:"Последняя попытка"`
}

// WebhookView is an address as the admin's page shows it. The secret is not here: it is shown
// once, when it is made.
type WebhookView struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	URL        string               `json:"url"`
	Events     []string             `json:"events" doc:"Какие события отправляются; пусто — все, и новые тоже"`
	Enabled    bool                 `json:"enabled" doc:"Выключенный адрес ничего не получает, события ждут его до недели"`
	AllowLocal bool                 `json:"allow_local" doc:"Можно ли слать на адреса этого сервера и его сети (localhost, 10.x, 192.168.x, 100.64.x). Адреса из диапазона 169.254 с метаданными облака и подобные закрыты всегда"`
	SecretHint string               `json:"secret_hint" doc:"Последние символы секрета: чтобы понять, какой из них у получателя"`
	SecretAt   time.Time            `json:"secret_at" doc:"Когда секрет создан или заменён"`
	CreatedAt  time.Time            `json:"created_at"`
	Waiting    int64                `json:"waiting" doc:"Сколько событий ждёт отправки этому адресу"`
	Last       *WebhookDeliveryView `json:"last,omitempty" doc:"Последняя доставка: по ней видно, всё ли в порядке"`
}

// WebhookCreatedView is an address with the secret, which is shown this once.
type WebhookCreatedView struct {
	WebhookView
	Secret string `json:"secret" doc:"Секрет подписи: показывается один раз, потом только его конец (secret_hint). Получатель проверяет им каждый запрос"`
}

type WebhooksView struct {
	Items []WebhookView `json:"items"`
	Limit int           `json:"limit" doc:"Сколько адресов можно завести"`
}

type WebhookEventsView struct {
	Types []string `json:"types" doc:"Типы событий, которые панель отправляет: каждый можно выбрать для адреса"`
}

// WebhookTestView is how a test event went: the answer of the receiver, or why there was none.
type WebhookTestView struct {
	OK         bool   `json:"ok" doc:"Получатель ответил 2xx"`
	Status     int    `json:"status" doc:"HTTP-статус ответа; 0 — ответа не было"`
	Error      string `json:"error,omitempty" doc:"Почему не вышло: те же коды, что у доставок"`
	DurationMS int64  `json:"duration_ms" doc:"Сколько шла проверка"`
}

type webhooksOutput struct{ Body WebhooksView }
type webhookEventsOutput struct{ Body WebhookEventsView }
type webhookOutput struct{ Body WebhookView }
type webhookCreatedOutput struct{ Body WebhookCreatedView }
type webhookTestOutput struct{ Body WebhookTestView }

type webhookIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createWebhookInput struct {
	Body struct {
		Name       string   `json:"name" minLength:"1" maxLength:"60" doc:"Название для списка"`
		URL        string   `json:"url" minLength:"1" maxLength:"2048" doc:"http:// или https:// и адрес получателя. Адреса этого сервера, его сети и облачных метаданных панель не принимает; для адресов сети нужен allow_local"`
		Events     []string `json:"events,omitempty" maxItems:"64" doc:"Типы событий (GET /webhooks/events); пусто или не задано — все, и те, что появятся позже"`
		Enabled    *bool    `json:"enabled,omitempty" doc:"Не задано — включён"`
		AllowLocal bool     `json:"allow_local,omitempty" doc:"Разрешить адреса этого сервера и его сети: localhost, 10.x, 172.16-31.x, 192.168.x, 100.64-127.x. Метаданные облака (169.254.169.254 и подобные) закрыты всегда"`
	}
}

type patchWebhookInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name       *string   `json:"name,omitempty" minLength:"1" maxLength:"60"`
		URL        *string   `json:"url,omitempty" minLength:"1" maxLength:"2048"`
		Events     *[]string `json:"events,omitempty" maxItems:"64" doc:"Пустой список — все события. Добавленные типы начинаются с этой минуты, если адрес ничего не должен; что ему должны, он получит"`
		Enabled    *bool     `json:"enabled,omitempty"`
		AllowLocal *bool     `json:"allow_local,omitempty"`
	}
}

type webhookDeliveriesInput struct {
	ID     int64  `path:"id" minimum:"1"`
	Before int64  `query:"before" minimum:"0" default:"0" doc:"Записи до этого номера; 0 — самые новые"`
	Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Status string `query:"status" enum:"retrying,delivered,failed" doc:"Только с этим статусом; не задано — любые"`
}

type webhookDeliveriesOutput struct {
	Body struct {
		Items []WebhookDeliveryView `json:"items" doc:"От новых к старым"`
		Next  int64                 `json:"next" doc:"Передайте как before за следующей страницей; 0 — это всё"`
	}
}

func (h *handlers) registerWebhooks() {
	op := sessionOps("webhooks")
	huma.Register(h.api, op("list-webhooks", http.MethodGet, "/api/v1/webhooks", "Адреса для событий (webhooks)", 0), h.listWebhooks)
	huma.Register(h.api, op("list-webhook-events", http.MethodGet, "/api/v1/webhooks/events", "Какие события можно отправлять", 0), h.listWebhookEvents)
	huma.Register(h.api, op("create-webhook", http.MethodPost, "/api/v1/webhooks", "Добавить адрес: секрет подписи показывается один раз", http.StatusCreated), h.createWebhook)
	huma.Register(h.api, op("update-webhook", http.MethodPatch, "/api/v1/webhooks/{id}", "Изменить адрес", 0), h.updateWebhook)
	huma.Register(h.api, op("delete-webhook", http.MethodDelete, "/api/v1/webhooks/{id}", "Удалить адрес вместе с его журналом", http.StatusNoContent), h.deleteWebhook)
	huma.Register(h.api, op("rotate-webhook-secret", http.MethodPost, "/api/v1/webhooks/{id}/secret", "Выпустить новый секрет: старый перестаёт подписывать сразу, новый показывается один раз", 0), h.rotateWebhookSecret)
	huma.Register(h.api, op("test-webhook", http.MethodPost, "/api/v1/webhooks/{id}/test", "Отправить проверочное событие webhook.test: одна попытка, ответ сразу", 0), h.testWebhook)
	huma.Register(h.api, op("list-webhook-deliveries", http.MethodGet, "/api/v1/webhooks/{id}/deliveries", "Журнал доставок адреса: хранится 7 суток после последней попытки", 0), h.listWebhookDeliveries)
}

func webhookDeliveryView(d db.WebhookDelivery) WebhookDeliveryView {
	return WebhookDeliveryView{ID: d.ID, Type: d.Type, EventID: store.Int64Ptr(d.EventID), Status: d.Status, Attempts: d.Attempts,
		ResponseCode: d.ResponseCode, Error: d.Error, CreatedAt: store.Time(d.CreatedAt), UpdatedAt: store.Time(d.UpdatedAt)}
}

func webhookView(e webhooks.Endpoint) WebhookView {
	v := WebhookView{ID: e.ID, Name: e.Name, URL: e.Url, Events: append([]string{}, e.Events...), Enabled: e.Enabled, AllowLocal: e.AllowLocal,
		SecretHint: webhooks.SecretHint(e.Secret), SecretAt: store.Time(e.SecretAt), CreatedAt: store.Time(e.CreatedAt), Waiting: e.Waiting}
	if e.Last != nil {
		last := webhookDeliveryView(*e.Last)
		v.Last = &last
	}
	return v
}

// webhookProblem is one refused field as the API words it: the code the admin panel turns into
// a text, under the field of the body.
func webhookProblem(p webhooks.Problem) error {
	switch p.Field {
	case "name":
		return fieldDetail("body.name", "name_blank", nil)
	case "events":
		return fieldDetail("body.events", "webhook_event_unknown", p.Value)
	}
	switch {
	case errors.Is(p.Err, webhooks.ErrURLScheme):
		return fieldDetail("body.url", "webhook_url_scheme", nil)
	case errors.Is(p.Err, webhooks.ErrURLPort):
		return fieldDetail("body.url", "webhook_url_port", nil)
	case errors.Is(p.Err, webhooks.ErrLocalAddress):
		return fieldDetail("body.url", "webhook_url_local", nil)
	case errors.Is(p.Err, webhooks.ErrBlockedAddress):
		return fieldDetail("body.url", "webhook_url_blocked", nil)
	}
	return fieldDetail("body.url", "webhook_url_invalid", nil)
}

// webhookErr is what the service refused with, as the API answers it; anything else is the
// server's own trouble and passes on.
func webhookErr(err error) error {
	var inv *webhooks.InvalidError
	switch {
	case errors.As(err, &inv):
		details := make([]error, len(inv.Problems))
		for i, p := range inv.Problems {
			details[i] = webhookProblem(p)
		}
		return fieldErrs(details...)
	case errors.Is(err, webhooks.ErrNotFound):
		return huma.Error404NotFound("not_found")
	case errors.Is(err, webhooks.ErrLimit):
		return huma.Error409Conflict("webhooks_limit")
	}
	return err
}

// webhookHost is the host of an address, for the journal: the path and the query may hold a
// token, and nothing but the host is written down.
func webhookHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func (h *handlers) listWebhooks(ctx context.Context, _ *struct{}) (*webhooksOutput, error) {
	rows, err := h.d.Webhooks.List(ctx)
	if err != nil {
		return nil, err
	}
	out := &webhooksOutput{}
	out.Body.Items = make([]WebhookView, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, webhookView(r))
	}
	out.Body.Limit = webhooks.MaxEndpoints
	return out, nil
}

func (h *handlers) listWebhookEvents(context.Context, *struct{}) (*webhookEventsOutput, error) {
	return &webhookEventsOutput{Body: WebhookEventsView{Types: events.Types()}}, nil
}

func (h *handlers) createWebhook(ctx context.Context, in *createWebhookInput) (*webhookCreatedOutput, error) {
	b := in.Body
	row, err := h.d.Webhooks.Create(ctx, webhooks.Input{Name: b.Name, URL: b.URL, Events: b.Events, Enabled: b.Enabled == nil || *b.Enabled, AllowLocal: b.AllowLocal})
	if err != nil {
		return nil, webhookErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "webhook.create", "webhook", strconv.FormatInt(row.ID, 10),
		map[string]any{"name": row.Name, "host": webhookHost(row.Url), "events": row.Events, "allow_local": row.AllowLocal})
	out := &webhookCreatedOutput{}
	out.Body.WebhookView, out.Body.Secret = webhookView(webhooks.Endpoint{WebhookEndpoint: row}), row.Secret
	return out, nil
}

func (h *handlers) updateWebhook(ctx context.Context, in *patchWebhookInput) (*webhookOutput, error) {
	b := in.Body
	row, err := h.d.Webhooks.Update(ctx, in.ID, webhooks.Patch{Name: b.Name, URL: b.URL, Events: b.Events, Enabled: b.Enabled, AllowLocal: b.AllowLocal})
	if err != nil {
		return nil, webhookErr(err)
	}
	// What was changed, not the values: the address may hold a token.
	var fields []string
	for _, f := range []struct {
		name string
		set  bool
	}{{"name", b.Name != nil}, {"url", b.URL != nil}, {"events", b.Events != nil}, {"enabled", b.Enabled != nil}, {"allow_local", b.AllowLocal != nil}} {
		if f.set {
			fields = append(fields, f.name)
		}
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "webhook.update", "webhook", strconv.FormatInt(row.ID, 10), map[string]any{"fields": fields, "host": webhookHost(row.Url)})
	return &webhookOutput{Body: webhookView(webhooks.Endpoint{WebhookEndpoint: row})}, nil
}

func (h *handlers) deleteWebhook(ctx context.Context, in *webhookIDInput) (*struct{}, error) {
	if err := h.d.Webhooks.Delete(ctx, in.ID); err != nil {
		return nil, webhookErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "webhook.delete", "webhook", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) rotateWebhookSecret(ctx context.Context, in *webhookIDInput) (*webhookCreatedOutput, error) {
	row, err := h.d.Webhooks.Rotate(ctx, in.ID)
	if err != nil {
		return nil, webhookErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "webhook.rotate", "webhook", strconv.FormatInt(row.ID, 10), nil)
	out := &webhookCreatedOutput{}
	out.Body.WebhookView, out.Body.Secret = webhookView(webhooks.Endpoint{WebhookEndpoint: row}), row.Secret
	return out, nil
}

func (h *handlers) testWebhook(ctx context.Context, in *webhookIDInput) (*webhookTestOutput, error) {
	res, err := h.d.Webhooks.Test(ctx, in.ID)
	if err != nil {
		return nil, webhookErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "webhook.test", "webhook", strconv.FormatInt(in.ID, 10), map[string]any{"ok": res.OK, "status": res.Status, "error": res.Failure})
	return &webhookTestOutput{Body: WebhookTestView{OK: res.OK, Status: res.Status, Error: res.Failure, DurationMS: res.Took.Milliseconds()}}, nil
}

func (h *handlers) listWebhookDeliveries(ctx context.Context, in *webhookDeliveriesInput) (*webhookDeliveriesOutput, error) {
	rows, err := h.d.Webhooks.Deliveries(ctx, in.ID, in.Before, in.Status, in.Limit)
	if err != nil {
		return nil, webhookErr(err)
	}
	out := &webhookDeliveriesOutput{}
	out.Body.Items = make([]WebhookDeliveryView, 0, len(rows))
	for _, d := range rows {
		out.Body.Items = append(out.Body.Items, webhookDeliveryView(d))
	}
	if len(rows) == in.Limit {
		out.Body.Next = rows[len(rows)-1].ID
	}
	return out, nil
}
