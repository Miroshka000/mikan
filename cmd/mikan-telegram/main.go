// Command mikan-telegram is the official bot addon of mikan (getmikan/marketplace, id
// telegram): the subscribers' bot, with the menu, the shop and the notices. It runs next to
// the panel as an addon of protocol 2 in client mode and sees the panel only through what
// the host hands it:
//
//	MIKAN_PANEL_URL, MIKAN_PANEL_TOKEN  the Panel API: the bot's data (POST /bot/{method})
//	                                     and the feed of events (payment messages, notices)
//	MIKAN_BOT_API_URL, MIKAN_BOT_TOKEN  the Bot API proxy: the updates and the sends; the
//	                                     panel keeps the real token and the payments
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/tgbot"
	"mikan/internal/tgapi"
)

// version is set at build time (-ldflags -X main.version=...).
var version = "dev"

// retryPause is how long a call that failed waits before it is made again: a second, then
// twice that for every failure in a row, up to a minute.
var retryPause = pace.Backoff{Base: time.Second, Max: time.Minute}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	env := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Error("not set", "var", k)
			os.Exit(2)
		}
		return v
	}
	panelURL, panelToken := env("MIKAN_PANEL_URL"), env("MIKAN_PANEL_TOKEN")
	botURL, botToken := env("MIKAN_BOT_API_URL"), env("MIKAN_BOT_TOKEN")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("mikan-telegram starting", "version", version)
	a := newAddon(panelURL, panelToken, botURL, botToken, log)
	a.Run(ctx)
	log.Info("mikan-telegram stopped")
}

// addon is the bot's screens on the panel's data and the Bot API proxy.
type addon struct {
	log         *slog.Logger
	client      *tgapi.Client
	out         *tgapi.Outbox
	bot         *tgbot.Bot
	backend     *tgbot.Remote
	running     sync.WaitGroup
	updatesWait time.Duration
}

func newAddon(panelURL, panelToken, botURL, botToken string, log *slog.Logger) *addon {
	a := &addon{log: log, updatesWait: 25 * time.Second}
	a.client = tgapi.NewClient(botURL, botToken, nil)
	a.out = tgapi.NewOutbox(a.client, tgapi.DefaultLimits, time.Now, func(int64) {},
		func(err error) { log.Warn("send", "err", err) })
	a.backend = tgbot.NewRemote(panelURL, panelToken, nil)
	a.bot = tgbot.New(tgbot.Deps{Backend: a.backend, Client: a.client, Outbox: a.out, Go: a.running.Go, Log: log, Now: time.Now})
	return a
}

// Run takes the updates and the events until ctx ends, and returns when all it started has.
func (a *addon) Run(ctx context.Context) {
	defer a.running.Wait()
	a.running.Go(func() { a.out.Run(ctx) })
	a.running.Go(func() { a.setUp(ctx) })
	a.running.Go(func() { a.events(ctx) })
	a.updates(ctx)
}

// setUp sets the chats' commands and menu button, again until it goes through: the panel's
// gateway may not reach Telegram yet when the addon starts.
func (a *addon) setUp(ctx context.Context) {
	for fails := 0; ; fails++ {
		err := a.bot.Started(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			break
		}
		a.log.Warn("set up the chats", "err", err)
		pace.Sleep(ctx, retryPause.Next(fails))
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.bot.RefreshSetup(ctx); err != nil && ctx.Err() == nil {
				a.log.Warn("refresh chat setup", "err", err)
			}
		}
	}
}

// updates reads the panel's buffer as Telegram's getUpdates: an update is acknowledged with
// the next call, after the screens took it.
func (a *addon) updates(ctx context.Context) {
	var offset int64
	fails := 0 // calls that failed in a row
	for ctx.Err() == nil {
		ups, err := a.client.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("updates", "err", err)
			pace.Sleep(ctx, retryPause.Next(fails))
			fails++
			continue
		}
		fails = 0
		for _, up := range ups {
			a.bot.Update(ctx, up)
			offset = up.UpdateID + 1
		}
	}
}

// events reads the feed of the events the bot acts on, and acknowledges what it handled.
func (a *addon) events(ctx context.Context) {
	fails := 0 // pulls that failed in a row
	for ctx.Err() == nil {
		evs, err := a.backend.Events(ctx, a.updatesWait, tgbot.EventTypes)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("events", "err", err)
			pace.Sleep(ctx, retryPause.Next(fails))
			fails++
			continue
		}
		fails = 0
		for _, e := range evs {
			if err := a.bot.HandleEvent(ctx, e); err != nil {
				// Not acknowledged: it comes again with the next pull.
				a.log.Warn("event", "id", e.ID, "type", e.Type, "err", err)
				pace.Sleep(ctx, 5*time.Second)
				break
			}
			if err := a.backend.Ack(ctx, e.ID); err != nil {
				a.log.Warn("ack", "id", e.ID, "err", err)
				break
			}
		}
	}
}
