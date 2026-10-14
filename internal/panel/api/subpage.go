package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/panel/subs"
)

// The subscription page as the admin builds it (subpage): its settings document, its
// images and its instructions. Every change is the session's: it is what every subscriber
// sees, links and CSS included, and a leaked key must not be able to put a phishing page
// in front of them. A key may read it.

type SubPageView struct {
	Config      subpage.Page       `json:"config"`
	Defaults    subpage.Page       `json:"defaults" doc:"Страница по умолчанию — к ней ведёт «Сбросить»"`
	Brand       string             `json:"brand" doc:"Название бренда (settings.brand)"`
	BrandAccent string             `json:"brand_accent" doc:"Цвет бренда (settings.brand_accent); его же получают приложения с брендингом"`
	Logo        *subpage.PageAsset `json:"logo,omitempty"`
	Background  *subpage.PageAsset `json:"background,omitempty"`
}

type subPageOutput struct{ Body SubPageView }

type putSubPageInput struct {
	Body struct {
		Config      subpage.Page `json:"config"`
		Brand       *string      `json:"brand,omitempty" maxLength:"40"`
		BrandAccent *string      `json:"brand_accent,omitempty" maxLength:"7" doc:"#RRGGBB или пусто"`
	}
}

type subImageInput struct {
	Name string `path:"name" enum:"logo,background"`
}

type putSubImageInput struct {
	Name string `path:"name" enum:"logo,background"`
	Body struct {
		Data string `json:"data" minLength:"1" maxLength:"2800000" doc:"Картинка в base64: PNG, JPEG или WebP; логотип до 512 КБ, фон до 2 МБ. SVG не принимается"`
	}
}

type subImageOutput struct{ Body subpage.PageAsset }

type subImageBytes struct {
	ContentType  string `header:"Content-Type"`
	CacheControl string `header:"Cache-Control"`
	Nosniff      string `header:"X-Content-Type-Options"`
	Body         []byte
}

// SubDocView is an instruction of the subscription page.
type SubDocView struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Emoji     string `json:"emoji"`
	Body      string `json:"body" doc:"Markdown"`
	Platform  string `json:"platform" doc:"ios, android, windows, macos, linux; пусто — для всех"`
	Published bool   `json:"published" doc:"Видна на странице подписки и в Mini App"`
	UpdatedAt int64  `json:"updated_at"`
}

type SubDocBody struct {
	Title     string `json:"title" minLength:"1" maxLength:"80"`
	Emoji     string `json:"emoji" maxLength:"32"`
	Body      string `json:"body" maxLength:"20000" doc:"Markdown: заголовки #, списки, **жирный**, *курсив*, код в обратных кавычках, [ссылки](https://…), картинки ![](https://…)"`
	Platform  string `json:"platform" enum:"ios,android,windows,macos,linux,"`
	Published bool   `json:"published"`
}

type subDocsOutput struct {
	Body struct {
		Items []SubDocView `json:"items"`
	}
}

type subDocOutput struct{ Body SubDocView }

type createSubDocInput struct{ Body SubDocBody }

type updateSubDocInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body SubDocBody
}

type subDocIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type orderSubDocsInput struct {
	Body struct {
		IDs []int64 `json:"ids" maxItems:"50" doc:"Все инструкции в новом порядке"`
	}
}

func (h *handlers) registerSubPage() {
	tags := []string{"sub-page"}
	op := sessionOps("sub-page")
	huma.Register(h.api, huma.Operation{OperationID: "get-sub-page", Method: http.MethodGet, Path: "/api/v1/sub-page", Summary: "Страница подписки: вид, блоки, приложения", Tags: tags}, h.getSubPage)
	huma.Register(h.api, op("update-sub-page", http.MethodPut, "/api/v1/sub-page", "Сохранить страницу подписки", 0), h.putSubPage)
	huma.Register(h.api, huma.Operation{OperationID: "get-sub-page-image", Method: http.MethodGet, Path: "/api/v1/sub-page/images/{name}", Summary: "Загруженная картинка страницы подписки", Tags: tags,
		Responses: map[string]*huma.Response{"200": {Description: "PNG, JPEG или WebP", Content: map[string]*huma.MediaType{"image/png": {}, "image/jpeg": {}, "image/webp": {}}}}}, h.getSubImage)
	put := op("put-sub-page-image", http.MethodPut, "/api/v1/sub-page/images/{name}", "Загрузить логотип или фон страницы подписки", 0)
	put.MaxBodyBytes = 3 << 20
	huma.Register(h.api, put, h.putSubImage)
	huma.Register(h.api, op("delete-sub-page-image", http.MethodDelete, "/api/v1/sub-page/images/{name}", "Убрать логотип или фон страницы подписки", http.StatusNoContent), h.deleteSubImage)
	huma.Register(h.api, huma.Operation{OperationID: "list-sub-docs", Method: http.MethodGet, Path: "/api/v1/sub-docs", Summary: "Инструкции страницы подписки", Tags: tags}, h.listSubDocs)
	huma.Register(h.api, op("create-sub-doc", http.MethodPost, "/api/v1/sub-docs", "Новая инструкция", http.StatusCreated), h.createSubDoc)
	huma.Register(h.api, op("update-sub-doc", http.MethodPut, "/api/v1/sub-docs/{id}", "Изменить инструкцию", 0), h.updateSubDoc)
	huma.Register(h.api, op("delete-sub-doc", http.MethodDelete, "/api/v1/sub-docs/{id}", "Удалить инструкцию", http.StatusNoContent), h.deleteSubDoc)
	huma.Register(h.api, op("order-sub-docs", http.MethodPut, "/api/v1/sub-docs/order", "Порядок инструкций", 0), h.orderSubDocs)
}

