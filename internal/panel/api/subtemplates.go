package api

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"mikan/internal/sub/ready"
	"mikan/internal/sub/routing"
)

// The subscription templates: the admin's own profiles of the cores (internal/sub/<core>)
// the panel puts the user's servers into, a default of each core and the ones the response
// rules name; and the preview of one before it is saved. Every change is the session's: it
// decides what every subscriber's app is given. A template may hold the admin's own
// servers, a controller secret or DNS tokens, so a key or an operator that is not shown
// secrets reads no text, and no routing of a simple one either: its lists and DNS are
// links of the admin's.
//
// A template is of text (body: the core's config) or simple (routing: the built-in profile of
// every core made by a routing of its own, kept as a Clash template). What a create or a change
// sends decides which: a body makes a template of text, a routing a simple one, never both.

// The kinds of a template, as the API names them.
const (
	templateText   = "text"
	templateSimple = "simple"
)

func templateKind(simple bool) string {
	if simple {
		return templateSimple
	}
	return templateText
}

// SubTemplateHead is a template as the list shows it.
type SubTemplateHead struct {
	ID        int64     `json:"id"`
	Core      string    `json:"core" enum:"mihomo,singbox,xray" doc:"Ядро: mihomo (Clash, YAML), singbox или xray (JSON); у простого шаблона mihomo"`
	Kind      string    `json:"kind" enum:"text,simple" doc:"text — свой конфиг ядра текстом, simple — простой: своя маршрутизация вместо текста, профиль для всех приложений"`
	Name      string    `json:"name"`
	Default   bool      `json:"default" doc:"Приложения этого ядра получают его, если правило ответа не выбрало другой"`
	Size      int64     `json:"size" doc:"Размер текста в байтах; 0 у простого шаблона"`
	UpdatedAt time.Time `json:"updated_at"`
	Rules     []string  `json:"rules" doc:"Правила ответа, которые выбирают этот шаблон"`
}

type SubTemplatesView struct {
	Templates []SubTemplateHead `json:"templates" doc:"По ядрам, в порядке создания"`
	Max       int               `json:"max" doc:"Сколько шаблонов может быть у одного ядра"`
	MaxSize   int               `json:"max_size" doc:"Наибольший размер текста шаблона в байтах"`
}

// SubTemplate is one template with its text, or its routing when it is a simple one.
type SubTemplate struct {
	ID        int64     `json:"id"`
	Core      string    `json:"core" enum:"mihomo,singbox,xray"`
	Kind      string    `json:"kind" enum:"text,simple"`
	Name      string    `json:"name"`
	Default   bool      `json:"default"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
	Rules     []string  `json:"rules" doc:"Правила ответа, которые выбирают этот шаблон"`
	Body      string    `json:"body" doc:"Текст шаблона; пусто у простого и когда он скрыт"`
	// Routing is nil for a template of text, and where secrets are hidden.
	Routing *subsrc.TemplateRouting `json:"routing" doc:"Маршрутизация простого шаблона; null у шаблона текстом и когда она скрыта"`
	Hidden  bool                    `json:"hidden" doc:"Текст и маршрутизация скрыты от этого ключа или оператора: в шаблоне могут быть свои серверы и секреты"`
}

type subTemplatesOutput struct{ Body SubTemplatesView }

type subTemplateOutput struct{ Body SubTemplate }

type subTemplateIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

// The bodies' maxLength is subsrc.MaxTemplateSize, which huma counts in runes: a coarse first
// filter. checkTemplateSize counts the bytes the limit is in.
type createSubTemplateInput struct {
	Body struct {
		Core    string                  `json:"core" enum:"mihomo,singbox,xray" doc:"Ядро; простой шаблон — mihomo"`
		Name    string                  `json:"name" minLength:"1" maxLength:"80"`
		Body    string                  `json:"body,omitempty" maxLength:"524288" doc:"Конфиг ядра без серверов: YAML для mihomo, JSON для sing-box и Xray. Вместе с routing не передаётся"`
		Routing *subsrc.TemplateRouting `json:"routing,omitempty" doc:"Простой шаблон вместо body: своя маршрутизация, из которой собирается встроенный профиль каждого ядра"`
		Default bool                    `json:"default,omitempty" doc:"Сделать шаблоном ядра по умолчанию"`
	}
}

type patchSubTemplateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name    *string                 `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Body    *string                 `json:"body,omitempty" maxLength:"524288" doc:"Новый текст; простой шаблон с ним становится шаблоном текстом"`
		Routing *subsrc.TemplateRouting `json:"routing,omitempty" doc:"Новая маршрутизация; шаблон Clash текстом с ней становится простым. Вместе с body не передаётся"`
		Default *bool                   `json:"default,omitempty" doc:"true — шаблон ядра по умолчанию, false — у ядра снова встроенный профиль"`
	}
}

