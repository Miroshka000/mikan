package tgbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/store/db"
)

// The referral programme. Callback data: f the screen, fh what it has earned so far.
//
// The invite link is a deep link with the account's own code in it,
// t.me/<bot>?start=ref_<code>. A Telegram account is bound to one referrer the first time
// it follows one and never leaves: referrals.referee_tg_id is unique, so one invitee cannot
// be counted twice however many links are followed.

// refPrefix marks a referral code in /start.
const refPrefix = "ref_"

// refListLimit is how many rewards the history screen lists.
const refListLimit = 10

// ReferralLink is the account's invite link, made with its code; "" while the bot is off.
func (b *Bot) ReferralLink(ctx context.Context, tgID int64) string {
	st := b.Status()
	if !st.Running || st.Bot.Username == "" || b.d.Referral == nil {
		return ""
	}
	code, err := b.d.Referral.Code(ctx, tgID)
	if err != nil || code == "" {
		b.d.Log.Warn("telegram: referral code", "tg", tgID, "err", err)
		return ""
	}
	return "https://t.me/" + st.Bot.Username + "?start=" + refPrefix + code
}

// Referral joins the chat to the account whose invite link it followed, and says what
// happened: an account that already belongs to somebody is left where it is.
func (b *Bot) Referral(ctx context.Context, w *words, chat int64, code string) string {
	if b.d.Referral == nil {
		return w.refOff
	}
	ref, err := b.d.Referral.Link(ctx, chat, code)
	switch {
	case errors.Is(err, referral.ErrOff):
		return w.refOff
	case errors.Is(err, referral.ErrSelf):
		return w.refSelf
	case errors.Is(err, referral.ErrBound):
		return fmt.Sprintf(w.refAlready, b.referrerName(ctx, ref.ReferrerTgID))
	case errors.Is(err, referral.ErrCode):
		return w.refCodeInvalid
	case err != nil:
		b.d.Log.Warn("telegram: referral", "tg", chat, "err", err)
		return w.refCodeInvalid
	}
	if name := b.referrerName(ctx, ref.ReferrerTgID); name != "" {
		return fmt.Sprintf(w.refJoined, html.EscapeString(name))
	}
	return w.refJoinedPlain
}

// referrerName is how the programme calls the account that invited this one.
func (b *Bot) referrerName(ctx context.Context, tgID int64) string {
	ch, err := b.d.Store.Q.GetTgChat(ctx, tgID)
	if err != nil {
		return ""
	}
	switch {
	case ch.Username != "":
		return "@" + ch.Username
	case strings.TrimSpace(ch.FirstName) != "":
		return html.EscapeString(ch.FirstName)
	}
	return ""
}

// referral is the screen: the account's own link, what it has brought and what it may
// spend. It opens with or without a subscription.
func (b *Bot) referral(ctx context.Context, w *words, chat int64, cmd string, notice string) (string, *Keyboard) {
	home := []Button{{Text: w.back, CallbackData: "m"}}
	title := func(s string) string {
		if notice == "" {
			return "<b>" + html.EscapeString(s) + "</b>"
		}
		return html.EscapeString(notice) + "\n\n<b>" + html.EscapeString(s) + "</b>"
	}
	if b.d.Referral == nil {
		return title(w.refTitle) + "\n\n" + html.EscapeString(w.refOff), &Keyboard{[][]Button{home}}
	}
	rcfg := b.d.Referral.Setup(ctx)
	if !rcfg.Enabled {
		return title(w.refTitle) + "\n\n" + html.EscapeString(w.refOff), &Keyboard{[][]Button{home}}
	}
	if cmd == "fh" {
		return b.refHistory(ctx, w, chat, title(w.refHistoryTitle), home)
	}
	link := b.ReferralLink(ctx, chat)
	if link == "" {
		return title(w.refTitle) + "\n\n" + html.EscapeString(w.refLinkUnavailable), &Keyboard{[][]Button{home}}
	}
	st, err := b.d.Referral.StatsOf(ctx, chat)
	if err != nil {
		b.d.Log.Warn("telegram: referral stats", "tg", chat, "err", err)
	}
	lines := []string{title(w.refTitle), "", html.EscapeString(w.refYourLink), "<code>" + html.EscapeString(link) + "</code>", "",
		html.EscapeString(fmt.Sprintf(w.refStats, st.Invited, st.Paid, w.rubles(st.Balance)))}
	if rcfg.Spend {
		lines = append(lines, html.EscapeString(w.refBalanceSpend))
	}
	if names := b.refRuleLines(ctx, w, rcfg, st); len(names) > 0 {
		lines = append(lines, "", "<b>"+html.EscapeString(w.refRulesTitle)+"</b>")
		for _, n := range names {
			lines = append(lines, "• "+html.EscapeString(n))
		}
	}
	share := "https://t.me/share/url?url=" + url.QueryEscape(link) + "&text=" + url.QueryEscape(b.refShareText(ctx, w))
	rows := [][]Button{{{Text: w.refShare, URL: share}}}
	if st.Paid > 0 {
		rows = append(rows, []Button{{Text: w.refHistory, CallbackData: "fh"}})
	}
	return strings.Join(lines, "\n"), &Keyboard{append(rows, home)}
}

