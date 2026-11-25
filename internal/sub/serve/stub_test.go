package serve

import (
	"testing"

	"mikan/internal/sub/apps"
)

func TestStubText(t *testing.T) {
	d := apps.Decision{Install: []string{"Happ", "INCY", "Koala Clash"}}
	for _, c := range []struct {
		lang string
		d    apps.Decision
		want string
	}{
		{"en", d, "⛔ This app is not supported — install Happ, INCY or Koala Clash"},
		{"ru", d, "⛔ Это приложение не поддерживается — установите Happ, INCY или Koala Clash"},
		{"en", apps.Decision{Install: []string{"Happ", "INCY"}}, "⛔ This app is not supported — install Happ or INCY"},
		{"en", apps.Decision{Install: []string{"Happ"}}, "⛔ This app is not supported — install Happ"},
		{"en", apps.Decision{}, "⛔ This app is not supported — open the subscription link in a browser"},
		{"ru", apps.Decision{}, "⛔ Это приложение не поддерживается — откройте ссылку подписки в браузере"},
		{"en", apps.Decision{Action: apps.Action{Text: " Use Happ,\r\nit is free "}, Install: d.Install}, "Use Happ,  it is free"},
	} {
		if got := StubText(c.lang, c.d); got != c.want {
			t.Errorf("%s: %q, want %q", c.lang, got, c.want)
		}
	}
}
