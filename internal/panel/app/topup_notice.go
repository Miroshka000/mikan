package app

import (
	"context"
	"fmt"
	"html"
	"strconv"

	"mikan/internal/panel/store/db"
)

// tellAdminOfTopup shows the admin, in the chat connected for the panel's alerts, a top-up
// that waits for them: the screenshot, who and how much. The decision is in the panel
// (Payments → Top-ups). The group and Discord of the alerts get the text through the alerts'
// queue. Where none of them is set nobody is told; the page has it anyway.
func (p *Panel) tellAdminOfTopup(ctx context.Context, t db.Topup) {
	g := p.Gateway
	if g == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	g.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, supportNoticeTimeout)
		defer cancel()
		who := "tg " + strconv.FormatInt(t.TgID, 10)
		if c, err := p.st.Q.GetTgChat(ctx, t.TgID); err == nil && c.Username != "" {
			who = "@" + c.Username
		}
		amount := fmt.Sprintf("%d.%02d %s", t.Amount/100, t.Amount%100, t.Currency)
		text := fmt.Sprintf("💰 <b>Top-up waits</b>\n%s · %s\nDecide it: Payments, Top-ups.", html.EscapeString(who), amount)
		if g.Lang(ctx) != "en" {
			text = fmt.Sprintf("💰 <b>Заявка на пополнение</b>\n%s · %s\nРешить: Платежи, Пополнения.", html.EscapeString(who), amount)
		}
		// The screenshot is the bot's file: the infrastructure bot is another, so it may not
		// be able to send it; the text goes alone then, through the alerts' queue, which tries
		// again while Telegram does not answer.
		sent := false
		if g.InfrastructureEnabled(ctx) {
			if chat, ok, err := g.InfrastructureAdminChat(ctx); err == nil && ok {
				if client, err := g.InfrastructureClient(ctx); err == nil {
					_, err = client.SendPhoto(ctx, chat, t.Photo, text)
					sent = err == nil
				}
			}
		}
		if p.Alerts == nil {
			return
		}
		key := "topup/" + strconv.FormatInt(t.ID, 10)
		if sent {
			p.Alerts.NotifyElsewhere(ctx, key, text)
		} else {
			p.Alerts.Notify(ctx, key, text)
		}
	})
}
