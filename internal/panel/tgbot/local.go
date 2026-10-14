package tgbot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// Local is the Backend inside the panel's process: its store, settings, users, devices and
// billing. It is the only part of the bot that touches them; the bot addon reaches it over
// the Panel API (RPCHandler), and the panel's side of the bot (Panel) calls it directly.
type Local struct {
	Store    *store.Store
	Settings *settings.Settings
	// Binder unbinds devices at their subscriber's request.
	Binder *domain.Devices
	// Users reissues a subscription's link at its subscriber's request.
	Users *domain.Users
	// Billing sells tariffs in the menu.
	Billing *billing.Service
	// Gateway reads the link codes it signed and asks who is in the channel.
	Gateway *tgate.Gateway
	// SubBase is https://host:port/<sub path>, "" while the panel has no public address.
	SubBase func(ctx context.Context) string
	// MiniApp: the panel's certificate is one Telegram apps accept (not self-signed).
	MiniApp func() bool
	// ReferralProgram is the referral programme the screens show (the method Referral
	// reads it); nil: none.
	ReferralProgram *referral.Service
	// Campaigns are the ad campaign tags a tagged /start counts; nil: none.
	Campaigns *campaigns.Service
	Now       func() time.Time
}

// referralHistory is how many rewards the history screen lists.
const referralHistory = 10

func (l *Local) Config(ctx context.Context) Config {
	// A setup saved before an option existed gets that option's default. A saved menu
	// replaces the default one whole.
	cfg, lang, saved := tgate.ReadSetup(ctx, l.Settings, func(lang string) Config {
		def := Default(lang)
		def.Buttons = nil
		return def
	})
	if !saved {
		return Default(lang)
	}
	_ = cfg.Validate()
	return cfg
}

// Member is the channel check, for the bot's own channel only: the screens cannot probe
// other chats or reset what the gateway knows about members.
func (l *Local) Member(ctx context.Context, chat string, tgID int64, fresh bool) (bool, error) {
	if ch := l.Gateway.ChannelConfig(ctx).Chat; ch == "" || chat != ch {
		return false, ErrNotChannel
	}
	return l.Gateway.Member(ctx, chat, tgID, fresh)
}

func (l *Local) Brand(ctx context.Context) string {
	s, _ := l.Settings.String(ctx, settings.KeyBrand)
	return s
}

func (l *Local) SupportURL(ctx context.Context) string {
	s, _ := l.Settings.String(ctx, settings.KeySupportURL)
	return s
}

func (l *Local) SubURL(ctx context.Context, u db.User) string {
	if base := l.SubBase(ctx); base != "" {
		return base + "/" + u.SubToken
	}
	return ""
}

func (l *Local) MiniAppURL(ctx context.Context) string {
	base := l.SubBase(ctx)
	if base == "" || l.MiniApp == nil || !l.MiniApp() {
		return ""
	}
	return base + "/tg"
}

func (l *Local) Context(ctx context.Context, tgID int64) (Account, error) {
	list, err := l.Store.Q.ListTgLinksOf(ctx, tgID)
	if err != nil {
		return Account{}, err
	}
	acc := Account{Links: list}
	if ch, err := l.Store.Q.GetTgChat(ctx, tgID); err == nil {
		acc.Chat, acc.HasChat = ch, true
	}
	return acc, nil
}

func (l *Local) Chat(ctx context.Context, tgID int64) (db.TgChat, error) {
	return l.Store.Q.GetTgChat(ctx, tgID)
}

func (l *Local) SetMenu(ctx context.Context, tgID, msgID int64) error {
	return l.Store.Q.SetTgMenu(ctx, db.SetTgMenuParams{MenuMsgID: msgID, TgID: tgID})
}

func (l *Local) SetCurrent(ctx context.Context, tgID, userID int64) error {
	return l.Store.Q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: userID, TgID: tgID})
}

func (l *Local) UserOfCode(ctx context.Context, code string) (int64, error) {
	return l.Gateway.UserOfCode(ctx, code)
}

