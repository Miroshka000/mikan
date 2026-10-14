package tgate

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Settings keys of the gateway.
const (
	KeyEnabled = "tg_enabled"
	KeyToken   = "tg_token" // never leaves the panel's API
	KeyBot     = "tg_bot"   // the token's bot, from getMe
	// KeyConfig is the bot's setup (tgbot.Config); the gateway reads its language and its
	// channel from it (ReadSetup).
	KeyConfig         = "tg_config"
	KeySecret         = "tg_secret" // signs the link codes
	KeyOffset         = "tg_offset" // the last update taken, so a restart does not hand them out again
	KeyInfraAdminChat = "infrastructure_admin_chat"
	keyInfraAdminLink = "infrastructure_admin_link"
)

// Enabled switches the bot on; off until the admin connects it.
var Enabled = settings.Switch{Key: KeyEnabled}

// MaxLinks: subscriptions one Telegram account may hold.
const MaxLinks = 5

// ErrOff: the bot is off, or not running yet.
var ErrOff = errors.New("bot_off")

// Deps is what the gateway needs from the panel.
type Deps struct {
	Store    *store.Store
	Settings *settings.Settings
	API      string // the Bot API; DefaultAPI unless testing
	Log      *slog.Logger
	Now      func() time.Time
	Limits   Limits // zero: DefaultLimits
	// Billing takes the Telegram Stars payments.
	Billing *billing.Service
	// Tunnel reaches addr through node id, for RouteNode; nil: the panel has no nodes.
	Tunnel func(ctx context.Context, nodeID int64, addr string) (net.Conn, error)

	// stars takes a successful Stars payment instead of Billing; tests only.
	stars func(ctx context.Context, tgID int64, payload, chargeID, currency string, amount int64) error
}

// Handler is the bot's side of the gateway: the buffer the bot addon reads through the Bot
// API proxy (panelapi.BotSlot).
type Handler interface {
	// Update is a message or a button press in a private chat, past the flood guard, that
	// the gateway does not handle itself. It is acknowledged to Telegram after the call; an
	// error offers the update again (the buffer could not take it).
	Update(ctx context.Context, up Update) error
}

// Status is what the admin panel shows.
type Status struct {
	Running bool
	Error   string
	Bot     User
}

type Gateway struct {
	d       Deps
	polled  *settings.Settings // d.Settings through a short cache, for loops that ask every few seconds
	reload  chan struct{}
	out     atomic.Pointer[Outbox] // nil while the bot is off
	client  atomic.Pointer[Client] // the running bot's, for calls outside the update loop
	handler atomic.Pointer[handlerRef]

	mu     sync.Mutex
	status Status
	input  map[int64][]time.Time // chat → its recent messages and presses, for the flood guard

	// What the gateway started and has not ended: the poll loop and its outbox, and the
	// calls that must not hold the loop up. Run returns when they have all returned, so that
	// the database is not closed under them.
	running sync.WaitGroup

	infraMu    sync.Mutex
	infraRT    http.RoundTripper // nil for the default route, see infraTransport
	infraRoute Route

	// The channel subscribers must join (channel.go): which one the rest is about, the
	// accounts seen in it lately, why it could not be asked last time, and when the bot
	// last asked about itself there.
	chanMu   sync.Mutex
	chanChat string
	members  map[int64]time.Time // account → until when its membership counts as checked
	chanErr  string
	probedAt time.Time
}

type handlerRef struct{ h Handler }

func New(d Deps) *Gateway {
	if d.API == "" {
		d.API = DefaultAPI
	}
	if d.Limits == (Limits{}) {
		d.Limits = DefaultLimits
	}
	polled := d.Settings
	if d.Store != nil {
		polled = settings.Cached(d.Store.Q, 15*time.Second)
	}
	return &Gateway{d: d, polled: polled, reload: make(chan struct{}, 1), input: map[int64][]time.Time{}, members: map[int64]time.Time{}}
}

// SetHandler plugs in what takes the bot's updates; without it the gateway still sends (alerts,
// backups), takes payments and links the infrastructure admin's chat.
func (g *Gateway) SetHandler(h Handler) { g.handler.Store(&handlerRef{h}) }