type previewSubTemplateInput struct {
	Body struct {
		Core    string                  `json:"core" enum:"mihomo,singbox,xray" doc:"Ядро профиля; для простого шаблона — любое: Clash, sing-box или Xray"`
		Body    string                  `json:"body,omitempty" maxLength:"524288" doc:"Шаблон; пусто — встроенный профиль ядра"`
		Routing *subsrc.TemplateRouting `json:"routing,omitempty" doc:"Маршрутизация простого шаблона вместо body: встроенный профиль ядра, собранный из неё"`
		Starter bool                    `json:"starter,omitempty" doc:"Начало нового шаблона вместо профиля: встроенный профиль Clash, где группы просят серверы сами (с routing — из этой маршрутизации), или пример для sing-box и Xray"`
	}
}

type previewSubTemplateOutput struct {
	Body struct {
		Profile string          `json:"profile" doc:"Что получит пользователь со всеми подключениями: профиль Clash (YAML), конфиг sing-box или список конфигов Xray (JSON); ключи — заглушки"`
		Notes   []submodel.Note `json:"notes" doc:"Что sing-box и Xray делают с шаблоном Clash, которого не могут передать как есть: ближайший вид группы; сервер, правило, список, политика DNS или категория, которые они опускают"`
	}
}

// ReadySubTemplate is a template of the ready catalog (internal/sub/ready), to load into the
// editor: loading saves nothing.
type ReadySubTemplate struct {
	ID      string `json:"id"`
	Core    string `json:"core" enum:"mihomo,singbox,xray"`
	Name    string `json:"name"`
	Summary string `json:"summary" doc:"Описание по-русски; панель переводит его по id"`
	Author  string `json:"author"`
	License string `json:"license" doc:"Лицензия текста шаблона, SPDX"`
	Source  string `json:"source" doc:"Откуда шаблон и списки, на которые он ссылается"`
	Builtin bool   `json:"builtin" doc:"Свой шаблон mikan: панель собирает его из своих настроек, когда его загружают"`
}

type readySubTemplatesOutput struct {
	Body struct {
		Templates []ReadySubTemplate `json:"templates" doc:"По ядрам, в порядке каталога"`
	}
}

type readySubTemplateInput struct {
	ID string `path:"id" maxLength:"64"`
}

