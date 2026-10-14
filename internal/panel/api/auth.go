package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

type AdminView struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	TOTPEnabled bool       `json:"totp_enabled"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type MeBody struct {
	Admin     AdminView `json:"admin"`
	CSRFToken string    `json:"csrf_token"`
}

func viewAdmin(a db.Admin) AdminView {
	v := AdminView{ID: a.ID, Username: a.Username, TOTPEnabled: a.TotpSecret.Valid}
	if a.LastLoginAt.Valid {
		t := time.Unix(a.LastLoginAt.Int64, 0).UTC()
		v.LastLoginAt = &t
	}
	return v
}

type loginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" maxLength:"64"`
		Password string `json:"password" minLength:"1" maxLength:"256"`
		TOTP     string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения или резервный код"`
	}
}

type meOutput struct {
	Body MeBody
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      MeBody
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type passwordInput struct {
	Body struct {
		Current string `json:"current" minLength:"1" maxLength:"256"`
		// A struct tag takes no constant: minLength repeats auth.MinPassword (TestPasswordTagIsMinPassword).
		New        string `json:"new" minLength:"12" maxLength:"256" doc:"Не короче 12 символов"`
		RevokeKeys *bool  `json:"revoke_keys,omitempty" doc:"Отозвать и ключи API (по умолчанию да): другие сессии завершаются при смене пароля, ключ, выпущенный из угнанной сессии, пережил бы это"`
	}
}

type totpSetupInput struct {
	Body struct {
		Password string `json:"password" minLength:"1" maxLength:"256" doc:"Пароль: без него украденная сессия включила бы 2FA на себя и закрыла вход владельцу"`
	}
}

type totpSetupOutput struct {
	Body struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}
}

type totpCodeInput struct {
	Body struct {
		Code string `json:"code" minLength:"6" maxLength:"16"`
	}
}

type totpDisableInput struct {
	Body struct {
		Password string `json:"password" minLength:"1" maxLength:"256"`
		Code     string `json:"code" minLength:"6" maxLength:"16"`
	}
}

type recoveryOutput struct {
	Body struct {
		RecoveryCodes []string `json:"recovery_codes" doc:"Показываются один раз"`
	}
}

