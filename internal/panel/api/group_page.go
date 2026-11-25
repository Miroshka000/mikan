package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
)

// A group's own subscription page (#278): the page's document the group's clients see in place
// of the panel's, with the panel's images, instructions and brand. Every change is the
// session's, as the panel's page is: a leaked key must not put a page in front of a group.

// GroupPageView is the page a group's clients see.
type GroupPageView struct {
	Own         bool               `json:"own" doc:"У группы своя страница; false — в config страница для всех"`
	Config      subpage.Page       `json:"config"`
	Defaults    subpage.Page       `json:"defaults" doc:"Страница по умолчанию — к ней ведёт «Сбросить»"`
	Brand       string             `json:"brand" doc:"Название бренда (settings.brand), общее для всех страниц"`
	BrandAccent string             `json:"brand_accent" doc:"Цвет бренда (settings.brand_accent), общий для всех страниц"`
	Logo        *subpage.PageAsset `json:"logo,omitempty"`
	Background  *subpage.PageAsset `json:"background,omitempty"`
}

type groupPageOutput struct{ Body GroupPageView }

type putGroupPageInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Config subpage.Page `json:"config"`
	}
}

func (h *handlers) registerGroupPage() {
	tags := []string{"users"}
	op := sessionOps("users")
	huma.Register(h.api, huma.Operation{OperationID: "get-group-page", Method: http.MethodGet, Path: "/api/v1/groups/{id}/page", Summary: "Страница подписки группы", Tags: tags}, h.getGroupPage)
	huma.Register(h.api, op("put-group-page", http.MethodPut, "/api/v1/groups/{id}/page", "Своя страница подписки группы", 0), h.putGroupPage)
	huma.Register(h.api, op("delete-group-page", http.MethodDelete, "/api/v1/groups/{id}/page", "Группе — страница подписки для всех", http.StatusNoContent), h.deleteGroupPage)
}

// groupPageView is the page group id's clients see: its own, or the panel's.
func (h *handlers) groupPageView(ctx context.Context, id int64) (GroupPageView, error) {
	if _, err := h.d.Store.Q.GetGroup(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return GroupPageView{}, huma.Error404NotFound("not_found")
	} else if err != nil {
		return GroupPageView{}, err
	}
	base, err := h.subPageView(ctx)
	if err != nil {
		return GroupPageView{}, err
	}
	v := GroupPageView{Config: base.Config, Defaults: base.Defaults, Brand: base.Brand, BrandAccent: base.BrandAccent, Logo: base.Logo, Background: base.Background}
	if _, v.Own, err = domain.GroupPage(ctx, h.d.Store.Q, id); err != nil {
		return v, err
	}
	if v.Own {
		if v.Config, err = h.subPages().PageOf(ctx, id); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (h *handlers) getGroupPage(ctx context.Context, in *groupIDInput) (*groupPageOutput, error) {
	v, err := h.groupPageView(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &groupPageOutput{Body: v}, nil
}

// putGroupPage gives the group its own page, held to the rules of the panel's: the brand's
// colour and the images are the panel's, and a page may show only those there are.
func (h *handlers) putGroupPage(ctx context.Context, in *putGroupPageInput) (*groupPageOutput, error) {
	c := in.Body.Config
	details := problems("body.config.", subpage.Normalize(&c))
	accent, err := h.d.Settings.String(ctx, settings.KeyBrandAccent)
	if err != nil {
		return nil, err
	}
	if c.Look.Accent == "brand" && accent == "" {
		details = append(details, fieldDetail("body.config.look.accent", "color_required", nil))
	}
	missing, err := h.pageImagesMissing(ctx, c)
	if err != nil {
		return nil, err
	}
	if details = append(details, missing...); len(details) > 0 {
		return nil, fieldErrs(details...)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := h.setGroupPage(ctx, in.ID, string(raw)); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "group.page", "group", strconv.FormatInt(in.ID, 10), nil)
	return h.getGroupPage(ctx, &groupIDInput{ID: in.ID})
}

func (h *handlers) deleteGroupPage(ctx context.Context, in *groupIDInput) (*struct{}, error) {
	if err := h.setGroupPage(ctx, in.ID, ""); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "group.page_delete", "group", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

// setGroupPage writes the group's page (raw; "": the panel's again), the group held from being
// deleted meanwhile. The subscriptions' config, which says which groups have a page, is read
// again.
func (h *handlers) setGroupPage(ctx context.Context, id int64, raw string) error {
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		held, err := q.LockGroups(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(held) == 0 {
			return huma.Error404NotFound("not_found")
		}
		return domain.SetGroupPage(ctx, q, id, raw)
	})
	if err == nil {
		settings.Touch()
	}
	return err
}
