package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Traffic packages (GitHub issue #12): extra traffic for the main quota or a pool, sold in
// the bot and the Mini App or given by the admin. The rules are in domain (grants.go,
// packages.go); here are the catalog and a user's grants.

type PackageView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	PoolID     *int64 `json:"pool_id" doc:"Пул трафика; null — основной трафик"`
	Lifetime   string `json:"lifetime" enum:"used,period,days" doc:"used — пока не израсходован, period — до конца периода, days — N дней с покупки"`
	Days       int64  `json:"days" doc:"Срок в днях для lifetime=days"`
	PriceStars *int64 `json:"price_stars" doc:"Цена в Telegram Stars; null — не продаётся за Stars"`
	PriceRub   *int64 `json:"price_rub" doc:"Цена в копейках; null — не продаётся за рубли"`
	OnSale     bool   `json:"on_sale"`
	Sort       int64  `json:"sort"`
}

func viewPackage(p db.TrafficPackage) PackageView {
	return PackageView{ID: p.ID, Name: p.Name, Bytes: p.Bytes, PoolID: store.Int64Ptr(p.PoolID), Lifetime: p.Lifetime, Days: p.Days,
		PriceStars: store.Int64Ptr(p.PriceStars), PriceRub: store.Int64Ptr(p.PriceRub), OnSale: p.OnSale != 0, Sort: p.Sort}
}

type PackageBody struct {
	Name       string `json:"name" minLength:"1" maxLength:"60"`
	Bytes      int64  `json:"bytes" minimum:"1073741824" maximum:"109951162777600" doc:"От 1 ГБ до 100 ТБ"`
	PoolID     *int64 `json:"pool_id,omitempty" minimum:"1" doc:"Пул трафика; не передан — основной трафик"`
	Lifetime   string `json:"lifetime" enum:"used,period,days"`
	Days       int64  `json:"days,omitempty" minimum:"0" maximum:"3650" doc:"Для lifetime=days: 1–3650"`
	PriceStars *int64 `json:"price_stars,omitempty" minimum:"1" maximum:"10000"`
	PriceRub   *int64 `json:"price_rub,omitempty" minimum:"100" maximum:"100000000" doc:"Цена в копейках: 7900 — 79 ₽"`
	OnSale     bool   `json:"on_sale,omitempty" doc:"Продавать в боте и Mini App; нужна хотя бы одна цена"`
	Sort       int64  `json:"sort,omitempty"`
}

func (b PackageBody) input() domain.PackageInput {
	in := domain.PackageInput{Name: b.Name, Bytes: b.Bytes, Lifetime: b.Lifetime, Days: b.Days, PriceStars: domain.NullInt(b.PriceStars),
		PriceRub: domain.NullInt(b.PriceRub), OnSale: b.OnSale, Sort: b.Sort}
	if b.PoolID != nil {
		in.PoolID = *b.PoolID
	}
	return in
}

type packageInput struct{ Body PackageBody }
type packageUpdateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body PackageBody
}
type packageOutput struct{ Body PackageView }
type packagesOutput struct{ Body []PackageView }

