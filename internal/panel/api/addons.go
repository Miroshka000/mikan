package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/billing"
)

// The marketplace of payment adapters: what the signed catalog offers, what the server
// runs, and each adapter's settings. Only a session may change them: whoever installs a
// payment adapter or sets its keys decides where the money goes.

type AddonCatalogEntry struct {
	ID          string            `json:"id"`
	Name        map[string]string `json:"name" doc:"Название по языкам"`
	Description map[string]string `json:"description"`
	Version     string            `json:"version"`
	Homepage    string            `json:"homepage"`
	Installed   bool              `json:"installed"`
	Update      bool              `json:"update" doc:"Установлен, а в каталоге другая сборка"`
}

type AddonOption struct {
	Value string            `json:"value"`
	Label map[string]string `json:"label"`
}

type AddonField struct {
	Key         string            `json:"key"`
	Label       map[string]string `json:"label"`
	Type        string            `json:"type" enum:"string,bool,select,number,text,code" doc:"Другой тип панель не знает: см. unsupported"`
	Unsupported bool              `json:"unsupported,omitempty" doc:"Панель не знает этот тип: поле не заполнить и не сохранить"`
	Secret      bool              `json:"secret" doc:"Значение API не отдаёт; пусто при сохранении — оставить прежнее"`
	Required    bool              `json:"required"`
	Pattern     string            `json:"pattern,omitempty" doc:"Только для string"`
	Options     []AddonOption     `json:"options,omitempty" doc:"Только для select"`
	Min         *int64            `json:"min,omitempty" doc:"Только для number"`
	Max         *int64            `json:"max,omitempty" doc:"Только для number"`
	Default     any               `json:"default,omitempty" doc:"Подсказка в пустом поле; панель его не сохраняет, адаптер применяет сам"`
	Help        map[string]string `json:"help,omitempty"`
	Value       any               `json:"value,omitempty" doc:"Текущее значение, кроме секретов"`
	Set         bool              `json:"set" doc:"Значение сохранено"`
}

type AddonView struct {
	ID         string            `json:"id"`
	Version    string            `json:"version"`
	Status     string            `json:"status" enum:"running"`
	Name       map[string]string `json:"name"`
	Help       map[string]string `json:"help"`
	Enabled    bool              `json:"enabled"`
	Available  bool              `json:"available" doc:"Принимает оплату прямо сейчас: включён, настроен, работает"`
	Settings   []AddonField      `json:"settings"`
	WebhookURL string            `json:"webhook_url" doc:"Адрес для уведомлений в кабинете провайдера"`
	InfoError  string            `json:"info_error,omitempty" doc:"Адаптер не ответил о себе: настройки недоступны"`
}

// AddonRequest is what the panel asked the server for.
type AddonRequest struct {
	Action string `json:"action" enum:"install,remove"`
	ID     string `json:"id"`
	At     string `json:"at" doc:"RFC 3339"`
}

// AddonResult is how the server did what was asked.
type AddonResult struct {
	Action string `json:"action" enum:"install,remove"`
	ID     string `json:"id"`
	State  string `json:"state" enum:"done,failed"`
	Error  string `json:"error,omitempty"`
	At     string `json:"at" doc:"RFC 3339"`
}

type AddonsView struct {
	Catalog      []AddonCatalogEntry `json:"catalog"`
	CatalogError string              `json:"catalog_error,omitempty" doc:"catalog_unavailable — каталог не загрузился"`
	Installed    []AddonView         `json:"installed"`
	Queue        []AddonRequest      `json:"queue,omitempty" doc:"Все заявки, которые сервер ещё не взял, по порядку"`
	Last         *AddonResult        `json:"last,omitempty" doc:"Как сервер выполнил последнюю заявку"`
	Supported    bool                `json:"supported" doc:"Панель видит каталог данных сервера; иначе ставить адаптеры нельзя"`
}

type addonsOutput struct{ Body AddonsView }

