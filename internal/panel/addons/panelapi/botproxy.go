package panelapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The Bot API proxy: the addon that holds the bot (permission bot, chosen by the admin) runs
// on any Telegram framework pointed at MIKAN_BOT_API_URL with MIKAN_BOT_TOKEN. The panel keeps
// the real token, the polling and the payments: getUpdates reads what the gateway buffered,
// the calls an addon may make go to Telegram through the gateway's outbox (its limits) and
// come back with Telegram's own answer, and the rest is refused as Telegram would.

// SlotKey is the setting naming the addon that holds the bot; "" or absent: none, and the
// updates wait in the buffer for one (app/botaddon.go installs the official bot).
const SlotKey = "addons.bot"

// AutoBotKey is false once the admin removed the addon that answered in the bot: the panel
// stops installing the official one by itself until the admin turns the bot on again or
// installs a bot from the catalog. Absent means true.
const AutoBotKey = "addons.bot_auto"

const (
	maxBuffered = 2000           // updates kept for an addon that does not read them
	keepUpdates = 24 * time.Hour // as long as Telegram keeps them itself
	maxCall     = 1 << 20        // a call that carries no file
	maxUpload   = tgate.MaxDocument + 1<<20
)

// Telegram is the gateway as the proxy needs it.
type Telegram interface {
	Client() *tgate.Client
	Outbox() *tgate.Outbox
}

// direct calls carry no message to a chat: they go to Telegram at once.
var direct = map[string]bool{
	"getMe": true, "getChat": true, "getChatMember": true, "answerCallbackQuery": true,
	"setMyCommands": true, "getMyCommands": true, "deleteMyCommands": true,
	"setChatMenuButton": true, "getChatMenuButton": true,
}

// queued calls send to a chat (chat_id): they wait their turn in the outbox.
var queued = map[string]bool{
	"sendMessage": true, "editMessageText": true, "editMessageReplyMarkup": true, "editMessageCaption": true,
	"editMessageMedia": true, "deleteMessage": true, "deleteMessages": true, "sendPhoto": true,
	"sendDocument": true, "sendMediaGroup": true, "sendChatAction": true,
	"sendSticker": true, "sendAnimation": true, "sendVideo": true,
	"sendAudio": true, "sendVoice": true, "sendLocation": true, "pinChatMessage": true, "unpinChatMessage": true,
}

// Slot is the addon holding the bot: the one the admin chose, while it still has a grant
// with bot. Without it the bot has no holder.
func (s *Server) Slot(ctx context.Context) string {
	id := s.slotSetting(ctx)
	if id == "" {
		return ""
	}
	perms, _, err := addons.LoadGrant(ctx, s.d.Store.Q, id)
	if err != nil && !errors.Is(err, addons.ErrNoGrant) {
		s.d.Log.Warn("addons api: grant", "addon", id, "err", err)
	}
	if err != nil || !addons.HasPerm(perms, "bot") {
		return ""
	}
	return id
}

// BotHolder is the addon the admin chose to hold the bot, grant or not; "" when none or the
// setting cannot be read.
func BotHolder(ctx context.Context, set *settings.Settings) string {
	id, _, err := settings.Get[string](ctx, set, SlotKey)
	if err != nil {
		return ""
	}
	return id
}

func (s *Server) slotSetting(ctx context.Context) string { return BotHolder(ctx, s.d.Settings) }

// holds is Slot for a caller whose grant authenticate has just read: it needs the setting
// alone, not the grant a second time.
func (s *Server) holds(ctx context.Context, c Caller) bool {
	return c.Can("bot") && s.slotSetting(ctx) == c.ID
}

// BotSlot is the gateway's handler: it keeps the updates in the buffer for the addon that
// holds the bot, or for the one to come, as the bot addon is installed after the bot is on.
type BotSlot struct {
	s *Server

	mu       sync.Mutex
	toldFull time.Time
}

func (s *Server) BotSlot() *BotSlot { return &BotSlot{s: s} }

func (b *BotSlot) Update(ctx context.Context, up tgate.Update) error {
	n, err := b.s.d.Store.Q.CountAddonUpdates(ctx)
	if err != nil {
		return err
	}
	if n >= maxBuffered {
		// The addon does not read: what comes now is dropped, said once a minute.
		b.mu.Lock()
		if time.Since(b.toldFull) > time.Minute {
			b.toldFull = time.Now()
			b.s.d.Log.Warn("addons api: the bot addon does not take its updates, new ones are dropped", "kept", n)
		}
		b.mu.Unlock()
		return nil
	}
	if err := b.s.d.Store.Q.AddAddonUpdate(ctx, db.AddAddonUpdateParams{UpdateID: up.UpdateID, Body: string(up.Raw), CreatedAt: b.s.d.Now().Unix()}); err != nil {
		return err
	}
	b.s.updates.Kick()
	return nil
}

