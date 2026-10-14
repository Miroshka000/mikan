package tgate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefundStarsAlreadyRefundedIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot1:x/refundStarPayment" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: CHARGE_ALREADY_REFUNDED"})
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "1:x", nil).RefundStars(context.Background(), 7, "charge-1"); err != nil {
		t.Fatalf("repeated refund should succeed idempotently: %v", err)
	}
}
