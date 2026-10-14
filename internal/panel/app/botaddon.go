package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The panel has no screens of its own in the subscribers' bot: they are the official bot
// addon's. A panel whose bot is on and that no addon answers for installs that addon from
// the signed catalog by itself, with the permissions the catalog gives it, and hands it the
// bot once it runs. The admin may hand the bot to another addon (PUT
// /api/v1/telegram/bot-addon); then this leaves it alone. Once the admin removes the bot
// addon it stays removed (panelapi.AutoBotKey) until they turn the bot on again or install
// a bot from the catalog.

// OfficialBot is the id of the official bot addon in the catalog.
const OfficialBot = addons.OfficialBotID

// officialBotPermissions bound what the official bot is granted when it installs itself.
var officialBotPermissions = []string{"bot", "users:read", "users:write", "billing:invoice", "events", "kv"}

// botAddonAgain: how long a failed install waits before it is asked again.
const botAddonAgain = time.Hour

type botAddon struct {
	p     *Panel
	asked time.Time
}

func (b *botAddon) Run(ctx context.Context) {
	b.check(ctx)
	every(ctx, time.Minute, func() { b.check(ctx) })
}

func (b *botAddon) check(ctx context.Context) {
	p := b.p
	if p.Addons == nil || !p.Addons.Supported() {
		return
	}
	on, _, err := settings.Get[bool](ctx, p.Settings, tgate.KeyEnabled)
	if err != nil || !on || p.AddonsAPI.Slot(ctx) != "" {
		return
	}
	if auto, ok, err := settings.Get[bool](ctx, p.Settings, panelapi.AutoBotKey); err != nil || ok && !auto {
		return // the admin removed the bot addon: theirs to bring back
	}
	st, err := p.Addons.State()
	if err != nil || st.HostVersion == "" {
		return // a host before protocol 2 runs no bot addon: `mikan update` first
	}
	if a, ok := st.Adapters[OfficialBot]; ok && a.KindOf() != addons.KindPayment {
		if a.Status != "running" || !b.granted(ctx) {
			return
		}
		if err := settings.Set(ctx, p.Settings, panelapi.SlotKey, OfficialBot); err != nil {
			p.log.Error("bot addon: hand over the bot", "err", err)
			return
		}
		p.log.Info("bot addon: the official bot answers in the bot now")
		p.Gateway.Reload()
		return
	}
	if slices.ContainsFunc(p.Addons.Pending(), func(r addons.Request) bool { return r.ID == OfficialBot }) {
		return
	}
	if l := st.Request; l != nil && l.ID == OfficialBot && l.State == "failed" && p.now().Sub(b.asked) < botAddonAgain {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := p.Addons.Catalog(cctx)
	if err != nil {
		return
	}
	i := slices.IndexFunc(c.Adapters, func(e addons.Entry) bool { return e.ID == OfficialBot && e.Kind != addons.KindPayment })
	if i < 0 {
		return // not in the catalog yet
	}
	e := c.Adapters[i]
	// Installed without the admin's look, so never with more than the official bot needs,
	// whatever a catalog says.
	if !addons.Covers(officialBotPermissions, e.Permissions) {
		p.log.Warn("bot addon: the catalog asks for more than the official bot may have", "permissions", e.Permissions)
		return
	}
	if err := p.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: e.ID, Version: e.Version,
		Permissions: addons.EncodePermissions(e.Permissions), ApprovedAt: p.now().Unix()}); err != nil {
		p.log.Error("bot addon: grant", "err", err)
		return
	}
	if err := p.Addons.Ask(addons.Request{Action: "install", ID: e.ID}); err != nil {
		p.log.Error("bot addon: ask the host", "err", err)
		return
	}
	b.asked = p.now()
	p.log.Info("bot addon: installing the official bot", "version", e.Version)
}

// granted: the official addon may hold the bot.
func (b *botAddon) granted(ctx context.Context) bool {
	perms, _, err := addons.LoadGrant(ctx, b.p.st.Q, OfficialBot)
	if err != nil && !errors.Is(err, addons.ErrNoGrant) {
		b.p.log.Warn("bot addon: grant", "err", err)
	}
	return err == nil && addons.HasPerm(perms, "bot")
}
