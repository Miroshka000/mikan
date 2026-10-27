package infraalerts

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/settings"
	"mikan/internal/textutil"
	"mikan/internal/tgapi"
)

// deliveryTarget says where a queued message goes and how it is sent. The value is stored in
// the state's JSON, so the strings are the on-disk format and stay as they are.
type deliveryTarget string

const (
	// targetAdmin is a private message to the chat connected as the infrastructure admin.
	targetAdmin deliveryTarget = "admin"
	// targetNotice is one to the same chat that the alerts' settings do not switch off: the
	// admin's own notices (a support operator came in, a top-up waits), queued by Notify.
	targetNotice deliveryTarget = "notice"
	// targetSummary is the one public status message in the channel: sent once, pinned, then
	// edited in place.
	targetSummary deliveryTarget = "summary"
	// targetPublicChange is a new, silent channel message about one server's level changing.
	targetPublicChange deliveryTarget = "public-change"
	// targetGroup is an admin's message or a notice again, in the Telegram group of the alerts'
	// settings (GroupChat), in its topic when one is set; delivery.Of says which of the two.
	targetGroup deliveryTarget = "group"
	// targetDiscord is such a message in the Discord channel of the webhook under KeyDiscord.
	targetDiscord deliveryTarget = "discord"
)

// telegramTargets are the targets the bot sends to: without the bot their messages go.
var telegramTargets = []deliveryTarget{targetAdmin, targetNotice, targetSummary, targetPublicChange, targetGroup}

const (
	// deliveryTimeout bounds each Telegram call group of one queued message, so a slow API
	// holds the queue for no longer than this per message.
	deliveryTimeout = 12 * time.Second
	// deliveryMaxAge is how long a message is kept for retries before it is dropped as stale.
	deliveryMaxAge = 24 * time.Hour
	// maxDeliveryAttempts is how many times a message is tried before it is given up.
	maxDeliveryAttempts = 8
	// retryBaseDelay is the wait after the first failure; it doubles with each attempt, up to
	// retryMaxDelay.
	retryBaseDelay = 5 * time.Second
	retryMaxDelay  = time.Hour
	// maxPending is how many queued messages are kept; the oldest go first.
	maxPending = 200
)

var retryBackoff = pace.Backoff{Base: retryBaseDelay, Max: retryMaxDelay}

type delivery struct {
	Key           string         `json:"key"`
	Target        deliveryTarget `json:"target"`
	Of            deliveryTarget `json:"of,omitempty"` // the copied message's target (copyOut)
	Chat          int64          `json:"chat,omitempty"`
	Text          string         `json:"text"`
	CreatedAt     int64          `json:"created_at,omitempty"`
	Attempts      int            `json:"attempts,omitempty"`
	NextAttemptAt int64          `json:"next_attempt_at,omitempty"`
}

// sender is the Telegram client's calls the queue makes (*tgapi.Client): a test hands the
// delivery a fake and no network.
type sender interface {
	Send(ctx context.Context, chat int64, text string, kb *tgapi.Keyboard, silent bool) (tgapi.Message, error)
	SendTo(ctx context.Context, chat string, text string, silent bool) (tgapi.Message, error)
	SendToTopic(ctx context.Context, chat string, thread int64, text string) (tgapi.Message, error)
	EditTo(ctx context.Context, chat string, msg int64, text string) error
	PinTo(ctx context.Context, chat string, msg int64) error
}

// startDelivery runs Telegram calls separately from health sampling. State is merged only
// after the network calls finish, so a slow Telegram API never delays the next monitor round.
// One delivery runs at a time: asked for while one runs, it runs once more when that one is
// done, so what was queued meanwhile does not wait for the next round.
func (m *Monitor) startDelivery(ctx context.Context) {
	m.dispatchMu.Lock()
	if m.dispatching {
		m.again = true
		m.dispatchMu.Unlock()
		return
	}
	m.dispatching = true
	m.dispatchMu.Unlock()
	m.delivering.Go(func() {
		for {
			m.deliveryPass(ctx)
			m.dispatchMu.Lock()
			again := m.again
			m.again, m.dispatching = false, again
			m.dispatchMu.Unlock()
			if !again {
				return
			}
		}
	})
}