// refHistory is what the account has earned, most recent first.
func (b *Bot) refHistory(ctx context.Context, w *words, chat int64, head string, home []Button) (string, *Keyboard) {
	rows, err := b.d.Store.Q.ListReferralRewardsOf(ctx, db.ListReferralRewardsOfParams{ReferrerTgID: chat, Lim: refListLimit})
	if err != nil {
		b.d.Log.Warn("telegram: referral history", "tg", chat, "err", err)
		return head + "\n\n" + html.EscapeString(w.refHistoryNone), &Keyboard{[][]Button{home}}
	}
	if len(rows) == 0 {
		return head + "\n\n" + html.EscapeString(w.refHistoryNone), &Keyboard{[][]Button{home}}
	}
	lines := []string{head, ""}
	for _, r := range rows {
		what := w.rubles(r.Amount)
		if r.Kind == referral.KindDays {
			what = fmt.Sprintf(w.refDays, r.Days)
		}
		lines = append(lines, "• "+html.EscapeString(what)+" — "+html.EscapeString(w.shortDate(time.Unix(r.CreatedAt, 0).UTC())))
	}
	return strings.Join(lines, "\n"), &Keyboard{[][]Button{home}}
}

// refRuleLines says what the programme pays, each rule with the tariffs it counts.
func (b *Bot) refRuleLines(ctx context.Context, w *words, cfg referral.Program, st referral.Stats) []string {
	out := make([]string, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if !r.Enabled {
			continue
		}
		text, ok := b.refRuleText(ctx, w, r)
		if !ok {
			continue
		}
		// A rule that pays on every N-th payment shows how close the next one is.
		if r.Every > 1 && st.Progress != nil {
			if n := st.Progress[r.ID]; n > 0 && n%r.Every != 0 {
				text = fmt.Sprintf(w.refRuleProgress, text, n%r.Every, r.Every)
			}
		}
		out = append(out, text)
	}
	return out
}

func (b *Bot) refRuleText(ctx context.Context, w *words, r referral.Rule) (string, bool) {
	name := strings.TrimSpace(r.Name)
	for _, id := range r.TariffIDs {
		t, err := b.d.Store.Q.GetTariff(ctx, id)
		if err != nil {
			return "", false
		}
		if name == "" {
			name = t.Name
		} else {
			name += " · " + t.Name
		}
	}
	if name == "" {
		name = w.refAnyTariff
	}
	switch r.Kind {
	case referral.KindPercent:
		if r.Every > 1 {
			return fmt.Sprintf(w.refRulePercentEvery, name, r.Value, r.Every), true
		}
		return fmt.Sprintf(w.refRulePercent, name, r.Value), true
	case referral.KindDays:
		if r.Every > 1 {
			return fmt.Sprintf(w.refRuleDaysEvery, name, r.Value, r.Every), true
		}
		return fmt.Sprintf(w.refRuleDaysEach, name, r.Value), true
	}
	return "", false
}

func (b *Bot) refShareText(ctx context.Context, w *words) string {
	return fmt.Sprintf(w.refShareText, b.brand(ctx))
}

// rubles is an amount in kopecks the way the bot writes money.
func (w *words) rubles(kopecks int64) string { return w.price(kopecks, "RUB") }

// Reward is what an invitee's payment gave a referrer, told to them by referral.Service.
func (b *Bot) Reward(ctx context.Context, r referral.Reward) {
	out := b.out.Load()
	if out == nil || r.Referrer == r.Referee {
		return
	}
	w := wordsFor(b.lang(ctx))
	var text string
	switch {
	case r.Days > 0 && r.EveryN > 1:
		text = fmt.Sprintf(w.refEarnedDaysEvery, r.EveryN, w.days(int(r.Days)))
	case r.Days > 0:
		text = fmt.Sprintf(w.refEarnedDays, w.days(int(r.Days)))
	default:
		text = fmt.Sprintf(w.refEarnedRub, w.rubles(r.Rub))
	}
	chat := r.Referrer
	out.Notice(chat, func(ctx context.Context, c *Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}, nil)
}
