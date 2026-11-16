package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Fields of the admin's own on users and nodes: an order number, a phone, the hoster's
// account. The admin names a field once, for users or for nodes; each card then shows it
// and takes a value, the API gives the values under custom_fields by the field's key, and
// the users' search finds them. They only describe: nothing a user may do or a node
// carries depends on them.

// maxCustomFields is how many fields users or nodes may have each: a card stays a card.
const maxCustomFields = 30

// maxFieldValue is the longest value, in characters.
const maxFieldValue = 500

var fieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// plainNumber is a decimal: a sign, the whole part, the fraction.
var plainNumber = regexp.MustCompile(`^([+-]?)([0-9]+)(?:\.([0-9]+))?$`)

type CustomFieldView struct {
	ID     int64  `json:"id"`
	Scope  string `json:"scope" enum:"user,node" doc:"Чьё поле: пользователей или нод"`
	Key    string `json:"key" doc:"Имя поля в API: ключ в custom_fields пользователя или ноды"`
	Label  string `json:"label" doc:"Название поля в панели"`
	Kind   string `json:"kind" enum:"text,number,bool,date,url" doc:"text — строка, number — число, bool — да/нет (значение true), date — дата YYYY-MM-DD, url — ссылка http(s)"`
	Values int    `json:"values" doc:"У скольких пользователей или нод поле заполнено"`
}

type customFieldsOutput struct{ Body []CustomFieldView }
type customFieldOutput struct{ Body CustomFieldView }

type customFieldIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createCustomFieldInput struct {
	Body struct {
		Scope string `json:"scope" enum:"user,node"`
		Key   string `json:"key" pattern:"^[a-z][a-z0-9_]{0,31}$" doc:"Латиница в нижнем регистре, цифры и _, с буквы; потом не меняется"`
		Label string `json:"label" minLength:"1" maxLength:"64"`
		Kind  string `json:"kind" enum:"text,number,bool,date,url" doc:"Потом не меняется"`
	}
}

type patchCustomFieldInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Label string `json:"label" minLength:"1" maxLength:"64"`
	}
}

type orderCustomFieldsInput struct {
	Body struct {
		Scope string  `json:"scope" enum:"user,node"`
		IDs   []int64 `json:"ids" minItems:"1" maxItems:"100" doc:"Все поля этого scope, каждое один раз, в том порядке, в каком их показывают карточки"`
	}
}

func (h *handlers) registerCustomFields() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "list-custom-fields", Method: http.MethodGet, Path: "/api/v1/custom-fields", Summary: "Свои поля пользователей и нод", Tags: tags}, h.listCustomFields)
	huma.Register(h.api, huma.Operation{OperationID: "create-custom-field", Method: http.MethodPost, Path: "/api/v1/custom-fields", Summary: "Добавить своё поле", Tags: tags, DefaultStatus: http.StatusCreated}, h.createCustomField)
	huma.Register(h.api, huma.Operation{OperationID: "update-custom-field", Method: http.MethodPatch, Path: "/api/v1/custom-fields/{id}", Summary: "Переименовать своё поле", Tags: tags}, h.updateCustomField)
	huma.Register(h.api, huma.Operation{OperationID: "order-custom-fields", Method: http.MethodPut, Path: "/api/v1/custom-fields/order", Summary: "Порядок своих полей", Tags: tags, DefaultStatus: http.StatusNoContent}, h.orderCustomFields)
	huma.Register(h.api, huma.Operation{OperationID: "delete-custom-field", Method: http.MethodDelete, Path: "/api/v1/custom-fields/{id}", Summary: "Удалить своё поле вместе с его значениями", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteCustomField)
}

func customFieldView(f db.CustomField, values int) CustomFieldView {
	return CustomFieldView{ID: f.ID, Scope: f.Scope, Key: f.Key, Label: f.Label, Kind: f.Kind, Values: values}
}

func (h *handlers) customFieldValues(ctx context.Context) (map[int64]int, error) {
	rows, err := h.d.Store.Q.CountCustomFieldValues(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]int, len(rows))
	for _, r := range rows {
		m[r.FieldID] = int(r.N)
	}
	return m, nil
}

func (h *handlers) listCustomFields(ctx context.Context, _ *struct{}) (*customFieldsOutput, error) {
	fs, err := h.d.Store.Q.ListCustomFields(ctx)
	if err != nil {
		return nil, err
	}
	n, err := h.customFieldValues(ctx)
	if err != nil {
		return nil, err
	}
	out := &customFieldsOutput{Body: make([]CustomFieldView, 0, len(fs))}
	for _, f := range fs {
		out.Body = append(out.Body, customFieldView(f, n[f.ID]))
	}
	return out, nil
}

