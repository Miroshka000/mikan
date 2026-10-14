package tgbot

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strings"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/tgate"
)

// The referral programme: the account's own invite link, what it has brought, and what an
// invitee's payment gives the account that invited them. Callback data: f the screen, fh
// what it has earned so far.
//
// The invite link is a deep link with the account's own code in it,
// t.me/<bot>?start=ref_<code>. A Telegram account is bound to one referrer the first time
// it follows one and never leaves: referrals.referee_tg_id is unique, so one invitee cannot
// be counted twice however many links are followed.
//
// The programme is the panel's (referral.Service) and the screens reach it through Backend:
// in the panel's own process, or over the Panel API when the bot runs as an addon.

// refPrefix marks a referral code in /start.
const refPrefix = "ref_"

// ReferralLink is the account's invite link, made with its own code; "" while the bot is
// off, the programme is off, or the account has no code yet.
func (b *Bot) ReferralLink(ctx context.Context, tgID int64) string {
	username := b.d.Backend.BotUsername(ctx)
	if username == "" {
		return ""
	}
	v, err := b.d.Backend.Referral(ctx, tgID)
	if err != nil {
		b.d.Log.Warn("telegram: referral link", "tg", tgID, "err", err)
		return ""
	}
	if !v.Enabled || v.Code == "" {
		return ""
	}
	return "https://t.me/" + username + "?start=" + refPrefix + v.Code
}

// Referral joins the chat to the account whose invite link it followed, and says what
// happened: an account that already belongs to somebody is left where it is.
func (b *Bot) Referral(ctx context.Context, w *words, chat int64, code string) string {
	j, err := b.d.Backend.ReferralJoin(ctx, chat, code)
	if err != nil {
		b.d.Log.Warn("telegram: referral", "tg", chat, "err", err)
		return w.refCodeInvalid
	}
	switch j.Status {
	case ReferralOff:
		return w.refOff
	case ReferralSelf:
		return w.refSelf
	case ReferralBound:
		// The account belongs to somebody else; the programme's words name them when it can.
		return fmt.Sprintf(w.refAlready, html.EscapeString(j.ReferrerName))
	case ReferralInvalid:
		return w.refCodeInvalid
	}
	if j.ReferrerName != "" {
		return fmt.Sprintf(w.refJoined, html.EscapeString(j.ReferrerName))
	}
	return w.refJoinedPlain
}

// CampaignVisit binds the chat to the campaign whose tagged link it followed, so the panel
// counts where it came from. A chat is counted once, on the first tagged link it opens; the
// newcomer is greeted like anybody else, with a short line saying the link was followed.
func (b *Bot) CampaignVisit(ctx context.Context, w *words, chat int64, tag string) string {
	g, err := b.d.Backend.CampaignVisit(ctx, chat, tag)
	if err != nil {
		b.d.Log.Warn("telegram: campaign visit", "tg", chat, "tag", tag, "err", err)
		return ""
	}
	if !g.Counted {
		return ""
	}
	if g.Name != "" {
		return fmt.Sprintf(w.campThanksNamed, html.EscapeString(g.Name))
	}
	return w.campThanks
}

