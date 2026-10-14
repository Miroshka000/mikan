package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
)

// How much one addon may keep in its own store (the panel's API for addons, /kv). 256 MiB
// by default; the admin may set another bound, or none at all past a warning: an addon
// that writes without end then fills the server's disk, and a big store bloats every
// backup (a backup to Telegram goes up to 50 MB). Only a session may change it.

type AddonStorageView struct {
	Unlimited  bool  `json:"unlimited" doc:"Без лимита: аддон может занять весь диск сервера"`
	LimitMiB   int64 `json:"limit_mib" doc:"Сколько МиБ один аддон может хранить; при unlimited не действует"`
	DefaultMiB int64 `json:"default_mib" doc:"Лимит по умолчанию"`
}

type addonStorageOutput struct{ Body AddonStorageView }

type addonStorageInput struct {
	Body struct {
		Unlimited bool  `json:"unlimited,omitempty"`
		LimitMiB  int64 `json:"limit_mib,omitempty" minimum:"1" maximum:"1048576" doc:"1 МиБ … 1 ТиБ; при unlimited не нужен"`
	}
}

func (h *handlers) registerAddonStorage() {
	op := func(id, method, summary string) huma.Operation {
		return sessionOps("addons")(id, method, "/api/v1/addon-storage", summary, 0)
	}
	huma.Register(h.api, op("get-addon-storage", http.MethodGet, "Сколько аддон может хранить в своём хранилище"), h.getAddonStorage)
	huma.Register(h.api, op("set-addon-storage", http.MethodPut, "Лимит хранилища аддона или без лимита"), h.setAddonStorage)
}

func (h *handlers) addonStorage(ctx context.Context) (AddonStorageView, error) {
	mib, unlimited, err := panelapi.KVLimitMiB(ctx, h.d.Settings)
	if unlimited {
		mib = panelapi.DefaultKVMiB // what the field offers when the bound is put back
	}
	return AddonStorageView{Unlimited: unlimited, LimitMiB: mib, DefaultMiB: panelapi.DefaultKVMiB}, err
}

func (h *handlers) getAddonStorage(ctx context.Context, _ *struct{}) (*addonStorageOutput, error) {
	v, err := h.addonStorage(ctx)
	if err != nil {
		return nil, err
	}
	return &addonStorageOutput{Body: v}, nil
}

func (h *handlers) setAddonStorage(ctx context.Context, in *addonStorageInput) (*addonStorageOutput, error) {
	mib := in.Body.LimitMiB
	switch {
	case in.Body.Unlimited:
		mib = 0
	case mib < 1:
		return nil, huma.Error422UnprocessableEntity("limit_required", &huma.ErrorDetail{Location: "body.limit_mib", Message: "limit_required"})
	}
	if err := settings.Set(ctx, h.d.Settings, panelapi.KVLimitKey, mib); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "addon.storage", "settings", panelapi.KVLimitKey, map[string]any{"limit_mib": mib, "unlimited": in.Body.Unlimited})
	return h.getAddonStorage(ctx, nil)
}