func (g *Gateway) handlerOf() Handler {
	if r := g.handler.Load(); r != nil {
		return r.h
	}
	return nil
}

// Status of the running bot.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}

func (g *Gateway) setStatus(fn func(*Status)) {
	g.mu.Lock()
	fn(&g.status)
	g.mu.Unlock()
}

// Outbox is the running bot's outbox; nil while the bot is off.
func (g *Gateway) Outbox() *Outbox { return g.out.Load() }

// Client is the running bot's client; nil while the bot is off.
func (g *Gateway) Client() *Client { return g.client.Load() }

// Go runs f as work of the gateway's: Run returns only after it has.
func (g *Gateway) Go(f func()) { g.running.Go(f) }

// Reload restarts the bot with the settings as they are now.
func (g *Gateway) Reload() {
	// The admin may have just made the bot the channel's admin: the page asks again.
	g.chanMu.Lock()
	g.probedAt = time.Time{}
	g.chanMu.Unlock()
	select {
	case g.reload <- struct{}{}:
	default:
	}
}

// Run keeps the bot polling while it is on.
func (g *Gateway) Run(ctx context.Context) {
	var stop context.CancelFunc
	start := func() {
		if stop != nil {
			stop()
			stop = nil
		}
		on, _ := g.d.Settings.On(ctx, Enabled)
		token, _ := g.d.Settings.String(ctx, KeyToken)
		if !on || token == "" {
			g.setStatus(func(s *Status) { *s = Status{} })
			return
		}
		rt, err := g.transport(g.Route(ctx))
		if err != nil {
			g.setStatus(func(s *Status) { *s = Status{Error: "route_invalid"} })
			return
		}
		pctx, cancel := context.WithCancel(ctx)
		stop = cancel
		c := NewClient(g.d.API, token, rt)
		g.running.Go(func() { g.poll(pctx, c) })
	}
	start()
	for {
		select {
		case <-ctx.Done():
			if stop != nil {
				stop()
			}
			g.running.Wait()
			return
		case <-g.reload:
			start()
		}
	}
}

// view is the part of the bot's setup (KeyConfig) the gateway reads itself: the language
// of its own words and the channel.
type view struct {
	Lang    string  `json:"lang"`
	Channel Channel `json:"channel"`
}

// ReadSetup reads the bot's setup into T: what was saved over fresh(lang), the defaults in
// the panel's language ("ru" or "en"), which are what it gives when nothing was saved or
// what was cannot be read. It also gives that language and whether a setup was saved. The
// gateway's view and tgbot.Config both read the setup here.
func ReadSetup[T any](ctx context.Context, set *settings.Settings, fresh func(lang string) T) (v T, lang string, saved bool) {
	lang, _ = set.Lang(ctx)
	if lang != "en" {
		lang = "ru"
	}
	v, saved, err := settings.GetOver(ctx, set, KeyConfig, fresh(lang))
	if err != nil || !saved {
		return fresh(lang), lang, false
	}
	return v, lang, true
}

func (g *Gateway) view(ctx context.Context, set *settings.Settings) view {
	v, _, _ := ReadSetup(ctx, set, func(lang string) view { return view{Lang: lang} })
	if v.Lang != "en" {
		v.Lang = "ru"
	}
	_ = v.Channel.Validate()
	return v
}

// Lang is the bot's language, "ru" or "en".
func (g *Gateway) Lang(ctx context.Context) string { return g.view(ctx, g.d.Settings).Lang }

// PolledLang and PolledInfrastructure are Lang and InfrastructureEnabled for a loop that
// asks every few seconds (the infrastructure alerts): read through a short cache.
func (g *Gateway) PolledLang(ctx context.Context) string { return g.view(ctx, g.polled).Lang }

func (g *Gateway) PolledInfrastructure(ctx context.Context) bool {
	return g.infrastructure(ctx, g.polled)
}

