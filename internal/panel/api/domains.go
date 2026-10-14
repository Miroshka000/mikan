package api

import (
	"context"
	"errors"
	"net/netip"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
)

// A domain of the panel or of a node is taken only when public DNS sends clients to that
// machine: otherwise anyone could write someone else's domain into every subscription. The
// check itself is domain.DomainHere, the one the shell uses too.

// domainHere checks that domain points only to own; nil when it does (or when the panel
// runs without the check, in tests). field is where the error goes.
func (h *handlers) domainHere(ctx context.Context, field, dom string, own []netip.Addr) *huma.ErrorDetail {
	var e *domain.DomainError
	if errors.As(domain.DomainHere(ctx, h.d.DNS, dom, own), &e) {
		return h.domainDetail(field, e)
	}
	return nil
}

// domainDetail is a domain the check refused, as the field's error.
func (h *handlers) domainDetail(field string, e *domain.DomainError) *huma.ErrorDetail {
	if e.Err != nil {
		h.d.Log.Warn("domain check", "domain", e.Domain, "err", e.Err)
	}
	var value any
	if e.Value != "" {
		value = e.Value
	}
	return fieldDetail(field, e.Code, value)
}