// deliveryPass makes one pass over the queue as it is in the database and merges the result
// into what the database holds by then.
func (m *Monitor) deliveryPass(ctx context.Context) {
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

// copyOut puts next to each admin's message and notice of pending that fresh says is new the
// same message for the Telegram group (group) and for Discord (discord): each copy is sent
// and tried again on its own, so a Discord that does not answer holds nothing of Telegram's.
// An admin's message is copied only while the admin's alerts are on, as it is sent only then.
func copyOut(pending []delivery, fresh func(key string) bool, cfg AlertsConfig, group, discord bool) []delivery {
	if !group && !discord {
		return pending
	}
	for _, d := range slices.Clone(pending) {
		if d.Target != targetAdmin && d.Target != targetNotice || d.Target == targetAdmin && !cfg.AdminEnabled || !fresh(d.Key) {
			continue
		}
		if group {
			pending = appendPending(pending, delivery{Key: "group|" + d.Key, Target: targetGroup, Of: d.Target, Text: d.Text, CreatedAt: d.CreatedAt})
		}
		if discord {
			pending = appendPending(pending, delivery{Key: "discord|" + d.Key, Target: targetDiscord, Of: d.Target, Text: d.Text, CreatedAt: d.CreatedAt})
		}
	}
	return pending
}

// outlets are where one pass of the queue sends.
type outlets struct {
	tg       sender // nil: the bot is off or gave no client, its messages wait
	admin    int64  // the admin's chat, while adminSet
	adminSet bool
	discord  string // the webhook; "" none
}

// discordHook is the Discord webhook the admin set, "" none.
func (m *Monitor) discordHook(ctx context.Context) string {
	if m.settings == nil {
		return ""
	}
	d, _, err := settings.Get[Discord](ctx, m.settings, KeyDiscord)
	if err != nil {
		m.logError("infrastructure alerts: Discord settings", err)
		return ""
	}
	return d.URL
}

// deliver sends what the queue holds and leaves in st.Pending what is to be tried again. With
// Telegram off its messages go: nothing is kept for a bot nobody turned on. Discord's do not
// need the bot.
func (m *Monitor) deliver(ctx context.Context, st *persistentState, cfg AlertsConfig) {
	tg := m.bot != nil && m.bot.InfrastructureEnabled(ctx)
	if !tg {
		dropPending(st, telegramTargets...)
	}
	if len(st.Pending) == 0 {
		return
	}
	out := outlets{discord: m.discordHook(ctx)}
	if tg {
		client, err := m.bot.InfrastructureClient(ctx)
		if err != nil {
			m.deferPending(st, err, telegramTargets...)
		} else {
			out.tg = client
			out.admin, out.adminSet, _ = m.bot.InfrastructureAdminChat(ctx)
		}
	}
	m.sendPending(ctx, st, cfg, out)
}

// sendPending makes one pass over the queue to out: a message that is stale, switched off in
// cfg or refused for good is dropped, one whose time has not come stays as it is, one that
// failed is kept for a later try with its attempt counted. Once Discord asked to wait (a 429),
// its other messages of the pass wait as long, with no attempt counted: the webhook's limit is
// one for all of them.
func (m *Monitor) sendPending(ctx context.Context, st *persistentState, cfg AlertsConfig, out outlets) {
	keep := st.Pending[:0]
	var discordUntil int64 // when Discord takes messages again, after a 429 in this pass
	for _, d := range st.Pending {
		now := m.now().Unix()
		if d.CreatedAt != 0 && now-d.CreatedAt > int64(deliveryMaxAge/time.Second) {
			continue
		}
		if d.Target == targetDiscord && discordUntil > now {
			d.NextAttemptAt = max(d.NextAttemptAt, discordUntil)
		}
		if d.NextAttemptAt != 0 && d.NextAttemptAt > now || out.tg == nil && d.Target != targetDiscord {
			keep = append(keep, d)
			continue
		}
		// A copy of an admin's message goes while the admin's alerts are on, as the message.
		adminOff := d.Of == targetAdmin && !cfg.AdminEnabled
		var err error
		switch d.Target {
		case targetAdmin:
			if !cfg.AdminEnabled || !out.adminSet {
				continue
			}
			err = sendAdmin(ctx, out.tg, out.admin, d.Text)
		case targetNotice:
			if !out.adminSet {
				continue
			}
			err = sendAdmin(ctx, out.tg, out.admin, d.Text)
		case targetSummary:
			if !cfg.PublicEnabled || !cfg.PublicSummary {
				continue
			}
			err = m.sendSummary(ctx, st, cfg, out.tg, d.Text)
		case targetPublicChange:
			if !cfg.PublicEnabled || !cfg.PublicChanges {
				continue
			}
			err = sendPublicChange(ctx, out.tg, cfg.PublicChannel, d.Text)
		case targetGroup:
			if cfg.GroupChat == "" || adminOff {
				continue
			}
			err = sendGroup(ctx, out.tg, cfg.GroupChat, cfg.GroupThread, d.Text)
		case targetDiscord:
			if out.discord == "" || m.discord == nil || adminOff {
				continue
			}
			err = m.discord.Post(ctx, out.discord, d.Text)
		}
		if err != nil {
			if permanentError(err) {
				m.refuse(ctx, d.Target, err)
				continue
			}
			if retryDelivery(&d, now, err) {
				keep = append(keep, d)
				if code, _ := answerOf(err); d.Target == targetDiscord && code == 429 {
					discordUntil = d.NextAttemptAt
				}
			}
			if m.log != nil {
				m.log.Warn("infrastructure alerts: delivery", "target", d.Target, "err", err)
			}
		}
	}
	st.Pending = keep
}

// KeyRefused keeps the last message Telegram or Discord refused for good (a 4xx: the admin
// blocked the bot, the chat, the channel or the webhook is gone or took the bot's rights): the
// Telegram page tells the admin, who would hear of it nowhere else.
const KeyRefused = "infra_alerts_refused"

// Refused is KeyRefused's value.
type Refused struct {
	At     int64  `json:"at"`
	Target string `json:"target"`
	Error  string `json:"error"`
}

// refuse notes a message Telegram refused for good: logged once an hour for a target and an
// error, and kept for the Telegram page (KeyRefused).
func (m *Monitor) refuse(ctx context.Context, target deliveryTarget, err error) {
	key, now := string(target)+"|"+err.Error(), m.now()
	m.refusedMu.Lock()
	last, seen := m.refusedAt[key]
	fresh := !seen || now.Sub(last) >= time.Hour
	if fresh {
		if m.refusedAt == nil {
			m.refusedAt = map[string]time.Time{}
		}
		m.refusedAt[key] = now
	}
	m.refusedMu.Unlock()
	if !fresh {
		return
	}
	if m.log != nil {
		m.log.Warn("infrastructure alerts: a message was refused for good, it is dropped", "target", target, "err", err)
	}
	if m.settings != nil {
		if serr := settings.Set(ctx, m.settings, KeyRefused, Refused{At: now.Unix(), Target: string(target), Error: textutil.Clip(err.Error(), 300)}); serr != nil {
			m.logError("infrastructure alerts: keep the refusal", serr)
		}
	}
}

// Notify puts a notice to the admin's chat (targetNotice) in the alerts' own queue and sends
// it at once: the queue is kept in the database, so a restart right after loses nothing, and
// a message Telegram did not take is tried again for a day. A notice with the key of one that
// still waits is not queued twice; with "" each is a message of its own. The group and Discord
// of the alerts' settings get it too.
func (m *Monitor) Notify(ctx context.Context, key, text string) { m.notify(ctx, key, text, true) }

// NotifyElsewhere is Notify for the group and Discord alone: the admin's chat was told
// already, in a way of its own (a top-up's screenshot).
func (m *Monitor) NotifyElsewhere(ctx context.Context, key, text string) {
	m.notify(ctx, key, text, false)
}

func (m *Monitor) notify(ctx context.Context, key, text string, chat bool) {
	if key == "" {
		key = strconv.FormatInt(m.now().UnixNano(), 36) + "." + strconv.FormatInt(m.notices.Add(1), 36)
	}
	key = "notice/" + key
	ctx = context.WithoutCancel(ctx)
	m.roundMu.Lock()
	st, err := m.load(ctx)
	if err == nil {
		waits := slices.ContainsFunc(st.Pending, func(d delivery) bool { return d.Key == key })
		st.Pending = appendPending(st.Pending, delivery{Key: key, Target: targetNotice, Text: text, CreatedAt: m.now().Unix()})
		if !waits {
			cfg := m.config(ctx)
			group := cfg.GroupChat != "" && m.bot != nil && m.bot.InfrastructureEnabled(ctx)
			st.Pending = copyOut(st.Pending, func(k string) bool { return k == key }, cfg, group, m.discordHook(ctx) != "")
			if !chat {
				st.Pending = slices.DeleteFunc(st.Pending, func(d delivery) bool { return d.Key == key })
			}
		}
		err = m.save(ctx, st)
	}
	m.roundMu.Unlock()
	if err != nil {
		m.logError("infrastructure alerts: queue a notice", err)
		return
	}
	m.startDelivery(ctx)
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

func sendGroup(ctx context.Context, c sender, chat string, thread int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	_, err := c.SendToTopic(ctx, chat, thread, text)
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
		var msg tgapi.Message
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
		var ae *tgapi.APIError
		if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Description), "message to edit not found") {
			st.PublicMessage, st.PublicPinned, st.PublicPinAttempted = 0, false, false
		}
	}
	return err
}

