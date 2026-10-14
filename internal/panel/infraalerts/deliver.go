package infraalerts

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"mikan/internal/panel/tgate"
)

// deliveryTarget says where a queued message goes and how it is sent. The value is stored in
// the state's JSON, so the strings are the on-disk format and stay as they are.
type deliveryTarget string

const (
	// targetAdmin is a private message to the chat connected as the infrastructure admin.
	targetAdmin deliveryTarget = "admin"
	// targetSummary is the one public status message in the channel: sent once, pinned, then
	// edited in place.
	targetSummary deliveryTarget = "summary"
	// targetPublicChange is a new, silent channel message about one server's level changing.
	targetPublicChange deliveryTarget = "public-change"
)

const (
	// deliveryTimeout bounds each Telegram call group of one queued message, so a slow API
	// holds the queue for no longer than this per message.
	deliveryTimeout = 12 * time.Second
	// deliveryMaxAge is how long a message is kept for retries before it is dropped as stale.
	deliveryMaxAge = 24 * time.Hour
	// maxDeliveryAttempts is how many times a message is tried before it is given up.
	maxDeliveryAttempts = 8
	// retryBaseDelay is the wait after the first failure; it doubles with each attempt, up to
	// retryMaxShift doublings and retryMaxDelay.
	retryBaseDelay = 5 * time.Second
	retryMaxShift  = 10
	retryMaxDelay  = time.Hour
	// maxPending is how many queued messages are kept; the oldest go first.
	maxPending = 200
)

type delivery struct {
	Key           string         `json:"key"`
	Target        deliveryTarget `json:"target"`
	Chat          int64          `json:"chat,omitempty"`
	Text          string         `json:"text"`
	CreatedAt     int64          `json:"created_at,omitempty"`
	Attempts      int            `json:"attempts,omitempty"`
	NextAttemptAt int64          `json:"next_attempt_at,omitempty"`
}

// sender is the Telegram client's calls the queue makes (*tgate.Client): a test hands the
// delivery a fake and no network.
type sender interface {
	Send(ctx context.Context, chat int64, text string, kb *tgate.Keyboard, silent bool) (tgate.Message, error)
	SendTo(ctx context.Context, chat string, text string, silent bool) (tgate.Message, error)
	EditTo(ctx context.Context, chat string, msg int64, text string) error
	PinTo(ctx context.Context, chat string, msg int64) error
}

// startDelivery runs Telegram calls separately from health sampling. State is merged only
// after the network calls finish, so a slow Telegram API never delays the next monitor round.
func (m *Monitor) startDelivery(ctx context.Context) {
	m.dispatchMu.Lock()
	if m.dispatching {
		m.dispatchMu.Unlock()
		return
	}
	m.dispatching = true
	m.dispatchMu.Unlock()
	m.delivering.Go(func() {
		defer func() { m.dispatchMu.Lock(); m.dispatching = false; m.dispatchMu.Unlock() }()
		m.roundMu.Lock()
		st, err := m.load(ctx)
		m.roundMu.Unlock()
		if err != nil {
			m.logError("infrastructure alerts: load delivery queue", err)
			return
		}
		before := append([]delivery(nil), st.Pending...)
		if len(before) == 0 {
			return
		}
		cfg := m.config(ctx)
		m.deliver(ctx, &st, cfg)
		m.roundMu.Lock()
		defer m.roundMu.Unlock()
		latest, err := m.load(ctx)
		if err != nil {
			m.logError("infrastructure alerts: reload delivery queue", err)
			return
		}
		latest.Pending = mergeDeliveryResults(latest.Pending, before, st.Pending)
		if latest.PublicTarget == st.PublicTarget {
			latest.PublicMessage = st.PublicMessage
			latest.PublicPinned = st.PublicPinned
			latest.PublicPinAttempted = st.PublicPinAttempted
		}
		if err := m.save(ctx, latest); err != nil {
			m.logError("infrastructure alerts: save delivery queue", err)
		}
	})
}

func mergeDeliveryResults(latest, before, after []delivery) []delivery {
	result := append([]delivery(nil), latest...)
	afterByKey := make(map[string]delivery, len(after))
	for _, d := range after {
		afterByKey[d.Key] = d
	}
	for _, old := range before {
		position := -1
		for i := range result {
			if result[i].Key == old.Key {
				position = i
				break
			}
		}
		if position < 0 {
			continue
		}
		updated, remains := afterByKey[old.Key]
		if !remains {
			// A round may have put new text under the same key meanwhile (the summary): that
			// text is not sent yet and stays queued.
			if result[position].Text == old.Text {
				result = append(result[:position], result[position+1:]...)
			}
			continue
		}
		result[position].Attempts = updated.Attempts
		result[position].NextAttemptAt = updated.NextAttemptAt
		if result[position].CreatedAt == 0 {
			result[position].CreatedAt = updated.CreatedAt
		}
	}
	return result
}

func appendPending(all []delivery, next delivery) []delivery {
	for _, d := range all {
		if d.Key == next.Key {
			return all
		}
	}
	if next.CreatedAt == 0 {
		next.CreatedAt = time.Now().Unix()
	}
	if len(all) >= maxPending {
		all = append([]delivery(nil), all[len(all)-maxPending+1:]...)
	}
	return append(all, next)
}

// dropPending removes the queued messages bound for any of targets, keeping the order of the
// rest.
func dropPending(st *persistentState, targets ...deliveryTarget) {
	kept := st.Pending[:0]
	for _, d := range st.Pending {
		if !slices.Contains(targets, d.Target) {
			kept = append(kept, d)
		}
	}
	st.Pending = kept
}

