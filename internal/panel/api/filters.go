package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/filters"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The ingress and egress filters (internal/panel/filters): the nodes refuse users' traffic
// to chosen places and connections from chosen networks.

type EgressFilter struct {
	Enabled  bool     `json:"enabled" doc:"Ноды не пропускают трафик пользователей туда, что указано ниже"`
	Mail     bool     `json:"mail" doc:"Закрыть почтовые порты 465, 587 и 2525 (25 закрыт всегда): с общего адреса VPN, с которого шлют почту, адрес быстро попадает в чёрные списки"`
	Ports    []string `json:"ports" maxItems:"1000" doc:"Порты и диапазоны: 6881, 6881-6889"`
	Networks []string `json:"networks" maxItems:"1000" doc:"Сети и адреса: 203.0.113.0/24, 198.51.100.7, 2001:db8::/32"`
	Domains  []string `json:"domains" maxItems:"1000" doc:"Домены вместе с поддоменами: example.com"`
}

type IngressFilter struct {
	Enabled  bool     `json:"enabled" doc:"Ноды проверяют, откуда подключаются"`
	Allow    bool     `json:"allow" doc:"true — подключаться можно только из сетей списка; false — из сетей списка нельзя"`
	Networks []string `json:"networks" maxItems:"1000" doc:"Сети и адреса. Другие ноды панели (каскад) проверку не проходят: их пускают всегда"`
}

type FiltersView struct {
	Egress    EgressFilter  `json:"egress"`
	Ingress   IngressFilter `json:"ingress"`
	MailPorts []string      `json:"mail_ports" doc:"Что закрывает почтовый пресет"`
}

type filtersOutput struct{ Body FiltersView }

type patchFiltersInput struct {
	Body struct {
		Egress  *EgressFilter  `json:"egress,omitempty" doc:"Весь исходящий фильтр"`
		Ingress *IngressFilter `json:"ingress,omitempty" doc:"Весь входящий фильтр"`
	}
}

func (h *handlers) registerFilters() {
	tags := []string{"filters"}
	huma.Register(h.api, huma.Operation{OperationID: "get-filters", Method: http.MethodGet, Path: "/api/v1/filters", Summary: "Фильтры трафика", Tags: tags}, h.getFilters)
	huma.Register(h.api, huma.Operation{OperationID: "update-filters", Method: http.MethodPatch, Path: "/api/v1/filters", Summary: "Настроить фильтры трафика", Description: "Каждый переданный фильтр заменяется целиком. Ноды получают его со своим состоянием; ноды старше фильтров его не применяют.", Tags: tags}, h.updateFilters)
}

func filtersView(c filters.Config) FiltersView {
	return FiltersView{
		Egress:    EgressFilter{Enabled: c.Egress.Enabled, Mail: c.Egress.Mail, Ports: c.Egress.Ports, Networks: c.Egress.Networks, Domains: c.Egress.Domains},
		Ingress:   IngressFilter{Enabled: c.Ingress.Enabled, Allow: c.Ingress.Allow, Networks: c.Ingress.Networks},
		MailPorts: filters.MailPorts,
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
			c.Egress = filters.Egress{Enabled: e.Enabled, Mail: e.Mail, Ports: e.Ports, Networks: e.Networks, Domains: e.Domains}
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
		details["egress_items"] = len(saved.Egress.Ports) + len(saved.Egress.Networks) + len(saved.Egress.Domains)
	}
	if b.Ingress != nil {
		details["ingress"] = saved.Ingress.Enabled
		details["ingress_allow"] = saved.Ingress.Allow
		details["ingress_items"] = len(saved.Ingress.Networks)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "filters.update", "settings", filters.KeyConfig, details)
	return &filtersOutput{Body: filtersView(saved)}, nil
}
