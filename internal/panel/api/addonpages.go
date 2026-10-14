package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/pages"
)

// The admin pages of an addon of protocol 2 (docs/pipeline/addons.md, section 9). The page
// runs in a sandboxed frame without the admin's cookie: the panel gives it a signed address
// for its files, and its calls come through the admin panel's own window, which posts them
// here with the session. Only a session: an API key has no page to show.

type AddonPageView struct {
	URL       string            `json:"url" doc:"Адрес страницы относительно адреса админки (addons-ui/<id>/<подпись>/…); подпись действует 10 минут"`
	Title     map[string]string `json:"title" doc:"Название страницы по языкам"`
	ExpiresAt int64             `json:"expires_at" doc:"Unix-время, после которого адрес не откроет файлы страницы"`
}

type addonPageOutput struct{ Body AddonPageView }

type addonRPCInput struct {
	AddonIDInput
	Body struct {
		Method string          `json:"method" enum:"GET,POST,PUT,DELETE" doc:"Метод для аддона; GET не пишется в журнал"`
		Path   string          `json:"path" minLength:"1" maxLength:"1024" pattern:"^/[^\\x00-\\x1f\\x7f]*$" doc:"Путь для аддона, начинается с /"`
		Body   json.RawMessage `json:"body,omitempty" doc:"Любой JSON для аддона"`
		Lang   string          `json:"lang,omitempty" enum:"ru,en" doc:"Язык админки; пусто — язык панели"`
	}
}

type AddonRPCResult struct {
	Status int             `json:"status" doc:"Код ответа аддона: ошибки аддона тоже приходят сюда"`
	Body   json.RawMessage `json:"body" doc:"JSON аддона как есть, до 1 МиБ"`
}

type addonRPCOutput struct{ Body AddonRPCResult }

func (h *handlers) registerAddonPages() {
	op := sessionOps("addons")
	huma.Register(h.api, op("get-addon-page", http.MethodGet, "/api/v1/addons/{id}/ui", "Подписанный адрес страницы аддона в админке", 0), h.addonPage)
	huma.Register(h.api, op("call-addon-page", http.MethodPost, "/api/v1/addons/{id}/ui/rpc", "Вызов страницы аддона: панель передаёт его аддону", 0), h.callAddonPage)
}

// pageErr turns what the pages refused into the API's codes.
func pageErr(err error) error {
	switch {
	case errors.Is(err, pages.ErrNoPages):
		return huma.Error404NotFound("addon_no_pages")
	case errors.Is(err, pages.ErrForbidden):
		return huma.Error403Forbidden("addon_ui_forbidden")
	case errors.Is(err, addons.ErrBadAnswer):
		return huma.Error502BadGateway("addon_bad_answer")
	}
	var ae *addons.Error
	if errors.Is(err, addons.ErrUnreachable) || errors.As(err, &ae) {
		return huma.Error502BadGateway("addon_unreachable")
	}
	return err
}

func (h *handlers) addonPage(ctx context.Context, in *AddonIDInput) (*addonPageOutput, error) {
	ui, err := h.d.Pages.Open(ctx, in.ID)
	if err != nil {
		if !errors.Is(err, pages.ErrNoPages) && !errors.Is(err, pages.ErrForbidden) {
			h.d.Log.Warn("addon pages: open", "addon", in.ID, "err", err)
		}
		return nil, pageErr(err)
	}
	url, exp := h.d.Pages.URL(in.ID, ui, sessionOf(ctx))
	title := ui.Title
	if title == nil {
		title = addons.Text{}
	}
	return &addonPageOutput{Body: AddonPageView{URL: url, Title: title, ExpiresAt: exp.Unix()}}, nil
}

// callAddonPage forwards a call of the page to the addon. What changes something is written
// to the audit log by its method and path; bodies stay out of it.
func (h *handlers) callAddonPage(ctx context.Context, in *addonRPCInput) (*addonRPCOutput, error) {
	a, err := h.d.Pages.Allowed(ctx, in.ID)
	if err != nil {
		return nil, pageErr(err)
	}
	lang := in.Body.Lang
	if lang == "" {
		lang, _ = h.d.Settings.Lang(ctx)
	}
	status, body, err := h.d.Addons.CallUI(ctx, a, addons.RPC{Method: in.Body.Method, Path: in.Body.Path, Body: in.Body.Body, Admin: addons.RPCAdmin{Lang: lang}})
	if in.Body.Method != http.MethodGet {
		details := map[string]any{"method": in.Body.Method, "path": in.Body.Path}
		if err != nil {
			details["failed"] = true
		} else {
			details["status"] = status
		}
		h.audit(ctx, sessionOf(ctx).AdminID, "addon.ui_call", "addon", in.ID, details)
	}
	if err != nil {
		h.d.Log.Warn("addon pages: call", "addon", in.ID, "err", err)
		return nil, pageErr(err)
	}
	return &addonRPCOutput{Body: AddonRPCResult{Status: status, Body: body}}, nil
}
