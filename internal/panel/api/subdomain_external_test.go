package api

import (
	"context"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/settings"
	"testing"
	"time"
)

func TestExternalDomainCertificateIsStagedOnlyWhenReturningToPanel(t *testing.T) {
	calls := 0
	h := &handlers{d: Deps{StageSubCert: func(context.Context, string, time.Duration) acme.Staged { calls++; return acme.Staged{OK: true} }}}
	prev := settings.Address{SubDomain: "edge.example", SubExternal: true}
	off := false
	if details := h.stageNewNames(t.Context(), prev, &patchSettingsBody{SubExternal: &off}); len(details) != 0 || calls != 1 {
		t.Fatalf("returning domain did not stage local TLS: %v, calls=%d", details, calls)
	}
	on := true
	other := "other-edge.example"
	if details := h.stageNewNames(t.Context(), prev, &patchSettingsBody{SubExternal: &on, SubDomain: &other}); len(details) != 0 || calls != 1 {
		t.Fatal("external domain staged a local certificate")
	}
}
