package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/filters"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The ingress and egress filters (internal/panel/filters): the nodes refuse users' traffic
// to chosen places and connections from chosen networks.

type EgressFilter struct {
	Enabled  bool            `json:"enabled" doc:"Ноды не пропускают трафик пользователей туда, что указано ниже"`
	Mail     bool            `json:"mail" doc:"Закрыть почтовые порты 465, 587 и 2525 (25 закрыт всегда): с общего адреса VPN, с которого шлют почту, адрес быстро попадает в чёрные списки"`
	Ports    []string        `json:"ports" maxItems:"1000" doc:"Порты и диапазоны: 6881, 6881-6889"`
	Networks []string        `json:"networks" maxItems:"1000" doc:"Сети и адреса: 203.0.113.0/24, 198.51.100.7, 2001:db8::/32"`
	Domains  []string        `json:"domains" maxItems:"1000" doc:"Домены вместе с поддоменами: example.com"`
	Rules    *[]filters.Rule `json:"rules,omitempty" maxItems:"1000" doc:"Фильтры для выбранных клиентов, папок или тарифов. При отсутствии поля текущие правила сохраняются"`
}

type IngressFilter struct {
	Enabled  bool     `json:"enabled" doc:"Ноды проверяют, откуда подключаются"`
	Allow    bool     `json:"allow" doc:"true — подключаться можно только из сетей списка; false — из сетей списка нельзя"`
	Networks []string `json:"networks" maxItems:"1000" doc:"Сети и адреса. Другие ноды панели (каскад) проверку не проходят: их пускают всегда"`
}

type FiltersView struct {
	Egress    EgressFilter   `json:"egress"`
	Ingress   IngressFilter  `json:"ingress"`
	MailPorts []string       `json:"mail_ports" doc:"Что закрывает почтовый пресет"`
	Sites     []filters.Site `json:"sites" doc:"Персональные правила доменов из карточек клиентов"`
}

type filtersOutput struct{ Body FiltersView }

type patchFiltersInput struct {
	Body struct {
		Egress  *EgressFilter  `json:"egress,omitempty" doc:"Весь исходящий фильтр"`
		Ingress *IngressFilter `json:"ingress,omitempty" doc:"Весь входящий фильтр"`
	}
}

type putSiteFilterInput struct {
	Body struct {
		UserID int64  `json:"user_id,omitempty" minimum:"1" doc:"Клиент для личного правила"`
		Domain string `json:"domain" maxLength:"1024"`
		Action string `json:"action" enum:"block-all,block,allow,clear"`
	}
}

func (h *handlers) registerFilters() {
	tags := []string{"filters"}
	huma.Register(h.api, huma.Operation{OperationID: "get-filters", Method: http.MethodGet, Path: "/api/v1/filters", Summary: "Фильтры трафика", Tags: tags}, h.getFilters)
	huma.Register(h.api, huma.Operation{OperationID: "update-filters", Method: http.MethodPatch, Path: "/api/v1/filters", Summary: "Настроить фильтры трафика", Description: "Каждый переданный фильтр заменяется целиком. Ноды получают его со своим состоянием; ноды старше фильтров его не применяют.", Tags: tags}, h.updateFilters)
	huma.Register(h.api, huma.Operation{OperationID: "set-site-filter", Method: http.MethodPut, Path: "/api/v1/filters/site", Summary: "Изменить фильтр домена из карточки клиента", Tags: tags}, h.putSiteFilter)
}

func filtersView(c filters.Config) FiltersView {
	rules := c.Egress.Rules
	if rules == nil {
		rules = []filters.Rule{}
	}
	sites := c.Egress.Sites
	if sites == nil {
		sites = []filters.Site{}
	}
	return FiltersView{
		Egress:    EgressFilter{Enabled: c.Egress.Enabled, Mail: c.Egress.Mail, Ports: c.Egress.Ports, Networks: c.Egress.Networks, Domains: c.Egress.Domains, Rules: &rules},
		Ingress:   IngressFilter{Enabled: c.Ingress.Enabled, Allow: c.Ingress.Allow, Networks: c.Ingress.Networks},
		MailPorts: filters.MailPorts,
		Sites:     sites,
	}
}