// AddonIDInput is exported so that the inputs embedding it keep a settable field.
type AddonIDInput struct {
	ID string `path:"id" pattern:"^[a-z0-9][a-z0-9-]{0,31}$"`
}

type patchAddonInput struct {
	AddonIDInput
	Body struct {
		Enabled  *bool          `json:"enabled,omitempty"`
		Settings map[string]any `json:"settings,omitempty" doc:"Только то, что меняется; секрет пустой строкой — оставить прежний"`
	}
}

// sessionOps makes the operations of a tag that only a session may call: the API key
// middleware refuses an API key by the mark, the reference shows it.
func sessionOps(tag string) func(id, method, path, summary string, status int) huma.Operation {
	return func(id, method, path, summary string, status int) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{tag}, DefaultStatus: status, Metadata: sessionOnly, Extensions: sessionOnlyExt}
	}
}

func (h *handlers) registerAddons() {
	op := sessionOps("payments")
	huma.Register(h.api, op("list-addons", http.MethodGet, "/api/v1/addons", "Маркетплейс способов оплаты", 0), h.listAddons)
	huma.Register(h.api, op("install-addon", http.MethodPost, "/api/v1/addons/{id}/install", "Установить или обновить адаптер: заявка серверу", http.StatusAccepted), h.installAddon)
	huma.Register(h.api, op("remove-addon", http.MethodPost, "/api/v1/addons/{id}/remove", "Удалить адаптер: заявка серверу", http.StatusAccepted), h.removeAddon)
	huma.Register(h.api, op("update-addon", http.MethodPatch, "/api/v1/addons/{id}", "Настройки адаптера", 0), h.patchAddon)
}

func (h *handlers) catalog(ctx context.Context) (addons.Catalog, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := h.d.Addons.Catalog(cctx)
	if err != nil {
		h.d.Log.Warn("addons: catalog", "err", err)
	}
	return c, err
}

// officialCatalog is the signed catalog, or nil when it cannot be read: the views then show
// what they can without it.
func (h *handlers) officialCatalog(ctx context.Context) *addons.Catalog {
	c, err := h.catalog(ctx)
	if err != nil {
		return nil
	}
	return &c
}

// requestSummary is what the host has not taken yet, in order, and how it handled the last
// request: both marketplaces show it.
func requestSummary(m *addons.Manager, st addons.State) (queue []AddonRequest, last *AddonResult) {
	for _, r := range m.Pending() {
		queue = append(queue, AddonRequest{Action: r.Action, ID: r.ID, At: r.At})
	}
	if l := st.Request; l != nil {
		last = &AddonResult{Action: l.Action, ID: l.ID, State: l.State, Error: l.Error, At: l.At}
	}
	return queue, last
}

// addonState is the host's state for a change of addons: addons_unavailable without a
// manager.
func (h *handlers) addonState() (addons.State, error) {
	if h.d.Addons == nil {
		return addons.State{}, huma.Error409Conflict("addons_unavailable")
	}
	return h.d.Addons.State()
}

// addonHost is addonState for what only protocol 2 does: host_outdated when the host
// predates it.
func (h *handlers) addonHost() (addons.State, error) {
	st, err := h.addonState()
	if err == nil && st.HostVersion == "" {
		return st, huma.Error409Conflict("host_outdated")
	}
	return st, err
}

// installEntry is the signed catalog's entry for an install of id, a payment adapter or any
// other kind, refused as the API has it: the catalog down, no such entry, an id the admin's
// own addon holds, the very build already running.
func (h *handlers) installEntry(ctx context.Context, st addons.State, id string, payment bool) (addons.Entry, error) {
	c, err := h.catalog(ctx)
	if err != nil {
		return addons.Entry{}, huma.Error502BadGateway("catalog_unavailable")
	}
	i := slices.IndexFunc(c.Adapters, func(e addons.Entry) bool { return e.ID == id && (e.Kind == addons.KindPayment) == payment })
	if i < 0 {
		return addons.Entry{}, huma.Error404NotFound("addon_unknown")
	}
	e := c.Adapters[i]
	a, ok := st.Adapters[id]
	if ok && a.SourceOf() != addons.SourceOfficial {
		// The admin's own addon holds the id: the catalog's would take over its tokens and grant.
		return addons.Entry{}, huma.Error409Conflict("addon_id_taken")
	}
	if ok && a.Status == "running" && a.Digest == e.Digest {
		return addons.Entry{}, huma.Error409Conflict("addon_current")
	}
	return e, nil
}

