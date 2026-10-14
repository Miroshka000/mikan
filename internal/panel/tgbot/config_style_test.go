package tgbot

import (
	"errors"
	"testing"

	"mikan/internal/panel/tgate"
)

// A fresh bot's menu carries the colors: the subscription and its devices blue, renewing
// green. Styles a send may not carry are refused with the style's own error.
func TestButtonStyles(t *testing.T) {
	for _, lang := range []string{"ru", "en"} {
		def := Default(lang)
		for _, d := range def.Buttons {
			if d.Action == "sub" && d.Style != tgate.StylePrimary || d.Action == "devices" && d.Style != tgate.StylePrimary || d.Action == "renew" && d.Style != tgate.StyleSuccess {
				t.Fatalf("%s: %s got style %q", lang, d.Action, d.Style)
			}
		}
	}
	cfg := Default("ru")
	mutate := func(f func(c *Config)) *Config {
		c := cfg
		f(&c)
		return &c
	}
	for style, ok := range map[string]bool{"": true, " primary": true, tgate.StylePrimary: true, tgate.StyleSuccess: true, tgate.StyleDanger: true, "green": false} {
		c := mutate(func(c *Config) { c.Buttons[4].Style = style })
		err := c.Validate()
		if ok != (err == nil) {
			t.Errorf("style %q: err %v", style, err)
		}
	}
	c := mutate(func(c *Config) { c.Buttons[0].Style = "purple" })
	if err := c.Validate(); !errors.Is(err, ErrButtonStyle) {
		t.Fatalf("want the style's error, got %v", err)
	}
}

// A menu kept before the colors came in gets them when nobody customized it: the old
// default labels take the new label and its color; what the admin wrote — a label or a
// different color — stands.
func TestStyleMigration(t *testing.T) {
	old := Default("ru")
	for i := range old.Buttons {
		b := &old.Buttons[i]
		b.Style = ""
		if lab, ok := legacyLabels[b.Action]; ok {
			b.Label = lab["ru"] // the labels as a bot saved before the colors has them
		}
	}
	for i := range old.Buttons {
		if old.Buttons[i].Action == "sub" || old.Buttons[i].Action == "support" {
			old.Buttons[i].Label = "Мишенина кнопка" // the admin's own
		}
	}
	if err := (&old).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, d := range old.Buttons {
		switch d.Action {
		case "sub", "support":
			if d.Label != "Мишенина кнопка" {
				t.Errorf("a label of the admin's must stay: %s %q", d.Action, d.Label)
			}
		case "connect", "referral":
			if d.Style != "" {
				t.Errorf("%s: stays plain", d.Action)
			}
		case "renew":
			if d.Style != tgate.StyleSuccess {
				t.Errorf("renew: %q", d.Style)
			}
		case "devices", "app":
			want, got := defaults("ru"), d
			var w MenuButton
			for _, x := range want {
				if x.Action == got.Action {
					w = x
				}
			}
			if got.Label != w.Label || got.Style != w.Style {
				t.Errorf("%s: %q %q, want %q %q", got.Action, got.Label, got.Style, w.Label, w.Style)
			}
		}
	}
}
