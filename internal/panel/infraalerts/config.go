// Package infraalerts contains the infrastructure notification policy and its state
// transitions. It deliberately keeps Telegram delivery and panel health collection out
// of the transition logic so those paths can be tested independently.
package infraalerts

import (
	"errors"
	"strings"
)

const KeyConfig = "infrastructure_alerts"

var ErrConfig = errors.New("infra_alerts_config")

type Events struct {
	Node             bool `json:"node"`
	Warp             bool `json:"warp"`
	Exit             bool `json:"exit"`
	Inbound          bool `json:"inbound"`
	Autotune         bool `json:"autotune"`
	AutotuneRecovery bool `json:"autotune_recovery"`
	TLS              bool `json:"tls"`
	Update           bool `json:"update"`
	Torrent          bool `json:"torrent"`
}

type AlertsConfig struct {
	AdminEnabled  bool   `json:"admin_enabled"`
	PublicEnabled bool   `json:"public_enabled"`
	PublicChannel string `json:"public_channel,omitempty"`
	PublicSummary bool   `json:"public_summary"`
	PublicChanges bool   `json:"public_changes"`
	// GroupChat is a Telegram group the admin's alerts and notices also go to, and GroupThread
	// the topic in it when the group has topics: a team that shares one chat reads them there.
	GroupChat   string `json:"group_chat,omitempty" maxLength:"40" doc:"Группа Telegram, куда тоже идут уведомления админу: @username или числовой ID; пусто — нет"`
	GroupThread int64  `json:"group_thread,omitempty" minimum:"0" doc:"Тема группы (message_thread_id) для групп с темами; 0 — общий чат группы"`
	Events      Events `json:"events"`
}

// AlertsConfigPatch preserves omitted switches when the admin UI or an API client sends
// only the fields it wants to change.
type AlertsConfigPatch struct {
	AdminEnabled  *bool        `json:"admin_enabled,omitempty"`
	PublicEnabled *bool        `json:"public_enabled,omitempty"`
	PublicChannel *string      `json:"public_channel,omitempty"`
	PublicSummary *bool        `json:"public_summary,omitempty"`
	PublicChanges *bool        `json:"public_changes,omitempty"`
	GroupChat     *string      `json:"group_chat,omitempty" maxLength:"40"`
	GroupThread   *int64       `json:"group_thread,omitempty" minimum:"0"`
	Events        *EventsPatch `json:"events,omitempty"`
	// DiscordWebhook is not part of AlertsConfig: the address is a secret, kept under
	// KeyDiscord and never shown back. The API checks and saves it; Merge leaves it.
	DiscordWebhook *string `json:"discord_webhook,omitempty" maxLength:"300" doc:"Адрес webhook канала Discord (https://discord.com/api/webhooks/…), куда тоже идут уведомления админу; пустая строка — удалить. В ответе не возвращается"`
}

type EventsPatch struct {
	Node             *bool `json:"node,omitempty"`
	Warp             *bool `json:"warp,omitempty"`
	Exit             *bool `json:"exit,omitempty"`
	Inbound          *bool `json:"inbound,omitempty"`
	Autotune         *bool `json:"autotune,omitempty"`
	AutotuneRecovery *bool `json:"autotune_recovery,omitempty"`
	TLS              *bool `json:"tls,omitempty"`
	Update           *bool `json:"update,omitempty"`
	Torrent          *bool `json:"torrent,omitempty"`
}

func (p AlertsConfigPatch) Merge(c AlertsConfig) AlertsConfig {
	if p.AdminEnabled != nil {
		c.AdminEnabled = *p.AdminEnabled
	}
	if p.PublicEnabled != nil {
		c.PublicEnabled = *p.PublicEnabled
	}
	if p.PublicChannel != nil {
		c.PublicChannel = *p.PublicChannel
	}
	if p.PublicSummary != nil {
		c.PublicSummary = *p.PublicSummary
	}
	if p.PublicChanges != nil {
		c.PublicChanges = *p.PublicChanges
	}
	if p.GroupChat != nil {
		c.GroupChat = *p.GroupChat
	}
	if p.GroupThread != nil {
		c.GroupThread = *p.GroupThread
	}
	if e := p.Events; e != nil {
		if e.Node != nil {
			c.Events.Node = *e.Node
		}
		if e.Warp != nil {
			c.Events.Warp = *e.Warp
		}
		if e.Exit != nil {
			c.Events.Exit = *e.Exit
		}
		if e.Inbound != nil {
			c.Events.Inbound = *e.Inbound
		}
		if e.Autotune != nil {
			c.Events.Autotune = *e.Autotune
		}
		if e.AutotuneRecovery != nil {
			c.Events.AutotuneRecovery = *e.AutotuneRecovery
		}
		if e.TLS != nil {
			c.Events.TLS = *e.TLS
		}
		if e.Update != nil {
			c.Events.Update = *e.Update
		}
		if e.Torrent != nil {
			c.Events.Torrent = *e.Torrent
		}
	}
	return c
}

func Default() AlertsConfig {
	return AlertsConfig{Events: Events{Node: true, Warp: true, Exit: true, Inbound: true, Autotune: true,
		AutotuneRecovery: true, TLS: true, Update: true, Torrent: true}, PublicSummary: true, PublicChanges: true}
}

// Validate trims the channel and the group and rejects values that cannot identify a
// Telegram chat. Telegram accepts either a numeric chat ID or @username for them; a topic is
// only of a group.
func (c *AlertsConfig) Validate() error {
	c.PublicChannel = strings.TrimSpace(c.PublicChannel)
	c.GroupChat = strings.TrimSpace(c.GroupChat)
	if c.PublicEnabled && c.PublicChannel == "" {
		return ErrConfig
	}
	if c.PublicChannel != "" && !validChat(c.PublicChannel) || c.GroupChat != "" && !validChat(c.GroupChat) {
		return ErrConfig
	}
	// A group's numeric ID is negative; a positive one is a private chat, such as the admin's
	// own, which would get every alert twice.
	if c.GroupChat != "" && c.GroupChat[0] != '@' && c.GroupChat[0] != '-' {
		return ErrConfig
	}
	if c.GroupThread < 0 || c.GroupThread > 0 && c.GroupChat == "" {
		return ErrConfig
	}
	return nil
}

// validChat: s names a Telegram chat, as @username or as a numeric ID.
func validChat(s string) bool {
	if name, ok := strings.CutPrefix(s, "@"); ok {
		if len(name) < 5 || len(name) > 32 {
			return false
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
				return false
			}
		}
		return true
	}
	id := strings.TrimPrefix(s, "-")
	if id == "" || id == s && s[0] == '-' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
