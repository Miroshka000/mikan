package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subsrc"
	submodel "mikan/internal/sub/model"
)

// The subscription templates: the admin's own profiles of the cores (internal/sub/<core>)
// the panel puts the user's servers into, a default of each core and the ones the response
// rules name; and the preview of one before it is saved. Every change is the session's: it
// decides what every subscriber's app is given. A template may hold the admin's own
// servers, a controller secret or DNS tokens, so a key or an operator that is not shown
// secrets reads no text.

// SubTemplateHead is a template as the list shows it.
type SubTemplateHead struct {
	ID        int64     `json:"id"`
	Core      string    `json:"core" enum:"mihomo,singbox,xray" doc:"Ядро: mihomo (Clash, YAML), singbox или xray (JSON)"`
	Name      string    `json:"name"`
	Default   bool      `json:"default" doc:"Приложения этого ядра получают его, если правило ответа не выбрало другой"`
	Size      int64     `json:"size" doc:"Размер текста в байтах"`
	UpdatedAt time.Time `json:"updated_at"`
	Rules     []string  `json:"rules" doc:"Правила ответа, которые выбирают этот шаблон"`
}

type SubTemplatesView struct {
	Templates []SubTemplateHead `json:"templates" doc:"По ядрам, в порядке создания"`
	Max       int               `json:"max" doc:"Сколько шаблонов может быть у одного ядра"`
	MaxSize   int               `json:"max_size" doc:"Наибольший размер текста шаблона в байтах"`
}

// SubTemplate is one template with its text.
type SubTemplate struct {
	ID        int64     `json:"id"`
	Core      string    `json:"core" enum:"mihomo,singbox,xray"`
	Name      string    `json:"name"`
	Default   bool      `json:"default"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
	Rules     []string  `json:"rules" doc:"Правила ответа, которые выбирают этот шаблон"`
	Body      string    `json:"body" doc:"Текст шаблона; пусто, когда он скрыт"`
	Hidden    bool      `json:"hidden" doc:"Текст скрыт от этого ключа или оператора: в шаблоне могут быть свои серверы и секреты"`
}

type subTemplatesOutput struct{ Body SubTemplatesView }

type subTemplateOutput struct{ Body SubTemplate }

type subTemplateIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createSubTemplateInput struct {
	Body struct {
		Core    string `json:"core" enum:"mihomo,singbox,xray"`
		Name    string `json:"name" minLength:"1" maxLength:"80"`
		Body    string `json:"body" maxLength:"524288" doc:"Конфиг ядра без серверов: YAML для mihomo, JSON для sing-box и Xray"`
		Default bool   `json:"default,omitempty" doc:"Сделать шаблоном ядра по умолчанию"`
	}
}

type patchSubTemplateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name    *string `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Body    *string `json:"body,omitempty" maxLength:"524288"`
		Default *bool   `json:"default,omitempty" doc:"true — шаблон ядра по умолчанию, false — у ядра снова встроенный профиль"`
	}
}

type previewSubTemplateInput struct {
	Body struct {
		Core    string `json:"core" enum:"mihomo,singbox,xray"`
		Body    string `json:"body,omitempty" maxLength:"524288" doc:"Шаблон; пусто — встроенный профиль ядра"`
		Starter bool   `json:"starter,omitempty" doc:"Начало нового шаблона вместо профиля: встроенный профиль Clash, где группы просят серверы сами, или пример для sing-box и Xray"`
	}
}

type previewSubTemplateOutput struct {
	Body struct {
		Profile string          `json:"profile" doc:"Что получит пользователь со всеми подключениями: профиль Clash (YAML), конфиг sing-box или список конфигов Xray (JSON); ключи — заглушки"`
		Notes   []submodel.Note `json:"notes" doc:"Что sing-box и Xray делают с группами шаблона Clash, которых у них нет как есть"`
	}
}