// UserOfAddress: the own token is a path segment of 24 letters and digits. An old panel's
// address is taken as the panel serves it (panelimport.LinkToken), and only while old
// links are set up; the signature of a token Marzban or PasarGuard signed is checked as
// there (Verifier.User).
func (l *Local) UserOfAddress(ctx context.Context, urlPath string) (int64, bool) {
	for seg := range strings.SplitSeq(urlPath, "/") {
		if !nativeToken.MatchString(seg) {
			continue
		}
		if u, err := l.Store.Q.GetUserBySubToken(ctx, seg); err == nil {
			return u.ID, true
		}
	}
	legacyPath, verifier, err := panelimport.LoadLegacy(ctx, l.Settings)
	if err != nil {
		return 0, false
	}
	token, _, ok := panelimport.LinkToken(urlPath, legacyPath)
	if !ok {
		return 0, false
	}
	if u, ok := verifier.User(ctx, l.Store.Q, token); ok {
		return u.ID, true
	}
	return 0, false
}

// Link: a subscription has one owner. An owner who cannot be asked, as they blocked the
// bot, is not in the way.
func (l *Local) Link(ctx context.Context, tgID, userID int64) (Linked, error) {
	u, err := l.Store.Q.GetUser(ctx, userID)
	if err != nil {
		return Linked{}, err
	}
	prev, err := l.Store.Q.GetTgLink(ctx, userID)
	switch {
	case err == nil && prev.TgID == tgID:
		_ = l.SetCurrent(ctx, tgID, userID)
		return Linked{Status: LinkAlready, User: u}, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return Linked{}, err
	}
	if n, err := l.Store.Q.CountTgLinksOf(ctx, tgID); err != nil || n >= tgate.MaxLinks {
		return Linked{Status: LinkLimit, User: u}, nil
	}
	if prev.TgID != 0 && l.reachable(ctx, prev.TgID) {
		return Linked{Status: LinkTaken, User: u, Owner: prev.TgID}, nil
	}
	if err := l.Store.Q.LinkTg(ctx, db.LinkTgParams{UserID: userID, TgID: tgID, CreatedAt: l.Now().Unix()}); err != nil {
		return Linked{}, err
	}
	_ = l.SetCurrent(ctx, tgID, userID)
	return Linked{Status: LinkDone, User: u}, nil
}

// reachable: the account has a chat with the bot and has not blocked it, so it can be asked.
func (l *Local) reachable(ctx context.Context, tgID int64) bool {
	ch, err := l.Store.Q.GetTgChat(ctx, tgID)
	return err == nil && ch.Blocked == 0
}

func (l *Local) Owner(ctx context.Context, userID int64) (int64, error) {
	link, err := l.Store.Q.GetTgLink(ctx, userID)
	return link.TgID, err
}

func (l *Local) Transfer(ctx context.Context, userID, to int64) error {
	if n, err := l.Store.Q.CountTgLinksOf(ctx, to); err != nil || n >= tgate.MaxLinks {
		return ErrLinkLimit
	}
	if err := l.Store.Q.LinkTg(ctx, db.LinkTgParams{UserID: userID, TgID: to, CreatedAt: l.Now().Unix()}); err != nil {
		return err
	}
	_ = l.SetCurrent(ctx, to, userID)
	return nil
}

func (l *Local) Unlink(ctx context.Context, userID int64) error {
	return l.Store.Q.UnlinkTg(ctx, userID)
}

func (l *Local) User(ctx context.Context, id int64) (db.User, error) {
	return l.Store.Q.GetUser(ctx, id)
}

func (l *Local) GrantsLeft(ctx context.Context, userID int64, now time.Time) (domain.GrantsLeft, error) {
	return domain.UserGrantsLeft(ctx, l.Store.Q, userID, now)
}

