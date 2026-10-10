package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/certcheck"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tlscert"
)

// The admin's own certificates (GitHub issue #9): the panel's instead of Let's Encrypt,
// and a node's instead of its self-signed one. A private key comes in and never goes
// out: not in a response, a log or the audit log. API keys cannot reach these.

type certInput struct {
	Body struct {
		Cert string `json:"cert" minLength:"1" maxLength:"65536" doc:"Цепочка в PEM: сначала сертификат, за ним промежуточные (fullchain.pem)"`
		Key  string `json:"key" minLength:"1" maxLength:"65536" doc:"Закрытый ключ в PEM (privkey.pem): RSA от 2048 бит, ECDSA P-256/384/521 или Ed25519"`
	}
}

type nodeCertInput struct {
	ID int64 `path:"id" minimum:"1"`
	certInput
}

// NodeCertView is a node's own certificate as the admin panel shows it.
type NodeCertView struct {
	tlscert.Info
	Error string `json:"error,omitempty" doc:"custom_expired, custom_invalid — сертификат не используется, нода на своём самоподписанном"`
}

// NodeTLSView is the certificate a node's protocols on TLS (Hysteria2, TUIC, AnyTLS,
// TrustTunnel, VLESS TLS) use now, and the public one the panel orders for it.
type NodeTLSView struct {
	Kind     string     `json:"kind" enum:"custom,acme,panel,self-signed" doc:"custom — свой, acme — публичный, полученный панелью для ноды, panel — публичный сертификат панели (своя нода), self-signed — самоподписанный"`
	Pinned   bool       `json:"pinned" doc:"Ссылки закрепляют самоподписанный сертификат: в sing-box приложениях (SFA, SFI) протоколы ноды на TLS не появятся, TUIC без проверки сертификата"`
	CA       string     `json:"ca,omitempty" enum:"letsencrypt,zerossl,google"`
	Issuer   string     `json:"issuer,omitempty"`
	Names    []string   `json:"names,omitempty"`
	NotAfter *time.Time `json:"not_after,omitempty"`
	// ACME is the order of the node's public certificate; nil for the panel's own node and
	// for a node with a certificate of its own.
	ACME *acme.NodeStatus `json:"acme,omitempty"`
}

type nodeTLSOutput struct{ Body NodeTLSView }

type certCheckOutput struct{ Body certcheck.CertReport }

// renewWait is how long «Получить сейчас» waits for the CA: an order takes seconds, a port
// 80 that hangs a minute and more; then the answer says the order still runs.
const renewWait = 90 * time.Second

func (h *handlers) registerCerts() {
	tags := []string{"settings"}
	huma.Register(h.api, huma.Operation{OperationID: "check-certificate", Method: http.MethodPost, Path: "/api/v1/settings/certificate/check",
		Summary: "Проверить сертификаты панели и нод снаружи: что отдают порты, DNS, порт 80, заказы", Tags: tags}, h.checkCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "renew-node-certificate", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/certificate/renew",
		Summary: "Получить публичный сертификат ноды сейчас (ждёт до 90 с)", Tags: []string{"node"}}, h.renewNodeCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "set-certificate", Method: http.MethodPut, Path: "/api/v1/settings/certificate", Summary: "Поставить свой сертификат панели",
		Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.setCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "clear-certificate", Method: http.MethodDelete, Path: "/api/v1/settings/certificate", Summary: "Вернуть сертификат Let's Encrypt",
		Tags: tags, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusNoContent}, h.clearCertificate)
	nodeTags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "set-node-certificate", Method: http.MethodPut, Path: "/api/v1/nodes/{id}/certificate", Summary: "Поставить ноде свой сертификат",
		Tags: nodeTags, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.setNodeCertificate)
	huma.Register(h.api, huma.Operation{OperationID: "clear-node-certificate", Method: http.MethodDelete, Path: "/api/v1/nodes/{id}/certificate", Summary: "Убрать свой сертификат ноды: вернётся публичный, полученный панелью, или самоподписанный",
		Tags: nodeTags, Metadata: sessionOnly, Extensions: sessionOnlyExt, DefaultStatus: http.StatusNoContent}, h.clearNodeCertificate)
}

// certError maps a refused certificate to the field to fix.
func certError(err error, host string) error {
	field, value := "body.cert", ""
	switch {
	case errors.Is(err, tlscert.ErrKeyPEM), errors.Is(err, tlscert.ErrKeyMismatch), errors.Is(err, tlscert.ErrKeyWeak):
		field = "body.key"
	case errors.Is(err, tlscert.ErrWrongHost):
		value = host
	case errors.Is(err, tlscert.ErrCertPEM), errors.Is(err, tlscert.ErrExpired), errors.Is(err, tlscert.ErrNotYet):
	default:
		return err
	}
	return huma.Error422UnprocessableEntity("bad_certificate", &huma.ErrorDetail{Location: field, Message: err.Error(), Value: value})
}

