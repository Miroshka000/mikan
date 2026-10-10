package api

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/hostname"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
	"mikan/internal/proto"
)

type SettingsView struct {
	Brand        string      `json:"brand"`
	SupportURL   string      `json:"support_url"`
	SubTitle     string      `json:"sub_title" doc:"Название подписки в приложениях (заголовок profile-title); пусто — бренд. Переменные: {brand} — бренд, {name} — имя пользователя, {date} — дата окончания (ДД.ММ.ГГГГ, МСК), {days} — дней осталось, {used} — израсходовано, {left} — осталось трафика, {total} — всего; без срока или лимита — ∞"`
	Announce     string      `json:"sub_announce" doc:"Объявление над профилем в приложениях (заголовок announce): Happ и v2RayTun показывают его под названием подписки; пусто — нет. Те же переменные, что в sub_title"`
	AnnounceURL  string      `json:"sub_announce_url" doc:"Куда ведёт нажатие на объявление"`
	AppBranding  bool        `json:"app_branding" doc:"Брендинг в приложениях, читающих операторские заголовки (ClashFest, SlothClash): название, логотип, цвет, ссылки"`
	BrandAccent  string      `json:"brand_accent" doc:"Цвет бренда #RRGGBB; пусто — цвет приложения"`
	BrandLogoURL string      `json:"brand_logo_url" doc:"Логотип: https, PNG, WebP или JPEG до 512 КБ; пусто — значок приложения"`
	PublicHost   string      `json:"public_host"`
	Domain       string      `json:"domain"`
	PanelPort    int         `json:"panel_port"`
	SubPort      int         `json:"sub_port" doc:"Отдельный порт подписок; 0 — порт панели. Порт панели отдаёт подписки в любом случае"`
	SubPortError string      `json:"sub_port_error,omitempty" doc:"sub_port_busy — сохранённый порт занят на сервере, подписки пока идут через порт панели"`
	QuietHourUTC int         `json:"quiet_hour_utc" doc:"Час (UTC), когда пополняется пул слотов: переподключение QUIC-клиентов"`
	AdminURL     string      `json:"admin_url"`
	SubBaseURL   string      `json:"sub_base_url"`
	SubGroupMain string      `json:"sub_group_main" doc:"Главная группа в Clash-приложениях"`
	SubGroupAuto string      `json:"sub_group_auto" doc:"Группа автовыбора самого быстрого подключения"`
	SubRules     string      `json:"sub_rules" doc:"Свои правила Clash: по строке TYPE,VALUE,TARGET[,no-resolve]; # — комментарий"`
	RuleTargets  []string    `json:"rule_targets" doc:"Куда правило может направить трафик: DIRECT, REJECT, REJECT-DROP, PROXY и группы"`
	SubRouting   string      `json:"sub_routing" enum:"ru_direct,all,blocked" doc:"Маршруты в Clash-приложениях: ru_direct — российские сайты и IP напрямую по геобазам mihomo, all — всё через VPN, blocked — через VPN только заблокированное (списки privWL-clash), остальное напрямую"`
	SubTemplate  string      `json:"sub_template" doc:"Свой профиль Clash (YAML) вместо встроенного для приложений на mihomo; пусто — встроенный. Серверы панель подставляет сама: в proxies и в группы с include-all-proxies или mikan: {nodes, types}"`
	SubRoutes    subs.Routes `json:"sub_routes" doc:"Куда идут сервисы (services: id → vpn, direct, block или node:<id>), какие приложения и сайты идут мимо VPN (direct) и свои DNS (dns). Каталог — GET /api/v1/settings/routes/catalog"`
	Fingerprint  string      `json:"client_fingerprint" doc:"Отпечаток TLS (uTLS) у клиентов, если у подключения не задан свой: chrome, firefox, safari, ios, android, edge, 360, qq, random, randomized или своё значение"`
	AutoPort     bool        `json:"auto_port" doc:"Переносить подключение на другой порт, если клиенты перестали до него доходить"`
	AutoSNI      bool        `json:"auto_sni" doc:"Менять сайт маскировки REALITY, если он перестал подходить"`
	// Devices: see domain.Devices.
	DeviceBinding bool        `json:"device_binding" doc:"Привязывать подписку к устройствам: у каждого устройства свои ключи"`
	RequireHWID   bool        `json:"device_require_hwid" doc:"Не выдавать подписку приложениям без ID устройства (иначе они вместе занимают одно место)"`
	DefaultLang   string      `json:"default_lang" enum:"auto,ru,en" doc:"Язык админки и страницы подписки, пока человек не выбрал свой; auto — по языку браузера. На нём же названия по умолчанию: группа автовыбора и меню ненастроенного бота"`
	Certificate   acme.Status `json:"certificate"`
	// The certificate authority of the panel and its nodes.
	ACMECA      string `json:"acme_ca" enum:"letsencrypt,zerossl,google" doc:"Центр сертификации панели и нод: letsencrypt, zerossl (нужен e-mail) или google (нужен ключ EAB). IP-адреса всегда получают сертификат Let's Encrypt"`
	ACMEEmail   string `json:"acme_email" doc:"E-mail для центра сертификации; ZeroSSL привязывает к нему аккаунт"`
	ACMEEABKID  string `json:"acme_eab_kid" doc:"Google Trust Services: keyId ключа EAB"`
	ACMEEABHMAC bool   `json:"acme_eab_hmac_set" doc:"Google Trust Services: ключ HMAC сохранён (сам ключ не показывается)"`

	// Happ: see subs.Happ.
	HappRouting      string `json:"happ_routing" doc:"Профиль маршрутизации Happ: ссылка happ://routing/onadd/… (добавить и включить), happ://routing/add/… или happ://routing/off; auto — панель собирает его сама из маршрутизации Clash-профиля (Настройки → Маршрутизация); уходит только в Happ заголовком routing"`
	HappProviderID   string `json:"happ_provider_id" doc:"Provider ID с happ-proxy.com; без него Happ не принимает hide-settings"`
	HappHideSettings bool   `json:"happ_hide_settings" doc:"Скрыть в Happ настройки серверов подписки (нужен Provider ID)"`
	HappCrypt        string `json:"happ_crypt" enum:"off,api,local" doc:"Шифрованная ссылка для кнопки Happ: off — обычная happ://add/, api — через сервис Happ (адрес подписки уходит на crypto.happ.su), local — панель шифрует сама"`
}

