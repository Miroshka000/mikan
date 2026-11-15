package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
	"mikan/internal/tgapi"
)

// Accounts of other services an admin signs in with beside the password (#20): a GitHub
// account, through the admin's own OAuth app, and a Telegram one, through the panel's bot.
// The admin links one from the profile (after the password, and the code when 2FA is on);
// then signing in with it stands for the password, and the code is still asked when 2FA is
// on: the account is one factor, held by someone else's service.
//
// GitHub sends the browser back to the panel's callback, which trades the code for the
// account's id and sends the browser on to the admin's page: a link is made there and then
// (the state says whose), a sign-in leaves a ticket in the page's address (#github=…) that
// the login page redeems, with the code when 2FA asks for one. Telegram's login page sends
// the browser straight back to the admin's page with the signed account (#tgAuthResult=…),
// which the page hands to the panel. The panel's session cookie is SameSite=Strict, so the
// callback, reached from GitHub, never relies on it.

const (
	// KeyGitHubClientID and KeyGitHubSecret are the admin's GitHub OAuth app; the secret
	// never leaves the panel's API.
	KeyGitHubClientID = "github_client_id"
	KeyGitHubSecret   = "github_client_secret"

	// identityTTL is how long a round trip through GitHub or Telegram may take, and a
	// ticket wait for its code.
	identityTTL = 5 * time.Minute
	// Sign-ins are begun before anyone is known: so many waiting per address and in all.
	identityStatesPerIP = 5
	identityStates      = 1024

	providerGitHub   = "github"
	providerTelegram = "telegram"
)

// The services' addresses; tests point them at their own servers.
var (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubUserURL      = "https://api.github.com/user"
	telegramAuthURL    = "https://oauth.telegram.org/auth"
	identityHTTP       = &http.Client{Timeout: 15 * time.Second}
)

// githubState is a round trip through GitHub under way: a link of admin link's, or a
// sign-in (link 0) begun from ip.
type githubState struct {
	link    int64
	ip      string
	expires time.Time
}

// identityTicket is an account GitHub vouched for, waiting for the login page to redeem it
// (with the code when 2FA asks for one) from the address that began the sign-in.
type identityTicket struct {
	identity db.AdminIdentity
	name     string
	ip       string
	expires  time.Time
}

type IdentityView struct {
	ID         int64      `json:"id"`
	Provider   string     `json:"provider" enum:"github,telegram"`
	Name       string     `json:"name" doc:"Имя аккаунта там: логин GitHub, @username или имя в Telegram"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func viewIdentity(r db.AdminIdentity) IdentityView {
	v := IdentityView{ID: r.ID, Provider: r.Provider, Name: r.Name, CreatedAt: store.Time(r.CreatedAt)}
	if r.LastUsedAt.Valid {
		t := store.Time(r.LastUsedAt.Int64)
		v.LastUsedAt = &t
	}
	return v
}

type identitiesOutput struct{ Body []IdentityView }
type identityOutput struct{ Body IdentityView }

// IdentityProviders is how the panel is set up for each way in.
type IdentityProviders struct {
	GitHub   GitHubProvider   `json:"github"`
	Telegram TelegramProvider `json:"telegram"`
}

type GitHubProvider struct {
	ClientID    string `json:"client_id" doc:"Client ID OAuth-приложения GitHub; пусто — вход через GitHub выключен"`
	SecretSet   bool   `json:"secret_set" doc:"Client secret сохранён (сам он не показывается)"`
	CallbackURL string `json:"callback_url" doc:"Authorization callback URL, который нужно указать в приложении GitHub; пусто, пока у панели нет адреса"`
}

type TelegramProvider struct {
	Bot    string `json:"bot" doc:"Бот панели (@username), через которого идёт вход; пусто — бот не подключён"`
	Domain string `json:"domain" doc:"Домен, который нужно указать боту в @BotFather (/setdomain); пусто — у панели нет домена, вход через Telegram недоступен"`
}

type identityProvidersOutput struct{ Body IdentityProviders }

type githubAppInput struct {
	Body struct {
		ClientID string  `json:"client_id" maxLength:"64" doc:"Client ID; пусто — выключить вход через GitHub"`
		Secret   *string `json:"client_secret,omitempty" maxLength:"128" doc:"Client secret; не передан — остаётся сохранённый"`
	}
}

type identityBeginInput struct {
	Body struct {
		Password string `json:"password" minLength:"1" maxLength:"256" doc:"Пароль: без него украденная сессия привязала бы свой аккаунт"`
		TOTP     string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения, если включена 2FA"`
	}
}