func (h *handlers) setCertificate(ctx context.Context, in *certInput) (*settingsOutput, error) {
	if h.d.SetCert == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	if err := h.d.SetCert(ctx, []byte(in.Body.Cert), []byte(in.Body.Key)); err != nil {
		ep, _ := h.d.Settings.Endpoint(ctx)
		return nil, certError(err, ep.Host)
	}
	st := h.d.Cert()
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.certificate", "", "", map[string]any{"kind": "custom", "names": st.Names, "not_after": st.NotAfter})
	v, err := h.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &settingsOutput{Body: v}, nil
}

func (h *handlers) clearCertificate(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if h.d.ClearCert == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	if err := h.d.ClearCert(); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.certificate", "", "", map[string]any{"kind": "automatic"})
	return nil, nil
}

func (h *handlers) setNodeCertificate(ctx context.Context, in *nodeCertInput) (*nodeInfoOutput, error) {
	if h.d.NodeCerts == nil {
		return nil, huma.Error409Conflict("node_certs_disabled")
	}
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	c, err := h.d.NodeCerts.Set(n.ID, []byte(in.Body.Cert), []byte(in.Body.Key))
	if err != nil {
		return nil, certError(err, "")
	}
	// The node gets the new certificate with its next state; links get the new pin.
	if h.d.Changes != nil {
		h.d.Changes.SlotsChanged()
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.certificate", "node", strconv.FormatInt(n.ID, 10), map[string]any{"kind": "custom", "names": c.Leaf.DNSNames, "not_after": c.Leaf.NotAfter})
	return h.nodeInfo(ctx, n.ID)
}

func (h *handlers) clearNodeCertificate(ctx context.Context, in *nodeIDInput) (*struct{}, error) {
	if h.d.NodeCerts == nil {
		return nil, huma.Error409Conflict("node_certs_disabled")
	}
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := h.d.NodeCerts.Clear(n.ID); err != nil {
		return nil, err
	}
	if h.d.Changes != nil {
		h.d.Changes.SlotsChanged()
	}
	// Without its own one the node gets a public certificate from the panel.
	if h.d.WakeNodeCerts != nil {
		h.d.WakeNodeCerts()
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.certificate", "node", strconv.FormatInt(n.ID, 10), map[string]any{"kind": "self-signed"})
	return nil, nil
}

func (h *handlers) checkCertificate(ctx context.Context, _ *struct{}) (*certCheckOutput, error) {
	if h.d.CheckCerts == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	r, err := h.d.CheckCerts(ctx)
	if err != nil {
		return nil, err
	}
	if r.Checks == nil {
		r.Checks = []certcheck.CertCheck{}
	}
	return &certCheckOutput{Body: r}, nil
}

// renewNodeCertificate orders the node's public certificate now and answers with what it
// has then: the new certificate, the classified error, or the order still running.
func (h *handlers) renewNodeCertificate(ctx context.Context, in *nodeIDInput) (*nodeTLSOutput, error) {
	if h.d.RenewNodeCert == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	n, err := h.getNode(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if _, _, err := h.d.RenewNodeCert(ctx, n.ID, renewWait); errors.Is(err, acme.ErrNoTarget) {
		return nil, huma.Error409Conflict("node_cert_not_ordered")
	} else if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.renew_certificate", "node", strconv.FormatInt(n.ID, 10), nil)
	v := h.nodeTLSView(ctx, n)
	if v == nil {
		return nil, huma.Error409Conflict("acme_disabled")
	}
	return &nodeTLSOutput{Body: *v}, nil
}

func (h *handlers) nodeTLSView(ctx context.Context, n db.Node) *NodeTLSView {
	if h.d.NodeTLS == nil {
		return nil
	}
	return h.d.NodeTLS(ctx, n)
}

// nodeCertView is what a node's own certificate is now, nil without one.
func (h *handlers) nodeCertView(id int64, host string) *NodeCertView {
	if h.d.NodeCerts == nil {
		return nil
	}
	c, trusted, err := h.d.NodeCerts.Get(id, host)
	switch {
	case c != nil:
		v := &NodeCertView{Info: tlscert.Describe(c, host, h.d.Now())}
		v.Trusted = trusted
		return v
	case errors.Is(err, tlscert.ErrExpired):
		return &NodeCertView{Error: "custom_expired"}
	case err != nil:
		return &NodeCertView{Error: "custom_invalid"}
	}
	return nil
}
