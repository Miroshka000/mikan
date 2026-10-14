package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
)

// Who answers in the subscribers' bot: an addon holding the bot (permission bot) through the
// Bot API proxy. The admin chooses; while none holds it the updates wait in the panel's
// buffer, and the official bot addon is installed by app/botaddon.go. The panel keeps the
// token, the payments and the alerts either way. Only a session may change it: the addon
// gets the bot's chats.

type BotSlotView struct {
	Addon string `json:"addon" doc:"Аддон, который отвечает в боте; пусто — никто, обновления ждут в буфере панели"`
	Auto  bool   `json:"auto" doc:"Панель сама ставит официального бота, пока бот включён и никто не отвечает; false — админ удалил бота"`
}

type botSlotOutput struct{ Body BotSlotView }

type botSlotInput struct {
	Body struct {
		Addon string `json:"addon" pattern:"^([a-z0-9][a-z0-9-]{0,31})?$" doc:"id аддона с правом bot или пусто"`
	}
}

func (h *handlers) registerBotSlot() {
	op := func(id, method, summary string) huma.Operation {
		return sessionOps("addons")(id, method, "/api/v1/telegram/bot-addon", summary, 0)
	}
	huma.Register(h.api, op("get-bot-addon", http.MethodGet, "Кто отвечает в боте подписчиков"), h.getBotSlot)
	huma.Register(h.api, op("set-bot-addon", http.MethodPut, "Отдать бота аддону"), h.setBotSlot)
}

func (h *handlers) getBotSlot(ctx context.Context, _ *struct{}) (*botSlotOutput, error) {
	auto, set, err := settings.Get[bool](ctx, h.d.Settings, panelapi.AutoBotKey)
	if err != nil {
		return nil, err
	}
	return &botSlotOutput{Body: BotSlotView{Addon: panelapi.BotHolder(ctx, h.d.Settings), Auto: auto || !set}}, nil
}

func (h *handlers) setBotSlot(ctx context.Context, in *botSlotInput) (*botSlotOutput, error) {
	id := in.Body.Addon
	if id != "" {
		st, err := h.addonState()
		if err != nil {
			return nil, err
		}
		if a, ok := st.Adapters[id]; !ok || a.KindOf() == addons.KindPayment {
			return nil, huma.Error404NotFound("addon_not_installed")
		}
		perms, err := h.granted(ctx, id)
		if err != nil {
			return nil, err
		}
		if !addons.HasPerm(perms, "bot") {
			return nil, huma.Error409Conflict("addon_no_bot")
		}
	}
	if err := settings.Set(ctx, h.d.Settings, panelapi.SlotKey, id); err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.bot_addon", "addon", id, nil)
	// The gateway starts again: the commands and the menu button are the new holder's to set.
	if h.d.Gateway != nil {
		h.d.Gateway.Reload()
	}
	return h.getBotSlot(ctx, nil)
}
