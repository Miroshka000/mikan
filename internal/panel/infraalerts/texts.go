package infraalerts

// texts is every sentence the monitor says to the admin or to the public channel, in one
// language. A round picks the language once (textsFor) and the code below only fills the
// sentences in: no function branches on the language itself. Everything here is Telegram HTML
// except the plain fragments, and the %s verbs are escaped by the caller before they go in.
type texts struct {
	// The state words of a node, an inbound, a WARP or an exit alert (eventFor).
	stateUnavailable string
	stateRecovered   string
	stateDegraded    string
	// relayLabel names the cascade relay in an alert, as the Nodes page does.
	relayLabel string

	// The line that says why the WARP check failed (warpReason); the causes are the codes the
	// node sends (nodeapi.WarpStatus). warpTimeout takes the endpoint.
	warpReason       string
	warpTimeout      string
	warpHTTPSTimeout string
	warpDNS          string
	warpTLS          string
	warpRefused      string
	warpNotLoaded    string
	warpBadAnswer    string
	warpCheckFailed  string

	// Autotune: a move off a busy port (node, inbound, old port, new port), the title of a
	// changed inbound, and an inbound the tuner could not restore (node, inbound, why).
	inboundMoved    string
	autotuneChanged string
	autotuneStuck   string
	// The TLS certificate going bad and coming back, and the Mikan update that failed.
	tlsBad       string
	tlsRecovered string
	updateFailed string
	// nodeUpdateFailed opens a failed node update's message (the node's name follows) and
	// nodeUpdateRollout ends it.
	nodeUpdateFailed  string
	nodeUpdateRollout string

	// The torrent blocker's message: the title, the ban (the time) and the rest of the catches
	// (their count).
	torrentTitle  string
	torrentBanned string
	torrentMore   string

	// The public status message: its title, the line when no server is published, and the
	// word for each level (PublicText).
	publicTitle      string
	publicNone       string
	levelChecking    string
	levelWorking     string
	levelIssues      string
	levelUnavailable string
}

var ruTexts = texts{
	stateUnavailable: "Недоступен",
	stateRecovered:   "Восстановлен",
	stateDegraded:    "Есть проблемы",
	relayLabel:       "служебный вход каскада",

	warpReason:       "Причина: ",
	warpTimeout:      "нет ответа от endpoint %s по UDP: возможно, хостер блокирует UDP, попробуйте другой endpoint",
	warpHTTPSTimeout: "туннель поднят, но cloudflare.com не ответил вовремя",
	warpDNS:          "WARP не смог разрешить имя",
	warpTLS:          "ошибка TLS через WARP",
	warpRefused:      "соединение отклонено",
	warpNotLoaded:    "WARP ещё не загружен на ноде",
	warpBadAnswer:    "Cloudflare ответил непонятно",
	warpCheckFailed:  "проверка не прошла",

	inboundMoved:      "🛠 <b>Подключение перенесено</b>: %s / %s с порта %s на %s, порт занят другой программой",
	autotuneChanged:   "Автонастройка изменила подключение",
	autotuneStuck:     "⚠️ <b>Автонастройка не восстановила подключение</b>: %s / %s (%s)",
	tlsBad:            "🟡 <b>Проблема с TLS-сертификатом</b>",
	tlsRecovered:      "🟢 <b>TLS-сертификат снова в порядке</b>",
	updateFailed:      "🔴 <b>Ошибка обновления Mikan</b>",
	nodeUpdateFailed:  "🔴 <b>Ошибка обновления ноды</b> ",
	nodeUpdateRollout: ". Обновление остальных нод остановлено; ноду можно обновить снова на странице «Ноды».",

	torrentTitle:  "🧲 <b>Пойман торрент</b>",
	torrentBanned: "бан до %s UTC",
	torrentMore:   "и ещё %d",

	publicTitle:      "🌐 <b>Состояние серверов</b>",
	publicNone:       "Публичные серверы пока не настроены.",
	levelChecking:    "Проверяем",
	levelWorking:     "Работает",
	levelIssues:      "Есть проблемы",
	levelUnavailable: "Недоступен",
}

var enTexts = texts{
	stateUnavailable: "Unavailable",
	stateRecovered:   "Recovered",
	stateDegraded:    "Degraded",
	relayLabel:       "cascade relay",

	warpReason:       "Reason: ",
	warpTimeout:      "no answer from the WARP endpoint %s over UDP: the host may block UDP, try another endpoint",
	warpHTTPSTimeout: "the tunnel is up, but cloudflare.com did not answer in time",
	warpDNS:          "WARP could not resolve a name",
	warpTLS:          "TLS error through WARP",
	warpRefused:      "the connection was refused",
	warpNotLoaded:    "WARP is not loaded on the node yet",
	warpBadAnswer:    "Cloudflare answered something unexpected",
	warpCheckFailed:  "check failed",

	inboundMoved:      "🛠 <b>Inbound moved</b>: %s / %s from port %s to %s, the port is held by another program",
	autotuneChanged:   "Autotune changed an inbound",
	autotuneStuck:     "⚠️ <b>Autotune could not restore an inbound</b>: %s / %s (%s)",
	tlsBad:            "🟡 <b>TLS certificate problem</b>",
	tlsRecovered:      "🟢 <b>TLS certificate is healthy again</b>",
	updateFailed:      "🔴 <b>Mikan update failed</b>",
	nodeUpdateFailed:  "🔴 <b>Node update failed</b> ",
	nodeUpdateRollout: ". The rollout to the other nodes is stopped; the node can be updated again from the Nodes page.",

	torrentTitle:  "🧲 <b>Torrent caught</b>",
	torrentBanned: "banned until %s UTC",
	torrentMore:   "and %d more",

	publicTitle:      "🌐 <b>Server status</b>",
	publicNone:       "No servers are published yet.",
	levelChecking:    "Checking",
	levelWorking:     "Working",
	levelIssues:      "Issues",
	levelUnavailable: "Unavailable",
}

// textsFor is the texts of lang: English for "en", Russian for anything else, as the alerts
// have always been.
func textsFor(lang string) *texts {
	if lang == "en" {
		return &enTexts
	}
	return &ruTexts
}