// fieldLabel trims a label and refuses an empty one or one with control characters.
func fieldLabel(s string) (string, error) {
	label := strings.TrimSpace(s)
	if label == "" || strings.ContainsFunc(label, unicode.IsControl) {
		return "", fieldErr("body.label", "label_blank")
	}
	return label, nil
}

func fieldKeyTaken() error {
	return huma.Error409Conflict("field_key_taken", &huma.ErrorDetail{Location: "body.key", Message: "field_key_taken"})
}

func (h *handlers) createCustomField(ctx context.Context, in *createCustomFieldInput) (*customFieldOutput, error) {
	b := in.Body
	label, err := fieldLabel(b.Label)
	if err != nil {
		return nil, err
	}
	if !fieldKey.MatchString(b.Key) {
		return nil, fieldErr("body.key", "bad_key")
	}
	var f db.CustomField
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListCustomFields(ctx)
		if err != nil {
			return err
		}
		n := 0
		for _, o := range all {
			if o.Scope == b.Scope {
				n++
				if o.Key == b.Key {
					return fieldKeyTaken()
				}
			}
		}
		if n >= maxCustomFields {
			return huma.Error409Conflict("field_limit")
		}
		f, err = q.CreateCustomField(ctx, db.CreateCustomFieldParams{Scope: b.Scope, Key: b.Key, Label: label, Kind: b.Kind, CreatedAt: h.d.Now().Unix()})
		return err
	})
	if store.IsUnique(err) {
		err = fieldKeyTaken()
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "field.create", "field", strconv.FormatInt(f.ID, 10), map[string]any{"scope": f.Scope, "key": f.Key, "label": f.Label, "kind": f.Kind})
	return &customFieldOutput{Body: customFieldView(f, 0)}, nil
}

func (h *handlers) updateCustomField(ctx context.Context, in *patchCustomFieldInput) (*customFieldOutput, error) {
	label, err := fieldLabel(in.Body.Label)
	if err != nil {
		return nil, err
	}
	f, err := h.d.Store.Q.RenameCustomField(ctx, db.RenameCustomFieldParams{Label: label, ID: in.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	n, err := h.customFieldValues(ctx)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "field.update", "field", strconv.FormatInt(f.ID, 10), map[string]any{"label": f.Label})
	return &customFieldOutput{Body: customFieldView(f, n[f.ID])}, nil
}

// orderCustomFields sets the order of a scope's fields: every one of them once, as for the
// folders.
func (h *handlers) orderCustomFields(ctx context.Context, in *orderCustomFieldsInput) (*struct{}, error) {
	ids := in.Body.IDs
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListCustomFields(ctx)
		if err != nil {
			return err
		}
		have := []int64{}
		for _, f := range all {
			if f.Scope == in.Body.Scope {
				have = append(have, f.ID)
			}
		}
		return setOrder(ids, have, "field_order_mismatch", func(id, place int64) error {
			return q.SetCustomFieldSort(ctx, db.SetCustomFieldSortParams{Sort: place, ID: id})
		})
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "field.order", "field", "", map[string]any{"scope": in.Body.Scope, "ids": ids})
	return nil, nil
}

func (h *handlers) deleteCustomField(ctx context.Context, in *customFieldIDInput) (*struct{}, error) {
	f, err := h.d.Store.Q.GetCustomField(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	if _, err := h.d.Store.Q.DeleteCustomField(ctx, in.ID); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "field.delete", "field", strconv.FormatInt(f.ID, 10), map[string]any{"scope": f.Scope, "key": f.Key, "label": f.Label})
	return nil, nil
}

// fieldValue is a value as it is kept for a field of kind, "" for none: a bool keeps only
// true, a number its plain decimal form; ok false when the value is not one of the kind.
func fieldValue(kind, raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", true
	}
	if utf8.RuneCountInString(v) > maxFieldValue || strings.ContainsFunc(v, unicode.IsControl) {
		return "", false
	}
	switch kind {
	case "number":
		// A plain decimal, kept digit for digit: a float would round 9007199254740993.
		m := plainNumber.FindStringSubmatch(v)
		if m == nil {
			return "", false
		}
		whole := strings.TrimLeft(m[2], "0")
		if whole == "" {
			whole = "0"
		}
		frac := strings.TrimRight(m[3], "0")
		out := whole
		if frac != "" {
			out += "." + frac
		}
		if m[1] == "-" && out != "0" {
			out = "-" + out
		}
		return out, true
	case "bool":
		switch v {
		case "true":
			return "true", true
		case "false":
			return "", true
		}
		return "", false
	case "date":
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return "", false
		}
		return d.Format(time.DateOnly), true
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", false
		}
	}
	return v, true
}

