package app

import (
	"context"
	"fmt"
	"html"
	"strconv"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

// addressMoved follows the panel's own change of its server's address (selfaddr): it is
// written to the journal, followed at once (certificates, nodes, the bot's button), and the
// admin is told, in the chat of the panel's alerts.
func (p *Panel) addressMoved(ctx context.Context, from, to string) {
	if err := audit.Write(ctx, p.st.Q, p.now(), audit.Entry{Action: "auto.public_host", TargetType: "settings", TargetID: settings.KeyPublicHost,
		Details: map[string]string{"from": from, "to": to}}); err != nil {
		p.log.Warn("address: not written to the journal", "err", err)
	}
	if err := p.address.Check(ctx); err != nil {
		p.log.Warn("address: not followed", "err", err)
	}
	if p.Gateway == nil || p.Alerts == nil {
		return
	}
	domain, err := p.Settings.String(ctx, settings.KeyDomain)
	if err != nil {
		domain = ""
	}
	p.Alerts.Notify(ctx, "address/"+to, addressNoticeText(p.Gateway.Lang(ctx), from, to, domain != ""))
}

// addressNoticeText is the message in Telegram HTML: what the address was and is, and what
// it means for the links handed out.
func addressNoticeText(lang, from, to string, domain bool) string {
	from, to = html.EscapeString(from), html.EscapeString(to)
	if !textutil.Lang(lang).En() {
		text := fmt.Sprintf("📍 <b>Адрес сервера сменился</b>\nБыло: <code>%s</code>\nСтало: <code>%s</code>\nПанель проверила новый адрес и сохранила его сама.", from, to)
		if domain {
			return text + "\nСсылки подписок идут на домен и работают как прежде."
		}
		return text + "\nУ панели нет домена: ссылки подписок у клиентов ведут на старый адрес, им нужно добавить подписку заново. С доменом в следующий раз всё переедет само."
	}
	text := fmt.Sprintf("📍 <b>The server's address changed</b>\nWas: <code>%s</code>\nNow: <code>%s</code>\nThe panel checked the new address and saved it itself.", from, to)
	if domain {
		return text + "\nSubscription links go to the domain and work as before."
	}
	return text + "\nThe panel has no domain: the clients' subscription links lead to the old address, they need to add the subscription again. With a domain, everything moves by itself next time."
}

// nodeMoved tells the admin the panel moved a remote node to the address its hello came
// from (readdressNode).
func (p *Panel) nodeMoved(ctx context.Context, n db.Node, from, to string) {
	if p.Gateway == nil || p.Alerts == nil {
		return
	}
	p.Alerts.Notify(ctx, "node-address/"+strconv.FormatInt(n.ID, 10)+"/"+to, nodeMovedText(p.Gateway.Lang(ctx), n.Name, from, to, n.Domain != ""))
}

// nodeMovedText is the message in Telegram HTML.
func nodeMovedText(lang, name, from, to string, domain bool) string {
	name, from, to = html.EscapeString(name), html.EscapeString(from), html.EscapeString(to)
	if !textutil.Lang(lang).En() {
		text := fmt.Sprintf("📍 <b>Нода %s сменила адрес</b>\nБыло: <code>%s</code>\nСтало: <code>%s</code>\nНода сама сообщила новый адрес, панель достучалась до неё там и сохранила его.", name, from, to)
		if domain {
			return text + "\nКлиенты подключаются по домену ноды и ничего не заметят."
		}
		return text + "\nКлиенты получат новый адрес со следующим обновлением подписки."
	}
	text := fmt.Sprintf("📍 <b>Node %s changed its address</b>\nWas: <code>%s</code>\nNow: <code>%s</code>\nThe node told its new address itself, the panel reached it there and saved it.", name, from, to)
	if domain {
		return text + "\nClients connect by the node's domain and notice nothing."
	}
	return text + "\nClients get the new address with their next subscription update."
}