func (h *handlers) subPages() *subpage.Service { return subpage.NewService(h.d.Store.Q) }

func (h *handlers) subPageView(ctx context.Context) (SubPageView, error) {
	s := h.subPages()
	c, err := s.Page(ctx)
	if err != nil {
		return SubPageView{}, err
	}
	v := SubPageView{Config: c, Defaults: subpage.Default()}
	if v.Brand, err = h.d.Settings.String(ctx, settings.KeyBrand); err != nil {
		return v, err
	}
	if v.Brand == "" {
		v.Brand = "VPN"
	}
	if v.BrandAccent, err = h.d.Settings.String(ctx, settings.KeyBrandAccent); err != nil {
		return v, err
	}
	assets, err := s.Assets(ctx)
	if err != nil {
		return v, err
	}
	if a, ok := assets[subpage.AssetLogo]; ok {
		v.Logo = &a
	}
	if a, ok := assets[subpage.AssetBackground]; ok {
		v.Background = &a
	}
	return v, nil
}

func (h *handlers) getSubPage(ctx context.Context, _ *struct{}) (*subPageOutput, error) {
	v, err := h.subPageView(ctx)
	if err != nil {
		return nil, err
	}
	return &subPageOutput{Body: v}, nil
}

// problems turns what subpage found into the API's field errors under prefix.
func problems(prefix string, ps []subpage.Problem) []error {
	out := make([]error, 0, len(ps))
	for _, p := range ps {
		out = append(out, &huma.ErrorDetail{Location: prefix + p.Field, Message: p.Code, Value: p.Value})
	}
	return out
}

func (h *handlers) putSubPage(ctx context.Context, in *putSubPageInput) (*subPageOutput, error) {
	c := in.Body.Config
	details := problems("body.config.", subpage.Normalize(&c))
	accent := ""
	if in.Body.BrandAccent != nil {
		accent = strings.ToUpper(strings.TrimSpace(*in.Body.BrandAccent))
		if accent != "" && !subs.ValidAccent(accent) {
			details = append(details, &huma.ErrorDetail{Location: "body.brand_accent", Message: "color_invalid"})
		}
	} else {
		saved, err := h.d.Settings.String(ctx, settings.KeyBrandAccent)
		if err != nil {
			return nil, err
		}
		accent = saved
	}
	if c.Look.Accent == "brand" && accent == "" {
		details = append(details, &huma.ErrorDetail{Location: "body.brand_accent", Message: "color_required"})
	}
	if in.Body.Brand != nil && (strings.ContainsAny(*in.Body.Brand, "\r\n") || subpage.HasControl(*in.Body.Brand)) {
		details = append(details, &huma.ErrorDetail{Location: "body.brand", Message: "one_line"})
	}
	assets, err := h.subPages().Assets(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := assets[subpage.AssetLogo]; !ok && (c.Brand.Logo == "image" || c.OG.Image) {
		field := "body.config.brand.logo"
		if c.Brand.Logo != "image" {
			field = "body.config.og.image"
		}
		details = append(details, &huma.ErrorDetail{Location: field, Message: "logo_missing"})
	}
	if _, ok := assets[subpage.AssetBackground]; !ok && c.Look.Background.Kind == "image" {
		details = append(details, &huma.ErrorDetail{Location: "body.config.look.background.kind", Message: "background_missing"})
	}
	if len(details) > 0 {
		return nil, fieldErrs(details...)
	}
	err = h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if err := settings.Set(ctx, set, settings.KeySubPage, c); err != nil {
			return err
		}
		if in.Body.Brand != nil {
			if err := settings.Set(ctx, set, settings.KeyBrand, strings.TrimSpace(*in.Body.Brand)); err != nil {
				return err
			}
		}
		if in.Body.BrandAccent != nil {
			return settings.Set(ctx, set, settings.KeyBrandAccent, accent)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_page.update", "", "", nil)
	return h.getSubPage(ctx, nil)
}

func (h *handlers) getSubImage(ctx context.Context, in *subImageInput) (*subImageBytes, error) {
	a, ok, err := h.subPages().Image(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, huma.Error404NotFound("not_found")
	}
	return &subImageBytes{ContentType: a.ContentType, CacheControl: "private, no-cache", Nosniff: "nosniff", Body: a.Data}, nil
}

func (h *handlers) putSubImage(ctx context.Context, in *putSubImageInput) (*subImageOutput, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Body.Data))
	if err != nil {
		return nil, fieldErr("body.data", "image_type")
	}
	ct, err := subpage.CheckImage(in.Name, data)
	switch {
	case errors.Is(err, subpage.ErrImageTooBig):
		return nil, fieldErrValue("body.data", "image_too_big", subpage.AssetLimit(in.Name)>>10)
	case err != nil:
		return nil, fieldErr("body.data", "image_type")
	}
	a := subpage.PageAsset{Name: in.Name, Type: ct, Hash: subpage.Hash(data), Size: int64(len(data)), UpdatedAt: h.d.Now().Unix()}
	if err := h.d.Store.Q.PutSubAsset(ctx, db.PutSubAssetParams{Name: a.Name, ContentType: a.Type, Data: data, Hash: a.Hash, UpdatedAt: a.UpdatedAt}); err != nil {
		return nil, err
	}
	// The apps' logo comes from the subscription config, built again on the next fetch.
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_page.image", "sub_page_image", in.Name, map[string]any{"size": a.Size, "type": a.Type})
	return &subImageOutput{Body: a}, nil
}