// referral is the screen: the account's own link, what it has brought and what it may
// spend. It opens with or without a subscription.
func (b *Bot) referral(ctx context.Context, w *words, chat int64, cmd string, notice string) (string, *tgate.Keyboard) {
	home := []tgate.Button{{Text: w.back, CallbackData: "m"}}
	title := func(s string) string {
		if notice == "" {
			return "<b>" + html.EscapeString(s) + "</b>"
		}
		return html.EscapeString(notice) + "\n\n<b>" + html.EscapeString(s) + "</b>"
	}
	v, err := b.d.Backend.Referral(ctx, chat)
	if err != nil {
		b.d.Log.Warn("telegram: referral", "tg", chat, "err", err)
	}
	if !v.Enabled {
		return title(w.refTitle) + "\n\n" + html.EscapeString(w.refOff), &tgate.Keyboard{InlineKeyboard: [][]tgate.Button{home}}
	}
	if cmd == "fh" {
		return refHistory(w, v, title(w.refHistoryTitle), home)
	}
	link := b.ReferralLink(ctx, chat)
	if link == "" {
		return title(w.refTitle) + "\n\n" + html.EscapeString(w.refLinkUnavailable), &tgate.Keyboard{InlineKeyboard: [][]tgate.Button{home}}
	}
	lines := []string{title(w.refTitle), "", html.EscapeString(w.refYourLink), "<code>" + html.EscapeString(link) + "</code>", "",
		html.EscapeString(fmt.Sprintf(w.refStats, v.Invited, v.Paid, w.rubles(v.Balance)))}
	if v.Spend {
		lines = append(lines, html.EscapeString(w.refBalanceSpend))
	}
	if names := refRuleLines(w, v); len(names) > 0 {
		lines = append(lines, "", "<b>"+html.EscapeString(w.refRulesTitle)+"</b>")
		for _, n := range names {
			lines = append(lines, "• "+html.EscapeString(n))
		}
	}
	share := "https://t.me/share/url?url=" + url.QueryEscape(link) + "&text=" + url.QueryEscape(refShareText(ctx, w, b))
	rows := [][]tgate.Button{{{Text: w.refShare, URL: share}}}
	if v.Paid > 0 {
		rows = append(rows, []tgate.Button{{Text: w.refHistory, CallbackData: "fh"}})
	}
	return strings.Join(lines, "\n"), &tgate.Keyboard{InlineKeyboard: append(rows, home)}
}

// refHistory is what the account has earned, most recent first.
func refHistory(w *words, v ReferralView, head string, home []tgate.Button) (string, *tgate.Keyboard) {
	if len(v.Rewards) == 0 {
		return head + "\n\n" + html.EscapeString(w.refHistoryNone), &tgate.Keyboard{InlineKeyboard: [][]tgate.Button{home}}
	}
	lines := []string{head, ""}
	for _, r := range v.Rewards {
		what := w.rubles(r.Rub)
		if r.Kind == referral.KindDays {
			what = fmt.Sprintf(w.refDays, r.Days)
		}
		lines = append(lines, "• "+html.EscapeString(what)+" — "+html.EscapeString(w.shortDate(r.At)))
	}
	return strings.Join(lines, "\n"), &tgate.Keyboard{InlineKeyboard: [][]tgate.Button{home}}
}

// refRuleLines says what the programme pays, each rule with the tariffs it counts.
func refRuleLines(w *words, v ReferralView) []string {
	out := make([]string, 0, len(v.Rules))
	for _, r := range v.Rules {
		name := strings.Join(r.Names, " · ")
		if name == "" {
			name = w.refAnyTariff
		}
		var text string
		switch r.Kind {
		case referral.KindPercent:
			if r.Every > 1 {
				text = fmt.Sprintf(w.refRulePercentEvery, name, r.Value, r.Every)
			} else {
				text = fmt.Sprintf(w.refRulePercent, name, r.Value)
			}
		case referral.KindDays:
			if r.Every > 1 {
				text = fmt.Sprintf(w.refRuleDaysEvery, name, r.Value, r.Every)
			} else {
				text = fmt.Sprintf(w.refRuleDaysEach, name, r.Value)
			}
		default:
			continue
		}
		// A rule that pays on every N-th payment shows how close the next one is.
		if r.Every > 1 && r.Count > 0 && r.Count%r.Every != 0 {
			text = fmt.Sprintf(w.refRuleProgress, text, r.Count%r.Every, r.Every)
		}
		out = append(out, text)
	}
	return out
}

// refShareText is what the share button puts in the message: the panel's brand and the
// account's link, filled in by the words.
func refShareText(ctx context.Context, w *words, b *Bot) string {
	return fmt.Sprintf(w.refShareText, b.brand(ctx))
}

// rubles is an amount in kopecks the way the bot writes money.
func (w *words) rubles(kopecks int64) string { return w.price(kopecks, "RUB") }
