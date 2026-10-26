package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subsrc"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/serve"
)

// How the subscription answers a request (internal/sub/apps): which known apps get the
// servers, what each request is answered by its headers, and the tester that shows it; and
// the log of the requests, in the user's card. Every change is the session's: it decides
// what every subscriber's app is given. A key may read.

// SubAppView is a known app as the admin's list shows it.
type SubAppView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Family  string `json:"family" enum:"mihomo,xray,singbox,stash,other"`
	Generic bool   `json:"generic" doc:"Остальные приложения этого семейства, у которых нет своей строки"`
	Blocked bool   `json:"blocked"`
}

type SubAppsView struct {
	Apps    []SubAppView `json:"apps"`
	Text    string       `json:"text" doc:"Свой текст записи заблокированного приложения; пусто — встроенный"`
	Preview string       `json:"preview" doc:"Что увидит заблокированное приложение: свой текст или встроенный на языке панели"`
}

type subAppsOutput struct{ Body SubAppsView }

type putSubAppsInput struct {
	Body struct {
		Blocked []string `json:"blocked" maxItems:"64" doc:"Идентификаторы заблокированных приложений из списка"`
		Text    string   `json:"text" maxLength:"200" doc:"Один ряд текста: приложение показывает его вместо серверов"`
	}
}

// SubRuleLimits are the bounds of what a rule may hold, for the editor to keep within.
type SubRuleLimits struct {
	Rules   int `json:"rules"`
	Conds   int `json:"conds"`
	Groups  int `json:"groups"`
	Value   int `json:"value"`
	Name    int `json:"name"`
	Headers int `json:"headers"`
	HTML    int `json:"html"`
	Text    int `json:"text"`
}

type SubRulesView struct {
	Rules   []apps.Rule   `json:"rules"`
	Builtin bool          `json:"builtin" doc:"Встроенные правила включены: ShadowsocksR и брошенные приложения Clash получают заглушку"`
	Formats []string      `json:"formats" doc:"Форматы, которые правило может навязать"`
	Limits  SubRuleLimits `json:"limits"`
}

type subRulesOutput struct{ Body SubRulesView }

type putSubRulesInput struct {
	Body struct {
		Rules   []apps.Rule `json:"rules" maxItems:"100"`
		Builtin bool        `json:"builtin"`
	}
}

type testSubRulesInput struct {
	Body struct {
		UserAgent string        `json:"user_agent" maxLength:"1024" doc:"User-Agent приложения"`
		Headers   []apps.Header `json:"headers" maxItems:"30" doc:"Остальные заголовки запроса: X-Device-Os, Accept, свои"`
		Rules     *[]apps.Rule  `json:"rules,omitempty" maxItems:"100" doc:"Правила вместо сохранённых: проверить, пока они не сохранены"`
		Builtin   *bool         `json:"builtin,omitempty" doc:"Встроенные правила вместо сохранённого выбора"`
	}
}

// SubRuleTest is what a request would be answered with.
type SubRuleTest struct {
	AppID    string        `json:"app_id" doc:"Известное приложение по User-Agent; пусто — неизвестное"`
	AppName  string        `json:"app_name"`
	Rule     string        `json:"rule" doc:"Что решило: имя правила, builtin:ssr, builtin:old, apps (список приложений); пусто — ничего, ответ как обычно"`
	Position int           `json:"position" doc:"Место сработавшего правила в списке, с 1; 0 — сработало не правило из списка"`
	Respond  string        `json:"respond" enum:"serve,stub,403,404,html,drop"`
	Status   int           `json:"status" doc:"Код ответа; 0 — соединение закрыто без ответа"`
	Format   string        `json:"format" doc:"Формат профиля или заглушки; пусто, когда профиля в ответе нет"`
	Headers  []apps.Header `json:"headers" doc:"Заголовки, которые добавляет правило"`
	SkipHWID bool          `json:"skip_hwid"`
	Text     string        `json:"text" doc:"Текст заглушки"`
	Template int64         `json:"template" doc:"Шаблон подписки, из которого собран профиль; 0 — встроенный профиль"`
}

