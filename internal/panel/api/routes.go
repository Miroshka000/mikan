package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/subs"
)

// The routing of the Clash profiles: what the admin can send where (the catalog), and the
// profile it makes before it is saved (the preview).

type routeService struct {
	ID   string `json:"id"`
	Icon string `json:"icon"`
	Name string `json:"name" doc:"По-русски"`
	En   string `json:"name_en"`
}

type routeDirectSet struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	En   string `json:"name_en"`
}

type routesCatalogOutput struct {
	Body struct {
		Services []routeService   `json:"services"`
		Direct   []routeDirectSet `json:"direct"`
	}
}

type routesPreviewInput struct {
	Body struct {
		SubRouting  string      `json:"sub_routing" enum:"ru_direct,all,blocked"`
		SubRoutes   subs.Routes `json:"sub_routes"`
		SubTemplate string      `json:"sub_template,omitempty" maxLength:"524288" doc:"Свой профиль Clash: показать его вместо встроенного"`
		Starter     bool        `json:"starter,omitempty" doc:"Встроенный профиль с этими sub_routing и sub_routes как начало своего: без серверов, группы просят их сами"`
		SubRules    *string     `json:"sub_rules,omitempty" maxLength:"65536" doc:"Свои правила вместо сохранённых"`
		GroupMain   *string     `json:"sub_group_main,omitempty" maxLength:"200" doc:"Имя главной группы вместо сохранённого"`
		GroupAuto   *string     `json:"sub_group_auto,omitempty" maxLength:"200" doc:"Имя группы автовыбора вместо сохранённого"`
	}
}

type routesPreviewOutput struct {
	Body struct {
		Profile string `json:"profile" doc:"Профиль Clash (YAML) пользователя со всеми подключениями; ключи — заглушки"`
	}
}

func (h *handlers) registerRoutes() {
	huma.Register(h.api, huma.Operation{OperationID: "routes-catalog", Method: http.MethodGet, Path: "/api/v1/settings/routes/catalog", Summary: "Сервисы и списки для маршрутизации", Tags: []string{"settings"}}, h.routesCatalog)
	huma.Register(h.api, huma.Operation{OperationID: "routes-preview", Method: http.MethodPost, Path: "/api/v1/settings/routes/preview", Summary: "Профиль Clash с этой маршрутизацией, без сохранения", Tags: []string{"settings"}}, h.routesPreview)
}

func (h *handlers) routesCatalog(context.Context, *struct{}) (*routesCatalogOutput, error) {
	out := &routesCatalogOutput{}
	out.Body.Services = []routeService{}
	for _, s := range subs.Services {
		out.Body.Services = append(out.Body.Services, routeService{ID: s.ID, Icon: s.Icon, Name: s.Name, En: s.NameEN})
	}
	out.Body.Direct = []routeDirectSet{}
	for _, d := range subs.DirectSets {
		out.Body.Direct = append(out.Body.Direct, routeDirectSet{ID: d.ID, Name: d.Name, En: d.NameEN})
	}
	return out, nil
}

func (h *handlers) routesPreview(ctx context.Context, in *routesPreviewInput) (*routesPreviewOutput, error) {
	if h.d.RoutesPreview == nil {
		return nil, huma.Error503ServiceUnavailable("preview_unavailable")
	}
	exists := func(id int64) bool {
		_, err := h.d.Store.Q.GetNode(ctx, id)
		return err == nil
	}
	if err := in.Body.SubRoutes.Check(exists); err != nil {
		return nil, fieldErr("body.sub_routes", err.Error())
	}
	req := subs.PreviewRequest{Routing: subs.ParseRouting(in.Body.SubRouting), Routes: in.Body.SubRoutes, Template: in.Body.SubTemplate, Starter: in.Body.Starter, Rules: in.Body.SubRules}
	if in.Body.GroupMain != nil || in.Body.GroupAuto != nil {
		// The saved names with the given ones over them, checked as a save would check them.
		g, err := subs.LoadGroups(ctx, h.d.Settings)
		if err != nil {
			return nil, err
		}
		if in.Body.GroupMain != nil {
			g.Main = strings.TrimSpace(*in.Body.GroupMain)
		}
		if in.Body.GroupAuto != nil {
			g.Auto = strings.TrimSpace(*in.Body.GroupAuto)
		}
		if bad := h.checkGroups(ctx, g); len(bad) > 0 {
			return nil, fieldErrs(bad...)
		}
		req.Groups = &g
	}
	raw, err := h.d.RoutesPreview(ctx, req)
	if errors.Is(err, subs.ErrNoProxies) {
		return nil, huma.Error409Conflict("no_proxies")
	}
	var te *subs.TemplateError
	if errors.As(err, &te) {
		return nil, fieldErrValue("body.sub_template", te.Code, te.Detail)
	}
	if err != nil {
		return nil, err
	}
	out := &routesPreviewOutput{}
	out.Body.Profile = string(raw)
	return out, nil
}