// fieldChange is one value of an owner to write: "" clears it.
type fieldChange struct {
	field int64
	value string
}

// checkFieldValues reads the values of a body's custom_fields against scope's fields: each
// key must be a field, each value one of its kind. Everything wrong is refused together,
// at body.custom_fields.<key>.
func checkFieldValues(ctx context.Context, q *db.Queries, scope string, in map[string]string) ([]fieldChange, error) {
	if len(in) == 0 {
		return nil, nil
	}
	all, err := q.ListCustomFields(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]db.CustomField{}
	for _, f := range all {
		if f.Scope == scope {
			byKey[f.Key] = f
		}
	}
	var out []fieldChange
	var bad []error
	for key, raw := range in {
		f, ok := byKey[key]
		if !ok {
			bad = append(bad, fieldDetail("body.custom_fields."+key, "unknown_field", key))
			continue
		}
		v, ok := fieldValue(f.Kind, raw)
		if !ok {
			bad = append(bad, fieldDetail("body.custom_fields."+key, "bad_field_value", f.Kind))
			continue
		}
		out = append(out, fieldChange{field: f.ID, value: v})
	}
	if len(bad) > 0 {
		return nil, fieldErrs(bad...)
	}
	return out, nil
}

// liveChanges keeps the changes whose field still exists and locks those fields until the
// transaction ends: a field deleted since the values were checked takes its value along.
func liveChanges(ctx context.Context, q *db.Queries, changes []fieldChange) ([]fieldChange, error) {
	ids := make([]int64, len(changes))
	for i, c := range changes {
		ids[i] = c.field
	}
	live, err := q.LockCustomFields(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []fieldChange
	for _, c := range changes {
		if slices.Contains(live, c.field) {
			out = append(out, c)
		}
	}
	return out, nil
}

// setUserFields writes a user's checked values.
func setUserFields(ctx context.Context, q *db.Queries, userID int64, changes []fieldChange) error {
	changes, err := liveChanges(ctx, q, changes)
	if err != nil {
		return err
	}
	for _, c := range changes {
		var err error
		if c.value == "" {
			err = q.ClearUserFieldValue(ctx, db.ClearUserFieldValueParams{UserID: userID, FieldID: c.field})
		} else {
			err = q.SetUserFieldValue(ctx, db.SetUserFieldValueParams{UserID: userID, FieldID: c.field, Value: c.value})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func setNodeFields(ctx context.Context, q *db.Queries, nodeID int64, changes []fieldChange) error {
	changes, err := liveChanges(ctx, q, changes)
	if err != nil {
		return err
	}
	for _, c := range changes {
		var err error
		if c.value == "" {
			err = q.ClearNodeFieldValue(ctx, db.ClearNodeFieldValueParams{NodeID: nodeID, FieldID: c.field})
		} else {
			err = q.SetNodeFieldValue(ctx, db.SetNodeFieldValueParams{NodeID: nodeID, FieldID: c.field, Value: c.value})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// userFields are the values of the users of ids by user and key; ids nil: every user's.
func userFields(ctx context.Context, q *db.Queries, ids []int64) (map[int64]map[string]string, error) {
	rows, err := q.UserFieldValues(ctx, ids)
	if err != nil {
		return nil, err
	}
	m := map[int64]map[string]string{}
	for _, r := range rows {
		if m[r.UserID] == nil {
			m[r.UserID] = map[string]string{}
		}
		m[r.UserID][r.Key] = r.Value
	}
	return m, nil
}

func nodeFields(ctx context.Context, q *db.Queries) (map[int64]map[string]string, error) {
	rows, err := q.NodeFieldValues(ctx)
	if err != nil {
		return nil, err
	}
	m := map[int64]map[string]string{}
	for _, r := range rows {
		if m[r.NodeID] == nil {
			m[r.NodeID] = map[string]string{}
		}
		m[r.NodeID][r.Key] = r.Value
	}
	return m, nil
}

// nodeFieldsOf are one node's values by key, an object even with none.
func nodeFieldsOf(ctx context.Context, q *db.Queries, id int64) (map[string]string, error) {
	rows, err := q.NodeFieldValuesOf(ctx, id)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

// fieldsOrEmpty is an owner's values for JSON: an object even with none.
func fieldsOrEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// searchText is the users' values, lowercased, by user: what the list's search looks
// through besides the name, contact, note and tags. Each value is matched on its own, so the
// answer does not depend on the order a map gives them in.
func searchText(fields map[int64]map[string]string) map[int64][]string {
	out := make(map[int64][]string, len(fields))
	for id, kv := range fields {
		for _, v := range kv {
			out[id] = append(out[id], strings.ToLower(v))
		}
	}
	return out
}