func (h *handlers) registerSubTemplates() {
	tags := []string{"sub-templates"}
	op := sessionOps("sub-templates")
	huma.Register(h.api, huma.Operation{OperationID: "list-sub-templates", Method: http.MethodGet, Path: "/api/v1/sub-templates", Summary: "Шаблоны подписки по ядрам", Tags: tags}, h.listSubTemplates)
	huma.Register(h.api, huma.Operation{OperationID: "get-sub-template", Method: http.MethodGet, Path: "/api/v1/sub-templates/{id}", Summary: "Шаблон подписки с текстом", Tags: tags}, h.getSubTemplate)
	huma.Register(h.api, op("create-sub-template", http.MethodPost, "/api/v1/sub-templates", "Новый шаблон подписки", http.StatusCreated), h.createSubTemplate)
	huma.Register(h.api, op("update-sub-template", http.MethodPatch, "/api/v1/sub-templates/{id}", "Изменить шаблон подписки или сделать его шаблоном по умолчанию", 0), h.patchSubTemplate)
	huma.Register(h.api, op("delete-sub-template", http.MethodDelete, "/api/v1/sub-templates/{id}", "Удалить шаблон подписки", http.StatusNoContent), h.deleteSubTemplate)
	huma.Register(h.api, huma.Operation{OperationID: "preview-sub-template", Method: http.MethodPost, Path: "/api/v1/sub-templates/preview", Summary: "Профиль из шаблона, без сохранения",
		Description: "Ничего не меняет: показывает, что получит пользователь со всеми подключениями.", Tags: tags}, h.previewSubTemplate)
}

// templateRules are the names of the response rules that name each template.
func (h *handlers) templateRules(ctx context.Context) (map[int64][]string, error) {
	rules, err := subsrc.LoadRules(ctx, h.d.Store.Q)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for _, r := range rules {
		if r.Do.Template != 0 {
			out[r.Do.Template] = append(out[r.Do.Template], r.Name)
		}
	}
	return out, nil
}

func (h *handlers) listSubTemplates(ctx context.Context, _ *struct{}) (*subTemplatesOutput, error) {
	rows, err := h.d.Store.Q.ListSubTemplateHeads(ctx)
	if err != nil {
		return nil, err
	}
	named, err := h.templateRules(ctx)
	if err != nil {
		return nil, err
	}
	v := SubTemplatesView{Templates: make([]SubTemplateHead, 0, len(rows)), Max: subsrc.MaxTemplates, MaxSize: subsrc.MaxTemplateSize}
	for _, r := range rows {
		v.Templates = append(v.Templates, SubTemplateHead{ID: r.ID, Core: r.Core, Name: r.Name, Default: r.IsDefault, Size: r.Size,
			UpdatedAt: store.Time(r.UpdatedAt), Rules: rulesOf(named, r.ID)})
	}
	return &subTemplatesOutput{Body: v}, nil
}

func rulesOf(named map[int64][]string, id int64) []string {
	if r := named[id]; r != nil {
		return r
	}
	return []string{}
}

// subTemplateView is row as the API gives it: without its text where secrets are hidden.
func (h *handlers) subTemplateView(ctx context.Context, row db.SubTemplate) (SubTemplate, error) {
	named, err := h.templateRules(ctx)
	if err != nil {
		return SubTemplate{}, err
	}
	v := SubTemplate{ID: row.ID, Core: row.Core, Name: row.Name, Default: row.IsDefault, Size: int64(len(row.Body)), UpdatedAt: store.Time(row.UpdatedAt),
		Rules: rulesOf(named, row.ID), Body: row.Body}
	if hidesSecrets(ctx) {
		v.Body, v.Hidden = "", true
	}
	return v, nil
}

func (h *handlers) getSubTemplate(ctx context.Context, in *subTemplateIDInput) (*subTemplateOutput, error) {
	row, err := h.d.Store.Q.GetSubTemplate(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	v, err := h.subTemplateView(ctx, row)
	if err != nil {
		return nil, err
	}
	return &subTemplateOutput{Body: v}, nil
}

// checkSubTemplate refuses a template of core that would break the profiles, at the field
// body. With no inbound to check it against, it goes unchecked: the profiles check it again
// for each user, and one that does not render gives the built-in profile.
func (h *handlers) checkSubTemplate(ctx context.Context, core submodel.Core, body string) error {
	if strings.TrimSpace(body) == "" {
		return fieldErr("body.body", "template_empty")
	}
	if h.d.CheckTemplate == nil {
		return nil
	}
	err := h.d.CheckTemplate(ctx, core, body)
	var te *submodel.TemplateError
	switch {
	case errors.As(err, &te):
		return fieldErrValue("body.body", te.Code, te.Detail)
	case errors.Is(err, submodel.ErrNoProxies):
		return nil
	}
	return err
}

// setTemplateDefault makes the template of core the core's default (on), or leaves the core
// without one when it was (off), in the caller's transaction.
func setTemplateDefault(ctx context.Context, q *db.Queries, row db.SubTemplate, on bool) error {
	if !on && !row.IsDefault {
		return nil
	}
	if err := q.ClearSubTemplateDefault(ctx, row.Core); err != nil {
		return err
	}
	if !on {
		return nil
	}
	return q.SetSubTemplateDefault(ctx, row.ID)
}

func (h *handlers) createSubTemplate(ctx context.Context, in *createSubTemplateInput) (*subTemplateOutput, error) {
	core, name := submodel.Core(in.Body.Core), strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, fieldErr("body.name", "template_name")
	}
	if err := h.checkSubTemplate(ctx, core, in.Body.Body); err != nil {
		return nil, err
	}
	var row db.SubTemplate
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		n, err := q.CountSubTemplates(ctx, in.Body.Core)
		if err != nil {
			return err
		}
		if n >= subsrc.MaxTemplates {
			return huma.Error409Conflict("template_limit", &huma.ErrorDetail{Message: "template_limit", Value: subsrc.MaxTemplates})
		}
		if row, err = q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: in.Body.Core, Name: name, Body: in.Body.Body, At: h.d.Now().Unix()}); err != nil {
			return err
		}
		if !in.Body.Default {
			return nil
		}
		if err := setTemplateDefault(ctx, q, row, true); err != nil {
			return err
		}
		row.IsDefault = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The subscriptions' config holds the templates: built again on the next fetch.
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.create", "sub_template", strconv.FormatInt(row.ID, 10), map[string]any{"core": row.Core, "name": row.Name, "default": row.IsDefault})
	v, err := h.subTemplateView(ctx, row)
	if err != nil {
		return nil, err
	}
	return &subTemplateOutput{Body: v}, nil
}