type testSubRulesOutput struct{ Body SubRuleTest }

// SubLogItem is one request of a subscription.
type SubLogItem struct {
	At      time.Time `json:"at"`
	IP      string    `json:"ip"`
	Agent   string    `json:"agent" doc:"User-Agent"`
	App     string    `json:"app" doc:"Идентификатор известного приложения; пусто — неизвестное"`
	AppName string    `json:"app_name"`
	Rule    string    `json:"rule" doc:"Что решило: имя правила, builtin:ssr, builtin:old, apps; пусто — ничего"`
	Format  string    `json:"format" doc:"Формат ответа; пусто — запрос отклонён правилом"`
	Outcome string    `json:"outcome" enum:"served,stub,blocked,dropped"`
}

type userSubLogInput struct {
	ID    int64 `path:"id" minimum:"1"`
	Limit int   `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type userSubLogOutput struct {
	Body struct {
		Days  int          `json:"days" doc:"Сколько дней хранится журнал; 0 — журнал выключен"`
		Items []SubLogItem `json:"items" doc:"Новые сверху"`
	}
}

func (h *handlers) registerSubRules() {
	tags := []string{"sub-rules"}
	op := sessionOps("sub-rules")
	huma.Register(h.api, huma.Operation{OperationID: "get-sub-apps", Method: http.MethodGet, Path: "/api/v1/sub-apps", Summary: "Известные приложения: какие получают серверы", Tags: tags}, h.getSubApps)
	huma.Register(h.api, op("put-sub-apps", http.MethodPut, "/api/v1/sub-apps", "Заблокировать или разрешить приложения", 0), h.putSubApps)
	huma.Register(h.api, huma.Operation{OperationID: "get-sub-rules", Method: http.MethodGet, Path: "/api/v1/sub-rules", Summary: "Правила ответа на запрос подписки", Tags: tags}, h.getSubRules)
	huma.Register(h.api, op("put-sub-rules", http.MethodPut, "/api/v1/sub-rules", "Сохранить правила ответа на запрос подписки", 0), h.putSubRules)
	huma.Register(h.api, huma.Operation{OperationID: "test-sub-rules", Method: http.MethodPost, Path: "/api/v1/sub-rules/test", Summary: "Какое правило сработает на такой запрос",
		Description: "Ничего не меняет: правила можно передать ещё не сохранёнными.", Tags: tags, Metadata: supportDiagnostic}, h.testSubRules)
	huma.Register(h.api, huma.Operation{OperationID: "user-sub-log", Method: http.MethodGet, Path: "/api/v1/users/{id}/sub-log", Summary: "Журнал запросов подписки пользователя", Tags: []string{"users"}}, h.userSubLog)
}

func (h *handlers) subAppsView(ctx context.Context) (SubAppsView, error) {
	ids, err := subsrc.LoadBlocked(ctx, h.d.Store.Q)
	if err != nil {
		return SubAppsView{}, err
	}
	text, err := h.d.Settings.String(ctx, settings.KeySubBlockedText)
	if err != nil {
		return SubAppsView{}, err
	}
	lang, err := h.d.Settings.Lang(ctx)
	if err != nil {
		return SubAppsView{}, err
	}
	blocked := map[string]bool{}
	for _, id := range ids {
		blocked[id] = true
	}
	v := SubAppsView{Apps: make([]SubAppView, 0, len(apps.Catalog)), Text: text}
	for _, k := range apps.Catalog {
		v.Apps = append(v.Apps, SubAppView{ID: k.ID, Name: k.Name, Family: string(k.Family), Generic: k.Generic, Blocked: blocked[k.ID]})
	}
	v.Preview = serve.StubText(lang, apps.Decision{Action: apps.Action{Text: text}, Install: apps.Alternatives(blocked, 3)})
	return v, nil
}

func (h *handlers) getSubApps(ctx context.Context, _ *struct{}) (*subAppsOutput, error) {
	v, err := h.subAppsView(ctx)
	if err != nil {
		return nil, err
	}
	return &subAppsOutput{Body: v}, nil
}

func (h *handlers) putSubApps(ctx context.Context, in *putSubAppsInput) (*subAppsOutput, error) {
	for i, id := range in.Body.Blocked {
		if !apps.ValidID(id) {
			return nil, fieldErrValue("body.blocked["+strconv.Itoa(i)+"]", "app_unknown", id)
		}
	}
	text := strings.TrimSpace(in.Body.Text)
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		if err := subsrc.SaveBlocked(ctx, q, in.Body.Blocked); err != nil {
			return err
		}
		return settings.Set(ctx, settings.New(q), settings.KeySubBlockedText, text)
	})
	if err != nil {
		return nil, err
	}
	// The subscriptions' config holds the list: built again on the next fetch.
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_apps.update", "", "", map[string]any{"blocked": in.Body.Blocked})
	return h.getSubApps(ctx, nil)
}

func (h *handlers) getSubRules(ctx context.Context, _ *struct{}) (*subRulesOutput, error) {
	rules, err := subsrc.LoadRules(ctx, h.d.Store.Q)
	if err != nil {
		return nil, err
	}
	on, err := h.d.Settings.On(ctx, settings.SubBuiltin)
	if err != nil {
		return nil, err
	}
	return &subRulesOutput{Body: SubRulesView{Rules: rules, Builtin: on, Formats: serve.Formats, Limits: SubRuleLimits{Rules: apps.MaxRules, Conds: apps.MaxConds,
		Groups: apps.MaxGroups, Value: apps.MaxValue, Name: apps.MaxName, Headers: apps.MaxHeaders, HTML: apps.MaxHTML, Text: apps.MaxText}}}, nil
}

// listIndex is a place in a list as the API words it: rules[2], where apps.CheckError says rules.2.
var listIndex = regexp.MustCompile(`\.(\d+)`)

// checkSubRules refuses the rules that would not work: the first thing wrong is named by its
// place in the body, as the schema names the places of its own errors (rules[2].when.op).
func (h *handlers) checkSubRules(ctx context.Context, rules []apps.Rule, field string) error {
	templates, err := subsrc.TemplateIDs(ctx, h.d.Store.Q)
	if err != nil {
		return err
	}
	err = apps.CheckRules(rules, subsrc.RuleOpts(templates))
	var ce *apps.CheckError
	if errors.As(err, &ce) {
		return fieldErrValue("body."+field+listIndex.ReplaceAllString(strings.TrimPrefix(ce.Path, "rules"), "[$1]"), ce.Code, ce.Detail)
	}
	return err
}

func (h *handlers) putSubRules(ctx context.Context, in *putSubRulesInput) (*subRulesOutput, error) {
	if err := h.checkSubRules(ctx, in.Body.Rules, "rules"); err != nil {
		return nil, err
	}
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		if err := subsrc.SaveRules(ctx, q, in.Body.Rules); err != nil {
			return err
		}
		return settings.Set(ctx, settings.New(q), settings.KeySubBuiltin, in.Body.Builtin)
	})
	if err != nil {
		return nil, err
	}
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_rules.update", "", "", map[string]any{"rules": len(in.Body.Rules), "builtin": in.Body.Builtin})
	return h.getSubRules(ctx, nil)
}

func (h *handlers) testSubRules(ctx context.Context, in *testSubRulesInput) (*testSubRulesOutput, error) {
	req := http.Header{}
	if in.Body.UserAgent != "" {
		req.Set("User-Agent", in.Body.UserAgent)
	}
	for i, hd := range in.Body.Headers {
		if !apps.ValidHeaderName(hd.Name) {
			return nil, fieldErrValue("body.headers["+strconv.Itoa(i)+"].name", "resp_header", hd.Name)
		}
		req.Add(hd.Name, hd.Value)
	}
	p, err := subsrc.LoadPolicy(ctx, h.d.Store.Q, h.d.Settings, nil)
	if err != nil {
		return nil, err
	}
	if in.Body.Rules != nil {
		if err := h.checkSubRules(ctx, *in.Body.Rules, "rules"); err != nil {
			return nil, err
		}
		p.Rules = *in.Body.Rules
	}
	if in.Body.Builtin != nil {
		p.NoBuiltin = !*in.Body.Builtin
	}
	r, err := apps.NewResponder(p)
	if err != nil {
		return nil, fieldErr("body.rules", "resp_regex")
	}
	lang, err := h.d.Settings.Lang(ctx)
	if err != nil {
		return nil, err
	}
	xrayJSON, err := h.d.Settings.On(ctx, settings.XrayJSON)
	if err != nil {
		return nil, err
	}
	d := r.Decide(req)
	out := &testSubRulesOutput{}
	out.Body = SubRuleTest{AppID: d.App.ID, AppName: d.App.Name, Rule: d.Rule, Position: d.Position, Respond: d.Responds(), Headers: []apps.Header{}, SkipHWID: d.Action.SkipHWID}
	if d.Action.Headers != nil {
		out.Body.Headers = d.Action.Headers
	}
	switch d.Responds() {
	case apps.Respond403:
		out.Body.Status = http.StatusForbidden
	case apps.Respond404:
		out.Body.Status = http.StatusNotFound
	case apps.RespondDrop:
	case apps.RespondHTML:
		out.Body.Status = http.StatusOK
	case apps.RespondStub:
		out.Body.Status, out.Body.Text = http.StatusOK, serve.StubText(lang, d)
		out.Body.Format = serve.Resolve(req, "", d, xrayJSON)
	default:
		out.Body.Status = http.StatusOK
		out.Body.Format = serve.Resolve(req, "", d, xrayJSON)
		if out.Body.Template, err = h.testedTemplate(ctx, out.Body.Format, req.Get("User-Agent"), d.Action.Template); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// testedTemplate is the saved template a profile in format is made of for the app of the
// User-Agent ua when the deciding rule names the template named, as Serve picks it
// (serve.Template); 0 for the built-in profile, which a Clash app that takes no own profile
// gets too (mihomo.Profile.Routable: Stash, an app of another core).
func (h *handlers) testedTemplate(ctx context.Context, format, ua string, named int64) (int64, error) {
	if format == serve.FormatClash && !(mihomo.Profile{App: apps.DetectApp(ua)}).Routable() {
		return 0, nil
	}
	rows, err := h.d.Store.Q.ListSubTemplateHeads(ctx)
	if err != nil {
		return 0, err
	}
	ts := make([]submodel.Template, 0, len(rows))
	for _, r := range rows {
		ts = append(ts, submodel.Template{ID: r.ID, Core: submodel.Core(r.Core), Default: r.IsDefault})
	}
	return serve.Template(ts, format, named).ID, nil
}

func (h *handlers) userSubLog(ctx context.Context, in *userSubLogInput) (*userSubLogOutput, error) {
	if _, err := h.d.Store.Q.GetUser(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	} else if err != nil {
		return nil, err
	}
	items, err := subsrc.UserLog(ctx, h.d.Store.Q, in.ID, in.Limit)
	if err != nil {
		return nil, err
	}
	out := &userSubLogOutput{}
	out.Body.Days = subsrc.LogDays(ctx, h.d.Settings)
	out.Body.Items = make([]SubLogItem, 0, len(items))
	for _, it := range items {
		k, _ := apps.Lookup(it.App)
		out.Body.Items = append(out.Body.Items, SubLogItem{At: it.At, IP: it.IP, Agent: it.Agent, App: it.App, AppName: k.Name, Rule: it.Rule, Format: it.Format, Outcome: it.Outcome})
	}
	return out, nil
}