type readySubTemplateOutput struct {
	Body struct {
		ReadySubTemplate
		Text string `json:"body" doc:"Текст шаблона: для своего шаблона mikan собран из текущих настроек панели"`
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
	huma.Register(h.api, huma.Operation{OperationID: "list-ready-sub-templates", Method: http.MethodGet, Path: "/api/v1/sub-templates/ready", Summary: "Готовые шаблоны подписки",
		Description: "Каталог, из которого редактор загружает шаблон. В базу панель их сама не кладёт.", Tags: tags}, h.listReadySubTemplates)
	huma.Register(h.api, huma.Operation{OperationID: "get-ready-sub-template", Method: http.MethodGet, Path: "/api/v1/sub-templates/ready/{id}", Summary: "Готовый шаблон подписки с текстом",
		Description: "Ничего не сохраняет: текст для редактора.", Tags: tags}, h.getReadySubTemplate)
}

func readyView(r ready.Template) ReadySubTemplate {
	return ReadySubTemplate{ID: r.ID, Core: string(r.Core), Name: r.Name, Summary: r.Summary, Author: r.Author, License: r.License, Source: r.Source, Builtin: r.Starter}
}

func (h *handlers) listReadySubTemplates(context.Context, *struct{}) (*readySubTemplatesOutput, error) {
	out := &readySubTemplatesOutput{}
	out.Body.Templates = []ReadySubTemplate{}
	for _, r := range ready.All() {
		out.Body.Templates = append(out.Body.Templates, readyView(r))
	}
	return out, nil
}

// getReadySubTemplate is a ready template's text; mikan's own is the starter the editor's
// own button gives, made of the panel's settings now.
func (h *handlers) getReadySubTemplate(ctx context.Context, in *readySubTemplateInput) (*readySubTemplateOutput, error) {
	r, ok := ready.Get(in.ID)
	if !ok {
		return nil, huma.Error404NotFound("not_found")
	}
	out := &readySubTemplateOutput{}
	out.Body.ReadySubTemplate, out.Body.Text = readyView(r), r.Body
	if !r.Starter {
		return out, nil
	}
	if h.d.TemplatePreview == nil {
		return nil, huma.Error503ServiceUnavailable("preview_unavailable")
	}
	pv, err := h.d.TemplatePreview(ctx, subsrc.TemplatePreview{Core: r.Core, Starter: true})
	if errors.Is(err, submodel.ErrNoProxies) {
		return nil, huma.Error409Conflict("no_proxies")
	}
	if err != nil {
		return nil, err
	}
	out.Body.Text = string(pv.Profile)
	return out, nil
}

// templateRules are the names of the response rules that name each template, read through q
// (a transaction's, when the answer decides a change).
func templateRules(ctx context.Context, q *db.Queries) (map[int64][]string, error) {
	rules, err := subsrc.LoadRules(ctx, q)
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
	named, err := templateRules(ctx, h.d.Store.Q)
	if err != nil {
		return nil, err
	}
	v := SubTemplatesView{Templates: make([]SubTemplateHead, 0, len(rows)), Max: subsrc.MaxTemplates, MaxSize: subsrc.MaxTemplateSize}
	for _, r := range rows {
		v.Templates = append(v.Templates, SubTemplateHead{ID: r.ID, Core: r.Core, Kind: templateKind(r.Simple), Name: r.Name, Default: r.IsDefault, Size: r.Size,
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

// subTemplateView is row as the API gives it: without its text or routing where secrets are
// hidden.
func (h *handlers) subTemplateView(ctx context.Context, row db.SubTemplate) (SubTemplate, error) {
	named, err := templateRules(ctx, h.d.Store.Q)
	if err != nil {
		return SubTemplate{}, err
	}
	v := SubTemplate{ID: row.ID, Core: row.Core, Kind: templateKind(row.Routing.Valid), Name: row.Name, Default: row.IsDefault, Size: int64(len(row.Body)),
		UpdatedAt: store.Time(row.UpdatedAt), Rules: rulesOf(named, row.ID), Body: row.Body}
	if row.Routing.Valid {
		r := subsrc.TemplateRoutingOf(row.Routing.String)
		v.Routing = &r
	}
	if hidesSecrets(ctx) {
		v.Body, v.Routing, v.Hidden = "", nil, true
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
	if err := checkTemplateSize(body); err != nil {
		return err
	}
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

// checkTemplateSize refuses a text over subsrc.MaxTemplateSize bytes, at the field body: the
// tags' maxLength lets through that many runes, and a text in Cyrillic is twice as many bytes.
func checkTemplateSize(body string) error {
	if len(body) > subsrc.MaxTemplateSize {
		return fieldErrValue("body.body", "template_size", subsrc.MaxTemplateSize)
	}
	return nil
}

// oneContent refuses a body and a routing sent together: a template is of text or simple.
func oneContent(body string, r *subsrc.TemplateRouting) error {
	if r != nil && body != "" {
		return fieldErr("body.routing", "template_body_or_routing")
	}
	return nil
}

// checkTemplateRoutes refuses the routes of a simple template that would break the profiles, as
// the setting sub_routes is refused (routing.Routes.Check).
func (h *handlers) checkTemplateRoutes(ctx context.Context, r routing.Routes) error {
	if err := r.Check(h.nodeExists(ctx)); err != nil {
		return fieldErr("body.routing.routes", err.Error())
	}
	return nil
}

// templateRouting checks the routing r of a simple template of core with the checks the
// settings it stands for take (sub_routes, sub_rules), at the field of body.routing it is
// about, and gives it as the row keeps it: the rules without the blank end the settings drop.
func (h *handlers) templateRouting(ctx context.Context, core submodel.Core, r subsrc.TemplateRouting) (sql.NullString, error) {
	if core != submodel.CoreMihomo {
		return sql.NullString{}, fieldErr("body.core", "template_simple_core")
	}
	if err := h.checkTemplateRoutes(ctx, r.Routes); err != nil {
		return sql.NullString{}, err
	}
	g, err := subsrc.LoadGroups(ctx, h.d.Settings)
	if err != nil {
		return sql.NullString{}, err
	}
	r.Rules = strings.TrimRight(r.Rules, " \n\r\t")
	if _, err := routing.ParseRules(r.Rules, g); err != nil {
		var re *routing.RuleError
		if !errors.As(err, &re) {
			return sql.NullString{}, err
		}
		return sql.NullString{}, fieldErrValue("body.routing.rules", re.Code, re.Line)
	}
	doc, err := json.Marshal(r)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{Valid: true, String: string(doc)}, nil
}

// groupInTemplateRules refuses the groups cur renamed to next (main: the main one is renamed,
// else the automatic one) when a rule of a simple template that reads now sends to a name that
// goes: the rule would point nowhere, as the settings' own rules are kept from (group_in_rules).
// The value names the templates; nil when no rule does.
func (h *handlers) groupInTemplateRules(ctx context.Context, cur, next submodel.GroupNames, main bool) (*huma.ErrorDetail, error) {
	rows, err := h.d.Store.Q.ListSimpleSubTemplates(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range rows {
		rules := subsrc.TemplateRoutingOf(r.Routing.String).Rules
		if _, err := routing.ParseRules(rules, cur); err != nil {
			continue // broken before the rename: not this change's to refuse
		}
		if _, err := routing.ParseRules(rules, next); err != nil {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	field := "body.sub_group_auto"
	if main {
		field = "body.sub_group_main"
	}
	return fieldDetail(field, "group_in_template_rules", strings.Join(names, ", ")), nil
}

// templateContent is what a template of core holds as a create or a change sends it: a body
// (checkSubTemplate) or a routing (templateRouting), never both.
func (h *handlers) templateContent(ctx context.Context, core submodel.Core, body *string, r *subsrc.TemplateRouting) (string, sql.NullString, error) {
	var text string
	if body != nil {
		text = *body
	}
	if err := oneContent(text, r); err != nil {
		return "", sql.NullString{}, err
	}
	if r == nil {
		return text, sql.NullString{}, h.checkSubTemplate(ctx, core, text)
	}
	doc, err := h.templateRouting(ctx, core, *r)
	return "", doc, err
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
	body, doc, err := h.templateContent(ctx, core, &in.Body.Body, in.Body.Routing)
	if err != nil {
		return nil, err
	}
	var row db.SubTemplate
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		// Two creates at once must not both count 19 and make 21.
		if err := q.LockSubTemplates(ctx); err != nil {
			return err
		}
		n, err := q.CountSubTemplates(ctx, in.Body.Core)
		if err != nil {
			return err
		}
		if n >= subsrc.MaxTemplates {
			return huma.Error409Conflict("template_limit", &huma.ErrorDetail{Message: "template_limit", Value: subsrc.MaxTemplates})
		}
		if row, err = q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: in.Body.Core, Name: name, Body: body, Routing: doc, At: h.d.Now().Unix()}); err != nil {
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
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.create", "sub_template", strconv.FormatInt(row.ID, 10),
		map[string]any{"core": row.Core, "kind": templateKind(row.Routing.Valid), "name": row.Name, "default": row.IsDefault})
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
	name, body, doc := row.Name, row.Body, row.Routing
	wasSimple := row.Routing.Valid
	if b.Name != nil {
		if name = strings.TrimSpace(*b.Name); name == "" {
			return nil, fieldErr("body.name", "template_name")
		}
	}
	// A body makes the template one of text, a routing a simple one, whatever it was.
	content := b.Body != nil || b.Routing != nil
	if content {
		if body, doc, err = h.templateContent(ctx, submodel.Core(row.Core), b.Body, b.Routing); err != nil {
			return nil, err
		}
	}
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		// Two "make default" at once would both clear the old default and one would fail on
		// the index of one default per core.
		if err := q.LockSubTemplates(ctx); err != nil {
			return err
		}
		if b.Name != nil || content {
			if row, err = q.UpdateSubTemplate(ctx, db.UpdateSubTemplateParams{ID: row.ID, Name: name, Body: body, Routing: doc, UpdatedAt: h.d.Now().Unix()}); err != nil {
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
	audit := map[string]any{"core": row.Core, "kind": templateKind(row.Routing.Valid), "name": row.Name, "default": row.IsDefault, "body": b.Body != nil, "routing": b.Routing != nil}
	if wasSimple != row.Routing.Valid {
		audit["kind_was"] = templateKind(wasSimple)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.update", "sub_template", strconv.FormatInt(row.ID, 10), audit)
	v, err := h.subTemplateView(ctx, row)
	if err != nil {
		return nil, err
	}
	return &subTemplateOutput{Body: v}, nil
}

func (h *handlers) deleteSubTemplate(ctx context.Context, in *subTemplateIDInput) (*struct{}, error) {
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		// Under the lock the rules saved now are all the rules there are (putSubRules).
		if err := q.LockSubTemplates(ctx); err != nil {
			return err
		}
		named, err := templateRules(ctx, q)
		if err != nil {
			return err
		}
		// A rule that named a template gone would be left out of the policy whole, with its
		// headers and its format: the admin changes the rule first.
		if rules := named[in.ID]; len(rules) > 0 {
			return huma.Error409Conflict("template_in_use", &huma.ErrorDetail{Location: "path.id", Message: "template_in_use", Value: strings.Join(rules, ", ")})
		}
		n, err := q.DeleteSubTemplate(ctx, in.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return huma.Error404NotFound("not_found")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_template.delete", "sub_template", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) previewSubTemplate(ctx context.Context, in *previewSubTemplateInput) (*previewSubTemplateOutput, error) {
	if h.d.TemplatePreview == nil {
		return nil, huma.Error503ServiceUnavailable("preview_unavailable")
	}
	b := in.Body
	if err := checkTemplateSize(b.Body); err != nil {
		return nil, err
	}
	// A simple template's routing is checked as it is while it is typed: the routes, which
	// would break the profile; a rule line that does not parse yet is left out, as the routing
	// page's preview leaves it.
	if err := oneContent(b.Body, b.Routing); err != nil {
		return nil, err
	}
	if b.Routing != nil {
		if err := h.checkTemplateRoutes(ctx, b.Routing.Routes); err != nil {
			return nil, err
		}
	}
	pv, err := h.d.TemplatePreview(ctx, subsrc.TemplatePreview{Core: submodel.Core(b.Core), Body: b.Body, Routing: b.Routing, Starter: b.Starter})
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