func (h *handlers) patchSubTemplate(ctx context.Context, in *patchSubTemplateInput) (*subTemplateOutput, error) {
	row, err := h.d.Store.Q.GetSubTemplate(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	b := in.Body
	name, body := row.Name, row.Body
	if b.Name != nil {
		if name = strings.TrimSpace(*b.Name); name == "" {
			return nil, fieldErr("body.name", "template_name")
		}
	}
	if b.Body != nil {
		body = *b.Body
		if err := h.checkSubTemplate(ctx, submodel.Core(row.Core), body); err != nil {
			return nil, err
		}
	}
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		if b.Name != nil || b.Body != nil {
			if row, err = q.UpdateSubTemplate(ctx, db.UpdateSubTemplateParams{ID: row.ID, Name: name, Body: body, UpdatedAt: h.d.Now().Unix()}); err != nil {
				return err
			}
		}
		if b.Default != nil {
			if err := setTemplateDefault(ctx, q, row, *b.Default); err != nil {
				return err
			}
			row.IsDefault = *b.Default
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.update", "sub_template", strconv.FormatInt(row.ID, 10),
		map[string]any{"core": row.Core, "name": row.Name, "default": row.IsDefault, "body": b.Body != nil})
	v, err := h.subTemplateView(ctx, row)
	if err != nil {
		return nil, err
	}
	return &subTemplateOutput{Body: v}, nil
}

func (h *handlers) deleteSubTemplate(ctx context.Context, in *subTemplateIDInput) (*struct{}, error) {
	named, err := h.templateRules(ctx)
	if err != nil {
		return nil, err
	}
	// A rule that named a template gone would be left out of the policy whole, with its
	// headers and its format: the admin changes the rule first.
	if rules := named[in.ID]; len(rules) > 0 {
		return nil, huma.Error409Conflict("template_in_use", &huma.ErrorDetail{Location: "path.id", Message: "template_in_use", Value: strings.Join(rules, ", ")})
	}
	n, err := h.d.Store.Q.DeleteSubTemplate(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.delete", "sub_template", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) previewSubTemplate(ctx context.Context, in *previewSubTemplateInput) (*previewSubTemplateOutput, error) {
	if h.d.TemplatePreview == nil {
		return nil, huma.Error503ServiceUnavailable("preview_unavailable")
	}
	pv, err := h.d.TemplatePreview(ctx, subsrc.TemplatePreview{Core: submodel.Core(in.Body.Core), Body: in.Body.Body, Starter: in.Body.Starter})
	if errors.Is(err, submodel.ErrNoProxies) {
		return nil, huma.Error409Conflict("no_proxies")
	}
	var te *submodel.TemplateError
	if errors.As(err, &te) {
		return nil, fieldErrValue("body.body", te.Code, te.Detail)
	}
	if err != nil {
		return nil, err
	}
	out := &previewSubTemplateOutput{}
	out.Body.Profile, out.Body.Notes = string(pv.Profile), pv.Notes
	if out.Body.Notes == nil {
		out.Body.Notes = []submodel.Note{}
	}
	return out, nil
}
