package serve

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
	"mikan/internal/textutil"
)

// unsupportedError is why a request gets a stub that says its app is not supported.
type unsupportedError struct{ text string }

func (e *unsupportedError) Error() string { return "unsupported: " + e.text }

// StubText is the words of the entry an app that is turned away gets in place of the
// servers: the admin's own, else the built-in ones, which send the visitor to the apps the
// admin has not blocked. The admin panel's tester shows it too.
func StubText(lang string, d apps.Decision) string {
	if t := strings.TrimSpace(d.Action.Text); t != "" {
		return fillTitle(t, nil)
	}
	en := textutil.Lang(lang).En()
	switch {
	case len(d.Install) == 0 && en:
		return "⛔ This app is not supported — open the subscription link in a browser"
	case len(d.Install) == 0:
		return "⛔ Это приложение не поддерживается — откройте ссылку подписки в браузере"
	case en:
		return "⛔ This app is not supported — install " + listed(d.Install, "or")
	}
	return "⛔ Это приложение не поддерживается — установите " + listed(d.Install, "или")
}

// listed is "A", "A or B" and "A, B or C".
func listed(names []string, or string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + or + " " + names[len(names)-1]
}

// stub answers a device that gets no keys or a profile with nothing in it: one placeholder
// server named after the reason, so the app shows it where the servers would be, and the
// headers Happ-like apps read.
func (h *Handler) stub(w http.ResponseWriter, cfg Config, format string, reason error) {
	// The stub of an xray JSON answer is a link all the same: the app reads either, and one
	// placeholder server needs no routing.
	if format == FormatXray {
		format = FormatURI
	}
	en := textutil.Lang(cfg.Lang).En()
	name := "⛔ Все места для устройств заняты — откройте ссылку подписки в браузере"
	if en {
		name = "⛔ All device places are taken — open the subscription link in a browser"
	}
	var ue *UnboundError
	var ie *inactiveError
	var un *unsupportedError
	switch {
	case errors.As(reason, &un):
		name = un.text
	case errors.Is(reason, model.ErrNoProxies):
		// Nothing this app can use, or every server of the subscription is switched off: a
		// profile with an empty group would be refused by the app as a whole.
		name = "⛔ Для этого приложения нет подходящих серверов — откройте ссылку подписки в браузере"
		if en {
			name = "⛔ There are no servers this app can use — open the subscription link in a browser"
		}
	case errors.As(reason, &ie):
		name = ie.text
	case errors.Is(reason, ErrDeviceBanned):
		name = "⛔ Устройство заблокировано"
		if en {
			name = "⛔ This device is blocked"
		}
	case errors.As(reason, &ue):
		// The subscriber unbound it a moment ago: its app, still open, asks again.
		hours := max(1, int(math.Ceil(ue.Until.Sub(h.now()).Hours())))
		name = fmt.Sprintf("⏸ Устройство отвязано — подключится снова через %d ч", hours)
		if en {
			name = fmt.Sprintf("⏸ This device was unbound — it can connect again in %d h", hours)
		}
		if ue.Forever {
			name = "⏸ Устройство отвязано — подключить его снова может только администратор"
			if en {
				name = "⏸ This device was unbound — only the administrator can let it connect again"
			}
		}
	case errors.Is(reason, ErrNoHWID):
		name = "⛔ Приложение не сообщает ID устройства — откройте ссылку подписки в браузере"
		if en {
			name = "⛔ The app does not send a device ID — open the subscription link in a browser"
		}
		if names := cfg.Responses.HWIDApps(); len(names) > 0 {
			name = "⛔ Приложение не сообщает ID устройства — поставьте " + listed(names, "или")
			if en {
				name = "⛔ The app does not send a device ID — install " + listed(names, "or")
			}
		}
	}
	hd := w.Header()
	stubHeaders(hd, reason)
	contentType(hd, format)
	main := cfg.Groups.WithDefaults(cfg.Lang).Main
	switch format {
	case FormatClash:
		_, _ = w.Write(mihomo.Stub(name, main))
	case FormatSingBox:
		_, _ = w.Write(singbox.Stub(name, main))
	default:
		_, _ = w.Write(xray.Encode(xray.Stub(name)))
	}
}

// A subscription that cannot connect (its term over, its traffic used up, turned off) gets
// one "server" in place of the list when the admin turns it on (Config.InactiveServer): its
// name tells the subscriber what happened and what to do, so the app does not just fail on
// every server. The admin's own text (Config.InactiveText) takes the variables of the title.

// inactiveError is a subscription that cannot connect, with what its notice says.
type inactiveError struct{ text string }

func (e *inactiveError) Error() string { return "inactive: " + e.text }

// inactiveNotice is the notice for a subscription in state, or nil when it can connect or
// the notice is off.
func inactiveNotice(cfg Config, state State, vars map[string]string) error {
	if !cfg.InactiveServer || state.CanConnect() {
		return nil
	}
	if cfg.InactiveText != "" {
		if s := fillTitle(cfg.InactiveText, vars); s != "" {
			return &inactiveError{text: s}
		}
	}
	return &inactiveError{text: inactiveDefault(state, textutil.Lang(cfg.Lang).En())}
}

func inactiveDefault(state State, en bool) string {
	switch {
	case state == StateLimited && en:
		return "⛔ The traffic is used up — buy more or renew in the bot"
	case state == StateLimited:
		return "⛔ Трафик закончился — докупите или продлите подписку в боте"
	case state == StateDisabled && en:
		return "⛔ The subscription is turned off — write to support"
	case state == StateDisabled:
		return "⛔ Подписка отключена — напишите в поддержку"
	case en:
		return "⛔ The subscription has ended — renew it in the bot"
	}
	return "⛔ Подписка закончилась — продлите её в боте"
}
