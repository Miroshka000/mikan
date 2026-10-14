package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/campaigns"
)

// CampaignView is one ad campaign as the admin panel shows it: the tag its link carries,
// what the admin calls it and what it has brought.
type CampaignView struct {
	ID       int64  `json:"id"`
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Note     string `json:"note"`
	Enabled  bool   `json:"enabled"`
	Link     string `json:"link,omitempty" doc:"Ссылка на бота с меткой кампании; пусто, пока бот не подключён"`
	Visits   int64  `json:"visits" doc:"Сколько людей пришло по ссылке"`
	Bought   int64  `json:"bought" doc:"Сколько из них купили подписку впервые"`
	Renewed  int64  `json:"renewed" doc:"Сколько продлили подписку"`
	Amount   int64  `json:"amount" doc:"Сумма применённых оплат в копейках (только RUB)"`
	Currency string `json:"currency" enum:"RUB"`
}

// CampaignVisitView is one arrival through a campaign's link.
type CampaignVisitView struct {
	ChatID    int64     `json:"chat_id"`
	Name      string    `json:"name,omitempty"`
	Tag       string    `json:"tag"`
	FirstSeen time.Time `json:"first_seen"`
}

type CampaignStatsView struct {
	Campaigns int64 `json:"campaigns"`
	Visits    int64 `json:"visits"`
	Bought    int64 `json:"bought"`
	Renewed   int64 `json:"renewed"`
}

type campaignsOutput struct {
	Body struct {
		Items []CampaignView    `json:"items"`
		Total int64             `json:"total"`
		Stats CampaignStatsView `json:"stats"`
	}
}

type campaignVisitsInput struct {
	ID     int64 `path:"id"`
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

type campaignVisitsOutput struct {
	Body struct {
		Items []CampaignVisitView `json:"items"`
		Total int64               `json:"total"`
	}
}

type createCampaignInput struct {
	Body struct {
		Tag  string `json:"tag" minLength:"1" maxLength:"48" doc:"Метка в ссылке: латиница, цифры, дефис и подчёркивание"`
		Name string `json:"name" maxLength:"120"`
		Note string `json:"note" maxLength:"500"`
	}
}

type updateCampaignInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Name    string `json:"name" maxLength:"120"`
		Note    string `json:"note" maxLength:"500"`
		Enabled bool   `json:"enabled"`
	}
}

type campaignOneOutput struct{ Body CampaignView }

type campaignEnabledInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Enabled bool `json:"enabled"`
	}
}

