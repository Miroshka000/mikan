package subsrc

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/billing"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
)

type checkoutRequest struct {
	Key      string `json:"checkout_key"`
	Token    string `json:"token"`
	Email    string `json:"email"`
	TariffID int64  `json:"tariff_id"`
	TermDays *int64 `json:"term_days"`
	Provider string `json:"provider"`
}

func (h *Handler) webShop(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "no-store")
	if action == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && h.page != nil {
		cfg, _ := h.cfg(r.Context())
		h.servePage(w, r, cfg.Config)
		return
	}
	limit := h.webShopLimit(action)
	if limit == nil || r.Method != http.MethodPost || server.FetchSite(r.Header, r.Host) == server.SiteCross {
		server.NotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
	}
	now := h.now()
	if next := h.shopLimitSweep.Load(); now.Unix() >= next && h.shopLimitSweep.CompareAndSwap(next, now.Unix()+60) {
		h.shopPayLimit.Sweep(now)
		h.shopOffersLimit.Sweep(now)
		h.shopStatusLimit.Sweep(now)
	}
	if ok, wait, _ := limit.Reserve(auth.ClientKey(h.clientIP(r)), now); !ok {
		w.Header().Set("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
		code := "too_many_requests"
		if action == "pay" {
			code = billing.ErrTooMany.Error()
		}
		fail(http.StatusTooManyRequests, code)
		return
	}
	typ, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var in checkoutRequest
	if typ != "application/json" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in) != nil {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	ctx := r.Context()
	switch action {
	case "offers":
		var u db.User
		if in.Token != "" {
			var err error
			u, err = h.st.Q.GetUserBySubToken(ctx, in.Token)
			if errors.Is(err, sql.ErrNoRows) {
				fail(http.StatusNotFound, "not_yours")
				return
			}
			if err != nil {
				fail(http.StatusInternalServerError, "internal")
				return
			}
		}
		h.shopOffers(ctx, w, 0, u.ID, &u, fail)
	case "pay":
		p, err := h.shop.WebInvoice(ctx, billing.WebOrder{Key: in.Key, SubToken: in.Token, Email: in.Email,
			TariffID: in.TariffID, TermDays: in.TermDays, Provider: billing.AdapterOf(in.Provider)})
		switch {
		case errors.Is(err, billing.ErrCheckoutKey), errors.Is(err, billing.ErrCheckoutEmail):
			fail(http.StatusBadRequest, err.Error())
		case errors.Is(err, billing.ErrCheckoutConflict), errors.Is(err, billing.ErrCheckoutBusy):
			fail(http.StatusConflict, err.Error())
		case err != nil:
			status, code, unexplained := invoiceFailure(err)
			if unexplained {
				h.log.Warn("web checkout: invoice failed", "err", err)
			}
			fail(status, code)
		default:
			writeCheckoutStatus(w, p, "")
		}
	case "status":
		p, token, err := h.shop.WebStatus(ctx, in.Key)
		switch {
		case errors.Is(err, billing.ErrCheckoutKey):
			fail(http.StatusBadRequest, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			fail(http.StatusNotFound, "checkout_not_found")
		case err != nil:
			fail(http.StatusInternalServerError, "internal")
		default:
			writeCheckoutStatus(w, p, token)
		}
	default:
		server.NotFound(w)
	}
}

func (h *Handler) webShopLimit(action string) *auth.Limiter {
	switch action {
	case "offers":
		return h.shopOffersLimit
	case "pay":
		return h.shopPayLimit
	case "status":
		return h.shopStatusLimit
	}
	return nil
}

func writeCheckoutStatus(w http.ResponseWriter, p db.Payment, token string) {
	out := map[string]any{"status": p.Status}
	if billing.UnopenedWebInvoice(p) {
		out["retryable"] = true
	}
	if p.Status == billing.PaymentPending && p.PayUrl != "" {
		out["url"] = p.PayUrl
	}
	if token != "" {
		out["token"] = token
	}
	_ = json.NewEncoder(w).Encode(out)
}
