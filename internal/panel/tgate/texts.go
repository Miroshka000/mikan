package tgate

// The gateway's own words, in the bot's language: to the admin about its chat and the
// token, and the reason a Stars payment is refused before it is made. The bot's screens
// have theirs in tgbot.
type words struct {
	adminLinked, adminLinkFailed string
	// tokenConflict goes to the admin: another program takes this bot's updates.
	tokenConflict string
	payStale      string
}

var ru = words{
	adminLinked:     "✅ Чат подключён. Сюда будут приходить личные уведомления о состоянии инфраструктуры.",
	adminLinkFailed: "Не удалось подключить чат администратора. Создайте новую ссылку в панели.",
	tokenConflict:   "⚠️ Бот не получает сообщения: этот токен уже использует другая программа или сервер (старая панель, второй бот). Отправка работает, а нажатия и команды до бота не доходят.\n\nОстановите ту программу или выпустите новый токен в @BotFather (/revoke) и вставьте его в панели: Telegram → Подключение.",
	payStale:        "Счёт устарел. Откройте меню бота и оплатите заново.",
}

var en = words{
	adminLinked:     "✅ Admin chat connected. Infrastructure alerts will be sent here.",
	adminLinkFailed: "Could not connect the admin chat. Create a new link in the panel.",
	tokenConflict:   "⚠️ The bot gets no messages: another program or server uses this token (an old panel, a second bot). Sending works, but taps and commands do not reach the bot.\n\nStop that program, or make a new token in @BotFather (/revoke) and paste it in the panel: Telegram → Connection.",
	payStale:        "The invoice is out of date. Open the bot's menu and pay again.",
}

func wordsFor(lang string) *words {
	if lang == "en" {
		return &en
	}
	return &ru
}
