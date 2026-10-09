// Package tgbot is the panel's Telegram bot for subscription owners: a menu on inline
// buttons that edits one message in place, linking a subscription to a Telegram account,
// notifications about the term and traffic, broadcasts, and the Mini App's sign-in.
package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

// do sends a request made for the Bot API and reads its answer into out.
func (c *Client) do(parent context.Context, req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		// The error text would carry the URL, and the URL the token: only the cause is kept.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
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
	in := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if silent {
		in["disable_notification"] = true
	}
	if kb != nil {
		in["reply_markup"] = kb
	}
	err := c.call(ctx, "sendMessage", in, &m)
	return m, err
}

// SendTo is Send for a Telegram channel target: its numeric chat id or @username.
func (c *Client) SendTo(ctx context.Context, chat string, text string, silent bool) (Message, error) {
	var m Message
	in := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if silent {
		in["disable_notification"] = true
	}
	err := c.call(ctx, "sendMessage", in, &m)
	return m, err
}

// EditTo edits a message in a channel identified by numeric id or @username.
func (c *Client) EditTo(ctx context.Context, chat string, msg int64, text string) error {
	err := c.call(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": msg, "text": text,
		"parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}, nil)
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// PinTo pins a channel message without notifying subscribers.
func (c *Client) PinTo(ctx context.Context, chat string, msg int64) error {
	return c.call(ctx, "pinChatMessage", map[string]any{"chat_id": chat, "message_id": msg, "disable_notification": true}, nil)
}

// Edit replaces a message's text and buttons. An edit to the same content is not an error.
func (c *Client) Edit(ctx context.Context, chat, msg int64, text string, kb *Keyboard) error {
	in := map[string]any{"chat_id": chat, "message_id": msg, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if kb != nil {
		in["reply_markup"] = kb
	}
	err := c.call(ctx, "editMessageText", in, nil)
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
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
	err := c.call(ctx, "createInvoiceLink", map[string]any{"title": truncate(title, 32), "description": truncate(description, 255), "payload": payload,
		"currency": "XTR", "prices": []map[string]any{{"label": truncate(title, 32), "amount": stars}}}, &link)
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

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