type settingsOutput struct{ Body SettingsView }

type patchSettingsInput struct {
	Body struct {
		Brand         *string      `json:"brand,omitempty" maxLength:"40"`
		SupportURL    *string      `json:"support_url,omitempty" maxLength:"200" doc:"https://… или tg://…"`
		SubTitle      *string      `json:"sub_title,omitempty" maxLength:"200" doc:"Переменные — см. SettingsView.sub_title"`
		Announce      *string      `json:"sub_announce,omitempty" maxLength:"200"`
		AnnounceURL   *string      `json:"sub_announce_url,omitempty" maxLength:"200" doc:"https://… или tg://…"`
		AppBranding   *bool        `json:"app_branding,omitempty"`
		BrandAccent   *string      `json:"brand_accent,omitempty" maxLength:"7" doc:"#RRGGBB или пусто"`
		BrandLogoURL  *string      `json:"brand_logo_url,omitempty" maxLength:"500" doc:"https://… или пусто"`
		HappRouting   *string      `json:"happ_routing,omitempty" maxLength:"65536" doc:"happ://routing/…, auto — собрать из маршрутизации; пусто — не отдавать"`
		HappProvider  *string      `json:"happ_provider_id,omitempty" maxLength:"64"`
		HappHide      *bool        `json:"happ_hide_settings,omitempty"`
		HappCrypt     *string      `json:"happ_crypt,omitempty" enum:"off,api,local"`
		PublicHost    *string      `json:"public_host,omitempty" maxLength:"253"`
		Domain        *string      `json:"domain,omitempty" maxLength:"253"`
		QuietHourUTC  *int         `json:"quiet_hour_utc,omitempty" minimum:"0" maximum:"23"`
		SubGroupMain  *string      `json:"sub_group_main,omitempty" maxLength:"200"`
		SubGroupAuto  *string      `json:"sub_group_auto,omitempty" maxLength:"200"`
		SubRouting    *string      `json:"sub_routing,omitempty" enum:"ru_direct,all,blocked"`
		SubRoutes     *subs.Routes `json:"sub_routes,omitempty"`
		SubTemplate   *string      `json:"sub_template,omitempty" maxLength:"524288" doc:"Свой профиль Clash; пусто — вернуть встроенный"`
		SubRules      *string      `json:"sub_rules,omitempty" maxLength:"65536" doc:"Свои правила Clash, до 500 строк; ошибка указывает номер строки"`
		Fingerprint   *string      `json:"client_fingerprint,omitempty" pattern:"^[a-z0-9_]{1,32}$" doc:"Из списка или своё: латиница в нижнем регистре, цифры и _, до 32 символов"`
		AutoPort      *bool        `json:"auto_port,omitempty"`
		AutoSNI       *bool        `json:"auto_sni,omitempty"`
		DeviceBinding *bool        `json:"device_binding,omitempty"`
		RequireHWID   *bool        `json:"device_require_hwid,omitempty"`
		DefaultLang   *string      `json:"default_lang,omitempty" enum:"auto,ru,en"`
		SubPort       *int         `json:"sub_port,omitempty" minimum:"0" maximum:"65535" doc:"Отдельный порт подписок на сервере панели; 0 — убрать. Ссылки переезжают на него, старые продолжают работать"`
		ACMECA        *string      `json:"acme_ca,omitempty" enum:"letsencrypt,zerossl,google" doc:"Смена центра выпускает сертификаты панели и нод заново"`
		ACMEEmail     *string      `json:"acme_email,omitempty" maxLength:"254"`
		ACMEEABKID    *string      `json:"acme_eab_kid,omitempty" maxLength:"256"`
		ACMEEABHMAC   *string      `json:"acme_eab_hmac,omitempty" maxLength:"512" doc:"Ключ HMAC (base64url); пусто — убрать. Обратно не показывается"`
	}
}

