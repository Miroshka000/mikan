package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// A support operator the admin did not let see the subscribers gets no names, Telegram
// accounts, contacts, notes, addresses, device ids or subscription links. The endpoints
// that carry them leave the links out themselves (hidesLinks); this is the net under all of
// them: the JSON of every answer to such an operator goes through maskPeople, by the names
// the API gives these fields, so an endpoint added later is covered too.

const keyMask ctxKey = 102

// maskState is shared by the response writer and the middleware of one request: the
// middleware turns the masking on once it knows who the caller is.
type maskState struct{ on atomic.Bool }

// maskResponses wraps the API so that an answer to a caller whose maskState is on is held,
// masked and then sent. Other answers pass through as they are written.
func maskResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &maskState{}
		mw := &maskWriter{ResponseWriter: w, state: st}
		next.ServeHTTP(mw, r.WithContext(context.WithValue(r.Context(), keyMask, st)))
		mw.finish()
	})
}

type maskWriter struct {
	http.ResponseWriter
	state   *maskState
	decided bool // the status was given and the way (hold or pass) chosen
	hold    bool
	status  int
	buf     bytes.Buffer
}

func (w *maskWriter) WriteHeader(code int) {
	if w.decided {
		return
	}
	w.decided, w.status = true, code
	if w.state.on.Load() {
		w.hold = true
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *maskWriter) Write(b []byte) (int, error) {
	rw := w.ResponseWriter
	if !w.decided {
		// An answer written without a status of its own goes out with its type said: the
		// handler's own, or what net/http would sniff from these first bytes, and nosniff.
		h := rw.Header()
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", http.DetectContentType(b))
		}
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
	}
	if w.hold {
		return w.buf.Write(b)
	}
	return rw.Write(b)
}

func (w *maskWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok && !w.hold {
		f.Flush()
	}
}

// Hijack hands the connection over for a protocol upgrade (a WebSocket); the answer is then
// the handler's own.
func (w *maskWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is not supported")
	}
	w.decided, w.hold = true, false
	return h.Hijack()
}

func (w *maskWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finish sends what was held: the JSON masked, anything else as it is. No answer that is
// not JSON names a person: the metrics hold no user, the pictures are the page's own, and a
// transfer's screenshot refuses such a reader itself (topupPhoto).
func (w *maskWriter) finish() {
	if !w.hold {
		return
	}
	body := w.buf.Bytes()
	h := w.ResponseWriter.Header()
	ct := h.Get("Content-Type")
	if strings.Contains(ct, "json") && len(body) > 0 {
		body = maskPeople(body)
	}
	// The held answer goes out whole, so its type is said, not left to the browser: the
	// handler's own, or what net/http would have sniffed from the same bytes.
	if ct == "" && len(body) > 0 {
		ct = http.DetectContentType(body)
	}
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Del("Content-Length")
	if len(body) > 0 {
		h.Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(body)
}

// maskPeople hides the people in a JSON document. A body it cannot read is not sent: an
// answer it cannot check is not worth the chance of leaking.
func maskPeople(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []byte(`{"detail":"masking_failed"}`)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(maskValue(v, "")); err != nil {
		return []byte(`{"detail":"masking_failed"}`)
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

// peopleStrings are the fields that are text about a person, whatever object holds them.
var peopleStrings = map[string]bool{"contact": true, "note": true, "tg_username": true, "hwid": true, "sub_url": true}

func maskValue(v any, parent string) any {
	switch t := v.(type) {
	case []any:
		for i := range t {
			t[i] = maskValue(t[i], parent)
		}
		return t
	case map[string]any:
		return maskObject(t, parent)
	}
	return v
}

func maskObject(m map[string]any, parent string) map[string]any {
	_, hasUserID := m["user_id"]
	_, hasHWID := m["hwid"]
	_, hasSubURL := m["sub_url"]
	_, hasContact := m["contact"]
	// A user (the list, the card, the dashboard's top), a device and a payment name a person
	// with the plain "name" or "user_name"; a number stands in for the name.
	person := hasHWID || hasSubURL || hasContact || parent == "top_month"
	// An address is a person's by default (a device list names no user next to it, a hit
	// does); it stays only in what describes a server: a node (address, host), a probe
	// through a cascade or WARP (ok, checked_at: the exit address) and a neighbor scan.
	_, hasAddress := m["address"]
	_, hasHost := m["host"]
	_, hasChecked := m["checked_at"]
	_, hasScanned := m["scanned"]
	infrastructure := (hasAddress || hasHost || hasChecked || hasScanned) && !hasUserID && !person
	id := ""
	if n, ok := m["user_id"].(json.Number); ok {
		id = "#" + n.String()
	} else if n, ok := m["id"].(json.Number); ok && person {
		id = "#" + n.String()
	}
	for k, val := range m {
		switch {
		case peopleStrings[k]:
			if _, isString := val.(string); isString {
				m[k] = ""
			}
		case k == "tg_id":
			m[k] = json.Number("0")
		case k == "online_ips":
			m[k] = []any{}
		case k == "name" && person, k == "user_name":
			if _, isString := val.(string); isString {
				m[k] = id
			}
		case (k == "ip" || k == "last_ip") && !infrastructure:
			m[k] = ""
		case k == "custom_fields" && person:
			// The admin's own fields of a user may hold anything about the person: a phone, an order.
			if _, isObject := val.(map[string]any); isObject {
				m[k] = map[string]any{}
			}
		case k == "telegram", k == "legacy":
			// The linked Telegram account and the old links of an imported panel: gone.
			if _, isObject := val.(map[string]any); isObject {
				delete(m, k)
			}
		default:
			m[k] = maskValue(val, k)
		}
	}
	return m
}
