package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
)

// A batch of clients (#295): count clients of one spec at once, named by a pattern, for the
// admin who hands access out in batches; the form fills the spec from a template of clients.

// The count's maximum is domain.MaxBatch.
type createUsersBatchInput struct {
	Body struct {
		Count       int    `json:"count" minimum:"1" maximum:"500" doc:"Сколько клиентов создать"`
		NamePattern string `json:"name_pattern" minLength:"1" maxLength:"100" doc:"Имена клиентов: {n} — номер клиента, {nnn} — номер не короче трёх цифр (007)"`
		Start       int64  `json:"start,omitempty" minimum:"0" maximum:"999999999" default:"1" doc:"Номер первого клиента"`
		TemplateID  int64  `json:"template_id,omitempty" minimum:"1" doc:"Шаблон клиентов, из которого взяты значения: только для журнала"`
		ClientSpecBody
	}
}

// BatchUser is a client a batch made.
type BatchUser struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	SubURL string `json:"sub_url" doc:"Ссылка подписки; пусто, если ключу или оператору ссылки не показываются"`
}

type createUsersBatchOutput struct {
	Body struct {
		Users []BatchUser `json:"users" doc:"Созданные клиенты по порядку номеров"`
	}
}

func (h *handlers) registerUsersBatch() {
	huma.Register(h.api, huma.Operation{OperationID: "create-users-batch", Method: http.MethodPost, Path: "/api/v1/users/batch", Summary: "Создать пачку клиентов",
		Tags: []string{"users"}, DefaultStatus: http.StatusCreated, Description: "Все клиенты одной транзакцией: созданы все или ни один. " +
			"Имя, которое уже есть у другого клиента, отклоняет всю пачку: 409 user_names_taken с первыми такими именами. " +
			"Оператору поддержки без показа подписчиков закрыт: 403 support_denied"}, h.createUsersBatch)
}

func (h *handlers) createUsersBatch(ctx context.Context, in *createUsersBatchInput) (*createUsersBatchOutput, error) {
	// A batch refuses names other clients have, and so tells whether a name is a subscriber's:
	// an operator who may not see the subscribers makes none, as it searches none.
	if hidesPeople(ctx) {
		return nil, huma.Error403Forbidden("support_denied")
	}
	b := in.Body
	names, err := domain.BatchNames(b.NamePattern, b.Start, b.Count)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	us, err := h.d.Users.CreateMany(ctx, names, b.spec())
	var taken *domain.NamesTakenError
	switch {
	case errors.As(err, &taken):
		return nil, huma.Error409Conflict("user_names_taken", &huma.ErrorDetail{Location: "body.name_pattern", Message: "user_names_taken", Value: strings.Join(taken.Names, ", ")})
	case errors.Is(err, domain.ErrNotFound):
		return nil, fieldErr("body.tariff_id", "tariff_not_found")
	case err != nil:
		return nil, mapDomainErr(err)
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.create_batch", "user", "",
		byTemplate(map[string]any{"count": len(us), "tariff_id": b.TariffID, "first_id": us[0].ID, "last_id": us[len(us)-1].ID}, b.TemplateID))
	base := h.linkBase(ctx)
	out := &createUsersBatchOutput{}
	out.Body.Users = make([]BatchUser, len(us))
	for i, u := range us {
		out.Body.Users[i] = BatchUser{ID: u.ID, Name: u.Name, SubURL: subLink(base, u.SubToken)}
	}
	return out, nil
}