// secret signs link codes; made once per panel.
func (g *Gateway) secret(ctx context.Context) ([]byte, error) {
	s, err := g.d.Settings.String(ctx, KeySecret)
	if err != nil {
		return nil, err
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil && len(raw) == 32 {
		return raw, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return raw, settings.Set(ctx, g.d.Settings, KeySecret, base64.StdEncoding.EncodeToString(raw))
}

// LinkURL is the t.me link that opens the bot with a code for the user's subscription;
// "" while the bot is off.
func (g *Gateway) LinkURL(ctx context.Context, userID int64) string {
	st := g.Status()
	if !st.Running || st.Bot.Username == "" {
		return ""
	}
	secret, err := g.secret(ctx)
	if err != nil {
		return ""
	}
	return "https://t.me/" + st.Bot.Username + "?start=" + LinkCode(secret, userID, g.d.Now())
}

// UserOfCode is the subscription a /start code from LinkURL was made for: ErrLinkExpired,
// or ErrLinkInvalid for anything that is not a code of this panel.
func (g *Gateway) UserOfCode(ctx context.Context, code string) (int64, error) {
	secret, err := g.secret(ctx)
	if err != nil {
		return 0, ErrLinkInvalid
	}
	return ParseLinkCode(secret, code, g.d.Now())
}

// BeginInfrastructureAdminConnect makes a short lived deep link that binds one private
// Telegram chat to infrastructure alerts. The bot must be running so it can consume the
// one-time /start code.
func (g *Gateway) BeginInfrastructureAdminConnect(ctx context.Context) (string, error) {
	st := g.Status()
	if !st.Running || st.Bot.Username == "" {
		return "", ErrOff
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	pending, _ := json.Marshal(struct {
		Code  string `json:"code"`
		Until int64  `json:"until"`
	}{code, g.d.Now().Add(10 * time.Minute).Unix()})
	if err := settings.Set(ctx, g.d.Settings, keyInfraAdminLink, string(pending)); err != nil {
		return "", err
	}
	return "https://t.me/" + st.Bot.Username + "?start=infra_" + code, nil
}

// InfrastructureAdminChat returns the one chat connected to receive private alerts.
func (g *Gateway) InfrastructureAdminChat(ctx context.Context) (int64, bool, error) {
	id, ok, err := settings.Get[int64](ctx, g.d.Settings, KeyInfraAdminChat)
	return id, ok && id > 0, err
}

func (g *Gateway) DisconnectInfrastructureAdmin(ctx context.Context) error {
	return g.d.Store.TxRC(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if err := settings.Set(ctx, set, KeyInfraAdminChat, int64(0)); err != nil {
			return err
		}
		return settings.Set(ctx, set, keyInfraAdminLink, "")
	})
}

// InfrastructureClient builds a sending client with the same bot token and route used by
// the subscriber bot. Sending alerts does not require the subscriber bot's polling loop.
func (g *Gateway) InfrastructureClient(ctx context.Context) (*Client, error) {
	token, err := g.d.Settings.String(ctx, KeyToken)
	if err != nil || token == "" {
		return nil, ErrOff
	}
	rt, err := g.infraTransport(g.Route(ctx))
	if err != nil {
		return nil, err
	}
	return NewClient(g.d.API, token, rt), nil
}

// infraTransport is the transport of route for the alerts' deliveries, kept while the
// route stays: each delivery reuses its connections instead of a new TLS handshake
// through the tunnel, and a route left behind has its idle connections closed.
func (g *Gateway) infraTransport(r Route) (http.RoundTripper, error) {
	g.infraMu.Lock()
	defer g.infraMu.Unlock()
	if g.infraRT != nil && g.infraRoute == r {
		return g.infraRT, nil
	}
	rt, err := g.transport(r)
	if err != nil {
		return nil, err
	}
	if t, ok := g.infraRT.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	g.infraRT, g.infraRoute = rt, r
	return rt, nil
}

// InfrastructureEnabled reports whether Telegram delivery is enabled and has a token.
func (g *Gateway) InfrastructureEnabled(ctx context.Context) bool {
	return g.infrastructure(ctx, g.d.Settings)
}

func (g *Gateway) infrastructure(ctx context.Context, set *settings.Settings) bool {
	enabled, err := set.On(ctx, Enabled)
	if err != nil || !enabled {
		return false
	}
	token, err := set.String(ctx, KeyToken)
	return err == nil && token != ""
}

func (g *Gateway) claimInfrastructureAdmin(ctx context.Context, chat int64, code string) bool {
	claimed := false
	err := g.d.Store.Tx(ctx, func(q *db.Queries) error {
		// A conflict runs this again: an attempt that rolled back claimed nothing.
		claimed = false
		set := settings.New(q)
		var pending struct {
			Code  string `json:"code"`
			Until int64  `json:"until"`
		}
		raw, ok, err := settings.Get[string](ctx, set, keyInfraAdminLink)
		if err != nil {
			return err
		}
		if !ok || json.Unmarshal([]byte(raw), &pending) != nil || pending.Until < g.d.Now().Unix() || !secure.Equal(code, pending.Code) {
			return nil
		}
		if err := settings.Set(ctx, set, KeyInfraAdminChat, chat); err != nil {
			return err
		}
		if err := settings.Set(ctx, set, keyInfraAdminLink, ""); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return err == nil && claimed
}

// adminLink takes "/start infra_<code>" from BeginInfrastructureAdminConnect: the chat
// becomes the infrastructure admin's, and is told whether it did. false: not such a message.
func (g *Gateway) adminLink(ctx context.Context, out *Outbox, m *Message) bool {
	chat := m.Chat.ID
	messageText := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(messageText, "/start infra_") {
		return false
	}
	code := strings.TrimSpace(strings.TrimPrefix(messageText, "/start infra_"))
	w := wordsFor(g.Lang(ctx))
	text := w.adminLinkFailed
	if g.claimInfrastructureAdmin(ctx, chat, code) {
		text = w.adminLinked
	}
	out.Reply(chat, "infrastructure-admin", 1, func(ctx context.Context, c *Client) error { _, err := c.Send(ctx, chat, text, nil, false); return err })
	return true
}

func (g *Gateway) poll(ctx context.Context, c *Client) {
	me, err := c.Me(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // reloaded meanwhile: the new run's status is not ours to overwrite
		}
		g.setStatus(func(s *Status) { *s = Status{Error: errText(err)} })
		g.retryLater(ctx)
		return
	}
	_ = settings.Set(ctx, g.d.Settings, KeyBot, me)
	_ = c.DropWebhook(ctx)
	// Every message the bot sends goes through the outbox (see outbox.go).
	out := NewOutbox(c, g.d.Limits, time.Now, func(chat int64) {
		_ = g.d.Store.Q.SetTgBlocked(context.WithoutCancel(ctx), db.SetTgBlockedParams{Blocked: 1, TgID: chat})
	}, func(err error) { g.d.Log.Warn("telegram: send", "err", errText(err)) })
	g.running.Go(func() { out.Run(ctx) })
	g.out.Store(out)
	g.client.Store(c)
	defer g.client.CompareAndSwap(c, nil)
	defer g.out.CompareAndSwap(out, nil)
	g.setStatus(func(s *Status) { *s = Status{Running: true, Bot: me} })
	offset := g.loadOffset(ctx, me.ID)
	fails := map[int64]int{} // update → times it failed to be handled
	backoff := time.Second
	var conflictSince time.Time // another program polls this token (or set a webhook) since
	conflictTold := false
	for ctx.Err() == nil {
		ups, err := c.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var ae *APIError
			if errors.As(err, &ae) && ae.Code == 401 {
				g.setStatus(func(s *Status) { *s = Status{Error: "token_revoked"} })
				return
			}
			code := errText(err)
			if errors.As(err, &ae) && ae.Code == http.StatusConflict {
				// A restart of this bot conflicts for a few seconds too, while Telegram drops
				// the old poll; only one that lasts is another program on the token.
				code = "token_conflict"
				if conflictSince.IsZero() {
					conflictSince = time.Now()
				}
				if !conflictTold && time.Since(conflictSince) > conflictLasting {
					conflictTold = true
					g.d.Log.Warn("telegram: another program polls this bot's token", "err", ae.Description)
					g.tellConflict(ctx, out)
				}
			}
			g.setStatus(func(s *Status) { s.Error = code })
			sleep(ctx, backoff)
			backoff = min(2*backoff, time.Minute)
			continue
		}
		conflictSince, conflictTold = time.Time{}, false
		backoff = time.Second
		g.setStatus(func(s *Status) { s.Error = "" })
		for _, up := range ups {
			if err := g.handle(ctx, c, up); err != nil && ctx.Err() == nil {
				// Not acknowledged: Telegram sends it again, with whatever came after.
				if fails[up.UpdateID]++; fails[up.UpdateID] < triesOf(up) {
					sleep(ctx, min(time.Duration(fails[up.UpdateID])*updateRetryPause, time.Minute))
					break
				}
				if sp := paymentOf(up); sp != nil {
					// Stars were taken and nothing saved them: the admin refunds by this charge.
					g.d.Log.Error("telegram: stars payment given up, refund it by hand", "update", up.UpdateID, "err", err,
						"charge", sp.ChargeID, "payload", sp.InvoicePayload, "amount", sp.TotalAmount)
				} else {
					g.d.Log.Error("telegram: update given up", "update", up.UpdateID, "err", err)
				}
			}
			if ctx.Err() != nil {
				return // not handled to the end: stays unacknowledged for the next run
			}
			delete(fails, up.UpdateID)
			offset = up.UpdateID + 1
			g.saveOffset(ctx, me.ID, offset)
		}
	}
}

// maxUpdateTries: how often an update that cannot be handled is offered again before it
// is dropped, so one that never works does not stop the bot. updateRetryPause is the wait
// before each new try, longer every time.
const maxUpdateTries = 5

// maxPaymentTries is the same for a Stars payment not yet saved: the buyer has paid, so it
// waits out a database outage (about 25 minutes, the pause capped at a minute) rather
// than the few seconds other updates get.
const maxPaymentTries = 40

var updateRetryPause = 2 * time.Second

func triesOf(up Update) int {
	if paymentOf(up) != nil {
		return maxPaymentTries
	}
	return maxUpdateTries
}

// paymentOf is the Stars payment an update reports, if any.
func paymentOf(up Update) *SuccessfulPayment {
	if up.Message == nil {
		return nil
	}
	return up.Message.SuccessfulPayment
}

// offsetRecord is the saved position in Telegram's update stream, of one bot.
type offsetRecord struct {
	Bot    int64 `json:"bot"`
	Offset int64 `json:"offset"`
}

// loadOffset: where this bot's updates continue. What Telegram keeps for a bot that was
// switched off or restarted is handed out again from the last confirmation, which is the
// next getUpdates; saving the offset keeps a restart from replaying the last batch (taps
// that unbind a device, a purchase's confirmation) a second time.
func (g *Gateway) loadOffset(ctx context.Context, bot int64) int64 {
	rec, ok, err := settings.Get[offsetRecord](ctx, g.d.Settings, KeyOffset)
	if err != nil || !ok || rec.Bot != bot {
		return 0
	}
	return rec.Offset
}

// saveOffset writes the row itself, not through settings.Set: the position moves with
// every update, and it is not a setting anything is built from (settings.Generation).
func (g *Gateway) saveOffset(ctx context.Context, bot, offset int64) {
	raw, _ := json.Marshal(offsetRecord{Bot: bot, Offset: offset})
	if err := g.d.Store.Q.SetSetting(ctx, db.SetSettingParams{Key: KeyOffset, Value: string(raw)}); err != nil && ctx.Err() == nil {
		g.d.Log.Warn("telegram: offset not saved", "err", err)
	}
}

// retryLater re-reads the settings in a minute: a network hiccup at start must not leave
// the bot off until the admin touches it.
func (g *Gateway) retryLater(ctx context.Context) {
	g.running.Go(func() {
		sleep(ctx, time.Minute)
		if ctx.Err() == nil {
			g.Reload()
		}
	})
}

// conflictLasting: a getUpdates conflict this long is not this bot's own restart.
var conflictLasting = 2 * time.Minute

// tellConflict tells the admin's chat, once a conflict lasts, that another program takes
// this bot's updates: sending still works while polling does not.
func (g *Gateway) tellConflict(ctx context.Context, out *Outbox) {
	chat, ok, err := g.InfrastructureAdminChat(ctx)
	if err != nil || !ok {
		return
	}
	text := wordsFor(g.Lang(ctx)).tokenConflict
	out.Notice(chat, func(ctx context.Context, c *Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}, func(error) {})
}

func errText(err error) string {
	var ae *APIError
	switch {
	case errors.As(err, &ae) && ae.Code == 401:
		return "token_invalid"
	case errors.As(err, &ae):
		return ae.Description
	}
	return "unreachable"
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// A person taps and types far slower than this; past it a chat is ignored for a while,
// so one abuser cannot spend the bot's Telegram limits.
const (
	floodWindow = 10 * time.Second
	floodInputs = 20
)

func (g *Gateway) flooding(chat int64) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	recent := g.input[chat][:0]
	for _, t := range g.input[chat] {
		if now.Sub(t) < floodWindow {
			recent = append(recent, t)
		}
	}
	g.input[chat] = append(recent, now)
	if len(g.input) > 10000 {
		for k, ts := range g.input {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > floodWindow {
				delete(g.input, k)
			}
		}
	}
	return len(recent) >= floodInputs
}

// handle takes one update. Only a payment can fail in a way that is worth offering the
// update again for; the rest is answered, or dropped, as it comes. Messages and presses
// in private chats go to the handler, past the flood guard; a message first makes
// its account known (tg_chats).
func (g *Gateway) handle(ctx context.Context, c *Client, up Update) error {
	out := g.out.Load()
	if out == nil {
		return nil
	}
	switch {
	case up.PreCheckoutQuery != nil:
		// Ten seconds from Telegram, whatever else the loop waits for: its own goroutine.
		g.running.Go(func() { g.preCheckout(ctx, c, up.PreCheckoutQuery) })
	case up.Message != nil && up.Message.SuccessfulPayment != nil && up.Message.Chat.Type == "private":
		return g.starsPaid(ctx, up.Message)
	case up.Message != nil && up.Message.RefundedPayment != nil && up.Message.Chat.Type == "private":
		return g.starsRefunded(ctx, up.Message)
	case up.CallbackQuery != nil && up.CallbackQuery.Message != nil && up.CallbackQuery.Message.Chat.Type == "private":
		if g.flooding(up.CallbackQuery.Message.Chat.ID) {
			return nil
		}
		if h := g.handlerOf(); h != nil {
			return h.Update(ctx, up)
		}
	case up.Message != nil && up.Message.From != nil && up.Message.Chat.Type == "private":
		m := up.Message
		chat, now := m.Chat.ID, g.d.Now().Unix()
		if g.flooding(chat) || g.adminLink(ctx, out, m) {
			return nil
		}
		_ = g.d.Store.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: chat, Username: m.From.Username, FirstName: m.From.FirstName, CreatedAt: now, UpdatedAt: now})
		if h := g.handlerOf(); h != nil {
			return h.Update(ctx, up)
		}
	}
	return nil
}