func (h *handlers) registerCampaigns() {
	tags := []string{"campaigns"}
	huma.Register(h.api, huma.Operation{OperationID: "list-campaigns", Method: http.MethodGet, Path: "/api/v1/campaigns", Summary: "Метки рекламных кампаний", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listCampaigns)
	huma.Register(h.api, huma.Operation{OperationID: "create-campaign", Method: http.MethodPost, Path: "/api/v1/campaigns", Summary: "Создать метку кампании", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.createCampaign)
	huma.Register(h.api, huma.Operation{OperationID: "update-campaign", Method: http.MethodPut, Path: "/api/v1/campaigns/{id}", Summary: "Изменить метку кампании", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.updateCampaign)
	huma.Register(h.api, huma.Operation{OperationID: "enable-campaign", Method: http.MethodPost, Path: "/api/v1/campaigns/{id}/enabled", Summary: "Включить или выключить метку", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.enableCampaign)
	huma.Register(h.api, huma.Operation{OperationID: "delete-campaign", Method: http.MethodDelete, Path: "/api/v1/campaigns/{id}", Summary: "Удалить метку кампании", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.deleteCampaign)
	huma.Register(h.api, huma.Operation{OperationID: "list-campaign-visits", Method: http.MethodGet, Path: "/api/v1/campaigns/{id}/visits", Summary: "Кто пришёл по метке кампании", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listCampaignVisits)
}

// botLink is the deep link that carries a tag, made with the bot's username; "" while the
// bot is off or has no username yet.
func (h *handlers) botLink(tag string) string {
	if h.d.Telegram == nil {
		return ""
	}
	st := h.d.Telegram.Status()
	if !st.Running || st.Bot.Username == "" {
		return ""
	}
	return campaigns.Link(st.Bot.Username, tag)
}

func (h *handlers) campaignView(v campaigns.View) CampaignView {
	return CampaignView{ID: v.ID, Tag: v.Tag, Name: v.Name, Note: v.Note, Enabled: v.Enabled, Link: h.botLink(v.Tag),
		Visits: v.Visits, Bought: v.Bought, Renewed: v.Renewed, Amount: v.Rub, Currency: campaigns.Currency}
}

func (h *handlers) listCampaigns(ctx context.Context, in *campaignListInput) (*campaignsOutput, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	views, total, err := h.d.Campaigns.Views(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	summary, err := h.d.Campaigns.Summary(ctx)
	if err != nil {
		return nil, err
	}
	out := &campaignsOutput{}
	out.Body.Total = total
	out.Body.Stats = CampaignStatsView{Campaigns: summary.Campaigns, Visits: summary.Visits, Bought: summary.Bought, Renewed: summary.Renewed}
	out.Body.Items = make([]CampaignView, 0, len(views))
	for _, v := range views {
		out.Body.Items = append(out.Body.Items, h.campaignView(v))
	}
	return out, nil
}

type campaignListInput struct {
	Limit  int64 `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int64 `query:"offset" minimum:"0" default:"0"`
}

func (h *handlers) createCampaign(ctx context.Context, in *createCampaignInput) (*campaignOneOutput, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	c, err := h.d.Campaigns.Create(ctx, in.Body.Tag, in.Body.Name, in.Body.Note)
	if errors.Is(err, campaigns.ErrTagInvalid) {
		return nil, huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.tag", Message: "camp_tag_invalid"})
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "campaign.create", "campaign", c.Tag, map[string]any{"tag": c.Tag})
	out := &campaignOneOutput{}
	out.Body = CampaignView{ID: c.ID, Tag: c.Tag, Name: c.Name, Note: c.Note, Enabled: c.Enabled != 0, Link: h.botLink(c.Tag), Currency: campaigns.Currency}
	return out, nil
}

func (h *handlers) updateCampaign(ctx context.Context, in *updateCampaignInput) (*campaignOneOutput, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	c, err := h.d.Campaigns.Update(ctx, in.ID, in.Body.Name, in.Body.Note, in.Body.Enabled)
	if errors.Is(err, campaigns.ErrNotFound) {
		return nil, huma.Error404NotFound("campaign_not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "campaign.update", "campaign", c.Tag, map[string]any{"enabled": in.Body.Enabled})
	out := &campaignOneOutput{}
	out.Body = CampaignView{ID: c.ID, Tag: c.Tag, Name: c.Name, Note: c.Note, Enabled: c.Enabled != 0, Link: h.botLink(c.Tag), Currency: campaigns.Currency}
	return out, nil
}

func (h *handlers) enableCampaign(ctx context.Context, in *campaignEnabledInput) (*campaignOneOutput, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	if err := h.d.Campaigns.SetEnabled(ctx, in.ID, in.Body.Enabled); err != nil {
		if errors.Is(err, campaigns.ErrNotFound) {
			return nil, huma.Error404NotFound("campaign_not_found")
		}
		return nil, err
	}
	views, _, err := h.d.Campaigns.Views(ctx, 200, 0)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.ID == in.ID {
			out := &campaignOneOutput{}
			out.Body = h.campaignView(v)
			return out, nil
		}
	}
	return nil, huma.Error404NotFound("campaign_not_found")
}

func (h *handlers) deleteCampaign(ctx context.Context, in *campaignIDInput) (*struct{}, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	if err := h.d.Campaigns.Delete(ctx, in.ID); err != nil {
		if errors.Is(err, campaigns.ErrNotFound) {
			return nil, huma.Error404NotFound("campaign_not_found")
		}
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "campaign.delete", "campaign", "", map[string]any{"id": in.ID})
	return nil, nil
}

type campaignIDInput struct {
	ID int64 `path:"id"`
}

func (h *handlers) listCampaignVisits(ctx context.Context, in *campaignVisitsInput) (*campaignVisitsOutput, error) {
	if h.d.Campaigns == nil {
		return nil, huma.Error503ServiceUnavailable("campaigns_unavailable")
	}
	rows, total, err := h.d.Campaigns.Visits(ctx, in.ID, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := &campaignVisitsOutput{}
	out.Body.Total = total
	out.Body.Items = make([]CampaignVisitView, 0, len(rows))
	for _, v := range rows {
		name := v.FirstName
		if v.Username != "" {
			name = "@" + v.Username
		}
		out.Body.Items = append(out.Body.Items, CampaignVisitView{ChatID: v.ChatID, Name: name, Tag: v.Tag, FirstSeen: v.FirstSeen.UTC()})
	}
	return out, nil
}