type resetPathOutput struct {
	Body struct {
		AdminURL string `json:"admin_url"`
	}
}

func (h *handlers) registerSettings() {
	huma.Register(h.api, huma.Operation{OperationID: "get-settings", Method: http.MethodGet, Path: "/api/v1/settings", Summary: "Настройки", Tags: []string{"settings"}}, h.getSettings)
	huma.Register(h.api, huma.Operation{OperationID: "update-settings", Method: http.MethodPatch, Path: "/api/v1/settings", Summary: "Изменить настройки", Tags: []string{"settings"}}, h.updateSettings)
	huma.Register(h.api, huma.Operation{OperationID: "reset-admin-path", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/settings/reset-admin-path", Summary: "Выдать новую секретную ссылку на панель", Tags: []string{"settings"}}, h.resetAdminPath)
	huma.Register(h.api, huma.Operation{OperationID: "renew-certificate", Method: http.MethodPost, Path: "/api/v1/settings/certificate/renew",
		Summary: "Получить сертификат панели сейчас: ждёт до 90 с и отвечает тем, что вышло (ordering — заказ ещё идёт)", Tags: []string{"settings"}}, h.renewCertificate)
}

type certificateOutput struct{ Body acme.Status }

func (h *handlers) renewCertificate(ctx context.Context, _ *struct{}) (*certificateOutput, error) {
	if h.d.RenewCertNow == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.renew_certificate", "", "", nil)
	st, _ := h.d.RenewCertNow(ctx, renewWait)
	return &certificateOutput{Body: st}, nil
}

func (h *handlers) readSettings(ctx context.Context) (SettingsView, error) {
	var v SettingsView
	var err error
	get := func(key string, dst *string) {
		if err == nil {
			*dst, _, err = settings.Get[string](ctx, h.d.Settings, key)
		}
	}
	get(settings.KeyBrand, &v.Brand)
	get(settings.KeySupportURL, &v.SupportURL)
	get(settings.KeySubTitle, &v.SubTitle)
	get(settings.KeyAnnounce, &v.Announce)
	get(settings.KeyAnnounceURL, &v.AnnounceURL)
	get(settings.KeyBrandAccent, &v.BrandAccent)
	get(settings.KeyBrandLogo, &v.BrandLogoURL)
	get(settings.KeyHappRouting, &v.HappRouting)
	get(settings.KeyHappProvider, &v.HappProviderID)
	get(settings.KeyHappCrypt, &v.HappCrypt)
	get(settings.KeyPublicHost, &v.PublicHost)
	get(settings.KeyDomain, &v.Domain)
	get(settings.KeyGroupMain, &v.SubGroupMain)
	get(settings.KeyGroupAuto, &v.SubGroupAuto)
	get(settings.KeyRouting, &v.SubRouting)
	get(settings.KeyRules, &v.SubRules)
	v.SubRouting = string(subs.ParseRouting(v.SubRouting))
	if err == nil {
		v.SubRoutes, _, err = settings.Get[subs.Routes](ctx, h.d.Settings, settings.KeyRoutes)
	}
	// An own profile may hold the admin's own proxies, a controller secret or DNS tokens.
	if !hidesSecrets(ctx) {
		get(settings.KeyTemplate, &v.SubTemplate)
	}
	get(settings.KeyFingerprint, &v.Fingerprint)
	if !proto.ValidFingerprint(v.Fingerprint) {
		v.Fingerprint = proto.DefaultFingerprint
	}
	if err != nil {
		return v, err
	}
	if v.DefaultLang, err = h.d.Settings.Lang(ctx); err != nil {
		return v, err
	}
	g := subs.Groups{Main: v.SubGroupMain, Auto: v.SubGroupAuto}.WithDefaults(v.DefaultLang)
	v.SubGroupMain, v.SubGroupAuto = g.Main, g.Auto
	v.RuleTargets = subs.RuleTargets(g)
	if v.DefaultLang == "" {
		v.DefaultLang = "auto"
	}
	if v.PanelPort, _, err = settings.Get[int](ctx, h.d.Settings, settings.KeyPanelPort); err != nil {
		return v, err
	}
	if v.SubPort, _, err = settings.Get[int](ctx, h.d.Settings, settings.KeySubPort); err != nil {
		return v, err
	}
	if v.SubPort > 0 && h.d.SubPortError != nil {
		v.SubPortError = h.d.SubPortError()
	}
	if v.QuietHourUTC, _, err = settings.Get[int](ctx, h.d.Settings, settings.KeyQuietHour); err != nil {
		return v, err
	}
	if v.AutoPort, err = h.d.Settings.On(ctx, settings.AutoPort); err != nil {
		return v, err
	}
	if v.AutoSNI, err = h.d.Settings.On(ctx, settings.AutoSNI); err != nil {
		return v, err
	}
	if v.DeviceBinding, err = h.d.Settings.On(ctx, settings.DeviceBinding); err != nil {
		return v, err
	}
	if v.RequireHWID, err = h.d.Settings.On(ctx, settings.RequireHWID); err != nil {
		return v, err
	}
	if v.AppBranding, err = h.d.Settings.On(ctx, settings.AppBranding); err != nil {
		return v, err
	}
	if v.HappHideSettings, err = h.d.Settings.On(ctx, settings.HappHide); err != nil {
		return v, err
	}
	v.HappCrypt = cmp.Or(v.HappCrypt, "off")
	if v.Brand == "" {
		v.Brand = "VPN"
	}
	paths, err := h.d.Settings.Paths(ctx)
	if err != nil {
		return v, err
	}
	host := v.Domain
	if host == "" {
		host = v.PublicHost
	}
	// The addresses carry the secret path segments: not for a key that may only read.
	if host != "" && !hidesSecrets(ctx) {
		v.AdminURL = "https://" + net.JoinHostPort(host, strconv.Itoa(v.PanelPort)) + "/" + paths.Admin + "/"
		subPort := v.PanelPort
		if v.SubPort > 0 {
			subPort = v.SubPort
		}
		v.SubBaseURL = "https://" + net.JoinHostPort(host, strconv.Itoa(subPort)) + "/" + paths.Sub + "/"
	}
	v.Certificate = acme.Status{Kind: "self-signed", WantCA: acme.CALetsEncrypt}
	if h.d.Cert != nil {
		v.Certificate = h.d.Cert()
	}
	if v.ACMECA, err = acme.ChosenCA(ctx, h.d.Settings); err != nil {
		return v, err
	}
	if v.ACMEEmail, err = h.d.Settings.String(ctx, settings.KeyACMEEmail); err != nil {
		return v, err
	}
	if v.ACMEEABKID, err = h.d.Settings.String(ctx, settings.KeyACMEEABKID); err != nil {
		return v, err
	}
	hmac, err := h.d.Settings.String(ctx, settings.KeyACMEEABHMAC)
	if err != nil {
		return v, err
	}
	v.ACMEEABHMAC = hmac != ""
	return v, nil
}

func (h *handlers) getSettings(ctx context.Context, _ *struct{}) (*settingsOutput, error) {
	v, err := h.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &settingsOutput{Body: v}, nil
}

func (h *handlers) updateSettings(ctx context.Context, in *patchSettingsInput) (*settingsOutput, error) {
	b := in.Body
	// Where clients are sent, and what they are told to trust: a leaked API key must not
	// move subscriptions to another server or add rules to every client.
	for field, touched := range map[string]bool{"public_host": b.PublicHost != nil, "domain": b.Domain != nil, "sub_port": b.SubPort != nil,
		"sub_rules": b.SubRules != nil, "sub_routes": b.SubRoutes != nil, "sub_template": b.SubTemplate != nil, "sub_routing": b.SubRouting != nil, "support_url": b.SupportURL != nil,
		// What every subscriber's app shows: text, links and the logo it downloads.
		"sub_title": b.SubTitle != nil, "sub_announce": b.Announce != nil, "sub_announce_url": b.AnnounceURL != nil, "app_branding": b.AppBranding != nil,
		"brand_accent": b.BrandAccent != nil, "brand_logo_url": b.BrandLogoURL != nil,
		// What Happ is told to apply, to hide, and where the subscription address goes.
		"happ_routing": b.HappRouting != nil, "happ_provider_id": b.HappProvider != nil, "happ_hide_settings": b.HappHide != nil, "happ_crypt": b.HappCrypt != nil,
		// Who the panel's and the nodes' certificates come from, and the account's secret.
		"acme_ca": b.ACMECA != nil, "acme_email": b.ACMEEmail != nil, "acme_eab_kid": b.ACMEEABKID != nil, "acme_eab_hmac": b.ACMEEABHMAC != nil} {
		if touched {
			if err := requireSession(ctx, field); err != nil {
				return nil, err
			}
		}
	}
	var details []error
	if b.PublicHost != nil && !hostname.Valid(*b.PublicHost) {
		details = append(details, &huma.ErrorDetail{Location: "body.public_host", Message: "public_host_invalid"})
	}
	if b.Domain != nil && *b.Domain != "" && !hostname.Valid(*b.Domain) {
		details = append(details, &huma.ErrorDetail{Location: "body.domain", Message: "domain_invalid"})
	}
	if b.SupportURL != nil && *b.SupportURL != "" && !strings.HasPrefix(*b.SupportURL, "https://") && !strings.HasPrefix(*b.SupportURL, "tg://") {
		details = append(details, &huma.ErrorDetail{Location: "body.support_url", Message: "support_url_invalid"})
	}
	if b.AnnounceURL != nil && *b.AnnounceURL != "" && !subs.ValidLink(strings.TrimSpace(*b.AnnounceURL), true) {
		details = append(details, &huma.ErrorDetail{Location: "body.sub_announce_url", Message: "support_url_invalid"})
	}
	// A {word} that is no variable stays as text: an old announcement may hold one.
	for field, v := range map[string]*string{"sub_title": b.SubTitle, "sub_announce": b.Announce} {
		if v == nil {
			continue
		}
		if strings.ContainsAny(*v, "\r\n") {
			details = append(details, &huma.ErrorDetail{Location: "body." + field, Message: "one_line"})
		}
	}
	if b.BrandAccent != nil && *b.BrandAccent != "" && !subs.ValidAccent(strings.TrimSpace(*b.BrandAccent)) {
		details = append(details, &huma.ErrorDetail{Location: "body.brand_accent", Message: "color_invalid"})
	}
	if b.BrandLogoURL != nil && *b.BrandLogoURL != "" && !subs.ValidLink(strings.TrimSpace(*b.BrandLogoURL), false) {
		details = append(details, &huma.ErrorDetail{Location: "body.brand_logo_url", Message: "url_invalid"})
	}
	if b.HappRouting != nil && strings.TrimSpace(*b.HappRouting) != "" && !subs.ValidHappRouting(strings.TrimSpace(*b.HappRouting)) {
		details = append(details, &huma.ErrorDetail{Location: "body.happ_routing", Message: "happ_routing"})
	}
	if b.HappProvider != nil && strings.TrimSpace(*b.HappProvider) != "" && !subs.ValidHappProviderID(strings.TrimSpace(*b.HappProvider)) {
		details = append(details, &huma.ErrorDetail{Location: "body.happ_provider_id", Message: "happ_provider_id"})
	}
	if b.HappHide != nil && *b.HappHide {
		provider, _, err := settings.Get[string](ctx, h.d.Settings, settings.KeyHappProvider)
		if err != nil {
			return nil, err
		}
		if b.HappProvider != nil {
			provider = strings.TrimSpace(*b.HappProvider)
		}
		if provider == "" {
			details = append(details, &huma.ErrorDetail{Location: "body.happ_hide_settings", Message: "happ_needs_provider"})
		}
	}
	if b.SubGroupMain != nil || b.SubGroupAuto != nil || b.SubRules != nil {
		cur, err := h.groups(ctx)
		if err != nil {
			return nil, err
		}
		next := cur
		if b.SubGroupMain != nil {
			next.Main = strings.TrimSpace(*b.SubGroupMain)
		}
		if b.SubGroupAuto != nil {
			next.Auto = strings.TrimSpace(*b.SubGroupAuto)
		}
		if b.SubGroupMain != nil || b.SubGroupAuto != nil {
			details = append(details, h.checkGroups(ctx, next)...)
		}
		// The rules are checked against the groups they will meet: a renamed group must
		// not leave a rule pointing nowhere.
		rules := b.SubRules
		if rules == nil {
			saved, err := h.d.Settings.String(ctx, settings.KeyRules)
			if err != nil {
				return nil, err
			}
			rules = &saved
		}
		if _, err := subs.ParseRules(*rules, next); err != nil {
			var re *subs.RuleError
			if !errors.As(err, &re) {
				return nil, err
			}
			field := "body.sub_rules"
			if b.SubRules == nil {
				field, re.Code = "body.sub_group_main", "group_in_rules"
				if b.SubGroupMain == nil {
					field = "body.sub_group_auto"
				}
			}
			details = append(details, &huma.ErrorDetail{Location: field, Message: re.Code, Value: re.Line})
		}
	}
	if b.SubPort != nil {
		if d, err := h.checkSubPort(ctx, *b.SubPort); err != nil {
			return nil, err
		} else if d != nil {
			details = append(details, d)
		}
	}
	if b.SubRoutes != nil {
		exists := func(id int64) bool {
			_, err := h.d.Store.Q.GetNode(ctx, id)
			return err == nil
		}
		if err := b.SubRoutes.Check(exists); err != nil {
			details = append(details, &huma.ErrorDetail{Location: "body.sub_routes", Message: err.Error()})
		}
	}
	if b.SubTemplate != nil && strings.TrimSpace(*b.SubTemplate) != "" && h.d.CheckTemplate != nil {
		err := h.d.CheckTemplate(ctx, *b.SubTemplate)
		var te *subs.TemplateError
		switch {
		case errors.As(err, &te):
			details = append(details, &huma.ErrorDetail{Location: "body.sub_template", Message: te.Code, Value: te.Detail})
		case err != nil && !errors.Is(err, subs.ErrNoProxies):
			return nil, err
		}
	}
	if b.ACMECA != nil || b.ACMEEmail != nil || b.ACMEEABKID != nil || b.ACMEEABHMAC != nil {
		ds, err := h.checkCA(ctx, b.ACMECA, b.ACMEEmail, b.ACMEEABKID, b.ACMEEABHMAC)
		if err != nil {
			return nil, err
		}
		details = append(details, ds...)
	}
	// The domain must lead to this server: checked when it or the server's address changes,
	// after the cheap checks, since it asks public DNS.
	if (b.Domain != nil || b.PublicHost != nil) && len(details) == 0 {
		cur, err := h.d.Settings.String(ctx, settings.KeyDomain)
		if err != nil {
			return nil, err
		}
		dom := cur
		if b.Domain != nil {
			dom = strings.TrimSpace(*b.Domain)
		}
		if dom != "" && (dom != cur || b.PublicHost != nil) {
			host := ""
			if b.PublicHost != nil {
				host = strings.TrimSpace(*b.PublicHost)
			} else if host, err = h.d.Settings.String(ctx, settings.KeyPublicHost); err != nil {
				return nil, err
			}
			if d := h.domainHere(ctx, "body.domain", dom, dnscheck.Own(host)); d != nil {
				details = append(details, d)
			}
		}
	}
	if len(details) > 0 {
		return nil, huma.Error422UnprocessableEntity("validation", details...)
	}
	// The port opens first: one that cannot be had changes nothing. If the settings then
	// fail to save, the port is put back, so the server does not listen on one nobody saved.
	var oldPort int
	if b.SubPort != nil {
		if h.d.SubPort == nil {
			return nil, huma.Error503ServiceUnavailable("sub_port_unavailable")
		}
		var err error
		if oldPort, _, err = settings.Get[int](ctx, h.d.Settings, settings.KeySubPort); err != nil {
			return nil, err
		}
		if err := h.d.SubPort(*b.SubPort); err != nil {
			return nil, huma.Error422UnprocessableEntity("validation", &huma.ErrorDetail{Location: "body.sub_port", Message: "sub_port_busy", Value: *b.SubPort})
		}
	}
	// Every setting of the request is written in one transaction: a failure in the middle
	// leaves the settings as they were, not half changed. The values are written as given,
	// read from nothing: READ COMMITTED.
	err := h.d.Store.TxRC(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if b.SubPort != nil {
			if err := settings.Set(ctx, set, settings.KeySubPort, *b.SubPort); err != nil {
				return err
			}
		}
		if b.SubRules != nil {
			if err := settings.Set(ctx, set, settings.KeyRules, strings.TrimRight(*b.SubRules, " \n\r\t")); err != nil {
				return err
			}
		}
		if b.SubRoutes != nil {
			if err := settings.Set(ctx, set, settings.KeyRoutes, *b.SubRoutes); err != nil {
				return err
			}
		}
		if b.SubTemplate != nil {
			tpl := *b.SubTemplate
			if strings.TrimSpace(tpl) == "" {
				tpl = ""
			}
			if err := settings.Set(ctx, set, settings.KeyTemplate, tpl); err != nil {
				return err
			}
		}
		for key, v := range map[string]*string{settings.KeyBrand: b.Brand, settings.KeySupportURL: b.SupportURL, settings.KeyPublicHost: b.PublicHost, settings.KeyDomain: b.Domain,
			settings.KeySubTitle: b.SubTitle, settings.KeyAnnounce: b.Announce, settings.KeyAnnounceURL: b.AnnounceURL, settings.KeyBrandAccent: b.BrandAccent, settings.KeyBrandLogo: b.BrandLogoURL,
			settings.KeyGroupMain: b.SubGroupMain, settings.KeyGroupAuto: b.SubGroupAuto, settings.KeyRouting: b.SubRouting, settings.KeyFingerprint: b.Fingerprint, settings.KeyDefaultLang: b.DefaultLang,
			settings.KeyHappRouting: b.HappRouting, settings.KeyHappProvider: b.HappProvider} {
			if v == nil {
				continue
			}
			if err := settings.Set(ctx, set, key, strings.TrimSpace(*v)); err != nil {
				return err
			}
		}
		for key, v := range map[string]*string{settings.KeyACMECA: b.ACMECA, settings.KeyACMEEmail: b.ACMEEmail, settings.KeyACMEEABKID: b.ACMEEABKID, settings.KeyACMEEABHMAC: b.ACMEEABHMAC} {
			if v == nil {
				continue
			}
			if err := settings.Set(ctx, set, key, strings.TrimSpace(*v)); err != nil {
				return err
			}
		}
		if b.QuietHourUTC != nil {
			if err := settings.Set(ctx, set, settings.KeyQuietHour, *b.QuietHourUTC); err != nil {
				return err
			}
		}
		if b.HappCrypt != nil {
			mode := *b.HappCrypt
			if mode == "off" {
				mode = ""
			}
			if err := settings.Set(ctx, set, settings.KeyHappCrypt, mode); err != nil {
				return err
			}
		}
		// Without a provider id Happ ignores hide-settings: the switch goes off with it, so
		// the panel never shows a setting that does nothing.
		if b.HappProvider != nil && strings.TrimSpace(*b.HappProvider) == "" {
			if err := settings.Set(ctx, set, settings.KeyHappHide, false); err != nil {
				return err
			}
		}
		for key, v := range map[string]*bool{settings.KeyAutoPort: b.AutoPort, settings.KeyAutoSNI: b.AutoSNI,
			settings.KeyDeviceBinding: b.DeviceBinding, settings.KeyRequireHWID: b.RequireHWID, settings.KeyAppBranding: b.AppBranding, settings.KeyHappHide: b.HappHide} {
			if v != nil {
				if err := settings.Set(ctx, set, key, *v); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		if b.SubPort != nil {
			if rerr := h.d.SubPort(oldPort); rerr != nil {
				h.d.Log.Warn("sub port not put back", "port", oldPort, "err", rerr)
			}
		}
		return nil, err
	}
	// The bot's Mini App button points at the subscription page.
	if b.SubPort != nil && h.d.Telegram != nil {
		h.d.Telegram.Reload()
	}
	// The certificate is for the domain, or the address without one: a new name gets its
	// certificate now, not at the next six-hourly check.
	caChanged := b.ACMECA != nil || b.ACMEEmail != nil || b.ACMEEABKID != nil || b.ACMEEABHMAC != nil
	if (b.Domain != nil || b.PublicHost != nil || caChanged) && h.d.RenewCert != nil {
		h.d.RenewCert()
	}
	// Another CA reissues the nodes' certificates too; a new e-mail or key may let a failed
	// order through.
	if caChanged && h.d.WakeNodeCerts != nil {
		h.d.WakeNodeCerts()
	}
	auditDetails := map[string]any{}
	if b.SubPort != nil {
		auditDetails["sub_port"] = *b.SubPort
	}
	// The HMAC key never goes to the audit log, only the CA.
	if b.ACMECA != nil {
		auditDetails["acme_ca"] = *b.ACMECA
	}
	if len(auditDetails) == 0 {
		auditDetails = nil
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.update", "", "", auditDetails)
	v, err := h.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &settingsOutput{Body: v}, nil
}

// subPortReserved can never serve subscriptions: SSH, and 80 that Let's Encrypt needs.
var subPortReserved = map[int]bool{22: true, 80: true}

// checkSubPort says what is wrong with port as the subscription port, or nil: it has to
// differ from the panel's port and stay clear of what the panel's own node listens on
// over TCP (an inbound or the cascade relay), which shares the panel's server.
func (h *handlers) checkSubPort(ctx context.Context, port int) (*huma.ErrorDetail, error) {
	if port == 0 {
		return nil, nil
	}
	bad := func(code string, value any) (*huma.ErrorDetail, error) {
		return &huma.ErrorDetail{Location: "body.sub_port", Message: code, Value: value}, nil
	}
	if subPortReserved[port] {
		return bad("sub_port_reserved", port)
	}
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.Address != "" {
			continue
		}
		ports, err := domain.NodePorts(ctx, h.d.Store.Q, n)
		if err != nil {
			return nil, err
		}
		owner, busy := ports.Busy(strconv.Itoa(port), "tcp", domain.PortHolder{Kind: domain.PortSub})
		switch {
		case !busy:
		case owner.Kind == domain.PortPanel:
			return bad("sub_port_panel", port)
		case owner.Kind == domain.PortRelay:
			return bad("sub_port_relay", port)
		default:
			return bad("sub_port_inbound", owner.Name)
		}
	}
	return nil, nil
}

// groups returns the subscription group names with defaults applied.
func (h *handlers) groups(ctx context.Context) (subs.Groups, error) {
	var g subs.Groups
	var err error
	if g.Main, err = h.d.Settings.String(ctx, settings.KeyGroupMain); err != nil {
		return g, err
	}
	if g.Auto, err = h.d.Settings.String(ctx, settings.KeyGroupAuto); err != nil {
		return g, err
	}
	lang, err := h.d.Settings.Lang(ctx)
	if err != nil {
		return g, err
	}
	return g.WithDefaults(lang), nil
}

// checkGroups: a profile with a group named like a proxy, a built-in policy or the
// other group does not load in any Clash app.
func (h *handlers) checkGroups(ctx context.Context, g subs.Groups) []error {
	var out []error
	bad := func(field, code string, value any) {
		out = append(out, &huma.ErrorDetail{Location: "body." + field, Message: code, Value: value})
	}
	if err := subs.ValidName(g.Main); err != nil {
		bad("sub_group_main", err.Error(), nil)
	}
	if err := subs.ValidName(g.Auto); err != nil {
		bad("sub_group_auto", err.Error(), nil)
	}
	if strings.EqualFold(g.Auto, subs.AliasGroup) {
		bad("sub_group_auto", "group_alias_taken", subs.AliasGroup)
	}
	if strings.EqualFold(g.Main, g.Auto) {
		bad("sub_group_auto", "groups_same", nil)
	}
	if inbounds, err := h.d.Store.Q.ListInbounds(ctx); err == nil {
		for _, in := range inbounds {
			name := domain.ProxyName(in)
			if strings.EqualFold(name, g.Main) {
				bad("sub_group_main", "group_is_proxy", in.Name)
			}
			if strings.EqualFold(name, g.Auto) {
				bad("sub_group_auto", "group_is_proxy", in.Name)
			}
		}
	}
	return out
}

// checkSubName validates an inbound's name in the subscription and returns an error
// code, "" when the name is fine.
func (h *handlers) checkSubName(ctx context.Context, name string) string {
	if err := subs.ValidName(name); err != nil {
		return err.Error()
	}
	g, err := h.groups(ctx)
	if err != nil {
		return ""
	}
	for _, x := range []string{g.Main, g.Auto, subs.AliasGroup} {
		if strings.EqualFold(name, x) {
			return "name_is_group"
		}
	}
	return ""
}

func (h *handlers) resetAdminPath(ctx context.Context, _ *struct{}) (*resetPathOutput, error) {
	if err := settings.Set(ctx, h.d.Settings, settings.KeyAdminPath, secure.Token(24)); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.reset_admin_path", "", "", nil)
	v, err := h.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := &resetPathOutput{}
	out.Body.AdminURL = v.AdminURL
	return out, nil
}

// checkCA: ZeroSSL needs an e-mail, Google an EAB key; what is given is checked for form.
// What the request leaves out is the saved value.
func (h *handlers) checkCA(ctx context.Context, ca, email, kid, hmac *string) ([]error, error) {
	cur := func(v *string, key string) (string, error) {
		if v != nil {
			return strings.TrimSpace(*v), nil
		}
		return h.d.Settings.String(ctx, key)
	}
	chosen, err := acme.ChosenCA(ctx, h.d.Settings)
	if err != nil {
		return nil, err
	}
	if ca != nil {
		chosen = *ca
	}
	e, err := cur(email, settings.KeyACMEEmail)
	if err != nil {
		return nil, err
	}
	k, err := cur(kid, settings.KeyACMEEABKID)
	if err != nil {
		return nil, err
	}
	m, err := cur(hmac, settings.KeyACMEEABHMAC)
	if err != nil {
		return nil, err
	}
	var out []error
	bad := func(field, code string) {
		out = append(out, &huma.ErrorDetail{Location: "body." + field, Message: code})
	}
	if e != "" && !acme.ValidEmail(e) {
		bad("acme_email", "email_invalid")
	}
	if k != "" && !acme.ValidEABKID(k) {
		bad("acme_eab_kid", "eab_kid_invalid")
	}
	if m != "" && !acme.ValidEABKey(m) {
		bad("acme_eab_hmac", "eab_hmac_invalid")
	}
	switch chosen {
	case acme.CAZeroSSL:
		if e == "" {
			bad("acme_email", "zerossl_email_required")
		}
	case acme.CAGoogle:
		if k == "" {
			bad("acme_eab_kid", "google_eab_required")
		}
		if m == "" {
			bad("acme_eab_hmac", "google_eab_required")
		}
	}
	return out, nil
}
