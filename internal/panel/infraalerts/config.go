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
	Events        Events `json:"events"`
}

// AlertsConfigPatch preserves omitted switches when the admin UI or an API client sends
// only the fields it wants to change.
type AlertsConfigPatch struct {
	AdminEnabled  *bool        `json:"admin_enabled,omitempty"`
	PublicEnabled *bool        `json:"public_enabled,omitempty"`
	PublicChannel *string      `json:"public_channel,omitempty"`
	PublicSummary *bool        `json:"public_summary,omitempty"`
	PublicChanges *bool        `json:"public_changes,omitempty"`
	Events        *EventsPatch `json:"events,omitempty"`
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

// Validate trims the channel target and rejects values that cannot identify a Telegram
// channel. Telegram accepts either a numeric chat ID or @username for channel methods.
func (c *AlertsConfig) Validate() error {
	c.PublicChannel = strings.TrimSpace(c.PublicChannel)
	if c.PublicEnabled && c.PublicChannel == "" {
		return ErrConfig
	}
	if c.PublicChannel != "" {
		if strings.HasPrefix(c.PublicChannel, "@") {
			name := strings.TrimPrefix(c.PublicChannel, "@")
			if len(name) < 5 || len(name) > 32 {
				return ErrConfig
			}
			for _, r := range name {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
					return ErrConfig
				}
			}
		} else {
			id := strings.TrimPrefix(c.PublicChannel, "-")
			if id == "" || id == c.PublicChannel && c.PublicChannel[0] == '-' {
				return ErrConfig
			}
			for _, r := range id {
				if r < '0' || r > '9' {
					return ErrConfig
				}
			}
		}
	}
	return nil
}