// deliver sends what the queue holds and leaves in st.Pending what is to be tried again. With
// Telegram off the queue is emptied: nothing is kept for a bot nobody turned on.
func (m *Monitor) deliver(ctx context.Context, st *persistentState, cfg AlertsConfig) {
	if m.bot == nil || !m.bot.InfrastructureEnabled(ctx) {
		st.Pending = nil
		return
	}
	if len(st.Pending) == 0 {
		return
	}
	client, err := m.bot.InfrastructureClient(ctx)
	if err != nil {
		m.deferPending(st, err)
		return
	}
	adminID, adminSet, _ := m.bot.InfrastructureAdminChat(ctx)
	m.sendPending(ctx, st, cfg, client, adminID, adminSet)
}

// sendPending makes one pass over the queue with client: a message that is stale, switched off
// in cfg or refused for good is dropped, one whose time has not come stays as it is, one that
// failed is kept for a later try with its attempt counted.
func (m *Monitor) sendPending(ctx context.Context, st *persistentState, cfg AlertsConfig, client sender, adminID int64, adminSet bool) {
	keep := st.Pending[:0]
	for _, d := range st.Pending {
		now := m.now().Unix()
		if d.CreatedAt != 0 && now-d.CreatedAt > int64(deliveryMaxAge/time.Second) {
			continue
		}
		if d.NextAttemptAt != 0 && d.NextAttemptAt > now {
			keep = append(keep, d)
			continue
		}
		var err error
		switch d.Target {
		case targetAdmin:
			if !cfg.AdminEnabled || !adminSet {
				continue
			}
			err = sendAdmin(ctx, client, adminID, d.Text)
		case targetSummary:
			if !cfg.PublicEnabled || !cfg.PublicSummary {
				continue
			}
			err = m.sendSummary(ctx, st, cfg, client, d.Text)
		case targetPublicChange:
			if !cfg.PublicEnabled || !cfg.PublicChanges {
				continue
			}
			err = sendPublicChange(ctx, client, cfg.PublicChannel, d.Text)
		}
		if err != nil {
			if permanentTelegramError(err) {
				continue
			}
			if retryDelivery(&d, now, err) {
				keep = append(keep, d)
			}
			if m.log != nil {
				m.log.Warn("infrastructure alerts: Telegram delivery", "target", d.Target, "err", err)
			}
		}
	}
	st.Pending = keep
}

func sendAdmin(ctx context.Context, c sender, chat int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	_, err := c.Send(ctx, chat, text, nil, false)
	return err
}

func sendPublicChange(ctx context.Context, c sender, channel, text string) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	_, err := c.SendTo(ctx, channel, text, false)
	return err
}

// sendSummary puts text in the channel's status message: it sends the message when there is
// none yet, pins it once, then edits it. A message the channel no longer has is forgotten, so
// the next try sends a new one.
func (m *Monitor) sendSummary(ctx context.Context, st *persistentState, cfg AlertsConfig, client sender, text string) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	var err error
	if st.PublicMessage == 0 {
		var msg tgate.Message
		msg, err = client.SendTo(ctx, cfg.PublicChannel, text, true)
		if err == nil {
			st.PublicMessage = msg.MessageID
			st.PublicPinned, st.PublicPinAttempted = false, false
		}
	}
	if err == nil && !st.PublicPinned && !st.PublicPinAttempted {
		pinErr := client.PinTo(ctx, cfg.PublicChannel, st.PublicMessage)
		m.recordPublicPinResult(st, pinErr)
	}
	if err == nil {
		err = client.EditTo(ctx, cfg.PublicChannel, st.PublicMessage, text)
		var ae *tgate.APIError
		if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Description), "message to edit not found") {
			st.PublicMessage, st.PublicPinned, st.PublicPinAttempted = 0, false, false
		}
	}
	return err
}

func (m *Monitor) deferPending(st *persistentState, err error) {
	now := m.now().Unix()
	keep := st.Pending[:0]
	for _, d := range st.Pending {
		if retryDelivery(&d, now, err) {
			keep = append(keep, d)
		}
	}
	st.Pending = keep
}

func (m *Monitor) recordPublicPinResult(st *persistentState, err error) {
	if st.PublicPinAttempted || st.PublicPinned {
		return
	}
	st.PublicPinAttempted = true
	if err == nil {
		st.PublicPinned = true
		return
	}
	if m.log != nil {
		m.log.Warn("infrastructure alerts: pin public status failed; will not retry until channel changes", "channel", st.PublicTarget, "err", err)
	}
}

// retryDelivery counts an attempt and says when the next one is; false means the message is
// given up, after maxDeliveryAttempts or once it is older than deliveryMaxAge. The wait doubles
// from retryBaseDelay, and a 429 waits at least as long as Telegram asked.
func retryDelivery(d *delivery, now int64, err error) bool {
	d.Attempts++
	if d.Attempts >= maxDeliveryAttempts || d.CreatedAt != 0 && now-d.CreatedAt > int64(deliveryMaxAge/time.Second) {
		return false
	}
	delay := retryBaseDelay * time.Duration(1<<min(d.Attempts-1, retryMaxShift))
	var ae *tgate.APIError
	if errors.As(err, &ae) && ae.Code == 429 && ae.RetryAfter > delay {
		delay = ae.RetryAfter
	}
	if delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	d.NextAttemptAt = now + int64(delay/time.Second)
	return true
}

func permanentTelegramError(err error) bool {
	var ae *tgate.APIError
	return errors.As(err, &ae) && ae.Code >= 400 && ae.Code < 500 && ae.Code != 429
}
