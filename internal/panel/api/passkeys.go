package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Passkeys are a second way into the panel beside the password: the admin adds one from the
// profile (after the password, and the code when 2FA is on), then signs in with it alone. A
// passkey is a key on the admin's device unlocked by its PIN or biometrics (user verification
// is required), so it stands for the password and the code together and asks for neither.

const (
	maxPasskeys = 20
	// passkeyTTL is how long a ceremony may take, from the options to the answer.
	passkeyTTL = 5 * time.Minute
	// A sign-in's ceremony is begun before anyone is known, so its waiting state is bounded:
	// so many per address and so many in all.
	passkeyCeremoniesPerIP = 5
	passkeyCeremonies      = 1024
)

type PasskeyView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type passkeysOutput struct{ Body []PasskeyView }

type passkeyOutput struct{ Body PasskeyView }

type passkeyBeginInput struct {
	Body struct {
		Password string `json:"password" minLength:"1" maxLength:"256" doc:"Пароль: без него украденная сессия добавила бы свой вход"`
		TOTP     string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения, если включена 2FA"`
	}
}

type passkeyOptionsOutput struct {
	Body struct {
		Options json.RawMessage `json:"options" doc:"Параметры WebAuthn для navigator.credentials.create() ({publicKey: …}, двоичные поля в base64url)"`
	}
}

type passkeyAddInput struct {
	Body struct {
		Name       string          `json:"name" minLength:"1" maxLength:"60"`
		Credential json.RawMessage `json:"credential" doc:"Ответ navigator.credentials.create() в JSON (PublicKeyCredential.toJSON())"`
	}
}

type passkeyIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type passkeyLoginBeginOutput struct {
	Body struct {
		Ceremony string          `json:"ceremony" doc:"Передаётся обратно в POST /auth/login/passkey"`
		Options  json.RawMessage `json:"options" doc:"Параметры WebAuthn для navigator.credentials.get() ({publicKey: …}, двоичные поля в base64url)"`
	}
}

type passkeyLoginInput struct {
	Body struct {
		Ceremony   string          `json:"ceremony" minLength:"1" maxLength:"64"`
		Credential json.RawMessage `json:"credential" doc:"Ответ navigator.credentials.get() в JSON (PublicKeyCredential.toJSON())"`
	}
}

// passkeyCeremony is a ceremony between its options and the answer. ip is the address
// that began a sign-in; the admin's own ceremony of adding a key has none.
type passkeyCeremony struct {
	session webauthn.SessionData
	ip      string
	expires time.Time
}

func (h *handlers) registerPasskeys() {
	tags := []string{"auth"}
	public := map[string]any{"public": true}
	huma.Register(h.api, huma.Operation{OperationID: "list-passkeys", Method: http.MethodGet, Path: "/api/v1/auth/passkeys", Summary: "Ключи входа (passkey)", Tags: tags}, h.listPasskeys)
	huma.Register(h.api, huma.Operation{OperationID: "passkey-begin", Method: http.MethodPost, Path: "/api/v1/auth/passkeys/begin", Summary: "Начать добавление passkey", Tags: tags}, h.passkeyBegin)
	huma.Register(h.api, huma.Operation{OperationID: "add-passkey", Method: http.MethodPost, Path: "/api/v1/auth/passkeys", Summary: "Добавить passkey", Tags: tags, DefaultStatus: http.StatusCreated}, h.addPasskey)
	huma.Register(h.api, huma.Operation{OperationID: "delete-passkey", Method: http.MethodDelete, Path: "/api/v1/auth/passkeys/{id}", Summary: "Удалить passkey", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deletePasskey)
	huma.Register(h.api, huma.Operation{OperationID: "passkey-login-begin", Method: http.MethodPost, Path: "/api/v1/auth/login/passkey/begin", Summary: "Начать вход по passkey", Tags: tags, Metadata: public, Security: []map[string][]string{{}}}, h.passkeyLoginBegin)
	huma.Register(h.api, huma.Operation{OperationID: "passkey-login", Method: http.MethodPost, Path: "/api/v1/auth/login/passkey", Summary: "Вход по passkey", Tags: tags, Metadata: public, Security: []map[string][]string{{}}}, h.passkeyLogin)
}

// passkeyRP is the relying party passkeys are made for: the panel's domain, at the address
// the admin link opens (settings.Endpoint). A passkey is bound to a domain and an IP address
// cannot be one, so a panel with no domain has no passkeys.
func (h *handlers) passkeyRP(ctx context.Context) (*webauthn.WebAuthn, error) {
	domain, err := h.d.Settings.String(ctx, settings.KeyDomain)
	if err != nil {
		return nil, err
	}
	if domain == "" {
		return nil, huma.Error409Conflict("passkey_no_domain")
	}
	ep, err := h.d.Settings.Endpoint(ctx)
	if err != nil {
		return nil, err
	}
	origin := "https://" + domain
	if ep.Port > 0 && ep.Port != 443 {
		origin = ep.URL()
	}
	return webauthn.New(&webauthn.Config{
		RPID:          domain,
		RPDisplayName: "mikan",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyTTL, TimeoutUVD: passkeyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyTTL, TimeoutUVD: passkeyTTL},
		},
		// A password manager may answer with outputs of extensions nobody asked for; they
		// mean nothing here and must not fail the sign-in.
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
}

// passkeyUser is an admin as WebAuthn sees it. The user handle is the admin's id: stable,
// and names nobody outside the panel.
type passkeyUser struct {
	admin db.Admin
	rows  []db.AdminPasskey
	creds []webauthn.Credential
}

func passkeyHandle(id int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(id)) }

