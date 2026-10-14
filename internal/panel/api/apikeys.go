package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

// API keys let scripts call the API with "Authorization: Bearer <key>" instead of an
// admin's browser session. A key acts for the admin who made it; "read" keys only GET.
// Keys never reach the auth and api-keys endpoints: a leaked key must not be able to
// change the password, read sessions or mint more keys.
const (
	apiKeyPrefix    = "mk_"
	apiKeyLen       = 40 // base62 characters after the prefix: ~238 bits
	apiKeyShown     = 10 // characters of the key kept to tell keys apart
	maxAPIKeys      = 50 // per panel
	apiKeyTouchSecs = 60 // seconds between last-used updates of one key
	maxKeyDays      = 3650
)

// sessionOnlyTags are the endpoints an API key cannot use, with the operations marked
// sessionOnly (payment keys and refunds: whoever sets them decides where money goes).
var sessionOnlyTags = []string{"auth", "api-keys"}

var (
	sessionOnly    = map[string]any{"sessionOnly": true}
	sessionOnlyExt = map[string]any{"x-session-only": true} // tells the API reference
)

const keyAPIKey ctxKey = 100

type APIKeyView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix" doc:"Первые символы ключа, чтобы отличать ключи"`
	Scope      string     `json:"scope" enum:"read,full"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastIP     string     `json:"last_ip,omitempty"`
}

type apiKeysOutput struct{ Body []APIKeyView }

type createAPIKeyInput struct {
	Body struct {
		Name       string `json:"name" minLength:"1" maxLength:"60"`
		Scope      string `json:"scope" enum:"read,full" doc:"read — только GET-запросы, без ссылок подписок и секретных адресов; full — изменения, кроме входа, сессий, ключей и операций, где уходят деньги, ключи и адреса клиентов (в справочнике помечены «только сессия»)"`
		ExpireDays int    `json:"expire_days,omitempty" minimum:"0" maximum:"3650" doc:"Срок в днях; 0 — пока не отзовут"`
		Password   string `json:"password" minLength:"1" maxLength:"256" doc:"Пароль админа: ключ не выпускается из одной лишь украденной сессии"`
		TOTP       string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения, если включена 2FA"`
	}
}

type createAPIKeyOutput struct {
	Body struct {
		APIKeyView
		Key string `json:"key" doc:"Сам ключ: показывается один раз"`
	}
}

type apiKeyIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

func (h *handlers) registerAPIKeys() {
	tags := []string{"api-keys"}
	huma.Register(h.api, huma.Operation{OperationID: "list-api-keys", Method: http.MethodGet, Path: "/api/v1/api-keys", Summary: "Ключи API", Tags: tags}, h.listAPIKeys)
	huma.Register(h.api, huma.Operation{OperationID: "create-api-key", Method: http.MethodPost, Path: "/api/v1/api-keys", Summary: "Создать ключ API", Tags: tags, DefaultStatus: http.StatusCreated}, h.createAPIKey)
	huma.Register(h.api, huma.Operation{OperationID: "delete-api-key", Method: http.MethodDelete, Path: "/api/v1/api-keys/{id}", Summary: "Отозвать ключ API", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteAPIKey)
}

func viewAPIKey(k db.ApiKey) APIKeyView {
	v := APIKeyView{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Scope: k.Scope, CreatedAt: time.Unix(k.CreatedAt, 0).UTC(), LastIP: k.LastIp}
	if k.ExpiresAt.Valid {
		t := time.Unix(k.ExpiresAt.Int64, 0).UTC()
		v.ExpiresAt = &t
	}
	if k.LastUsedAt.Valid {
		t := time.Unix(k.LastUsedAt.Int64, 0).UTC()
		v.LastUsedAt = &t
	}
	return v
}

func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (h *handlers) listAPIKeys(ctx context.Context, _ *struct{}) (*apiKeysOutput, error) {
	keys, err := h.d.Store.Q.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := &apiKeysOutput{Body: make([]APIKeyView, 0, len(keys))}
	for _, k := range keys {
		out.Body = append(out.Body, viewAPIKey(k))
	}
	return out, nil
}

func (h *handlers) createAPIKey(ctx context.Context, in *createAPIKeyInput) (*createAPIKeyOutput, error) {
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, fieldErr("body.name", "name_blank")
	}
	admin, err := h.d.Store.Q.GetAdmin(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	if err := h.reauth(ctx, admin, in.Body.Password, strings.TrimSpace(in.Body.TOTP)); err != nil {
		return nil, err
	}
	n, err := h.d.Store.Q.CountAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	if n >= maxAPIKeys {
		return nil, huma.Error409Conflict("api_keys_limit")
	}
	now := h.d.Now()
	key := apiKeyPrefix + secure.Token(apiKeyLen)
	var expires sql.NullInt64
	if d := in.Body.ExpireDays; d > 0 {
		expires = sql.NullInt64{Int64: now.Add(time.Duration(min(d, maxKeyDays)) * 24 * time.Hour).Unix(), Valid: true}
	}
	row, err := h.d.Store.Q.CreateAPIKey(ctx, db.CreateAPIKeyParams{AdminID: sessionOf(ctx).AdminID, Name: name, Prefix: key[:apiKeyShown],
		Hash: hashAPIKey(key), Scope: in.Body.Scope, CreatedAt: now.Unix(), ExpiresAt: expires})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "api_key.create", "api_key", row.Prefix, map[string]any{"name": name, "scope": row.Scope})
	out := &createAPIKeyOutput{}
	out.Body.APIKeyView, out.Body.Key = viewAPIKey(row), key
	return out, nil
}

