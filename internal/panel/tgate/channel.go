package tgate

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The channel a subscriber must join before the bot shows its menu, and the one the free
// plan may ask for (billing.FreeChannel). The gateway checks who is in it; the bot's
// screens decide what to show.

// Channel is the bot's channel and whether joining it is required. The bot checks members
// with getChatMember, which in a channel only its admins may call.
type Channel struct {
	Required bool   `json:"required" doc:"Обязательная подписка: без неё бот показывает вместо меню экран с кнопкой канала"`
	Chat     string `json:"chat" maxLength:"64" doc:"Публичный @username канала или его числовой ID (-100…); бот должен быть администратором канала"`
	URL      string `json:"url,omitempty" maxLength:"256" doc:"Ссылка, по которой вступают (https://t.me/…); пусто: t.me/<username>. Для канала по числовому ID обязательна"`
}

// Link is where a subscriber joins the channel: the admin's link, or the public channel's
// own; "" when there is neither.
func (ch Channel) Link() string {
	if ch.URL != "" {
		return ch.URL
	}
	if name, ok := strings.CutPrefix(ch.Chat, "@"); ok {
		return "https://t.me/" + name
	}
	return ""
}

// Gate: the channel is required and set up. A switch without a channel locks nobody out.
func (ch Channel) Gate() bool { return ch.Required && ch.Chat != "" }

// channelChat is a channel as Bot API methods take it: a public @username (5 to 32 letters,
// digits and underscores, as Telegram allows) or a numeric chat id.
var channelChat = regexp.MustCompile(`^(@[A-Za-z0-9_]{5,32}|-?[0-9]{1,20})$`)

// Validation codes (the UI translates tg_<code>).
var (
	ErrChannel    = errors.New("tg_channel")
	ErrChannelURL = errors.New("tg_channel_url")
)

// Validate trims the channel and checks it can be asked about and joined: a channel by
// its numeric id has no public link, so the admin's link is required for it.
func (ch *Channel) Validate() error {
	ch.Chat, ch.URL = strings.TrimSpace(ch.Chat), strings.TrimSpace(ch.URL)
	switch {
	case ch.Chat == "" && ch.Required:
		return ErrChannel
	case ch.Chat != "" && !channelChat.MatchString(ch.Chat):
		return ErrChannel
	case ch.URL != "" && !SafeURL(ch.URL):
		return ErrChannelURL
	case ch.Chat != "" && ch.Link() == "":
		return ErrChannelURL
	}
	return nil
}

// SafeURL: links the bot may show — web pages and Telegram links.
func SafeURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" && u.Host != "" || u.Scheme == "tg" && u.Host != "") && u.User == nil
}

// memberTTL: how long an account seen in the channel is not asked about again. Leaving the
// channel is noticed this much later at most; a tap does not wait on Telegram meanwhile.
const memberTTL = 5 * time.Minute

// probeTTL: how often the admin's page asks Telegram whether the bot is the channel's admin.
const probeTTL = time.Minute

// maxMembers bounds the accounts kept as seen in the channel; past it the stale go.
const maxMembers = 10000

// Member says whether tgID is in chat, asking Telegram unless the account was seen there
// in the last memberTTL (fresh: ask anyway). An error when the bot cannot ask: it is off,
// it is not the channel's admin, Telegram does not answer.
func (g *Gateway) Member(ctx context.Context, chat string, tgID int64, fresh bool) (bool, error) {
	now := time.Now()
	g.chanMu.Lock()
	if g.chanChat != chat {
		g.chanChat, g.members, g.chanErr, g.probedAt = chat, map[int64]time.Time{}, "", time.Time{}
	}
	until, seen := g.members[tgID]
	g.chanMu.Unlock()
	if seen && !fresh && now.Before(until) {
		return true, nil
	}
	c := g.client.Load()
	if c == nil {
		return false, ErrOff
	}
	m, err := c.ChatMember(ctx, chat, tgID)
	if err != nil && unknownMember(err) {
		// An account Telegram does not know in the chat at all is not in it.
		m, err = ChatMember{Status: "left"}, nil
	}
	g.chanMu.Lock()
	defer g.chanMu.Unlock()
	if g.chanChat != chat {
		return m.In(), err // the channel changed meanwhile: nothing to keep
	}
	if err != nil {
		code := channelErrCode(err)
		if code != g.chanErr {
			g.d.Log.Warn("telegram: the channel's members cannot be checked: the gate lets everybody in", "channel", chat, "err", errText(err))
		}
		g.chanErr = code
		return false, err
	}
	g.chanErr = ""
	if !m.In() {
		delete(g.members, tgID)
		return false, nil
	}
	if len(g.members) >= maxMembers {
		for id, t := range g.members {
			if now.After(t) {
				delete(g.members, id)
			}
		}
	}
	g.members[tgID] = now.Add(memberTTL)
	return true, nil
}