func (h *handlers) getFilters(ctx context.Context, _ *struct{}) (*filtersOutput, error) {
	c, err := filters.Load(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	return &filtersOutput{Body: filtersView(c)}, nil
}

func (h *handlers) updateFilters(ctx context.Context, in *patchFiltersInput) (*filtersOutput, error) {
	b := in.Body
	var saved filters.Config
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		c, err := filters.Load(ctx, set)
		if err != nil {
			return err
		}
		if e := b.Egress; e != nil {
			c.Egress.Enabled, c.Egress.Mail = e.Enabled, e.Mail
			c.Egress.Ports, c.Egress.Networks, c.Egress.Domains = e.Ports, e.Networks, e.Domains
			if e.Rules != nil {
				c.Egress.Rules = slices.Clone(*e.Rules)
			}
		}
		if i := b.Ingress; i != nil {
			c.Ingress = filters.Ingress{Enabled: i.Enabled, Allow: i.Allow, Networks: i.Networks}
		}
		if err := c.Validate(); err != nil {
			var p *filters.Problem
			if errors.As(err, &p) {
				return fieldErrValue("body."+p.List+"["+strconv.Itoa(p.Index)+"]", "filters_entry", p.Value)
			}
			return fieldErr("body", "filters_config")
		}
		saved = c
		return settings.Set(ctx, set, filters.KeyConfig, c)
	})
	if err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	details := map[string]any{}
	if b.Egress != nil {
		details["egress"] = saved.Egress.Enabled
		details["egress_items"] = len(saved.Egress.Ports) + len(saved.Egress.Networks) + len(saved.Egress.Domains) + len(saved.Egress.Rules)
	}
	if b.Ingress != nil {
		details["ingress"] = saved.Ingress.Enabled
		details["ingress_allow"] = saved.Ingress.Allow
		details["ingress_items"] = len(saved.Ingress.Networks)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "filters.update", "settings", filters.KeyConfig, details)
	return &filtersOutput{Body: filtersView(saved)}, nil
}

func (h *handlers) putSiteFilter(ctx context.Context, in *putSiteFilterInput) (*filtersOutput, error) {
	b := in.Body
	host, ok := nodeapi.CanonDomain(b.Domain)
	if !ok {
		return nil, fieldErr("body.domain", "filters_entry")
	}
	var saved filters.Config
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		c, err := filters.Load(ctx, set)
		if err != nil {
			return err
		}
		switch b.Action {
		case "block-all":
			if !slices.Contains(c.Egress.Domains, host) {
				c.Egress.Domains = append(c.Egress.Domains, host)
			}
		case "block", "allow", "clear":
			if b.UserID < 1 {
				return fieldErr("body.user_id", "filters_config")
			}
			if _, err := q.GetUser(ctx, b.UserID); errors.Is(err, sql.ErrNoRows) {
				return huma.Error404NotFound("not_found")
			} else if err != nil {
				return err
			}
			c.Egress.Sites = slices.DeleteFunc(c.Egress.Sites, func(s filters.Site) bool {
				return s.UserID == b.UserID && s.Domain == host
			})
			if b.Action != "clear" {
				c.Egress.Sites = append(c.Egress.Sites, filters.Site{UserID: b.UserID, Domain: host, Allow: b.Action == "allow"})
			}
		}
		if b.Action == "block" || b.Action == "block-all" {
			c.Egress.Enabled = true
		}
		if err := c.Validate(); err != nil {
			return fieldErr("body", "filters_config")
		}
		saved = c
		return settings.Set(ctx, set, filters.KeyConfig, c)
	})
	if err != nil {
		return nil, err
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "filters.site", "settings", filters.KeyConfig, map[string]any{
		"domain": host, "action": b.Action, "user_id": b.UserID,
	})
	return &filtersOutput{Body: filtersView(saved)}, nil
}
