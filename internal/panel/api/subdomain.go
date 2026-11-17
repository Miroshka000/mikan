package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
)

// The subscription domain: links on a name of their own, apart from the panel's, with a
// certificate of their own (acme.NewSub). Requests to it reach the subscriptions alone.

// SubCerts is the subscription domain's certificate (acme.Manager).
type SubCerts interface {
	Status() acme.Status
	RenewNow(ctx context.Context, wait time.Duration) (acme.Status, bool)
	SetCustom(ctx context.Context, certPEM, keyPEM []byte) error
	ClearCustom() error
}

func (h *handlers) registerSubDomain() {
	tags := []string{"settings"}
	huma.Register(h.api, huma.Operation{OperationID: "renew-sub-certificate", Method: http.MethodPost, Path: "/api/v1/settings/sub-certificate/renew",
		Summary: "Получить сертификат домена подписки сейчас: ждёт до 90 с и отвечает тем, что вышло", Tags: tags}, h.renewSubCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "set-sub-certificate", Method: http.MethodPut, Path: "/api/v1/settings/sub-certificate",
		Summary: "Поставить свой сертификат домену подписки", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.setSubCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "clear-sub-certificate", Method: http.MethodDelete, Path: "/api/v1/settings/sub-certificate",
		Summary: "Убрать свой сертификат домена подписки: вернётся автоматический", Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt,
		DefaultStatus: http.StatusNoContent}, h.clearSubCertificate)
}

// checkSubDomain says what is wrong with the subscription domain the request leaves: it
// is not the panel's own name, and it leads to this server. Checked when it, the panel's
// domain or the server's address changes; public DNS only when it or the address does.
func (h *handlers) checkSubDomain(ctx context.Context, b *patchSettingsBody) ([]*huma.ErrorDetail, error) {
	cur, err := h.d.Settings.String(ctx, settings.KeySubDomain)
	if err != nil {
		return nil, err
	}
	dom := cur
	if b.SubDomain != nil {
		dom = strings.TrimSpace(*b.SubDomain)
	}
	external, _, err := settings.Get[bool](ctx, h.d.Settings, settings.KeySubExternal)
	if err != nil {
		return nil, err
	}
	if b.SubExternal != nil {
		external = *b.SubExternal
	}
	if dom == "" {
		if external {
			return []*huma.ErrorDetail{fieldDetail("body.sub_domain", "sub_domain_invalid", nil)}, nil
		}
		return nil, nil
	}
	panelDomain, err := h.d.Settings.String(ctx, settings.KeyDomain)
	if err != nil {
		return nil, err
	}
	if b.Domain != nil {
		panelDomain = strings.TrimSpace(*b.Domain)
	}
	if strings.EqualFold(dom, panelDomain) {
		return []*huma.ErrorDetail{fieldDetail("body.sub_domain", "sub_domain_panel", nil)}, nil
	}
	if external {
		return nil, nil
	}
	if dom == cur && b.PublicHost == nil && b.SubExternal == nil {
		return nil, nil
	}
	host := ""
	if b.PublicHost != nil {
		host = strings.TrimSpace(*b.PublicHost)
	} else if host, err = h.d.Settings.String(ctx, settings.KeyPublicHost); err != nil {
		return nil, err
	}
	if d := h.domainHere(ctx, "body.sub_domain", dom, dnscheck.Own(host)); d != nil {
		return []*huma.ErrorDetail{d}, nil
	}
	return nil, nil
}

func (h *handlers) renewSubCertificate(ctx context.Context, _ *struct{}) (*certificateOutput, error) {
	if h.d.SubCerts == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.renew_sub_certificate", "", "", nil)
	st, _ := h.d.SubCerts.RenewNow(ctx, renewWait)
	return &certificateOutput{Body: st}, nil
}

func (h *handlers) setSubCertificate(ctx context.Context, in *certInput) (*settingsOutput, error) {
	if h.d.SubCerts == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	dom, err := h.d.Settings.String(ctx, settings.KeySubDomain)
	if err != nil {
		return nil, err
	}
	if dom == "" {
		return nil, huma.Error409Conflict("sub_domain_unset")
	}
	if err := h.d.SubCerts.SetCustom(ctx, []byte(in.Body.Cert), []byte(in.Body.Key)); err != nil {
		return nil, certError(err, dom)
	}
	st := h.d.SubCerts.Status()
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.sub_certificate", "", "", map[string]any{"kind": "custom", "names": st.Names, "not_after": st.NotAfter})
	v, err := h.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &settingsOutput{Body: v}, nil
}

func (h *handlers) clearSubCertificate(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if h.d.SubCerts == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	if err := h.d.SubCerts.ClearCustom(); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.sub_certificate", "", "", map[string]any{"kind": "automatic"})
	return nil, nil
}
