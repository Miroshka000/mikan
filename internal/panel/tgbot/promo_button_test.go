package tgbot

import (
	"strings"
	"testing"

	"mikan/internal/panel/settings"
)

// menuTexts is the main menu as rows of button labels, and fails on an empty row.
func menuTexts(t *testing.T, kb *Keyboard) [][]string {
	t.Helper()
	var out [][]string
	for _, r := range kb.InlineKeyboard {
		if len(r) == 0 {
			t.Fatalf("an empty row in the menu: %+v", kb.InlineKeyboard)
		}
		row := []string{}
		for _, b := range r {
			row = append(row, b.Text)
		}
		out = append(out, row)
	}
	return out
}

func hasPromo(rows [][]string) bool {
	for _, r := range rows {
		for _, b := range r {
			if strings.Contains(b, "Промокоды") {
				return true
			}
		}
	}
	return false
}

// The «Промокоды» button is the admin's to hide. Hiding it takes only the menu entry:
// the Mini App page still has its promo field.
func TestPromoButtonInMenu(t *testing.T) {
	e := setup(t)
	w := wordsFor("ru")

	cfg := Default("ru")
	rows := menuTexts(t, e.bot.menu(e.ctx, cfg, w, 1))
	if !hasPromo(rows) {
		t.Fatalf("a default bot shows the promo codes button: %v", rows)
	}
	if last := rows[len(rows)-1]; len(last) != 1 || !strings.Contains(last[0], "Промокоды") {
		t.Fatalf("it sits alone in the last row: %v", rows)
	}

	cfg.PromoButton = false
	kb := e.bot.menu(e.ctx, cfg, w, 1)
	rows = menuTexts(t, kb)
	if hasPromo(rows) {
		t.Fatalf("hidden, the button is gone: %v", rows)
	}
	for _, r := range kb.InlineKeyboard {
		for _, b := range r {
			if b.WebApp != nil && strings.HasSuffix(b.WebApp.URL, "#promocodes") {
				t.Fatalf("no way in to the promo section is left: %+v", b)
			}
		}
	}
	// The other buttons stay as they were: sub + devices, connect, renew + support, the page.
	if len(rows) != 4 || len(rows[0]) != 2 || len(rows[1]) != 1 || len(rows[2]) != 2 || len(rows[3]) != 1 {
		t.Fatalf("the rest of the menu is unchanged: %v", rows)
	}
	var page *Button
	for _, r := range kb.InlineKeyboard {
		for i := range r {
			if r[i].WebApp != nil {
				page = &r[i]
			}
		}
	}
	if page == nil || page.WebApp.URL != "https://vpn.example.com:21355/sub/tg" {
		t.Fatalf("the Mini App page button stays: %+v", page)
	}

	// Subscriptions switcher still comes last and the menu has no empty row.
	rows = menuTexts(t, e.bot.menu(e.ctx, cfg, w, 2))
	if last := rows[len(rows)-1]; len(last) != 1 || !strings.HasPrefix(last[0], w.subscriptions) {
		t.Fatalf("the subscriptions button: %v", rows)
	}

	// Without the Mini App there is no promo button either way.
	cfg.PromoButton = true
	cfg.MiniApp = false
	if rows = menuTexts(t, e.bot.menu(e.ctx, cfg, w, 1)); hasPromo(rows) {
		t.Fatalf("no Mini App, no promo button: %v", rows)
	}
}

// Hiding other buttons leaves no empty row and no row of the promo button's own.
func TestMenuRowsAfterHiding(t *testing.T) {
	e := setup(t)
	w := wordsFor("ru")
	cfg := Default("ru")
	for i := range cfg.Buttons {
		if cfg.Buttons[i].Action == "sub" {
			cfg.Buttons[i].On = false // devices keeps its «in one row» flag with nothing before it
		}
	}
	cfg.PromoButton = false
	rows := menuTexts(t, e.bot.menu(e.ctx, cfg, w, 1))
	if len(rows[0]) != 1 || !strings.Contains(rows[0][0], "Устройства") {
		t.Fatalf("the first button starts a row of its own: %v", rows)
	}
}

// A setup saved by the panel with the option off keeps it off; one saved before the option
// existed shows the button.
func TestPromoButtonSaved(t *testing.T) {
	e := setup(t)
	cfg := Default("ru")
	cfg.PromoButton = false
	if err := settings.Set(e.ctx, e.set, KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	if e.bot.Config(e.ctx).PromoButton {
		t.Fatal("a saved off stays off")
	}
	cfg.PromoButton = true
	if err := settings.Set(e.ctx, e.set, KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	if !e.bot.Config(e.ctx).PromoButton {
		t.Fatal("a saved on stays on")
	}
}
