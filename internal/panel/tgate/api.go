// Package tgate is the panel's Telegram gateway: everything that touches the bot's token,
// money and access. It keeps the way to Telegram (route.go), the update loop with its
// offset, the outbox with Telegram's limits, the flood guard, the accounts that wrote to
// the bot, Telegram Stars, link codes and the Mini App's sign-in, the infrastructure
// admin's chat and the bot's channel. The bot's screens (tgbot) run in the bot addon: the
// gateway buffers the updates it does not handle itself for the addon, and the addon sends
// through the gateway's outbox over the Bot API proxy (addons/panelapi,
// docs/pipeline/addons.md).
package tgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mikan/internal/textutil"
)

// DefaultAPI is Telegram's Bot API; tests point the bot at a fake one.
const DefaultAPI = "https://api.telegram.org"

// Client calls the Bot API with one token.
type Client struct {
	base  string
	token string
	hc    *http.Client
	// How long a call may take: a long poll holds its request for pollTimeout and gets
	// this much on top, any other call only this much, an answer to a pre-checkout query
	// preCheckoutTimeout, which Telegram's ten seconds leave it.
	callTimeout time.Duration
}

// callTimeout is a call's time, getUpdates apart. Telegram answers in a fraction of it;
// past it the way there is down, and a call that hangs holds a worker of the outbox.
const callTimeout = 15 * time.Second

// preCheckoutTimeout: Telegram cancels the payment of a query not answered within ten
// seconds, so the answer is not worth waiting for past eight.
const preCheckoutTimeout = 8 * time.Second

// NewClient: rt is the way to Telegram (see route.go); nil goes straight.
func NewClient(base, token string, rt http.RoundTripper) *Client {
	if base == "" {
		base = DefaultAPI
	}
	// No timeout on the client as a whole: each call sets its own (see call).
	return &Client{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{Transport: rt}, callTimeout: callTimeout}
}

// APIError is Telegram's refusal. Code 403 means the user blocked the bot; 429 carries
// RetryAfter.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Description) }

// ErrUnreachable: the Bot API did not answer.
var ErrUnreachable = errors.New("telegram unreachable")