// GrantView is traffic a user got: the history of packages and the admin's gifts.
type GrantView struct {
	ID          int64      `json:"id"`
	PoolID      *int64     `json:"pool_id" doc:"Пул трафика; null — основной трафик"`
	Bytes       int64      `json:"bytes"`
	Remaining   int64      `json:"remaining"`
	Lifetime    string     `json:"lifetime" enum:"used,period,days"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Active      bool       `json:"active" doc:"Ещё считается: не израсходован и не истёк"`
	Source      string     `json:"source" enum:"purchase,admin"`
	PaymentID   *int64     `json:"payment_id"`
	PackageName string     `json:"package_name" doc:"Название пакета; пусто — начислено вручную"`
	Note        string     `json:"note"`
	CreatedAt   time.Time  `json:"created_at"`
}

type grantsOutput struct{ Body []GrantView }
type grantOutput struct{ Body GrantView }

type grantInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		PoolID   *int64 `json:"pool_id,omitempty" minimum:"1" doc:"Пул трафика; не передан — основной трафик"`
		Bytes    int64  `json:"bytes" minimum:"1073741824" maximum:"109951162777600" doc:"От 1 ГБ до 100 ТБ"`
		Lifetime string `json:"lifetime" enum:"used,period,days"`
		Days     int64  `json:"days,omitempty" minimum:"0" maximum:"3650"`
		Note     string `json:"note,omitempty" maxLength:"200" doc:"За что: бонус, компенсация"`
	}
}

func (h *handlers) registerPackages() {
	tags := []string{"tariffs"}
	huma.Register(h.api, huma.Operation{OperationID: "list-packages", Method: http.MethodGet, Path: "/api/v1/packages", Summary: "Пакеты трафика", Tags: tags}, h.listPackages)
	huma.Register(h.api, huma.Operation{OperationID: "create-package", Method: http.MethodPost, Path: "/api/v1/packages", Summary: "Создать пакет трафика", Tags: tags, DefaultStatus: http.StatusCreated}, h.createPackage)
	huma.Register(h.api, huma.Operation{OperationID: "update-package", Method: http.MethodPut, Path: "/api/v1/packages/{id}", Summary: "Изменить пакет трафика", Tags: tags}, h.updatePackage)
	huma.Register(h.api, huma.Operation{OperationID: "archive-package", Method: http.MethodDelete, Path: "/api/v1/packages/{id}", Summary: "Убрать пакет в архив", Tags: tags, DefaultStatus: http.StatusNoContent}, h.archivePackage)
	huma.Register(h.api, huma.Operation{OperationID: "user-grants", Method: http.MethodGet, Path: "/api/v1/users/{id}/grants", Summary: "Пакеты трафика пользователя", Tags: []string{"users"}}, h.userGrants)
	huma.Register(h.api, huma.Operation{OperationID: "grant-traffic", Method: http.MethodPost, Path: "/api/v1/users/{id}/grants", Summary: "Начислить трафик", Tags: []string{"users"}, DefaultStatus: http.StatusCreated}, h.grantTraffic)
}

func (h *handlers) listPackages(ctx context.Context, _ *struct{}) (*packagesOutput, error) {
	ps, err := h.d.Store.Q.ListTrafficPackages(ctx)
	if err != nil {
		return nil, err
	}
	out := &packagesOutput{Body: make([]PackageView, 0, len(ps))}
	for _, p := range ps {
		out.Body = append(out.Body, viewPackage(p))
	}
	return out, nil
}

func (h *handlers) createPackage(ctx context.Context, in *packageInput) (*packageOutput, error) {
	p, err := h.d.Packages.Create(ctx, in.Body.input())
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "package.create", "package", strconv.FormatInt(p.ID, 10), nil)
	return &packageOutput{Body: viewPackage(p)}, nil
}

func (h *handlers) updatePackage(ctx context.Context, in *packageUpdateInput) (*packageOutput, error) {
	p, err := h.d.Packages.Update(ctx, in.ID, in.Body.input())
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "package.update", "package", strconv.FormatInt(p.ID, 10), nil)
	return &packageOutput{Body: viewPackage(p)}, nil
}

func (h *handlers) archivePackage(ctx context.Context, in *userIDInput) (*struct{}, error) {
	if err := h.d.Packages.Archive(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "package.archive", "package", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) userGrants(ctx context.Context, in *userIDInput) (*grantsOutput, error) {
	q := h.d.Store.Q
	if err := requireUser(ctx, q, in.ID); err != nil {
		return nil, err
	}
	gs, err := q.ListUserGrants(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	names, err := h.packageNames(ctx)
	if err != nil {
		return nil, err
	}
	out := &grantsOutput{Body: make([]GrantView, 0, len(gs))}
	for _, g := range gs {
		out.Body = append(out.Body, h.viewGrant(g, names))
	}
	return out, nil
}

func (h *handlers) grantTraffic(ctx context.Context, in *grantInput) (*grantOutput, error) {
	b := in.Body
	gi := domain.GrantInput{Bytes: b.Bytes, Lifetime: b.Lifetime, Days: b.Days, Note: b.Note}
	if b.PoolID != nil {
		gi.PoolID = *b.PoolID
	}
	g, err := h.d.Users.Grant(ctx, in.ID, gi)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.grant", "user", strconv.FormatInt(in.ID, 10), map[string]any{"bytes": g.Bytes, "pool_id": b.PoolID, "lifetime": g.Lifetime})
	return &grantOutput{Body: h.viewGrant(g, nil)}, nil
}

// packageNames names every package, archived ones too: grants keep naming them.
func (h *handlers) packageNames(ctx context.Context) (map[int64]string, error) {
	ps, err := h.d.Store.Q.ListAllTrafficPackages(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(ps))
	for _, p := range ps {
		out[p.ID] = p.Name
	}
	return out, nil
}

func (h *handlers) viewGrant(g db.TrafficGrant, packages map[int64]string) GrantView {
	return GrantView{ID: g.ID, PoolID: store.Int64Ptr(g.PoolID), Bytes: g.Bytes, Remaining: g.Remaining, Lifetime: g.Lifetime,
		ExpiresAt: store.TimePtr(g.ExpiresAt), Active: domain.GrantActive(g, h.d.Now()), Source: g.Source,
		PaymentID: store.Int64Ptr(g.PaymentID), PackageName: packages[g.PackageID.Int64], Note: g.Note,
		CreatedAt: time.Unix(g.CreatedAt, 0).UTC()}
}