// deferPending counts a failed attempt for every queued message bound for one of targets:
// their sender gave no client.
func (m *Monitor) deferPending(st *persistentState, err error, targets ...deliveryTarget) {
	now := m.now().Unix()
	keep := st.Pending[:0]
	for _, d := range st.Pending {
		if !slices.Contains(targets, d.Target) || retryDelivery(&d, now, err) {
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
// from retryBaseDelay, and a 429 waits at least as long as Telegram or Discord asked.
func retryDelivery(d *delivery, now int64, err error) bool {
	d.Attempts++
	if d.Attempts >= maxDeliveryAttempts || d.CreatedAt != 0 && now-d.CreatedAt > int64(deliveryMaxAge/time.Second) {
		return false
	}
	delay := retryBackoff.Next(d.Attempts - 1)
	if code, after := answerOf(err); code == 429 && after > delay {
		delay = after
	}
	if delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	d.NextAttemptAt = now + int64(delay/time.Second)
	return true
}

// answerOf is the HTTP status Telegram or Discord refused with and how long it asked to wait;
// 0 when err is not such an answer.
func answerOf(err error) (int, time.Duration) {
	var ae *tgapi.APIError
	var de *DiscordError
	switch {
	case errors.As(err, &ae):
		return ae.Code, ae.RetryAfter
	case errors.As(err, &de):
		return de.Code, de.RetryAfter
	}
	return 0, 0
}

func permanentError(err error) bool {
	code, _ := answerOf(err)
	return code >= 400 && code < 500 && code != 429
}