func (h *handlers) deleteSubImage(ctx context.Context, in *subImageInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteSubAsset(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	settings.Touch()
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_page.image_delete", "sub_page_image", in.Name, nil)
	return nil, nil
}

func subDocView(d db.SubDoc) SubDocView {
	return SubDocView{ID: d.ID, Title: d.Title, Emoji: d.Emoji, Body: d.Body, Platform: d.Platform, Published: d.Published == 1, UpdatedAt: d.UpdatedAt}
}

func (h *handlers) listSubDocs(ctx context.Context, _ *struct{}) (*subDocsOutput, error) {
	rows, err := h.d.Store.Q.ListSubDocs(ctx)
	if err != nil {
		return nil, err
	}
	out := &subDocsOutput{}
	out.Body.Items = make([]SubDocView, 0, len(rows))
	for _, d := range rows {
		out.Body.Items = append(out.Body.Items, subDocView(d))
	}
	return out, nil
}

// checkSubDoc cleans b and refuses what the page could not show.
func checkSubDoc(b *SubDocBody) (subpage.Doc, error) {
	d := subpage.Doc{Title: b.Title, Emoji: b.Emoji, Body: b.Body, Platform: b.Platform}
	if ps := subpage.NormalizeDoc(&d); len(ps) > 0 {
		return d, fieldErrs(problems("body.", ps)...)
	}
	return d, nil
}

func (h *handlers) createSubDoc(ctx context.Context, in *createSubDocInput) (*subDocOutput, error) {
	d, err := checkSubDoc(&in.Body)
	if err != nil {
		return nil, err
	}
	var row db.SubDoc
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.CountSubDocs(ctx)
		if err != nil {
			return err
		}
		if n >= subpage.MaxDocs {
			return huma.Error409Conflict("too_many_docs", &huma.ErrorDetail{Message: "too_many_docs", Value: subpage.MaxDocs})
		}
		row, err = q.CreateSubDoc(ctx, db.CreateSubDocParams{Title: d.Title, Emoji: d.Emoji, Body: d.Body, Platform: d.Platform, Published: domain.Flag(in.Body.Published), At: h.d.Now().Unix()})
		return err
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_doc.create", "sub_doc", strconv.FormatInt(row.ID, 10), nil)
	return &subDocOutput{Body: subDocView(row)}, nil
}

func (h *handlers) updateSubDoc(ctx context.Context, in *updateSubDocInput) (*subDocOutput, error) {
	d, err := checkSubDoc(&in.Body)
	if err != nil {
		return nil, err
	}
	row, err := h.d.Store.Q.UpdateSubDoc(ctx, db.UpdateSubDocParams{ID: in.ID, Title: d.Title, Emoji: d.Emoji, Body: d.Body, Platform: d.Platform, Published: domain.Flag(in.Body.Published), UpdatedAt: h.d.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_doc.update", "sub_doc", strconv.FormatInt(row.ID, 10), nil)
	return &subDocOutput{Body: subDocView(row)}, nil
}

func (h *handlers) deleteSubDoc(ctx context.Context, in *subDocIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteSubDoc(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_doc.delete", "sub_doc", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) orderSubDocs(ctx context.Context, in *orderSubDocsInput) (*subDocsOutput, error) {
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		seen := map[int64]bool{}
		for i, id := range in.Body.IDs {
			if seen[id] {
				return fieldErrValue("body.ids", "duplicate_id", id)
			}
			seen[id] = true
			if _, err := q.SetSubDocPosition(ctx, db.SetSubDocPositionParams{ID: id, Position: int64(i + 1)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "sub_doc.order", "", "", nil)
	return h.listSubDocs(ctx, nil)
}