// tgFail answers as the Bot API refuses.
func tgFail(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "description": description})
}

func tgOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// botAPI serves /bot<n>:<token>/<method>.
func (s *Server) botAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/bot")
	token, method, ok := strings.Cut(rest, "/")
	if _, secret, found := strings.Cut(token, ":"); found {
		token = secret
	} else {
		ok = false
	}
	if !ok || method == "" || strings.Contains(method, "/") {
		tgFail(w, http.StatusNotFound, "Not Found")
		return
	}
	c, status, _ := s.authenticateToken(r.Context(), token)
	if status != 0 || !s.holds(r.Context(), c) {
		tgFail(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	limit := int64(maxCall)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		limit = maxUpload
		// A file is held in memory on its way: a few at a time, so that a burst cannot take
		// the panel's memory.
		select {
		case s.uploads <- struct{}{}:
			defer func() { <-s.uploads }()
		case <-r.Context().Done():
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		tgFail(w, http.StatusBadRequest, "Bad Request: the body could not be read")
		return
	}
	if int64(len(body)) > limit {
		tgFail(w, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
		return
	}
	ct := r.Header.Get("Content-Type")
	if len(body) == 0 && r.URL.RawQuery != "" {
		// Parameters in the URL go to Telegram as a form: the token stays out of any URL.
		body, ct = []byte(r.URL.RawQuery), "application/x-www-form-urlencoded"
	}
	p, err := params(ct, body)
	if err != nil {
		tgFail(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	switch {
	case method == "getUpdates":
		s.getUpdates(w, r, p)
		return
	case method == "deleteWebhook":
		// Frameworks drop a webhook before they poll: there is none, and the buffer is theirs.
		if p["drop_pending_updates"] == "true" {
			if err := s.d.Store.Q.AckAddonUpdates(r.Context(), math.MaxInt64); err != nil {
				s.d.Log.Warn("addons api: drop updates", "err", err)
			}
		}
		tgOK(w, true)
		return
	case !direct[method] && !queued[method]:
		tgFail(w, http.StatusForbidden, "Forbidden: "+method+" is not allowed for addons")
		return
	}
	if err := s.checkButtons(r.Context(), p); err != nil {
		tgFail(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	tg := s.d.Telegram
	var cl *tgate.Client
	var out *tgate.Outbox
	if tg != nil {
		cl, out = tg.Client(), tg.Outbox()
	}
	if cl == nil || out == nil {
		tgFail(w, http.StatusBadGateway, "Bad Gateway: the bot is not running")
		return
	}
	if direct[method] {
		code, answer, err := cl.Forward(r.Context(), method, ct, body)
		s.answer(w, code, answer, err)
		return
	}
	chat, ok := chatKey(p["chat_id"])
	if !ok {
		tgFail(w, http.StatusBadRequest, "Bad Request: chat_id is required")
		return
	}
	var (
		code   int
		answer []byte
	)
	done := make(chan error, 1)
	out.Do(chat, r.Header.Get("X-Mikan-Priority"), func(ctx context.Context, c *tgate.Client) error {
		var err error
		code, answer, err = c.Forward(ctx, method, ct, body)
		return err
	}, func(err error) { done <- err })
	select {
	case err := <-done:
		s.answer(w, code, answer, err)
	case <-r.Context().Done():
	}
}

// answer passes Telegram's answer on; a way to Telegram that failed is a 502.
func (s *Server) answer(w http.ResponseWriter, code int, answer []byte, err error) {
	var ae *tgate.APIError
	if err != nil && !errors.As(err, &ae) || code == 0 {
		tgFail(w, http.StatusBadGateway, "Bad Gateway: Telegram is out of reach")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(answer)
}

// getUpdates is Telegram's: what follows offset (acknowledging what is below it), up to
// limit, waiting up to timeout seconds for something new.
func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, p map[string]string) {
	offset := clampParam(p["offset"], 0, 0, math.MaxInt64)
	limit := clampParam(p["limit"], 100, 1, 100)
	timeout := clampParam(p["timeout"], 0, 0, maxPollWait)
	if offset > 0 {
		if err := s.d.Store.Q.AckAddonUpdates(r.Context(), offset); err != nil {
			tgFail(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	rows, err := longPoll(r.Context(), s.updates, time.Duration(timeout)*time.Second, func() ([]db.ListAddonUpdatesRow, error) {
		return s.d.Store.Q.ListAddonUpdates(r.Context(), db.ListAddonUpdatesParams{FromID: offset, Lim: pageSize(limit, 100)})
	})
	switch {
	case r.Context().Err() != nil:
	case err != nil:
		tgFail(w, http.StatusInternalServerError, "Internal Server Error")
	default:
		ups := make([]json.RawMessage, 0, len(rows))
		for _, u := range rows {
			ups = append(ups, json.RawMessage(u.Body))
			s.talk.markUpdate([]byte(u.Body))
		}
		tgOK(w, ups)
	}
}

// params reads a call's parameters as the Bot API takes them: JSON, a form, or multipart
// (its fields, not its files). A JSON value that is no string stays as its JSON text.
func params(ct string, body []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(body) == 0 {
		return out, nil
	}
	mt, mp, _ := mime.ParseMediaType(ct)
	switch mt {
	case "application/json":
		m, err := jsonObject(body)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			// A number or an object is no string: told by its first byte, not by a failed decode.
			if v[0] != '"' && string(v) != "null" {
				out[k] = string(v)
				continue
			}
			var s string
			_ = json.Unmarshal(v, &s)
			out[k] = s
		}
	case "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, errors.New("the form cannot be read")
		}
		for k, vs := range v {
			if len(vs) > 1 {
				return nil, errors.New(k + " is given twice")
			}
			out[k] = vs[0]
		}
	case "multipart/form-data":
		mr := multipart.NewReader(bytes.NewReader(body), mp["boundary"])
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, errors.New("the multipart body cannot be read")
			}
			if _, twice := out[part.FormName()]; twice {
				return nil, errors.New(part.FormName() + " is given twice")
			}
			out[part.FormName()] = ""
			if part.FileName() == "" {
				v, _ := io.ReadAll(io.LimitReader(part, maxCall))
				out[part.FormName()] = string(v)
			}
			part.Close()
		}
	default:
		return nil, errors.New("unsupported content type")
	}
	return out, nil
}

// chatKey is the chat the outbox paces a call by: the id, or a channel's @name made a number.
func chatKey(v string) (int64, bool) {
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n, true
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(v))
	return int64(h.Sum64() >> 1), true
}

// checkButtons: a Mini App button or a login link opens a page that gets the user's signed
// data, so it may open only the panel's own pages, never one the addon picks.
func (s *Server) checkButtons(ctx context.Context, p map[string]string) error {
	// Asked only when a call has such a button: most carry none.
	base, asked := "", false
	allowed := func(u string) bool {
		if !asked {
			asked = true
			if s.d.SubBase != nil {
				base = s.d.SubBase(ctx)
			}
		}
		return base != "" && strings.HasPrefix(u, base+"/")
	}
	var walk func(v any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, inner := range x {
				if k == "web_app" || k == "login_url" {
					m, _ := inner.(map[string]any)
					if u, _ := m["url"].(string); !allowed(u) {
						return errors.New(k + " may open only the panel's own pages")
					}
					continue
				}
				if err := walk(inner); err != nil {
					return err
				}
			}
		case []any:
			for _, inner := range x {
				if err := walk(inner); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, k := range []string{"reply_markup", "menu_button"} {
		raw := p[k]
		if raw == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return errors.New(k + " is not JSON")
		}
		if err := walk(v); err != nil {
			return err
		}
	}
	return nil
}

// pruneUpdates drops what no addon took for a day: Telegram would not have kept it either.
func (s *Server) pruneUpdates(ctx context.Context) {
	if _, err := s.d.Store.Q.PruneAddonUpdates(ctx, s.d.Now().Add(-keepUpdates).Unix()); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("addons api: prune updates", "err", err)
	}
}

// jsonObject reads a JSON object's members, refusing one given twice: the checks here would
// see one of them and Telegram might take the other.
func jsonObject(body []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("the body is not a JSON object")
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		k, ok := t.(string)
		if err != nil || !ok {
			return nil, errors.New("the body is not a JSON object")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, errors.New("the body is not a JSON object")
		}
		if _, twice := m[k]; twice {
			return nil, errors.New(k + " is given twice")
		}
		m[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') || dec.More() {
		return nil, errors.New("the body is not a JSON object")
	}
	return m, nil
}