func (l *Local) Pools(ctx context.Context, userID int64, now time.Time) ([]Pool, error) {
	rows, err := l.Store.Q.ListUserPools(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	pools, err := l.Store.Q.ListTrafficPools(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := domain.UserGrantsLeft(ctx, l.Store.Q, userID, now)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, p := range pools {
		names[p.ID] = p.Name
	}
	var out []Pool
	for _, r := range rows {
		if !r.TrafficLimit.Valid {
			continue
		}
		extra := grants.Pool(userID, r.PoolID)
		out = append(out, Pool{Name: names[r.PoolID], Used: r.UsedUp + r.UsedDown, Limit: r.TrafficLimit.Int64, Extra: extra,
			Exhausted: domain.PoolExhausted(r, extra)})
	}
	return out, nil
}

func (l *Local) DeviceBinding(ctx context.Context) bool {
	on, _ := l.Settings.On(ctx, settings.DeviceBinding)
	return on
}

func (l *Local) DeviceCount(ctx context.Context, userID int64) (int64, error) {
	return l.Store.Q.CountBoundDevices(ctx, userID)
}

func (l *Local) Devices(ctx context.Context, userID int64) ([]db.BoundDevice, error) {
	return l.Store.Q.ListBoundDevices(ctx, userID)
}

func (l *Local) DeviceTraffic(ctx context.Context, userID int64) (map[int64]int64, error) {
	rows, err := l.Store.Q.ListBoundDeviceTraffic(ctx, userID)
	if err != nil {
		return nil, err
	}
	used := map[int64]int64{}
	for _, r := range rows {
		used[r.DeviceID] = r.Up + r.Down
	}
	return used, nil
}

func (l *Local) UnbindState(ctx context.Context, userID int64) (domain.UnbindState, error) {
	return l.Binder.UnbindState(ctx, userID)
}

func (l *Local) Unbind(ctx context.Context, userID, deviceID int64) error {
	return l.Binder.Unbind(ctx, userID, deviceID, true)
}

func (l *Local) ReissueState(ctx context.Context, userID int64) (domain.ReissueState, error) {
	return l.Users.ReissueState(ctx, userID)
}

func (l *Local) Reissue(ctx context.Context, userID int64) error {
	_, err := l.Users.ReissueByOwner(ctx, userID, "bot")
	return err
}

func (l *Local) Notices(ctx context.Context, now time.Time) ([]db.ListNoticeSubscriptionsRow, error) {
	return l.Store.Q.ListNoticeSubscriptions(ctx, now.Unix())
}

func (l *Local) NoticeSent(ctx context.Context, userID int64, kind string, period int64) (bool, error) {
	return l.Store.Q.TgNoticeSent(ctx, db.TgNoticeSentParams{UserID: userID, Kind: kind, Period: period})
}

func (l *Local) NoticeDelivered(ctx context.Context, userID int64, kind string, period int64, at time.Time) error {
	_, err := l.Store.Q.AddTgNotice(ctx, db.AddTgNoticeParams{UserID: userID, Kind: kind, Period: period, SentAt: at.Unix()})
	return err
}

func (l *Local) PruneNotices(ctx context.Context, before time.Time) error {
	return l.Store.Q.PruneTgNotices(ctx, before.Unix())
}

func (l *Local) BroadcastTargets(ctx context.Context) ([]int64, error) {
	return l.Store.Q.BroadcastTargets(ctx)
}

func (l *Local) Shop() Shop { return l.Billing }

func (l *Local) Tariff(ctx context.Context, id int64) (db.Tariff, error) {
	return l.Store.Q.GetTariff(ctx, id)
}

// ReferralOn: the programme is the admin's to turn on.
func (l *Local) ReferralOn(ctx context.Context) bool {
	return l.ReferralProgram != nil && l.ReferralProgram.Setup(ctx).Enabled
}

// BotUsername is the bot's own @name, from the gateway's last getMe; "" while it is off.
func (l *Local) BotUsername(ctx context.Context) string {
	if l.Gateway == nil {
		return ""
	}
	st := l.Gateway.Status()
	if !st.Running {
		return ""
	}
	return st.Bot.Username
}

// Referral is the programme as this account sees it: off when the admin has not turned it
// on, else its code, what it pays and what it has brought.
func (l *Local) Referral(ctx context.Context, tgID int64) (ReferralView, error) {
	if l.ReferralProgram == nil {
		return ReferralView{}, nil
	}
	cfg := l.ReferralProgram.Setup(ctx)
	v := ReferralView{Enabled: cfg.Enabled, Spend: cfg.Spend}
	if !cfg.Enabled {
		return v, nil
	}
	code, err := l.ReferralProgram.Code(ctx, tgID)
	if err != nil {
		return v, err
	}
	v.Code = code
	st, err := l.ReferralProgram.StatsOf(ctx, tgID)
	if err != nil {
		return v, err
	}
	v.Invited, v.Paid, v.Balance = st.Invited, st.Paid, st.Balance
	for _, r := range cfg.Rules {
		if !r.Enabled {
			continue
		}
		v.Rules = append(v.Rules, ReferralRule{Kind: r.Kind, Value: r.Value, Every: r.Every,
			Count: st.Progress[r.ID], Names: l.ruleNames(ctx, r)})
	}
	rows, err := l.Store.Q.ListReferralRewardsOf(ctx, db.ListReferralRewardsOfParams{ReferrerTgID: tgID, Lim: referralHistory})
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		kind := referral.KindPercent
		if r.Kind == referral.KindDays {
			kind = referral.KindDays
		}
		v.Rewards = append(v.Rewards, ReferralReward{Kind: kind, Rub: r.Amount, Days: r.Days, At: time.Unix(r.CreatedAt, 0).UTC()})
	}
	return v, nil
}

// ruleNames is how a rule names the tariffs it counts, for the words of the screen: the
// rule's own name first when the admin gave one, then the tariffs'.
func (l *Local) ruleNames(ctx context.Context, r referral.Rule) []string {
	var names []string
	if name := strings.TrimSpace(r.Name); name != "" {
		names = append(names, name)
	}
	for _, id := range r.TariffIDs {
		t, err := l.Store.Q.GetTariff(ctx, id)
		if err != nil {
			return names
		}
		names = append(names, t.Name)
	}
	return names
}

// ReferralJoin follows an invite code: the account is bound to one inviter, the first whose
// link it opened, and the status says what came of this one.
func (l *Local) ReferralJoin(ctx context.Context, tgID int64, code string) (ReferralJoined, error) {
	if l.ReferralProgram == nil {
		return ReferralJoined{Status: ReferralOff}, nil
	}
	ref, err := l.ReferralProgram.Link(ctx, tgID, code)
	switch {
	case errors.Is(err, referral.ErrOff):
		return ReferralJoined{Status: ReferralOff}, nil
	case errors.Is(err, referral.ErrSelf):
		return ReferralJoined{Status: ReferralSelf}, nil
	case errors.Is(err, referral.ErrCode):
		return ReferralJoined{Status: ReferralInvalid}, nil
	case errors.Is(err, referral.ErrBound):
		return ReferralJoined{Status: ReferralBound, ReferrerName: l.referrerName(ctx, ref.ReferrerTgID)}, nil
	case err != nil:
		return ReferralJoined{}, err
	}
	return ReferralJoined{Status: ReferralDone, ReferrerName: l.referrerName(ctx, ref.ReferrerTgID)}, nil
}

// referrerName is how the programme calls an account that invited somebody: its @username,
// else its first name.
func (l *Local) referrerName(ctx context.Context, tgID int64) string {
	ch, err := l.Store.Q.GetTgChat(ctx, tgID)
	if err != nil {
		return ""
	}
	switch {
	case ch.Username != "":
		return "@" + ch.Username
	case strings.TrimSpace(ch.FirstName) != "":
		return ch.FirstName
	}
	return ""
}

// CampaignVisit counts a /start that came through a tag: once per chat, on the first tagged
// link it follows.
func (l *Local) CampaignVisit(ctx context.Context, tgID int64, tag string) (CampaignGreeting, error) {
	if l.Campaigns == nil {
		return CampaignGreeting{}, nil
	}
	c, counted, err := l.Campaigns.Visit(ctx, tgID, tag)
	if err != nil {
		return CampaignGreeting{}, err
	}
	return CampaignGreeting{Counted: counted, Name: c.Name}, nil
}