func (h *handlers) deleteAPIKey(ctx context.Context, in *apiKeyIDInput) (*struct{}, error) {
	n, err := h.d.Store.Q.DeleteAPIKey(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "api_key.delete", "api_key", "", map[string]any{"id": in.ID})
	return nil, nil
}

// bearer authenticates a request by API key; the caller has seen an Authorization header.
func (h *handlers) bearer(ctx huma.Context, next func(huma.Context), mutating bool) {
	c := clientOf(ctx.Context())
	ipKey, now := "key:"+auth.ClientKey(c.IP), h.d.Now()
	if ok, wait := h.d.IPLimit.Allowed(ipKey, now); !ok {
		ctx.SetHeader("Retry-After", strconv.Itoa(max(1, int(wait.Round(time.Second)/time.Second))))
		_ = huma.WriteErr(h.api, ctx, http.StatusTooManyRequests, "rate_limited")
		return
	}
	token, ok := strings.CutPrefix(ctx.Header("Authorization"), "Bearer ")
	token = strings.TrimSpace(token)
	var key db.ApiKey
	var err error
	if ok && strings.HasPrefix(token, apiKeyPrefix) && len(token) <= len(apiKeyPrefix)+apiKeyLen+8 {
		key, err = h.d.Store.Q.GetAPIKeyByHash(ctx.Context(), hashAPIKey(token))
	} else {
		err = sql.ErrNoRows
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.d.Log.Error("api key lookup", "err", err)
		_ = huma.WriteErr(h.api, ctx, http.StatusInternalServerError, "internal error")
		return
	}
	if err != nil || (key.ExpiresAt.Valid && key.ExpiresAt.Int64 <= now.Unix()) {
		h.d.IPLimit.Fail(ipKey, now)
		_ = huma.WriteErr(h.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	op := ctx.Operation()
	if only, _ := op.Metadata["sessionOnly"].(bool); only {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "session_only")
		return
	}
	for _, tag := range op.Tags {
		if slices.Contains(sessionOnlyTags, tag) {
			_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "session_only")
			return
		}
	}
	if mutating && key.Scope != "full" {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "read_only_key")
		return
	}
	if err := h.d.Store.Q.TouchAPIKey(ctx.Context(), db.TouchAPIKeyParams{LastUsedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, LastIp: c.IP,
		ID: key.ID, LastUsedAt_2: sql.NullInt64{Int64: now.Unix() - apiKeyTouchSecs, Valid: true}}); err != nil {
		h.d.Log.Warn("api key touch", "err", err)
	}
	ctx = huma.WithValue(ctx, keySession, db.Session{AdminID: key.AdminID})
	next(huma.WithValue(ctx, keyAPIKey, key))
}

// apiKeyOf is the key a request came with, if any.
func apiKeyOf(ctx context.Context) (db.ApiKey, bool) {
	k, ok := ctx.Value(keyAPIKey).(db.ApiKey)
	return k, ok
}