type SessionView struct {
	ID         string    `json:"id"`
	Current    bool      `json:"current"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type sessionsOutput struct {
	Body []SessionView
}

type sessionIDInput struct {
	ID string `path:"id" minLength:"64" maxLength:"64"`
}

type pendingTOTP struct {
	secret  string
	expires time.Time
}

func (h *handlers) registerAuth() {
	public := map[string]any{"public": true}
	huma.Register(h.api, huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/api/v1/auth/login", Summary: "Вход", Tags: []string{"auth"}, Metadata: public, Security: []map[string][]string{{}}}, h.login)
	huma.Register(h.api, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/api/v1/auth/logout", Summary: "Выход", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent}, h.logout)
	huma.Register(h.api, huma.Operation{OperationID: "me", Method: http.MethodGet, Path: "/api/v1/auth/me", Summary: "Текущий админ", Tags: []string{"auth"}}, h.me)
	huma.Register(h.api, huma.Operation{OperationID: "change-password", Method: http.MethodPost, Path: "/api/v1/auth/password", Summary: "Сменить пароль", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent}, h.changePassword)
	huma.Register(h.api, huma.Operation{OperationID: "totp-setup", Method: http.MethodPost, Path: "/api/v1/auth/totp/setup", Summary: "Начать настройку 2FA", Tags: []string{"auth"}}, h.totpSetup)
	huma.Register(h.api, huma.Operation{OperationID: "totp-enable", Method: http.MethodPost, Path: "/api/v1/auth/totp/enable", Summary: "Включить 2FA", Tags: []string{"auth"}}, h.totpEnable)
	huma.Register(h.api, huma.Operation{OperationID: "totp-disable", Method: http.MethodPost, Path: "/api/v1/auth/totp/disable", Summary: "Выключить 2FA", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent}, h.totpDisable)
	huma.Register(h.api, huma.Operation{OperationID: "list-sessions", Method: http.MethodGet, Path: "/api/v1/auth/sessions", Summary: "Активные сессии", Tags: []string{"auth"}}, h.listSessions)
	huma.Register(h.api, huma.Operation{OperationID: "revoke-session", Method: http.MethodDelete, Path: "/api/v1/auth/sessions/{id}", Summary: "Завершить сессию", Tags: []string{"auth"}, DefaultStatus: http.StatusNoContent}, h.revokeSession)
}

func sessionCookie(value string, maxAge time.Duration) http.Cookie {
	c := http.Cookie{Name: auth.CookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	} else {
		c.MaxAge = -1
	}
	return c
}

func tooManyAttempts(wait time.Duration) error {
	secs := int(wait.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return huma.ErrorWithHeaders(huma.Error429TooManyRequests("rate_limited"), http.Header{"Retry-After": {strconv.Itoa(secs)}})
}

// verifyPassword runs the password hash. Each run takes 64 MiB: only a few at a time, so
// a burst of logins queues instead of exhausting the server's memory.
func (h *handlers) verifyPassword(ctx context.Context, password, hash string) (bool, error) {
	select {
	case h.hashSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-h.hashSem }()
	return auth.VerifyPassword(password, hash)
}

func (h *handlers) login(ctx context.Context, in *loginInput) (*loginOutput, error) {
	c := clientOf(ctx)
	now := h.d.Now()
	username := strings.ToLower(strings.TrimSpace(in.Body.Username))
	ipKey, userKey := "ip:"+auth.ClientKey(c.IP), "user:"+username
	// The attempt is counted before the password is hashed: parallel requests all passed a
	// check made first and counted after, each getting its own free guess.
	okIP, waitIP, blockedIP := h.d.IPLimit.Reserve(ipKey, now)
	if !okIP {
		return nil, tooManyAttempts(waitIP)
	}
	okUser, waitUser, blockedUser := h.d.UserLimit.Reserve(userKey, now)
	if !okUser {
		return nil, tooManyAttempts(waitUser)
	}
	blocked := blockedIP || blockedUser

	admin, err := h.d.Store.Q.GetAdminByUsername(ctx, username)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	hash := h.dummyHash
	if found {
		hash = admin.PasswordHash
	}
	// The dummy hash keeps timing identical for unknown usernames.
	ok, err := h.verifyPassword(ctx, in.Body.Password, hash)
	if err != nil && found {
		return nil, err
	}
	if !found || !ok {
		h.loginFailed(ctx, username, found, "bad_password", blocked)
		return nil, huma.Error401Unauthorized("invalid_credentials")
	}

	if admin.TotpSecret.Valid {
		code := strings.TrimSpace(in.Body.TOTP)
		if code == "" {
			return nil, huma.Error401Unauthorized("totp_required")
		}
		if !h.d.TOTP.Validate(admin.ID, admin.TotpSecret.String, code, now) {
			used, err := h.spendRecoveryCode(ctx, admin, code)
			if err != nil {
				return nil, err
			}
			if !used {
				h.loginFailed(ctx, username, true, "bad_totp", blocked)
				return nil, huma.Error401Unauthorized("invalid_totp")
			}
			h.audit(ctx, admin.ID, "auth.recovery_code_used", "admin", admin.Username, nil)
		}
	}

	h.d.IPLimit.Reset(ipKey)
	h.d.UserLimit.Reset(userKey)
	token, sess, err := h.d.Sessions.Create(ctx, admin.ID, c.IP, c.UserAgent)
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.SetAdminLastLogin(ctx, db.SetAdminLastLoginParams{LastLoginAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: admin.ID}); err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.login", "admin", admin.Username, nil)
	return &loginOutput{SetCookie: sessionCookie(token, auth.MaxTTL), Body: MeBody{Admin: viewAdmin(admin), CSRFToken: sess.CsrfToken}}, nil
}

// loginFailed writes the audit entry of a refused login (the attempt itself was counted
// when it started). A name nobody has is not kept as typed: it is often a password pasted
// into the wrong field, and the log is readable; a short hash tells repeats apart.
func (h *handlers) loginFailed(ctx context.Context, username string, known bool, reason string, blocked bool) {
	target := username
	if !known {
		target = "unknown:" + secure.SHA256Hex(username)[:8]
	}
	h.audit(ctx, 0, "auth.login_failed", "admin", target, map[string]any{"reason": reason, "blocked": blocked})
}

// spendRecoveryCode uses a recovery code of the admin's if code is one. The code leaves the
// stored list only if the list is still the one it was found in: of two logins with the
// same code, one is refused.
func (h *handlers) spendRecoveryCode(ctx context.Context, admin db.Admin, code string) (bool, error) {
	rest, ok := auth.UseRecoveryCode(admin.RecoveryCodes.String, code)
	if !ok {
		return false, nil
	}
	n, err := h.d.Store.Q.SpendAdminRecoveryCodes(ctx, db.SpendAdminRecoveryCodesParams{Rest: sql.NullString{String: rest, Valid: true}, ID: admin.ID, Was: admin.RecoveryCodes})
	return n == 1, err
}

// reauth asks a session for the admin's password, and the TOTP code when 2FA is on, before
// what a stolen session must not do alone: minting an API key, starting 2FA. Wrong answers
// count against the admin's own allowance, so the password cannot be guessed through a
// session.
func (h *handlers) reauth(ctx context.Context, admin db.Admin, password, code string) error {
	return h.guarded(admin, func(now time.Time) error {
		ok, err := h.verifyPassword(ctx, password, admin.PasswordHash)
		if err != nil {
			return err
		}
		if !ok {
			return huma.Error422UnprocessableEntity("wrong_password", &huma.ErrorDetail{Location: "body.password", Message: "wrong_password"})
		}
		if admin.TotpSecret.Valid && !h.d.TOTP.Validate(admin.ID, admin.TotpSecret.String, code, now) {
			return huma.Error422UnprocessableEntity("invalid_totp", &huma.ErrorDetail{Location: "body.totp", Message: "invalid_totp"})
		}
		return nil
	})
}

// guarded runs a check of the admin's password (and code) against the admin's own
// allowance: every attempt counts, a right one clears it. Changing the password and
// turning 2FA off ask for the password too, so they go through it as well.
func (h *handlers) guarded(admin db.Admin, check func(now time.Time) error) error {
	now := h.d.Now()
	key := "reauth:" + strconv.FormatInt(admin.ID, 10)
	if ok, wait, _ := h.d.UserLimit.Reserve(key, now); !ok {
		return tooManyAttempts(wait)
	}
	if err := check(now); err != nil {
		return err
	}
	h.d.UserLimit.Reset(key)
	return nil
}

func (h *handlers) logout(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
	sess := sessionOf(ctx)
	if err := h.d.Store.Q.DeleteSession(ctx, sess.IDHash); err != nil {
		return nil, err
	}
	h.audit(ctx, sess.AdminID, "auth.logout", "", "", nil)
	return &logoutOutput{SetCookie: sessionCookie("", 0)}, nil
}

func (h *handlers) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	sess := sessionOf(ctx)
	admin, err := h.d.Store.Q.GetAdmin(ctx, sess.AdminID)
	if err != nil {
		return nil, err
	}
	return &meOutput{Body: MeBody{Admin: viewAdmin(admin), CSRFToken: sess.CsrfToken}}, nil
}

func (h *handlers) changePassword(ctx context.Context, in *passwordInput) (*struct{}, error) {
	sess := sessionOf(ctx)
	admin, err := h.d.Store.Q.GetAdmin(ctx, sess.AdminID)
	if err != nil {
		return nil, err
	}
	if err := h.guarded(admin, func(time.Time) error {
		ok, err := h.verifyPassword(ctx, in.Body.Current, admin.PasswordHash)
		if err == nil && !ok {
			err = huma.Error422UnprocessableEntity("wrong_password", &huma.ErrorDetail{Location: "body.current", Message: "wrong_password"})
		}
		return err
	}); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(in.Body.New)
	if err != nil {
		return nil, err
	}
	var keys int64
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetAdminPassword(ctx, db.SetAdminPasswordParams{PasswordHash: hash, ID: admin.ID}); err != nil {
			return err
		}
		if err := q.DeleteAdminSessionsExcept(ctx, db.DeleteAdminSessionsExceptParams{AdminID: admin.ID, IDHash: sess.IDHash}); err != nil {
			return err
		}
		// The other sessions end with the old password; so do the keys, unless the admin
		// keeps them: a key made from a hijacked session would otherwise outlive the reaction.
		if in.Body.RevokeKeys == nil || *in.Body.RevokeKeys {
			var err error
			keys, err = q.DeleteAPIKeysOf(ctx, admin.ID)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.password_changed", "admin", admin.Username, map[string]any{"keys_revoked": keys})
	return nil, nil
}

func (h *handlers) totpSetup(ctx context.Context, in *totpSetupInput) (*totpSetupOutput, error) {
	sess := sessionOf(ctx)
	admin, err := h.d.Store.Q.GetAdmin(ctx, sess.AdminID)
	if err != nil {
		return nil, err
	}
	if admin.TotpSecret.Valid {
		return nil, huma.Error409Conflict("totp_already_enabled")
	}
	if err := h.reauth(ctx, admin, in.Body.Password, ""); err != nil {
		return nil, err
	}
	key, err := auth.NewTOTPKey(admin.Username)
	if err != nil {
		return nil, err
	}
	h.pendingMu.Lock()
	h.pending[admin.ID] = pendingTOTP{secret: key.Secret(), expires: h.d.Now().Add(10 * time.Minute)}
	h.pendingMu.Unlock()
	out := &totpSetupOutput{}
	out.Body.Secret = key.Secret()
	out.Body.URI = key.URL()
	return out, nil
}

func (h *handlers) totpEnable(ctx context.Context, in *totpCodeInput) (*recoveryOutput, error) {
	sess := sessionOf(ctx)
	now := h.d.Now()
	h.pendingMu.Lock()
	p, ok := h.pending[sess.AdminID]
	h.pendingMu.Unlock()
	if !ok || now.After(p.expires) {
		return nil, huma.Error409Conflict("totp_setup_expired")
	}
	if !h.d.TOTP.Validate(sess.AdminID, p.secret, in.Body.Code, now) {
		return nil, huma.Error422UnprocessableEntity("invalid_totp", &huma.ErrorDetail{Location: "body.code", Message: "invalid_totp"})
	}
	plain, stored := auth.NewRecoveryCodes(10)
	if err := h.d.Store.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{
		TotpSecret:    sql.NullString{String: p.secret, Valid: true},
		RecoveryCodes: sql.NullString{String: stored, Valid: true},
		ID:            sess.AdminID,
	}); err != nil {
		return nil, err
	}
	h.pendingMu.Lock()
	delete(h.pending, sess.AdminID)
	h.pendingMu.Unlock()
	h.audit(ctx, sess.AdminID, "auth.totp_enabled", "", "", nil)
	out := &recoveryOutput{}
	out.Body.RecoveryCodes = plain
	return out, nil
}

func (h *handlers) totpDisable(ctx context.Context, in *totpDisableInput) (*struct{}, error) {
	sess := sessionOf(ctx)
	admin, err := h.d.Store.Q.GetAdmin(ctx, sess.AdminID)
	if err != nil {
		return nil, err
	}
	if !admin.TotpSecret.Valid {
		return nil, nil
	}
	if err := h.guarded(admin, func(now time.Time) error {
		ok, err := h.verifyPassword(ctx, in.Body.Password, admin.PasswordHash)
		if err != nil {
			return err
		}
		if !ok || !h.d.TOTP.Validate(admin.ID, admin.TotpSecret.String, in.Body.Code, now) {
			return huma.Error422UnprocessableEntity("invalid_credentials", &huma.ErrorDetail{Location: "body", Message: "invalid_password_or_code"})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{ID: admin.ID}); err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.totp_disabled", "", "", nil)
	return nil, nil
}

func (h *handlers) listSessions(ctx context.Context, _ *struct{}) (*sessionsOutput, error) {
	cur := sessionOf(ctx)
	rows, err := h.d.Store.Q.ListAdminSessions(ctx, cur.AdminID)
	if err != nil {
		return nil, err
	}
	out := &sessionsOutput{Body: make([]SessionView, 0, len(rows))}
	for _, s := range rows {
		out.Body = append(out.Body, SessionView{
			ID: s.IDHash, Current: s.IDHash == cur.IDHash, IP: s.Ip, UserAgent: s.UserAgent,
			CreatedAt: time.Unix(s.CreatedAt, 0).UTC(), LastSeenAt: time.Unix(s.LastSeenAt, 0).UTC(),
		})
	}
	return out, nil
}

func (h *handlers) revokeSession(ctx context.Context, in *sessionIDInput) (*struct{}, error) {
	cur := sessionOf(ctx)
	target, err := h.d.Store.Q.GetSession(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && target.AdminID != cur.AdminID) {
		return nil, huma.Error404NotFound("session_not_found")
	}
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.DeleteSession(ctx, target.IDHash); err != nil {
		return nil, err
	}
	h.audit(ctx, cur.AdminID, "auth.session_revoked", "session", target.IDHash[:12], nil)
	return nil, nil
}