type identityURLOutput struct {
	Body struct {
		URL string `json:"url" doc:"Куда перейти браузеру"`
	}
}

type telegramLinkInput struct {
	Body struct {
		Result string `json:"result" minLength:"1" maxLength:"4096" doc:"Значение tgAuthResult из адреса, с которым Telegram вернул на страницу"`
	}
}

type identityIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type loginMethodsOutput struct {
	Body struct {
		GitHub   bool `json:"github" doc:"Можно войти через GitHub"`
		Telegram bool `json:"telegram" doc:"Можно войти через Telegram"`
	}
}

type githubCallbackInput struct {
	Code  string `query:"code" maxLength:"512"`
	State string `query:"state" maxLength:"128"`
	Error string `query:"error" maxLength:"128"`
}

type redirectOutput struct {
	Status   int
	Location string `header:"Location"`
	Cache    string `header:"Cache-Control"`
}

type githubLoginInput struct {
	Body struct {
		Ticket string `json:"ticket" minLength:"1" maxLength:"64" doc:"Из адреса страницы входа (#github=…)"`
		TOTP   string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения, если включена 2FA"`
	}
}

type telegramLoginInput struct {
	Body struct {
		Result string `json:"result" minLength:"1" maxLength:"4096" doc:"Значение tgAuthResult из адреса страницы входа"`
		TOTP   string `json:"totp,omitempty" maxLength:"16" doc:"Код из приложения, если включена 2FA"`
	}
}

