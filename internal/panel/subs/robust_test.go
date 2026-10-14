package subs

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// Format and DetectApp read the same User-Agent, and must agree about an app: Hiddify says
// "like ClashMeta" and used to be given a Clash profile; Prizrak and its kin are mihomo
// apps that were given a link list.
func TestFormatFollowsDetectApp(t *testing.T) {
	cases := []struct {
		ua     string
		format string
		family Family
	}{
		{"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", "uri", FamilySingBox},
		{"Prizrak-Box/1.4.2 (windows)", "clash", FamilyMihomo},
		{"Flowvy/0.9 mihomo/1.19.31", "clash", FamilyMihomo},
		{"Murge/2.0", "clash", FamilyMihomo},
		{"Rabbit/1.1 (macos)", "clash", FamilyMihomo},
		{"clash-verge/v2.2.3", "clash", FamilyMihomo},
		{"Koala-Clash/1.4.0", "clash", FamilyMihomo},
		{"Stash/3.1.1 Clash/1.9.0", "clash", FamilyStash},
		{"Happ/3.4.1", "uri", FamilyXray},
		{"v2rayNG/1.10.2", "uri", FamilyXray},
		{"Karing/1.2.0", "uri", FamilySingBox},
		{"NekoBox/1.3.7", "uri", FamilySingBox},
		{"sing-box 1.11", "uri", FamilySingBox},
		{"Streisand/1.6", "uri", FamilyOther},
		{"Shadowrocket/2.2.60", "uri", FamilyOther},
		{"curl/8.9", "uri", FamilyOther},
	}
	for _, c := range cases {
		if got := DetectApp(c.ua).Family; got != c.family {
			t.Errorf("DetectApp(%q) = %s, want %s", c.ua, got, c.family)
		}
		if got := Format(c.ua, "*/*", ""); got != c.format {
			t.Errorf("Format(%q) = %s, want %s", c.ua, got, c.format)
		}
	}
	// Whatever DetectApp says is a core's family decides: never Clash for an Xray or sing-box app.
	for _, c := range cases {
		switch DetectApp(c.ua).Family {
		case FamilyXray, FamilySingBox:
			if Format(c.ua, "*/*", "") == "clash" {
				t.Errorf("%q: a Clash profile for a %s app", c.ua, DetectApp(c.ua).Family)
			}
		case FamilyMihomo, FamilyStash:
			if Format(c.ua, "*/*", "") != "clash" {
				t.Errorf("%q: no Clash profile for a mihomo app", c.ua)
			}
		}
	}
	if Format("Mozilla/5.0", "text/html", "") != "html" || Format("Happ/3.4.1", "text/html", "") != "uri" {
		t.Error("a browser gets the page, an app that names itself gets its format")
	}
}

// One inbound that cannot be rendered is left out and reported, not the end of everyone's
// subscription.
func TestBadInboundIsSkippedAndReported(t *testing.T) {
	prof := profile(t, "")
	want := len(prof.Inbounds)
	bad := prof.Inbounds[1]
	bad.ID, bad.Name, bad.Port = 99, "broken-port", "not-a-port"
	prof.Inbounds = append(prof.Inbounds, bad)
	broken := prof.Inbounds[2]
	broken.ID, broken.Name, broken.Config = 98, "broken-config", "type: [unclosed"
	prof.Inbounds = append(prof.Inbounds, broken)

	var skipped []string
	prof.Skip = func(in db.Inbound, err error) { skipped = append(skipped, in.Name+": "+err.Error()) }
	links, err := URIs(prof)
	if err != nil {
		t.Fatalf("one bad inbound failed the whole subscription: %v", err)
	}
	if got := len(strings.Split(links, "\n")); got != want {
		t.Fatalf("%d links, want the %d good ones", got, want)
	}
	if len(skipped) != 2 || !strings.HasPrefix(skipped[0], "broken-port") || !strings.HasPrefix(skipped[1], "broken-config") {
		t.Fatalf("skipped: %v", skipped)
	}
	if _, err := Mihomo(prof, Groups{}, RoutingAll); err != nil {
		t.Fatalf("the Clash profile: %v", err)
	}
	// Without a listener nothing is reported and nothing panics.
	prof.Skip = nil
	if _, err := URIs(prof); err != nil {
		t.Fatal(err)
	}
}