func (c *Client) call(parent context.Context, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	timeout := c.callTimeout
	switch method {
	case "getUpdates":
		timeout += pollTimeout
	case "answerPreCheckoutQuery":
		timeout = min(timeout, preCheckoutTimeout)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(parent, req, out)
}

// unreachable is the error of a request that got no answer: the caller's own when its
// context ended, else ErrUnreachable. The error text would carry the URL, and the URL the
// token: only the cause is kept.
func unreachable(parent context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%w: %v", ErrUnreachable, err)
}

// do sends a request made for the Bot API and reads its answer into out.
func (c *Client) do(parent context.Context, req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return unreachable(parent, err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("%w: HTTP %d is not a Bot API answer", ErrUnreachable, resp.StatusCode)
	}
	if !r.OK {
		return &APIError{Code: r.ErrorCode, Description: r.Description, RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// documentTimeout: a file goes up in one request, which over a slow way to Telegram (a
// node, a proxy) takes longer than a message.
const documentTimeout = 2 * time.Minute

// MaxDocument is the largest file a bot may send.
const MaxDocument = 50 << 20

// SendDocument sends size bytes from file to a chat as a document named name, with a
// caption under it. The body is read from file as it goes up, with its exact length: the
// file is never held in memory.
func (c *Client) SendDocument(parent context.Context, chat int64, name string, file io.Reader, size int64, caption string) (Message, error) {
	if size > MaxDocument {
		return Message{}, fmt.Errorf("a document of %d bytes is over the %d a bot may send", size, MaxDocument)
	}
	// The parts before the file and the closing boundary after it, written by the same
	// multipart writer so they fit together.
	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	_ = w.WriteField("chat_id", strconv.FormatInt(chat, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	if _, err := w.CreateFormFile("document", name); err != nil {
		return Message{}, err
	}
	prefix := append([]byte(nil), head.Bytes()...)
	head.Reset()
	if err := w.Close(); err != nil {
		return Message{}, err
	}
	trailer := head.Bytes()
	ctx, cancel := context.WithTimeout(parent, documentTimeout)
	defer cancel()
	body := io.MultiReader(bytes.NewReader(prefix), io.LimitReader(file, size), bytes.NewReader(trailer))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/sendDocument", body)
	if err != nil {
		return Message{}, err
	}
	req.ContentLength = int64(len(prefix)) + size + int64(len(trailer))
	req.Header.Set("Content-Type", w.FormDataContentType())
	var m Message
	return m, c.do(parent, req, &m)
}

// User is a Telegram account.
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	// SuccessfulPayment: a Stars invoice was paid (a service message from Telegram).
	SuccessfulPayment *SuccessfulPayment `json:"successful_payment"`
	// RefundedPayment: a Stars payment was refunded (a service message from Telegram).
	RefundedPayment *RefundedPayment `json:"refunded_payment"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID         int64             `json:"update_id"`
	Message          *Message          `json:"message"`
	CallbackQuery    *CallbackQuery    `json:"callback_query"`
	PreCheckoutQuery *PreCheckoutQuery `json:"pre_checkout_query"`
	// Raw is the update as Telegram sent it: a bot addon gets it whole, with what these
	// types leave out.
	Raw json.RawMessage `json:"-"`
}

func (u *Update) UnmarshalJSON(b []byte) error {
	type plain Update
	if err := json.Unmarshal(b, (*plain)(u)); err != nil {
		return err
	}
	u.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// Button is an inline keyboard button: one of callback data, a link or the Mini App.
type Button struct {
	Text         string  `json:"text"`
	CallbackData string  `json:"callback_data,omitempty"`
	URL          string  `json:"url,omitempty"`
	WebApp       *WebApp `json:"web_app,omitempty"`
}

type WebApp struct {
	URL string `json:"url"`
}

type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

// Me is getMe: the token's bot.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// ChatMember is getChatMember's answer: an account's place in a chat.
type ChatMember struct {
	// Status is creator, administrator, member, restricted, left or kicked.
	Status string `json:"status"`
	// IsMember: a restricted account is still in the chat.
	IsMember bool `json:"is_member"`
}

// In: the account is in the chat, whatever its rights there.
func (m ChatMember) In() bool {
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return m.IsMember
	}
	return false
}

// ChatMember is getChatMember: where user is in chat, a numeric id or @username. In a
// channel only its admins may ask.
func (c *Client) ChatMember(ctx context.Context, chat string, user int64) (ChatMember, error) {
	var m ChatMember
	err := c.call(ctx, "getChatMember", map[string]any{"chat_id": chat, "user_id": user}, &m)
	return m, err
}

// pollTimeout: how long getUpdates waits for news.
const pollTimeout = 50 * time.Second

func (c *Client) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(pollTimeout / time.Second),
		"allowed_updates": []string{"message", "callback_query", "pre_checkout_query"}}, &ups)
	return ups, err
}

// Send posts an HTML message; kb may be nil. A silent one arrives without a sound.
func (c *Client) Send(ctx context.Context, chat int64, text string, kb *Keyboard, silent bool) (Message, error) {
	var m Message
	in := htmlText(chat, text)
	if silent {
		in["disable_notification"] = true
	}
	if kb != nil {
		in["reply_markup"] = kb
	}
	err := c.call(ctx, "sendMessage", in, &m)
	if unparsable(err) {
		asPlain(in, text)
		err = c.call(ctx, "sendMessage", in, &m)
	}
	return m, err
}

// unparsable: Telegram refused the message's HTML. The admin's texts are made to parse
// (markdown.go); should one still not, it goes as plain text instead of not at all.
func unparsable(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == 400 && strings.Contains(strings.ToLower(ae.Description), "can't parse entities")
}

// asPlain makes a request's HTML text plain.
func asPlain(in map[string]any, text string) {
	in["text"] = PlainText(text)
	delete(in, "parse_mode")
}

// PlainText is what an HTML message reads as text: for the rare text Telegram still
// refuses, sent again without formatting rather than not at all.
func PlainText(s string) string {
	var out strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		out.WriteString(s[:i])
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			s = s[i+1:]
			continue
		}
		s = s[i+j+1:]
	}
	out.WriteString(s)
	return html.UnescapeString(out.String())
}

// SendTo is Send for a Telegram channel target: its numeric chat id or @username.
func (c *Client) SendTo(ctx context.Context, chat string, text string, silent bool) (Message, error) {
	var m Message
	in := htmlText(chat, text)
	if silent {
		in["disable_notification"] = true
	}
	err := c.call(ctx, "sendMessage", in, &m)
	return m, err
}

// EditTo edits a message in a channel identified by numeric id or @username.
func (c *Client) EditTo(ctx context.Context, chat string, msg int64, text string) error {
	in := htmlText(chat, text)
	in["message_id"] = msg
	return notModified(c.call(ctx, "editMessageText", in, nil))
}

// PinTo pins a channel message without notifying subscribers.
func (c *Client) PinTo(ctx context.Context, chat string, msg int64) error {
	return c.call(ctx, "pinChatMessage", map[string]any{"chat_id": chat, "message_id": msg, "disable_notification": true}, nil)
}

// Edit replaces a message's text and buttons. An edit to the same content is not an error.
func (c *Client) Edit(ctx context.Context, chat, msg int64, text string, kb *Keyboard) error {
	in := htmlText(chat, text)
	in["message_id"] = msg
	if kb != nil {
		in["reply_markup"] = kb
	}
	err := c.call(ctx, "editMessageText", in, nil)
	if unparsable(err) {
		asPlain(in, text)
		err = c.call(ctx, "editMessageText", in, nil)
	}
	return notModified(err)
}

// notModified: an edit to the same content is no error.
func notModified(err error) error {
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// htmlText is the start of a request that sends or edits an HTML text in chat (a numeric id
// or a channel's @username), with no link preview.
func htmlText(chat any, text string) map[string]any {
	return map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
}

func (c *Client) Delete(ctx context.Context, chat, msg int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chat, "message_id": msg}, nil)
}

// Answer stops the spinner on a pressed button; text, when set, pops up briefly.
func (c *Client) Answer(ctx context.Context, id, text string) error {
	in := map[string]any{"callback_query_id": id}
	if text != "" {
		in["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", in, nil)
}

// DropWebhook switches the bot to polling: a webhook left by another service would make
// every getUpdates fail.
func (c *Client) DropWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

// SetCommands shows /start in the command menu.
func (c *Client) SetCommands(ctx context.Context, desc string) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{{"command": "start", "description": desc}}}, nil)
}

// SetMenuButton puts the Mini App next to the input field of every private chat (url
// empty: back to the command list).
func (c *Client) SetMenuButton(ctx context.Context, text, url string) error {
	btn := map[string]any{"type": "commands"}
	if url != "" {
		btn = map[string]any{"type": "web_app", "text": text, "web_app": map[string]string{"url": url}}
	}
	return c.call(ctx, "setChatMenuButton", map[string]any{"menu_button": btn}, nil)
}

// SuccessfulPayment is what Telegram reports after a Stars payment.
type SuccessfulPayment struct {
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
	ChargeID       string `json:"telegram_payment_charge_id"`
}

// RefundedPayment is what Telegram reports after a Stars payment is refunded, by the bot
// or by Telegram itself.
type RefundedPayment struct {
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
	ChargeID       string `json:"telegram_payment_charge_id"`
}

// PreCheckoutQuery asks the bot to confirm a payment within ten seconds.
type PreCheckoutQuery struct {
	ID             string `json:"id"`
	From           User   `json:"from"`
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

// InvoiceLink makes a link to pay in Telegram Stars (XTR, no provider token).
func (c *Client) InvoiceLink(ctx context.Context, title, description, payload string, stars int64) (string, error) {
	var link string
	err := c.call(ctx, "createInvoiceLink", map[string]any{"title": textutil.ClipRunes(title, 32), "description": textutil.ClipRunes(description, 255), "payload": payload,
		"currency": "XTR", "prices": []map[string]any{{"label": textutil.ClipRunes(title, 32), "amount": stars}}}, &link)
	return link, err
}

// AnswerPreCheckout lets the payment go ahead, or refuses it with a reason the buyer sees.
func (c *Client) AnswerPreCheckout(ctx context.Context, id string, ok bool, reason string) error {
	in := map[string]any{"pre_checkout_query_id": id, "ok": ok}
	if !ok {
		in["error_message"] = reason
	}
	return c.call(ctx, "answerPreCheckoutQuery", in, nil)
}

// RefundStars returns a Stars payment.
func (c *Client) RefundStars(ctx context.Context, user int64, chargeID string) error {
	err := c.call(ctx, "refundStarPayment", map[string]any{"user_id": user, "telegram_payment_charge_id": chargeID}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(strings.ToUpper(apiErr.Description), "CHARGE_ALREADY_REFUNDED") {
		return nil
	}
	return err
}

// Forward makes a call of the Bot API as an addon put it (the Bot API proxy): the body as it
// came, with its content type, and Telegram's answer as it is. Only a way to Telegram that
// fails is an error; a refusal comes back in the answer, and as an *APIError when it is a
// 429, so the outbox waits it out. The token never leaves this client.
func (c *Client) Forward(parent context.Context, method, contentType string, body []byte) (int, []byte, error) {
	timeout := c.callTimeout
	if strings.HasPrefix(contentType, "multipart/") {
		timeout = documentTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, unreachable(parent, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		var r struct {
			Description string `json:"description"`
			Parameters  struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		_ = json.Unmarshal(answer, &r)
		return resp.StatusCode, answer, &APIError{Code: 429, Description: r.Description, RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	return resp.StatusCode, answer, nil
}

// maxAnswer bounds an answer of the Bot API read for an addon.
const maxAnswer = 4 << 20