func (h *handlers) registerIdentities() {
	tags := []string{"auth"}
	public := map[string]any{"public": true}
	none := []map[string][]string{{}}
	huma.Register(h.api, huma.Operation{OperationID: "list-identities", Method: http.MethodGet, Path: "/api/v1/auth/identities", Summary: "Аккаунты для входа (GitHub, Telegram)", Tags: tags}, h.listIdentities)
	huma.Register(h.api, huma.Operation{OperationID: "delete-identity", Method: http.MethodDelete, Path: "/api/v1/auth/identities/{id}", Summary: "Отвязать аккаунт", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteIdentity)
	huma.Register(h.api, huma.Operation{OperationID: "identity-providers", Method: http.MethodGet, Path: "/api/v1/auth/identity-providers", Summary: "Настройка входа через GitHub и Telegram", Tags: tags}, h.identityProviders)
	huma.Register(h.api, huma.Operation{OperationID: "set-github-app", Method: http.MethodPut, Path: "/api/v1/auth/identity-providers/github", Summary: "OAuth-приложение GitHub", Tags: tags}, h.setGitHubApp)
	huma.Register(h.api, huma.Operation{OperationID: "github-link-begin", Method: http.MethodPost, Path: "/api/v1/auth/identities/github/begin", Summary: "Начать привязку GitHub", Tags: tags}, h.githubLinkBegin)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-link-begin", Method: http.MethodPost, Path: "/api/v1/auth/identities/telegram/begin", Summary: "Начать привязку Telegram", Tags: tags}, h.telegramLinkBegin)
	huma.Register(h.api, huma.Operation{OperationID: "link-telegram", Method: http.MethodPost, Path: "/api/v1/auth/identities/telegram", Summary: "Привязать Telegram", Tags: tags, DefaultStatus: http.StatusCreated}, h.linkTelegram)
	huma.Register(h.api, huma.Operation{OperationID: "login-methods", Method: http.MethodGet, Path: "/api/v1/auth/login/methods", Summary: "Способы входа", Tags: tags, Metadata: public, Security: none}, h.loginMethods)
	huma.Register(h.api, huma.Operation{OperationID: "github-login-begin", Method: http.MethodPost, Path: "/api/v1/auth/login/github/begin", Summary: "Начать вход через GitHub", Tags: tags, Metadata: public, Security: none}, h.githubLoginBegin)
	huma.Register(h.api, huma.Operation{OperationID: "github-callback", Method: http.MethodGet, Path: "/api/v1/auth/github/callback", Summary: "Возврат с GitHub", Tags: tags, Metadata: public, Security: none,
		DefaultStatus: http.StatusFound}, h.githubCallback)
	huma.Register(h.api, huma.Operation{OperationID: "github-login", Method: http.MethodPost, Path: "/api/v1/auth/login/github", Summary: "Вход через GitHub", Tags: tags, Metadata: public, Security: none}, h.githubLogin)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-login-begin", Method: http.MethodPost, Path: "/api/v1/auth/login/telegram/begin", Summary: "Начать вход через Telegram", Tags: tags, Metadata: public, Security: none}, h.telegramLoginBegin)
	huma.Register(h.api, huma.Operation{OperationID: "telegram-login", Method: http.MethodPost, Path: "/api/v1/auth/login/telegram", Summary: "Вход через Telegram", Tags: tags, Metadata: public, Security: none}, h.telegramLogin)
}

// panelOrigin is the origin the admin's browser is at: the panel's domain, its port when
// not 443; the panel's IP address without a domain. "" while the panel has no address.
func (h *handlers) panelOrigin(ctx context.Context) (origin string, domain string, err error) {
	if domain, err = h.d.Settings.String(ctx, settings.KeyDomain); err != nil {
		return "", "", err
	}
	ep, err := h.d.Settings.Endpoint(ctx)
	if err != nil {
		return "", "", err
	}
	host := ep.Host
	if domain != "" {
		host = domain
	}
	if host == "" {
		return "", domain, nil
	}
	origin = "https://" + host
	if strings.Contains(host, ":") {
		origin = "https://[" + host + "]"
	}
	if ep.Port > 0 && ep.Port != 443 {
		origin += ":" + strconv.Itoa(ep.Port)
	}
	return origin, domain, nil
}

// adminBase is the address of the admin panel, without the trailing slash; "" while the
// panel has no address.
func (h *handlers) adminBase(ctx context.Context) (string, error) {
	origin, _, err := h.panelOrigin(ctx)
	if err != nil || origin == "" {
		return "", err
	}
	paths, err := h.d.Settings.Paths(ctx)
	if err != nil {
		return "", err
	}
	return origin + "/" + paths.Admin, nil
}

func (h *handlers) githubApp(ctx context.Context) (id, secret string, err error) {
	if id, err = h.d.Settings.String(ctx, KeyGitHubClientID); err != nil {
		return "", "", err
	}
	secret, err = h.d.Settings.String(ctx, KeyGitHubSecret)
	return id, secret, err
}

// telegramBot is the panel's bot token and its username; "" while no bot is connected.
func (h *handlers) telegramBot(ctx context.Context) (token, username string, err error) {
	if token, err = h.d.Settings.String(ctx, tgate.KeyToken); err != nil || token == "" {
		return "", "", err
	}
	if bot, ok, _ := settings.Get[tgapi.User](ctx, h.d.Settings, tgate.KeyBot); ok {
		username = bot.Username
	}
	return token, username, nil
}

func (h *handlers) listIdentities(ctx context.Context, _ *struct{}) (*identitiesOutput, error) {
	rows, err := h.d.Store.Q.ListAdminIdentities(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	out := &identitiesOutput{Body: []IdentityView{}}
	for _, r := range rows {
		out.Body = append(out.Body, viewIdentity(r))
	}
	return out, nil
}

func (h *handlers) deleteIdentity(ctx context.Context, in *identityIDInput) (*struct{}, error) {
	adminID := sessionOf(ctx).AdminID
	n, err := h.d.Store.Q.DeleteAdminIdentity(ctx, db.DeleteAdminIdentityParams{ID: in.ID, AdminID: adminID})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, huma.Error404NotFound("not_found")
	}
	h.audit(ctx, adminID, "auth.identity_deleted", "identity", strconv.FormatInt(in.ID, 10), nil)
	return nil, nil
}

func (h *handlers) identityProviders(ctx context.Context, _ *struct{}) (*identityProvidersOutput, error) {
	id, secret, err := h.githubApp(ctx)
	if err != nil {
		return nil, err
	}
	out := &identityProvidersOutput{}
	out.Body.GitHub = GitHubProvider{ClientID: id, SecretSet: secret != ""}
	// The callback carries the admin path, which is the panel's secret.
	if !hidesSecrets(ctx) {
		base, err := h.adminBase(ctx)
		if err != nil {
			return nil, err
		}
		if base != "" {
			out.Body.GitHub.CallbackURL = base + "/api/v1/auth/github/callback"
		}
	}
	_, bot, err := h.telegramBot(ctx)
	if err != nil {
		return nil, err
	}
	_, domain, err := h.panelOrigin(ctx)
	if err != nil {
		return nil, err
	}
	out.Body.Telegram = TelegramProvider{Bot: bot, Domain: domain}
	return out, nil
}

func (h *handlers) setGitHubApp(ctx context.Context, in *githubAppInput) (*identityProvidersOutput, error) {
	id := strings.TrimSpace(in.Body.ClientID)
	if strings.ContainsAny(id, " \t\r\n/?#&") {
		return nil, fieldErr("body.client_id", "github_client_id")
	}
	if err := settings.Set(ctx, h.d.Settings, KeyGitHubClientID, id); err != nil {
		return nil, err
	}
	if in.Body.Secret != nil {
		if err := settings.Set(ctx, h.d.Settings, KeyGitHubSecret, strings.TrimSpace(*in.Body.Secret)); err != nil {
			return nil, err
		}
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "auth.github_app", "settings", KeyGitHubClientID, map[string]any{"client_id": id, "secret_changed": in.Body.Secret != nil})
	return h.identityProviders(ctx, nil)
}

// githubAuthorize is the address that sends the browser to GitHub with state.
func (h *handlers) githubAuthorize(ctx context.Context, state string) (string, error) {
	id, secret, err := h.githubApp(ctx)
	if err != nil {
		return "", err
	}
	if id == "" || secret == "" {
		return "", huma.Error409Conflict("github_off")
	}
	base, err := h.adminBase(ctx)
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", huma.Error409Conflict("identity_no_address")
	}
	q := url.Values{"client_id": {id}, "redirect_uri": {base + "/api/v1/auth/github/callback"}, "state": {state}, "allow_signup": {"false"}}
	return githubAuthorizeURL + "?" + q.Encode(), nil
}