// unknownMember: Telegram has no such account in the chat, which is no fault of the bot.
func unknownMember(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 400 {
		return false
	}
	d := strings.ToLower(ae.Description)
	return strings.Contains(d, "user not found") || strings.Contains(d, "participant_id_invalid")
}

// channelErrCode names why the channel could not be asked, for the admin's page:
// channel_not_admin, channel_not_found, unreachable or Telegram's own words.
func channelErrCode(err error) string {
	var ae *APIError
	if !errors.As(err, &ae) {
		if errors.Is(err, ErrOff) {
			return ""
		}
		return "unreachable"
	}
	d := strings.ToLower(ae.Description)
	switch {
	case strings.Contains(d, "chat not found"):
		return "channel_not_found"
	case ae.Code == 403, strings.Contains(d, "inaccessible"), strings.Contains(d, "not enough rights"),
		strings.Contains(d, "administrator"), strings.Contains(d, "not a member"):
		return "channel_not_admin"
	}
	return ae.Description
}

// ChannelConfig is the bot's channel as the admin set it up.
func (g *Gateway) ChannelConfig(ctx context.Context) Channel {
	return g.view(ctx, g.d.Settings).Channel
}

// Channel implements billing.Telegram: the link to join the bot's channel.
func (g *Gateway) Channel(ctx context.Context) (string, bool) {
	link := g.ChannelConfig(ctx).Link()
	return link, link != ""
}

// Subscribed implements billing.Telegram: tgID is in the bot's channel, asked as the gate
// asks (an account seen there lately is not asked about again).
func (g *Gateway) Subscribed(ctx context.Context, tgID int64) (bool, error) {
	chat := g.ChannelConfig(ctx).Chat
	if chat == "" {
		return false, ErrOff
	}
	return g.Member(ctx, chat, tgID, false)
}

// ChannelError is why the bot cannot check its channel's members, for the admin's page:
// channel_not_admin, channel_not_found, unreachable or Telegram's words; "" when it can,
// no channel is set up, or the bot is off. The bot asks about itself there at most once
// a probeTTL (and again after a save), and a failed check of a subscriber counts too.
func (g *Gateway) ChannelError(ctx context.Context) string {
	chat := g.ChannelConfig(ctx).Chat
	c, st := g.client.Load(), g.Status()
	if chat == "" || c == nil || st.Bot.ID == 0 {
		return ""
	}
	g.chanMu.Lock()
	if g.chanChat == chat && time.Since(g.probedAt) < probeTTL {
		code := g.chanErr
		g.chanMu.Unlock()
		return code
	}
	g.chanMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m, err := c.ChatMember(ctx, chat, st.Bot.ID)
	code := ""
	switch {
	case err != nil:
		code = channelErrCode(err)
	case m.Status != "administrator" && m.Status != "creator":
		code = "channel_not_admin"
	}
	g.chanMu.Lock()
	defer g.chanMu.Unlock()
	if g.chanChat != chat {
		g.chanChat, g.members = chat, map[int64]time.Time{}
	}
	g.chanErr, g.probedAt = code, time.Now()
	return code
}