// ask queues r for the host and writes the audit entry of it.
func (h *handlers) ask(ctx context.Context, r addons.Request, details map[string]any) error {
	if err := h.d.Addons.Ask(r); err != nil {
		return askErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "addon."+r.Action, "addon", r.ID, details)
	return nil
}

func (h *handlers) addonsView(ctx context.Context) (AddonsView, error) {
	v := AddonsView{Catalog: []AddonCatalogEntry{}, Installed: []AddonView{}}
	m := h.d.Addons
	if m == nil {
		return v, nil
	}
	st, err := m.State()
	if err != nil {
		return v, err
	}
	// The marketplace of payment methods: the other kinds have their own section.
	payments := st.Of(addons.KindPayment)
	v.Supported = m.Supported()
	v.Queue, v.Last = requestSummary(m, st)
	if c, err := h.catalog(ctx); err != nil {
		v.CatalogError = "catalog_unavailable"
	} else {
		for _, e := range c.Of(addons.KindPayment) {
			a, ok := payments[e.ID]
			v.Catalog = append(v.Catalog, AddonCatalogEntry{ID: e.ID, Name: e.Name, Description: e.Description, Version: e.Version, Homepage: e.Homepage,
				Installed: ok, Update: ok && a.Digest != e.Digest})
		}
	}
	available := h.d.Billing.Available(ctx).Addons
	sub := ""
	if h.d.SubBase != nil {
		sub = h.d.SubBase(ctx)
	}
	for _, id := range slices.Sorted(maps.Keys(payments)) {
		a := payments[id]
		av := AddonView{ID: id, Version: a.Version, Status: a.Status, Name: map[string]string{}, Help: map[string]string{}, Settings: []AddonField{},
			Available: slices.Contains(available, id), WebhookURL: h.d.Billing.AddonWebhookURL(ctx, id, sub)}
		cfg, err := h.d.Billing.AddonConfig(ctx, id)
		if err != nil {
			return v, err
		}
		av.Enabled = cfg.Enabled
		if a.Status == "running" {
			info, err := m.Info(ctx, id)
			if err != nil {
				h.d.Log.Warn("addons: info", "addon", id, "err", err)
				av.InfoError = "addon_unreachable"
			} else {
				av.Name, av.Help = orEmpty(info.Name), orEmpty(info.Help)
				for _, f := range info.Settings {
					av.Settings = append(av.Settings, addonField(f, cfg.Values))
				}
			}
		}
		v.Installed = append(v.Installed, av)
	}
	return v, nil
}

func orEmpty(t addons.Text) map[string]string {
	if t == nil {
		return map[string]string{}
	}
	return t
}

func (h *handlers) listAddons(ctx context.Context, _ *struct{}) (*addonsOutput, error) {
	v, err := h.addonsView(ctx)
	if err != nil {
		return nil, err
	}
	return &addonsOutput{Body: v}, nil
}

// askErr turns what the manager says into the API's answer.
func askErr(err error) error {
	switch {
	case errors.Is(err, addons.ErrBusy):
		return huma.Error409Conflict("addon_busy")
	case errors.Is(err, addons.ErrUnavailable):
		return huma.Error409Conflict("addons_unavailable")
	}
	return err
}