func TestNothingToServeIsAnError(t *testing.T) {
	prof := profile(t, "")
	prof.Inbounds = nil
	if _, err := URIs(prof); !errors.Is(err, ErrNoProxies) {
		t.Fatalf("links: %v", err)
	}
	if _, err := Mihomo(prof, Groups{}, RoutingAll); !errors.Is(err, ErrNoProxies) {
		t.Fatalf("a profile with empty groups is not served: %v", err)
	}
}

func TestParsedTemplatesAreKeptAndCopied(t *testing.T) {
	src := profile(t, "").Inbounds[0].Config
	a, err := parseTemplate(src)
	if err != nil {
		t.Fatal(err)
	}
	a["type"] = "changed"
	a["reality-config"].(map[string]any)["dest"] = "evil.example:443"
	b, err := parseTemplate(src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Type() == "changed" || b["reality-config"].(map[string]any)["dest"] == "evil.example:443" {
		t.Fatal("a caller's change reached the kept template")
	}
	if _, err := parseTemplate("type: [unclosed"); err == nil {
		t.Fatal("a broken template parses")
	}
	if _, err := parseTemplate("type: [unclosed"); err == nil {
		t.Fatal("and keeps failing")
	}
}

// What the buyer can act on is told as it is; a provider that fails or a database that is
// busy is not "payment is off", and a cause nobody has logged yet is flagged for the log.
func TestInvoiceFailure(t *testing.T) {
	for _, c := range []struct {
		err         error
		status      int
		code        string
		unexplained bool
	}{
		{billing.ErrNotForSale, http.StatusConflict, "not_for_sale", false},
		{fmt.Errorf("wrapped: %w", billing.ErrTooMany), http.StatusConflict, "too_many_invoices", false},
		{billing.ErrNotYours, http.StatusConflict, "not_yours", false},
		{billing.ErrProviderOff, http.StatusConflict, "provider_off", false},
		{promo.ErrRefundUnsupported, http.StatusConflict, "promo_unavailable", false},
		{promo.ErrAlreadyApplied, http.StatusConflict, "promo_unavailable", false},
		{fmt.Errorf("invoice: %w", promo.ErrReservationExpired), http.StatusConflict, "promo_unavailable", false},
		{fmt.Errorf("%w: yookassa_unreachable", billing.ErrProviderOff), http.StatusBadGateway, "invoice_failed", false}, // billing logged it
		{errors.New("database is locked"), http.StatusBadGateway, "invoice_failed", true},
	} {
		status, code, unexplained := invoiceFailure(c.err)
		if status != c.status || code != c.code || unexplained != c.unexplained {
			t.Errorf("%v: %d %s %v, want %d %s %v", c.err, status, code, unexplained, c.status, c.code, c.unexplained)
		}
	}
}

func TestPromoUnavailableDoesNotRevealCodeExistence(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit, promo.ErrTariff} {
		if got := promoAttemptCode(err); got != "promo_unavailable" {
			t.Errorf("%v leaked as %q", err, got)
		}
	}
}

func TestPromoAttemptErrorsDoNotRevealEligibility(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit,
		promo.ErrTariff, promo.ErrMinimum, promo.ErrNewUser, promo.ErrFirstPurchase, promo.ErrCurrency, promo.ErrNotDiscount} {
		if got := promoAttemptCode(err); got != "promo_unavailable" {
			t.Errorf("%v leaked as %q", err, got)
		}
	}
}

func TestPromoAttemptInternalErrorIsNotHidden(t *testing.T) {
	if got := promoAttemptCode(errors.New("database is down")); got != "" {
		t.Errorf("an internal error answered as %q", got)
	}
	if got := promoAttemptCode(fmt.Errorf("redeem: %w", promo.ErrNotBonus)); got != "promo_unavailable" {
		t.Errorf("a wrapped promo error answered as %q", got)
	}
}

func TestPromoErrorsAreGenericAtCheckout(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrTariff, promo.ErrMinimum, promo.ErrCurrency, promo.ErrRefundUnsupported} {
		if !promoError(err) || promoAttemptCode(err) != "promo_unavailable" {
			t.Errorf("%v is not handled as a generic promo error", err)
		}
	}
	if promoError(billing.ErrProviderOff) {
		t.Fatal("provider error was classified as a promo error")
	}
}
