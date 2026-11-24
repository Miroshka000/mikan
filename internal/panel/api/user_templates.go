package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Templates of clients (#295): what a client made by one gets (a tariff and what goes over
// it, the folder, the group, the labels and the note) and the names of a batch, for the admin
// who hands access out in batches. A template only fills the form of a new client: a client
// keeps nothing of it, and a change of the template changes no client made before.

// maxUserTemplates keeps the picker of the form short.
const maxUserTemplates = 50

// ClientSpecBody is what a new client gets over its tariff, as a body sets it: a field left
// out is the tariff's own (domain.ClientSpec).
type ClientSpecBody struct {
	TariffID        int64    `json:"tariff_id" minimum:"1"`
	TrafficLimit    *int64   `json:"traffic_limit,omitempty" minimum:"0" doc:"Байты за период; 0 — без лимита; не передан — как в тарифе"`
	DeviceLimit     *int64   `json:"device_limit,omitempty" minimum:"0" maximum:"100" doc:"Устройств; 0 — без лимита; не передан — как в тарифе"`
	SpeedLimit      *int64   `json:"speed_limit,omitempty" minimum:"0" maximum:"100000" doc:"Мбит/с в каждую сторону; 0 — без ограничения; не передан — как в тарифе"`
	TermDays        *int64   `json:"term_days,omitempty" minimum:"0" maximum:"3650" doc:"Срок в днях с создания; 0 — бессрочно; не передан — как в тарифе"`
	StartsOnConnect *bool    `json:"starts_on_connect,omitempty" doc:"Срок начнётся с первого подключения; не задано — как у тарифа"`
	FolderID        *int64   `json:"folder_id,omitempty" minimum:"1" doc:"Папка; не передана — вне папок"`
	GroupID         *int64   `json:"group_id,omitempty" minimum:"1" doc:"Своя группа клиента; не передана — группа тарифа"`
	Tags            []string `json:"tags,omitempty" maxItems:"20"`
	Note            string   `json:"note,omitempty" maxLength:"2000"`
}

// byTemplate are the details of an audit record of clients made, with the template of clients
// the form took the values of, when it names one.
func byTemplate(details map[string]any, templateID int64) map[string]any {
	if templateID != 0 {
		details["template_id"] = templateID
	}
	return details
}

func (b ClientSpecBody) spec() domain.ClientSpec {
	return domain.ClientSpec{TariffID: b.TariffID, TrafficLimit: b.TrafficLimit, DeviceLimit: b.DeviceLimit, SpeedLimit: b.SpeedLimit,
		TermDays: b.TermDays, StartsOnConnect: b.StartsOnConnect, FolderID: b.FolderID, GroupID: b.GroupID, Tags: b.Tags, Note: b.Note}
}

// UserTemplateView is a template of clients.
type UserTemplateView struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	TariffID        int64     `json:"tariff_id"`
	TrafficLimit    *int64    `json:"traffic_limit" doc:"Байты за период; 0 — без лимита; null — как в тарифе"`
	DeviceLimit     *int64    `json:"device_limit" doc:"0 — без лимита; null — как в тарифе"`
	SpeedLimit      *int64    `json:"speed_limit" doc:"Мбит/с в каждую сторону; 0 — без ограничения; null — как в тарифе"`
	TermDays        *int64    `json:"term_days" doc:"Срок в днях; 0 — бессрочно; null — как в тарифе"`
	StartsOnConnect *bool     `json:"starts_on_connect" doc:"null — как у тарифа"`
	FolderID        *int64    `json:"folder_id" doc:"null — вне папок"`
	GroupID         *int64    `json:"group_id" doc:"null — группа тарифа"`
	Tags            []string  `json:"tags"`
	Note            string    `json:"note"`
	NamePattern     string    `json:"name_pattern" doc:"Имена клиентов пачки, как name_pattern у POST /users/batch; пусто — предложит панель"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type userTemplatesOutput struct{ Body []UserTemplateView }
type userTemplateOutput struct{ Body UserTemplateView }

type userTemplateIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

// userTemplateBody is a template as a body sets it, whole.
type userTemplateBody struct {
	Name        string `json:"name" minLength:"1" maxLength:"60"`
	NamePattern string `json:"name_pattern,omitempty" maxLength:"100" doc:"Имена клиентов пачки: {n} — номер, {nnn} — номер не короче трёх цифр (007); не передан — предложит панель"`
	ClientSpecBody
}

type createUserTemplateInput struct{ Body userTemplateBody }

type putUserTemplateInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body userTemplateBody
}

func (h *handlers) registerUserTemplates() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "list-user-templates", Method: http.MethodGet, Path: "/api/v1/user-templates", Summary: "Шаблоны клиентов", Tags: tags}, h.listUserTemplates)
	huma.Register(h.api, huma.Operation{OperationID: "create-user-template", Method: http.MethodPost, Path: "/api/v1/user-templates", Summary: "Создать шаблон клиентов",
		Tags: tags, DefaultStatus: http.StatusCreated}, h.createUserTemplate)
	huma.Register(h.api, huma.Operation{OperationID: "update-user-template", Method: http.MethodPut, Path: "/api/v1/user-templates/{id}", Summary: "Изменить шаблон клиентов целиком",
		Description: "Клиенты, созданные по шаблону раньше, не меняются.", Tags: tags}, h.updateUserTemplate)
	huma.Register(h.api, huma.Operation{OperationID: "delete-user-template", Method: http.MethodDelete, Path: "/api/v1/user-templates/{id}", Summary: "Удалить шаблон клиентов: созданные по нему клиенты остаются",
		Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteUserTemplate)
}

func userTemplateView(r db.UserTemplate) UserTemplateView {
	return UserTemplateView{ID: r.ID, Name: r.Name, TariffID: r.TariffID, TrafficLimit: store.Int64Ptr(r.TrafficLimit), DeviceLimit: store.Int64Ptr(r.DeviceLimit),
		SpeedLimit: store.Int64Ptr(r.SpeedLimit), TermDays: store.Int64Ptr(r.TermDays), StartsOnConnect: store.BoolPtr(r.StartsOnConnect),
		FolderID: store.Int64Ptr(r.FolderID), GroupID: store.Int64Ptr(r.GroupID), Tags: domain.DecodeTags(r.Tags), Note: r.Note,
		NamePattern: r.NamePattern, UpdatedAt: store.Time(r.UpdatedAt)}
}

func (h *handlers) listUserTemplates(ctx context.Context, _ *struct{}) (*userTemplatesOutput, error) {
	rows, err := h.d.Store.Q.ListUserTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := &userTemplatesOutput{Body: make([]UserTemplateView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, userTemplateView(r))
	}
	return out, nil
}

// userTemplateRow is b as a template is kept, checked on q: what it names is there
// (templateRefs) and its name is free (templateNameFree); self is the template being changed.
func userTemplateRow(ctx context.Context, q *db.Queries, b userTemplateBody, self int64) (db.CreateUserTemplateParams, error) {
	var row db.CreateUserTemplateParams
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return row, fieldErr("body.name", "name_blank")
	}
	if err := templateRefs(ctx, q, b); err != nil {
		return row, err
	}
	if err := templateNameFree(ctx, q, name, self); err != nil {
		return row, err
	}
	tags, err := domain.EncodeTags(b.Tags)
	if err != nil {
		return row, err
	}
	return db.CreateUserTemplateParams{Name: name, TariffID: b.TariffID, TrafficLimit: domain.NullInt(b.TrafficLimit), DeviceLimit: domain.NullInt(b.DeviceLimit),
		SpeedLimit: domain.NullInt(b.SpeedLimit), TermDays: domain.NullInt(b.TermDays), StartsOnConnect: domain.NullBool(b.StartsOnConnect),
		FolderID: domain.NullInt(b.FolderID), GroupID: domain.NullInt(b.GroupID), Tags: tags, Note: b.Note, NamePattern: strings.TrimSpace(b.NamePattern)}, nil
}

// templateRefs checks what b names: a batch's names can be made of its pattern (the first of
// them is checked), its tariff is there and not archived, and so are its folder and group,
// kept from being deleted until the transaction ends.
func templateRefs(ctx context.Context, q *db.Queries, b userTemplateBody) error {
	if pattern := strings.TrimSpace(b.NamePattern); pattern != "" {
		if _, err := domain.BatchNames(pattern, 1, 1); err != nil {
			return mapDomainErr(err)
		}
	}
	if t, err := q.GetTariff(ctx, b.TariffID); errors.Is(err, sql.ErrNoRows) || err == nil && t.Archived != 0 {
		return fieldErr("body.tariff_id", "tariff_not_found")
	} else if err != nil {
		return err
	}
	if b.FolderID != nil {
		if err := domain.LockFolder(ctx, q, *b.FolderID); err != nil {
			return mapDomainErr(err)
		}
	}
	if b.GroupID != nil {
		if err := domain.LockGroups(ctx, q, "group_id", *b.GroupID); err != nil {
			return mapDomainErr(err)
		}
	}
	return nil
}

// templateNameFree refuses a name another template than self has, in any letter case, and a
// new template past maxUserTemplates.
func templateNameFree(ctx context.Context, q *db.Queries, name string, self int64) error {
	all, err := q.ListUserTemplates(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(all, func(x db.UserTemplate) bool { return x.ID != self && strings.EqualFold(x.Name, name) }) {
		return userTemplateNameTaken()
	}
	if self == 0 && len(all) >= maxUserTemplates {
		return huma.Error409Conflict("user_template_limit", &huma.ErrorDetail{Message: "user_template_limit", Value: maxUserTemplates})
	}
	return nil
}

func userTemplateNameTaken() error {
	return huma.Error409Conflict("user_template_name_taken", &huma.ErrorDetail{Location: "body.name", Message: "user_template_name_taken"})
}

func (h *handlers) createUserTemplate(ctx context.Context, in *createUserTemplateInput) (*userTemplateOutput, error) {
	var r db.UserTemplate
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		row, err := userTemplateRow(ctx, q, in.Body, 0)
		if err != nil {
			return err
		}
		row.At = h.d.Now().Unix()
		r, err = q.CreateUserTemplate(ctx, row)
		return err
	})
	if store.IsUnique(err) {
		err = userTemplateNameTaken()
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user_template.create", "user_template", strconv.FormatInt(r.ID, 10), map[string]any{"name": r.Name, "tariff_id": r.TariffID})
	return &userTemplateOutput{Body: userTemplateView(r)}, nil
}

func (h *handlers) updateUserTemplate(ctx context.Context, in *putUserTemplateInput) (*userTemplateOutput, error) {
	var r db.UserTemplate
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetUserTemplate(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		} else if err != nil {
			return err
		}
		row, err := userTemplateRow(ctx, q, in.Body, in.ID)
		if err != nil {
			return err
		}
		r, err = q.UpdateUserTemplate(ctx, db.UpdateUserTemplateParams{ID: in.ID, Name: row.Name, TariffID: row.TariffID, TrafficLimit: row.TrafficLimit,
			DeviceLimit: row.DeviceLimit, SpeedLimit: row.SpeedLimit, TermDays: row.TermDays, StartsOnConnect: row.StartsOnConnect, FolderID: row.FolderID,
			GroupID: row.GroupID, Tags: row.Tags, Note: row.Note, NamePattern: row.NamePattern, At: h.d.Now().Unix()})
		return err
	})
	if store.IsUnique(err) {
		err = userTemplateNameTaken()
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user_template.update", "user_template", strconv.FormatInt(r.ID, 10), map[string]any{"name": r.Name, "tariff_id": r.TariffID})
	return &userTemplateOutput{Body: userTemplateView(r)}, nil
}

func (h *handlers) deleteUserTemplate(ctx context.Context, in *userTemplateIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteUserTemplate(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user_template.delete", "user_template", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}