func (h *handlers) githubLinkBegin(ctx context.Context, in *identityBeginInput) (*identityURLOutput, error) {
	admin, err := h.d.Store.Q.GetAdmin(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	state := secure.Token(32)
	u, err := h.githubAuthorize(ctx, state)
	if err != nil {
		return nil, err
	}
	if err := h.reauth(ctx, admin, in.Body.Password, strings.TrimSpace(in.Body.TOTP)); err != nil {
		return nil, err
	}
	h.identityMu.Lock()
	h.sweepIdentities(h.d.Now())
	h.githubStates[state] = githubState{link: admin.ID, expires: h.d.Now().Add(identityTTL)}
	h.identityMu.Unlock()
	out := &identityURLOutput{}
	out.Body.URL = u
	return out, nil
}

// sweepIdentities forgets the round trips and tickets that expired; under identityMu.
func (h *handlers) sweepIdentities(now time.Time) {
	for k, s := range h.githubStates {
		if now.After(s.expires) {
			delete(h.githubStates, k)
		}
	}
	for k, t := range h.identityTickets {
		if now.After(t.expires) {
			delete(h.identityTickets, k)
		}
	}
	for k, at := range h.telegramLinks {
		if now.After(at) {
			delete(h.telegramLinks, k)
		}
	}
	for k, at := range h.telegramUsed {
		if now.After(at) {
			delete(h.telegramUsed, k)
		}
	}
}

func (h *handlers) githubLoginBegin(ctx context.Context, _ *struct{}) (*identityURLOutput, error) {
	ip := auth.ClientKey(clientOf(ctx).IP)
	state := secure.Token(32)
	u, err := h.githubAuthorize(ctx, state)
	if err != nil {
		return nil, err
	}
	now := h.d.Now()
	h.identityMu.Lock()
	h.sweepIdentities(now)
	mine := 0
	for _, s := range h.githubStates {
		if s.link == 0 && s.ip == ip {
			mine++
		}
	}
	full := mine >= identityStatesPerIP || len(h.githubStates) >= identityStates
	if !full {
		h.githubStates[state] = githubState{ip: ip, expires: now.Add(identityTTL)}
	}
	h.identityMu.Unlock()
	if full {
		return nil, tooManyAttempts(time.Minute)
	}
	out := &identityURLOutput{}
	out.Body.URL = u
	return out, nil
}

// githubUser trades GitHub's code for the account it is of: its id and login.
func (h *handlers) githubUser(ctx context.Context, code string) (id int64, login string, err error) {
	clientID, secret, err := h.githubApp(ctx)
	if err != nil {
		return 0, "", err
	}
	base, err := h.adminBase(ctx)
	if err != nil {
		return 0, "", err
	}
	form := url.Values{"client_id": {clientID}, "client_secret": {secret}, "code": {code}, "redirect_uri": {base + "/api/v1/auth/github/callback"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := identityJSON(req, &tok); err != nil {
		return 0, "", err
	}
	if tok.AccessToken == "" {
		return 0, "", errors.New("github: no token: " + tok.Error)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, githubUserURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := identityJSON(req, &user); err != nil {
		return 0, "", err
	}
	if user.ID <= 0 {
		return 0, "", errors.New("github: no account id")
	}
	return user.ID, user.Login, nil
}

func identityJSON(req *http.Request, v any) error {
	resp, err := identityHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("github: " + resp.Status)
	}
	return json.Unmarshal(body, v)
}

// githubCallback is where GitHub sends the browser back. It always answers with a redirect
// to the admin's pages: what came of it is in the address's fragment, which no server and
// no Referer sees.
func (h *handlers) githubCallback(ctx context.Context, in *githubCallbackInput) (*redirectOutput, error) {
	base, err := h.adminBase(ctx)
	if err != nil {
		return nil, err
	}
	to := func(page, fragment string) (*redirectOutput, error) {
		return &redirectOutput{Status: http.StatusFound, Location: base + page + "#" + fragment, Cache: "no-store"}, nil
	}
	now := h.d.Now()
	h.identityMu.Lock()
	st, ok := h.githubStates[in.State]
	delete(h.githubStates, in.State)
	h.identityMu.Unlock()
	if !ok || now.After(st.expires) {
		return to("/login", "github_error=identity_expired")
	}
	page := "/login"
	if st.link != 0 {
		page = "/settings?tab=security"
	}
	if in.Error != "" || in.Code == "" {
		// The admin turned GitHub down.
		return to(page, "github_error=github_denied")
	}
	id, login, err := h.githubUser(ctx, in.Code)
	if err != nil {
		h.d.Log.Info("github sign-in failed", "err", err)
		return to(page, "github_error=github_failed")
	}
	subject := strconv.FormatInt(id, 10)
	if st.link != 0 {
		row, err := h.d.Store.Q.CreateAdminIdentity(ctx, db.CreateAdminIdentityParams{AdminID: st.link, Provider: providerGitHub, Subject: subject, Name: login, CreatedAt: now.Unix()})
		if store.IsUnique(err) {
			return to(page, "github_error=identity_taken")
		}
		if err != nil {
			return nil, err
		}
		h.audit(ctx, st.link, "auth.identity_added", "identity", strconv.FormatInt(row.ID, 10), map[string]any{"provider": providerGitHub, "name": login})
		return to(page, "identity=github")
	}
	row, err := h.d.Store.Q.GetAdminIdentity(ctx, db.GetAdminIdentityParams{Provider: providerGitHub, Subject: subject})
	if errors.Is(err, sql.ErrNoRows) {
		h.audit(ctx, 0, "auth.login_failed", "admin", "github", map[string]any{"reason": "identity_unknown"})
		return to(page, "github_error=identity_unknown")
	}
	if err != nil {
		return nil, err
	}
	ticket := secure.Token(32)
	h.identityMu.Lock()
	h.identityTickets[ticket] = identityTicket{identity: row, name: login, ip: st.ip, expires: now.Add(identityTTL)}
	h.identityMu.Unlock()
	return to(page, "github="+ticket)
}

func (h *handlers) githubLogin(ctx context.Context, in *githubLoginInput) (*loginOutput, error) {
	c := clientOf(ctx)
	now := h.d.Now()
	ipKey := "ip:" + auth.ClientKey(c.IP)
	okIP, waitIP, blocked := h.d.IPLimit.Reserve(ipKey, now)
	if !okIP {
		return nil, tooManyAttempts(waitIP)
	}
	h.identityMu.Lock()
	t, ok := h.identityTickets[in.Body.Ticket]
	h.identityMu.Unlock()
	if !ok || now.After(t.expires) || t.ip != auth.ClientKey(c.IP) {
		return nil, huma.Error401Unauthorized("identity_expired")
	}
	out, err := h.identityLogin(ctx, t.identity, t.name, in.Body.TOTP, ipKey, blocked)
	if err == nil {
		h.identityMu.Lock()
		delete(h.identityTickets, in.Body.Ticket)
		h.identityMu.Unlock()
	}
	return out, err
}

// identityLogin signs in the admin of the account r is (its name there now name): with the
// code when 2FA is on, which is asked (totp_required) before the account is spent.
func (h *handlers) identityLogin(ctx context.Context, r db.AdminIdentity, name, code, ipKey string, blocked bool) (*loginOutput, error) {
	c := clientOf(ctx)
	now := h.d.Now()
	admin, err := h.d.Store.Q.GetAdmin(ctx, r.AdminID)
	if err != nil {
		return nil, err
	}
	if admin.TotpSecret.Valid {
		code = strings.TrimSpace(code)
		if code == "" {
			return nil, huma.Error401Unauthorized("totp_required")
		}
		// The code counts against the admin's own allowance, as with the password.
		userKey := "user:" + admin.Username
		if ok, wait, _ := h.d.UserLimit.Reserve(userKey, now); !ok {
			return nil, tooManyAttempts(wait)
		}
		if !h.d.TOTP.Validate(admin.ID, admin.TotpSecret.String, code, now) {
			used, err := h.spendRecoveryCode(ctx, admin, code)
			if err != nil {
				return nil, err
			}
			if !used {
				h.loginFailed(ctx, admin.Username, true, "bad_totp", blocked)
				return nil, huma.Error401Unauthorized("invalid_totp")
			}
			h.audit(ctx, admin.ID, "auth.recovery_code_used", "admin", admin.Username, nil)
		}
		h.d.UserLimit.Reset(userKey)
	}
	if name == "" {
		name = r.Name
	}
	if err := h.d.Store.Q.UseAdminIdentity(ctx, db.UseAdminIdentityParams{LastUsedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, Name: name, ID: r.ID}); err != nil {
		return nil, err
	}
	h.d.IPLimit.Reset(ipKey)
	token, sess, err := h.d.Sessions.Create(ctx, admin.ID, c.IP, c.UserAgent)
	if err != nil {
		return nil, err
	}
	if err := h.d.Store.Q.SetAdminLastLogin(ctx, db.SetAdminLastLoginParams{LastLoginAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: admin.ID}); err != nil {
		return nil, err
	}
	h.audit(ctx, admin.ID, "auth.login", "admin", admin.Username, map[string]any{r.Provider: name})
	return &loginOutput{SetCookie: sessionCookie(token, auth.MaxTTL), Body: MeBody{Admin: viewAdmin(admin), CSRFToken: sess.CsrfToken}}, nil
}

// telegramAuth is the address of Telegram's login page that sends the browser back to page
// of the admin panel.
func (h *handlers) telegramAuth(ctx context.Context, page string) (string, error) {
	token, _, err := h.telegramBot(ctx)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", huma.Error409Conflict("telegram_off")
	}
	botID, _, _ := strings.Cut(token, ":")
	origin, domain, err := h.panelOrigin(ctx)
	if err != nil {
		return "", err
	}
	// Telegram signs in only on the domain the bot was given (@BotFather, /setdomain).
	if domain == "" {
		return "", huma.Error409Conflict("telegram_no_domain")
	}
	base, err := h.adminBase(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{"bot_id": {botID}, "origin": {origin}, "return_to": {base + page}}
	return telegramAuthURL + "?" + q.Encode(), nil
}

func (h *handlers) telegramLinkBegin(ctx context.Context, in *identityBeginInput) (*identityURLOutput, error) {
	admin, err := h.d.Store.Q.GetAdmin(ctx, sessionOf(ctx).AdminID)
	if err != nil {
		return nil, err
	}
	u, err := h.telegramAuth(ctx, "/settings?tab=security")
	if err != nil {
		return nil, err
	}
	if err := h.reauth(ctx, admin, in.Body.Password, strings.TrimSpace(in.Body.TOTP)); err != nil {
		return nil, err
	}
	h.identityMu.Lock()
	h.sweepIdentities(h.d.Now())
	h.telegramLinks[admin.ID] = h.d.Now().Add(identityTTL)
	h.identityMu.Unlock()
	out := &identityURLOutput{}
	out.Body.URL = u
	return out, nil
}

// telegramAccount checks a Telegram login's result and takes its signature: each is used
// once.
func (h *handlers) telegramAccount(ctx context.Context, result string) (tgate.LoginUser, error) {
	token, _, err := h.telegramBot(ctx)
	if err != nil {
		return tgate.LoginUser{}, err
	}
	now := h.d.Now()
	u, err := tgate.CheckLogin(token, result, now)
	if err != nil {
		return u, err
	}
	h.identityMu.Lock()
	defer h.identityMu.Unlock()
	if _, used := h.telegramUsed[u.Hash]; used {
		return u, tgate.ErrLogin
	}
	return u, nil
}

// spendTelegram marks the signature of u used.
func (h *handlers) spendTelegram(u tgate.LoginUser) {
	h.identityMu.Lock()
	h.telegramUsed[u.Hash] = h.d.Now().Add(tgate.LoginTTL + time.Minute)
	h.identityMu.Unlock()
}

func telegramName(u tgate.LoginUser) string {
	name := u.Name
	if u.Username != "" {
		name = "@" + u.Username
	}
	if r := []rune(name); len(r) > 128 {
		name = string(r[:128])
	}
	return name
}

func (h *handlers) linkTelegram(ctx context.Context, in *telegramLinkInput) (*identityOutput, error) {
	adminID := sessionOf(ctx).AdminID
	now := h.d.Now()
	// The link was begun with the password, a moment ago; it is used once.
	h.identityMu.Lock()
	until, ok := h.telegramLinks[adminID]
	delete(h.telegramLinks, adminID)
	h.identityMu.Unlock()
	if !ok || now.After(until) {
		return nil, huma.Error409Conflict("identity_expired")
	}
	u, err := h.telegramAccount(ctx, in.Body.Result)
	if errors.Is(err, tgate.ErrLogin) {
		return nil, fieldErr("body.result", "telegram_login")
	}
	if err != nil {
		return nil, err
	}
	name := telegramName(u)
	row, err := h.d.Store.Q.CreateAdminIdentity(ctx, db.CreateAdminIdentityParams{AdminID: adminID, Provider: providerTelegram, Subject: strconv.FormatInt(u.ID, 10), Name: name, CreatedAt: now.Unix()})
	if store.IsUnique(err) {
		return nil, huma.Error409Conflict("identity_taken")
	}
	if err != nil {
		return nil, err
	}
	h.spendTelegram(u)
	h.audit(ctx, adminID, "auth.identity_added", "identity", strconv.FormatInt(row.ID, 10), map[string]any{"provider": providerTelegram, "name": name})
	return &identityOutput{Body: viewIdentity(row)}, nil
}

func (h *handlers) loginMethods(ctx context.Context, _ *struct{}) (*loginMethodsOutput, error) {
	out := &loginMethodsOutput{}
	// A way in is offered once it is set up and someone has linked an account to it.
	id, secret, err := h.githubApp(ctx)
	if err != nil {
		return nil, err
	}
	if id != "" && secret != "" {
		n, err := h.d.Store.Q.CountAdminIdentities(ctx, providerGitHub)
		if err != nil {
			return nil, err
		}
		out.Body.GitHub = n > 0
	}
	token, _, err := h.telegramBot(ctx)
	if err != nil {
		return nil, err
	}
	if _, domain, err := h.panelOrigin(ctx); err != nil {
		return nil, err
	} else if token != "" && domain != "" {
		n, err := h.d.Store.Q.CountAdminIdentities(ctx, providerTelegram)
		if err != nil {
			return nil, err
		}
		out.Body.Telegram = n > 0
	}
	return out, nil
}

func (h *handlers) telegramLoginBegin(ctx context.Context, _ *struct{}) (*identityURLOutput, error) {
	u, err := h.telegramAuth(ctx, "/login")
	if err != nil {
		return nil, err
	}
	out := &identityURLOutput{}
	out.Body.URL = u
	return out, nil
}

func (h *handlers) telegramLogin(ctx context.Context, in *telegramLoginInput) (*loginOutput, error) {
	c := clientOf(ctx)
	now := h.d.Now()
	ipKey := "ip:" + auth.ClientKey(c.IP)
	okIP, waitIP, blocked := h.d.IPLimit.Reserve(ipKey, now)
	if !okIP {
		return nil, tooManyAttempts(waitIP)
	}
	u, err := h.telegramAccount(ctx, in.Body.Result)
	if errors.Is(err, tgate.ErrLogin) {
		h.audit(ctx, 0, "auth.login_failed", "admin", "telegram", map[string]any{"reason": "telegram_login", "blocked": blocked})
		return nil, huma.Error401Unauthorized("identity_expired")
	}
	if err != nil {
		return nil, err
	}
	row, err := h.d.Store.Q.GetAdminIdentity(ctx, db.GetAdminIdentityParams{Provider: providerTelegram, Subject: strconv.FormatInt(u.ID, 10)})
	if errors.Is(err, sql.ErrNoRows) {
		h.audit(ctx, 0, "auth.login_failed", "admin", "telegram", map[string]any{"reason": "identity_unknown", "blocked": blocked})
		return nil, huma.Error401Unauthorized("identity_unknown")
	}
	if err != nil {
		return nil, err
	}
	out, err := h.identityLogin(ctx, row, telegramName(u), in.Body.TOTP, ipKey, blocked)
	if err == nil {
		h.spendTelegram(u)
	}
	return out, err
}