func (h *handlers) installAddon(ctx context.Context, in *AddonIDInput) (*addonsOutput, error) {
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	e, err := h.installEntry(ctx, st, in.ID, true)
	if err != nil {
		return nil, err
	}
	if err := h.ask(ctx, addons.Request{Action: "install", ID: e.ID}, map[string]any{"version": e.Version, "digest": e.Digest}); err != nil {
		return nil, err
	}
	return h.listAddons(ctx, nil)
}

// removeAddon keeps the adapter's settings and its payments: installed again, it takes up
// where it was.
func (h *handlers) removeAddon(ctx context.Context, in *AddonIDInput) (*addonsOutput, error) {
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	if _, ok := st.Of(addons.KindPayment)[in.ID]; !ok {
		return nil, huma.Error404NotFound("addon_not_installed")
	}
	if err := h.ask(ctx, addons.Request{Action: "remove", ID: in.ID}, nil); err != nil {
		return nil, err
	}
	return h.listAddons(ctx, nil)
}

// patchAddon: the operation is session only, which the API key middleware enforces before
// this runs. The check here is the same refusal again: a code field is a script the addon
// runs, so setting one must never reach an API key if the operation's mark is ever lost.
func (h *handlers) patchAddon(ctx context.Context, in *patchAddonInput) (*addonsOutput, error) {
	if err := requireSession(ctx, "settings"); err != nil {
		return nil, err
	}
	cfg, err := h.d.Billing.AddonConfig(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	enabled := cfg.Enabled
	if in.Body.Enabled != nil {
		enabled = *in.Body.Enabled
	}
	next, err := h.d.Billing.SetAddonConfig(ctx, in.ID, enabled, in.Body.Settings)
	var se *billing.AddonSettingError
	var ae *addons.Error
	switch {
	case errors.As(err, &se):
		return nil, fieldErr("body.settings."+se.Key, "addon_setting_invalid")
	case errors.As(err, &ae) && ae.Status < 500:
		return nil, fieldErrValue("body.settings", "addon_settings_refused", ae.Code)
	case errors.Is(err, addons.ErrNotInstalled):
		return nil, huma.Error404NotFound("addon_not_installed")
	case errors.Is(err, addons.ErrUnavailable):
		return nil, huma.Error409Conflict("addons_unavailable")
	case errors.As(err, &ae), errors.Is(err, addons.ErrUnreachable):
		h.d.Log.Warn("addons: settings check", "addon", in.ID, "err", err)
		return nil, huma.Error502BadGateway("addon_unreachable")
	case err != nil:
		return nil, err
	}
	// What changed, never the values: some are keys.
	changed := []string{}
	for k := range in.Body.Settings {
		if _, now := next.Values[k]; now {
			changed = append(changed, k)
		} else if _, was := cfg.Values[k]; was {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	h.audit(ctx, sessionOf(ctx).AdminID, "addon.settings", "addon", in.ID, map[string]any{"enabled": enabled, "changed": changed})
	return h.listAddons(ctx, nil)
}

// addonField is one setting as the admin form needs it. A secret's value never leaves; a
// type the panel does not know is flagged and carries nothing but its name.
func addonField(f addons.Field, values addons.Settings) AddonField {
	val, set := values[f.Key]
	out := AddonField{Key: f.Key, Label: orEmpty(f.Label), Type: f.Type, Secret: f.Secret, Required: f.Required, Set: set, Help: orEmpty(f.Help)}
	if !addons.KnownField(f.Type) {
		out.Unsupported = true
		return out
	}
	switch f.Type {
	case addons.FieldString:
		out.Pattern = f.Pattern
	case addons.FieldSelect:
		out.Options = make([]AddonOption, 0, len(f.Options))
		for _, o := range f.Options {
			out.Options = append(out.Options, AddonOption{Value: o.Value, Label: orEmpty(o.Label)})
		}
	case addons.FieldNumber:
		out.Min, out.Max = f.Min, f.Max
	}
	out.Default = f.Default
	if !f.Secret {
		out.Value = val
	}
	return out
}