func (u *passkeyUser) WebAuthnID() []byte                         { return passkeyHandle(u.admin.ID) }
func (u *passkeyUser) WebAuthnName() string                       { return u.admin.Username }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.admin.Username }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (h *handlers) loadPasskeyUser(ctx context.Context, admin db.Admin) (*passkeyUser, error) {
	rows, err := h.d.Store.Q.ListAdminPasskeys(ctx, admin.ID)
	if err != nil {
		return nil, err
	}
	u := &passkeyUser{admin: admin, rows: rows, creds: make([]webauthn.Credential, len(rows))}
	for i, r := range rows {
		if err := json.Unmarshal([]byte(r.Credential), &u.creds[i]); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func viewPasskey(p db.AdminPasskey) PasskeyView {
	return PasskeyView{ID: p.ID, Name: p.Name, CreatedAt: store.Time(p.CreatedAt), LastUsedAt: store.TimePtr(p.LastUsedAt)}
}

func (h *handlers) listPasskeys(ctx context.Context, _ *struct{}) (*passkeysOutput, error) {
	rows, err := h.d.Store.Q.ListAdminPasskeys(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	out := &passkeysOutput{Body: make([]PasskeyView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, viewPasskey(r))
	}
	return out, nil
}

// passkeyBegin starts adding a passkey. It asks for the password (and the code) as minting
// an API key does: a stolen session must not give itself a way in that outlives it.
func (h *handlers) passkeyBegin(ctx context.Context, in *passkeyBeginInput) (*passkeyOptionsOutput, error) {
	admin, err := h.d.Store.Q.GetAdmin(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	wa, err := h.passkeyRP(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reauth(ctx, admin, in.Body.Password, strings.TrimSpace(in.Body.TOTP)); err != nil {
		return nil, err
	}
	u, err := h.loadPasskeyUser(ctx, admin)
	if err != nil {
		return nil, err
	}
	if len(u.rows) >= maxPasskeys {
		return nil, huma.Error409Conflict("passkey_limit")
	}
	// The keys the admin has already are excluded: a device holds one key of the panel.
	creation, session, err := wa.BeginRegistration(u, webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(creation)
	if err != nil {
		return nil, err
	}
	h.passkeyMu.Lock()
	h.passkeyAdd[admin.ID] = passkeyCeremony{session: *session, expires: h.d.Now().Add(passkeyTTL)}
	h.passkeyMu.Unlock()
	out := &passkeyOptionsOutput{}
	out.Body.Options = raw
	return out, nil
}

func (h *handlers) addPasskey(ctx context.Context, in *passkeyAddInput) (*passkeyOutput, error) {
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		return nil, fieldErr("body.name", "name_blank")
	}
	adminID := sessionOf(ctx).AdminID
	now := h.d.Now()
	// The ceremony is used once, whatever its answer.
	h.passkeyMu.Lock()
	c, ok := h.passkeyAdd[adminID]
	delete(h.passkeyAdd, adminID)
	h.passkeyMu.Unlock()
	if !ok || now.After(c.expires) {
		return nil, huma.Error409Conflict("passkey_setup_expired")
	}
	admin, err := h.d.Store.Q.GetAdmin(ctx, adminID)
	if err != nil {
		return nil, err
	}
	wa, err := h.passkeyRP(ctx)
	if err != nil {
		return nil, err
	}
	u, err := h.loadPasskeyUser(ctx, admin)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(in.Body.Credential)
	if err != nil {
		return nil, fieldErr("body.credential", "invalid_passkey")
	}
	cred, err := wa.CreateCredential(u, c.session, parsed)
	if err != nil {
		h.d.Log.Info("passkey refused", "admin", admin.ID, "err", err)
		return nil, fieldErr("body.credential", "invalid_passkey")
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	row, err := h.d.Store.Q.CreateAdminPasskey(ctx, db.CreateAdminPasskeyParams{AdminID: admin.ID, CredentialID: cred.ID, Name: name, Credential: string(raw), CreatedAt: now.Unix()})
	if store.IsUnique(err) {
		return nil, huma.Error409Conflict("passkey_exists")
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.passkey_added", "passkey", strconv.FormatInt(row.ID, 10), map[string]any{"name": name})
	return &passkeyOutput{Body: viewPasskey(row)}, nil
}

func (h *handlers) deletePasskey(ctx context.Context, in *passkeyIDInput) (*struct{}, error) {
	adminID := sessionOf(ctx).AdminID
	n, err := h.d.Store.Q.DeleteAdminPasskey(ctx, db.DeleteAdminPasskeyParams{ID: in.ID, AdminID: adminID})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, adminID, "auth.passkey_deleted", "passkey", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

// passkeyLoginBegin gives the options of a sign-in. Nobody is named: the device offers the
// panel's passkeys it holds and the answer says whose it is.
func (h *handlers) passkeyLoginBegin(ctx context.Context, _ *struct{}) (*passkeyLoginBeginOutput, error) {
	wa, err := h.passkeyRP(ctx)
	if err != nil {
		return nil, err
	}
	ip := auth.ClientKey(clientOf(ctx).IP)
	now := h.d.Now()
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(assertion)
	if err != nil {
		return nil, err
	}
	id := secure.Token(32)
	h.passkeyMu.Lock()
	mine := 0
	for k, c := range h.passkeyLogins {
		if now.After(c.expires) {
			delete(h.passkeyLogins, k)
		} else if c.ip == ip {
			mine++
		}
	}
	full := mine >= passkeyCeremoniesPerIP || len(h.passkeyLogins) >= passkeyCeremonies
	if !full {
		h.passkeyLogins[id] = passkeyCeremony{session: *session, ip: ip, expires: now.Add(passkeyTTL)}
	}
	h.passkeyMu.Unlock()
	if full {
		return nil, tooManyAttempts(time.Minute)
	}
	out := &passkeyLoginBeginOutput{}
	out.Body.Ceremony, out.Body.Options = id, raw
	return out, nil
}

// passkeyLogin signs in with a passkey. A refused answer counts against the address as a
// wrong password does; the name's allowance is the password's and is left alone, so the
// owner of a passkey gets in while someone guesses the password.
func (h *handlers) passkeyLogin(ctx context.Context, in *passkeyLoginInput) (*loginOutput, error) {
	c := clientOf(ctx)
	now := h.d.Now()
	ipKey := "ip:" + auth.ClientKey(c.IP)
	okIP, waitIP, blocked := h.d.IPLimit.Reserve(ipKey, now)
	if !okIP {
		return nil, tooManyAttempts(waitIP)
	}
	h.passkeyMu.Lock()
	cer, ok := h.passkeyLogins[in.Body.Ceremony]
	delete(h.passkeyLogins, in.Body.Ceremony)
	h.passkeyMu.Unlock()
	if !ok || now.After(cer.expires) {
		return nil, huma.Error401Unauthorized("passkey_expired")
	}
	wa, err := h.passkeyRP(ctx)
	if err != nil {
		return nil, err
	}
	var user *passkeyUser
	refuse := func(reason string, err error) error {
		h.d.Log.Info("passkey sign-in refused", "reason", reason, "err", err)
		if user != nil {
			h.loginFailed(ctx, user.admin.Username, true, reason, blocked)
		} else {
			h.audit(ctx, 0, "auth.login_failed", "admin", "passkey", map[string]any{"reason": reason, "blocked": blocked})
		}
		return huma.Error401Unauthorized("invalid_passkey")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Body.Credential)
	if err != nil {
		return nil, refuse("bad_passkey", err)
	}
	var lookupErr error
	_, cred, err := wa.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
		if len(handle) != 8 {
			return nil, errors.New("not a handle of this panel")
		}
		admin, err := h.d.Store.Q.GetAdmin(ctx, int64(binary.BigEndian.Uint64(handle)))
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				lookupErr = err
			}
			return nil, err
		}
		if user, err = h.loadPasskeyUser(ctx, admin); err != nil {
			lookupErr = err
			return nil, err
		}
		return user, nil
	}, cer.session, parsed)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if err != nil {
		return nil, refuse("bad_passkey", err)
	}
	// The signature counter went back: two copies of the key exist, one of them not the
	// admin's.
	if cred.Authenticator.CloneWarning {
		return nil, refuse("passkey_cloned", nil)
	}
	var row db.AdminPasskey
	for _, r := range user.rows {
		if bytes.Equal(r.CredentialID, cred.ID) {
			row = r
		}
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.UseAdminPasskey(ctx, db.UseAdminPasskeyParams{Credential: string(raw), LastUsedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: row.ID}); err != nil {
		return nil, err
	}

	admin := user.admin
	h.d.IPLimit.Reset(ipKey)
	token, sess, err := h.d.Sessions.Create(ctx, admin.ID, c.IP, c.UserAgent)
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.SetAdminLastLogin(ctx, db.SetAdminLastLoginParams{LastLoginAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: admin.ID}); err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.login", "admin", admin.Username, map[string]any{"passkey": row.Name})
	return &loginOutput{SetCookie: sessionCookie(token, auth.MaxTTL), Body: MeBody{Admin: viewAdmin(admin), CSRFToken: sess.CsrfToken}}, nil
}
