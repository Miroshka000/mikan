package addons

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// Settings are an adapter's settings as the admin filled them in; secrets included. They go
// with every call and the adapter keeps none of them.
type Settings map[string]any

// Field types. Any other is a type this panel does not know: the field cannot be filled
// in or saved (billing.fieldValue), and the admin is told so.
const (
	FieldString = "string" // one line, up to 4096 characters, an optional pattern
	FieldBool   = "bool"
	FieldSelect = "select" // one of Options
	FieldNumber = "number" // an integer, optionally within Min..Max
	FieldText   = "text"   // several lines, up to 4096 characters
	FieldCode   = "code"   // several lines of code, up to MaxCode bytes; set from a session only
)

// MaxCode is the longest value of a code field, in bytes.
const MaxCode = 64 << 10

// KnownField says whether the panel can render and validate a setting of this type.
func KnownField(t string) bool {
	switch t {
	case FieldString, FieldBool, FieldSelect, FieldNumber, FieldText, FieldCode:
		return true
	}
	return false
}

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label Text   `json:"label"`
}

// Field is one setting the adapter asks for.
type Field struct {
	Key      string   `json:"key"`
	Label    Text     `json:"label"`
	Type     string   `json:"type"` // string | bool | select | number | text | code
	Secret   bool     `json:"secret"`
	Required bool     `json:"required"`
	Pattern  string   `json:"pattern,omitempty"` // string only
	Options  []Option `json:"options,omitempty"` // select only
	Min      *int64   `json:"min,omitempty"`     // number only
	Max      *int64   `json:"max,omitempty"`     // number only
	// Default is shown as the placeholder. The panel does not store it: an addon applies
	// it itself while the key is absent.
	Default any  `json:"default,omitempty"`
	Help    Text `json:"help,omitempty"`
}

// Info is an adapter's description of itself.
type Info struct {
	ID           string   `json:"id"`
	Protocol     int      `json:"protocol"`
	Version      string   `json:"version"`
	Name         Text     `json:"name"`
	Currencies   []string `json:"currencies"`
	Capabilities []string `json:"capabilities"`
	Settings     []Field  `json:"settings"`
	Help         Text     `json:"help"`
}

func (i Info) Can(capability string) bool { return slices.Contains(i.Capabilities, capability) }

func (i Info) Takes(currency string) bool { return slices.Contains(i.Currencies, currency) }

// Error is an adapter's refusal: bad_settings, bad_credentials, provider_unavailable,
// not_found, bad_request, or its own code.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("adapter %d %s: %s", e.Status, e.Code, e.Message) }

// ErrUnreachable: the adapter did not answer.
var ErrUnreachable = errors.New("addon_unreachable")

type Client struct {
	base, token string
	hc          *http.Client
}

func NewClient(base, token string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, hc: hc}
}

// maxAnswer is the longest answer the panel reads from an adapter.
const maxAnswer = 1 << 20

// doBearer sends one request to an addon with its token and reads the answer, up to limit
// bytes: a longer one is ErrBadAnswer, a way that fails ErrUnreachable. in, when not nil,
// goes as the JSON body; hdr is added to the request.
func doBearer(ctx context.Context, hc *http.Client, token, method, url string, in []byte, hdr http.Header, limit int64) (*http.Response, []byte, error) {
	var body io.Reader
	if in != nil {
		body = bytes.NewReader(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if int64(len(data)) > limit {
		return nil, nil, ErrBadAnswer
	}
	return resp, data, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	resp, data, err := doBearer(ctx, c.hc, c.token, method, c.base+path, payload, nil, maxAnswer)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Code == "" {
			e.Code = "adapter_error"
		}
		return e
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("adapter %s: %w", path, err)
	}
	return nil
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.call(ctx, http.MethodGet, "/v1/info", nil, &i)
	return i, err
}

// Check asks the provider whether the settings work.
func (c *Client) Check(ctx context.Context, s Settings) error {
	return c.call(ctx, http.MethodPost, "/v1/check", map[string]any{"settings": s}, nil)
}

type InvoiceRequest struct {
	Settings       Settings `json:"settings"`
	PaymentID      int64    `json:"payment_id"`
	IdempotencyKey string   `json:"idempotency_key"`
	Amount         int64    `json:"amount"`
	Currency       string   `json:"currency"`
	Description    string   `json:"description"`
	ReturnURL      string   `json:"return_url"`
	WebhookURL     string   `json:"webhook_url"`
}

type Invoice struct {
	ExternalID string `json:"external_id"`
	PayURL     string `json:"pay_url"`
}

func (c *Client) CreateInvoice(ctx context.Context, r InvoiceRequest) (Invoice, error) {
	var inv Invoice
	if err := c.call(ctx, http.MethodPost, "/v1/invoices", r, &inv); err != nil {
		return inv, err
	}
	if inv.ExternalID == "" || !strings.HasPrefix(inv.PayURL, "https://") {
		return inv, &Error{Code: "adapter_bad_invoice", Message: "no id or no https payment link"}
	}
	return inv, nil
}

// Status of an invoice at the provider: pending, paid or canceled, with what was paid.
type Status struct {
	Status   string `json:"status"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (c *Client) Status(ctx context.Context, s Settings, externalID string) (Status, error) {
	var st Status
	err := c.call(ctx, http.MethodPost, "/v1/status", map[string]any{"settings": s, "external_id": externalID}, &st)
	return st, err
}

// Webhook hands the provider's request to the adapter, which says which invoice it is
// about ("" for one to ignore). The caller checks the invoice's status before trusting it.
func (c *Client) Webhook(ctx context.Context, s Settings, remoteIP string, headers http.Header, body []byte) (string, error) {
	var out struct {
		ExternalID string `json:"external_id"`
	}
	err := c.call(ctx, http.MethodPost, "/v1/webhook", map[string]any{"settings": s, "remote_ip": remoteIP, "headers": headers,
		"body": base64.StdEncoding.EncodeToString(body)}, &out)
	return out.ExternalID, err
}

// Refund sends a stable idempotency key so adapters can safely handle concurrent
// notifications and retries for the same late promo payment.
func (c *Client) Refund(ctx context.Context, s Settings, externalID string, amount int64, idempotencyKey string) error {
	return c.call(ctx, http.MethodPost, "/v1/refund", map[string]any{"settings": s, "external_id": externalID, "amount": amount, "idempotency_key": idempotencyKey}, nil)
}