// CheckTokenVia asks Telegram whose token this is, through route: a route and a token
// changed together are checked as they will run.
func (g *Gateway) CheckTokenVia(ctx context.Context, token string, route Route) (User, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rt, err := g.transport(route)
	if err != nil {
		return User{}, err
	}
	me, err := NewClient(g.d.API, token, rt).Me(ctx)
	var ae *APIError
	if err != nil && !errors.As(err, &ae) {
		g.routeFailed(route, err)
	}
	return me, err
}

// MiniAppUser checks the Mini App's initData and returns that Telegram account's id and
// its subscriptions, the one the chat shows first.
func (g *Gateway) MiniAppUser(ctx context.Context, initData string) (int64, []db.User, error) {
	token, err := g.d.Settings.String(ctx, KeyToken)
	if err != nil || token == "" {
		return 0, nil, ErrOff
	}
	tu, err := CheckInitData(token, initData, g.d.Now())
	if err != nil {
		return 0, nil, err
	}
	list, err := g.d.Store.Q.ListTgLinksOf(ctx, tu.ID)
	if err != nil {
		return 0, nil, err
	}
	if ch, err := g.d.Store.Q.GetTgChat(ctx, tu.ID); err == nil {
		for i, u := range list {
			if u.ID == ch.Current && i > 0 {
				list[0], list[i] = list[i], list[0]
			}
		}
	}
	return tu.ID, list, nil
}
