package panelimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 3x-ui (MHSanaei/3x-ui) keeps its clients inside the inbounds: GET
// <base>/panel/api/inbounds/list gives every inbound, its clients in settings (a JSON
// string) and their traffic in clientStats, in {"success", "msg", "obj"}. The base is the
// panel's address with its secret web path. Access is an API token (newer versions) or a
// session: POST <base>/login with a form, after GET <base>/csrf-token where the panel
// has one (its token goes in X-CSRF-Token). 3.x also lists the clients themselves
// (GET <base>/panel/api/clients/list), with the device limit (limitHwid) that the
// inbounds do not carry; older versions have no such list.
//
// A person in 3x-ui is a subscription: the clients that share a subId, on any inbound,
// are one mikan user, named after the first of them, and the subId is the old link's
// token, as Remnawave's short UUID is. A client without a subId is a user of its own.
// Limits are per client (per email) there: the user gets their sum, used traffic summed
// too; no limit, no term or no device limit on any of them is none for the user.

// xuiListBytes bounds the inbounds' list, which comes in one answer.
const xuiListBytes = 128 << 20

// xuiProtocols are the inbound protocols mikan has: a client only on others (vmess,
// wireguard, mtproto, …) is reported and left out.
var xuiProtocols = []string{"vless", "trojan", "shadowsocks", "hysteria", "hysteria2", "tuic"}

// xuiNow is the time a client's term is judged by; a variable for the tests.
var xuiNow = time.Now

type xuiAnswer struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

type xuiInbound struct {
	ID          int64            `json:"id"`
	Remark      string           `json:"remark"`
	Protocol    string           `json:"protocol"`
	Settings    string           `json:"settings"`
	ClientStats []xuiClientStats `json:"clientStats"`
}

type xuiClient struct {
	Email      string   `json:"email"`
	SubID      string   `json:"subId"`
	Enable     *xuiBool `json:"enable"`     // missing: on
	TotalGB    xuiInt   `json:"totalGB"`    // bytes, whatever the name says
	ExpiryTime xuiInt   `json:"expiryTime"` // unix ms; negative: that long from the first use
	LimitIP    xuiInt   `json:"limitIp"`
	LimitHwid  xuiInt   `json:"limitHwid"` // 3.x, on the clients' list
	TgID       xuiInt   `json:"tgId"`
	Comment    string   `json:"comment"`
}

type xuiClientStats struct {
	Email   string `json:"email"`
	Up      xuiInt `json:"up"`
	Down    xuiInt `json:"down"`
	AllTime xuiInt `json:"allTime"` // some versions: traffic before the last reset too
}

// xuiInt is a number 3x-ui wrote as a number or, in older versions, as a string; one
// that is not a number is 0, as 3x-ui itself takes it.
type xuiInt int64

func (n *xuiInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s != "" && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.ReplaceAll(strings.TrimSpace(str), " ", "")
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = xuiInt(v)
	} else if f, err := strconv.ParseFloat(s, 64); err == nil && f > -1e18 && f < 1e18 {
		*n = xuiInt(f)
	} else {
		*n = 0
	}
	return nil
}

// xuiBool is true, false, or the same as a string or a number.
type xuiBool bool

func (v *xuiBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(strings.TrimSpace(string(b)), `"`) {
	case "false", "0":
		*v = false
	default:
		*v = true
	}
	return nil
}

func fetchXUI(ctx context.Context, hc *http.Client, src Source) ([]User, error) {
	auth := func(r *http.Request) {}
	if src.Token != "" {
		token := src.Token
		auth = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	} else {
		var err error
		if hc, err = xuiLogin(ctx, hc, src); err != nil {
			return nil, err
		}
	}
	get := func(path string, limit int64, out any) error {
		req, err := http.NewRequest(http.MethodGet, src.URL+path, nil)
		if err != nil {
			return err
		}
		auth(req)
		// Without it 3x-ui answers a request it does not let in with 404, as to a scan.
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		var a xuiAnswer
		if err := getJSONLimit(ctx, hc, req, limit, &a); err != nil {
			return err
		}
		if !a.Success {
			return fmt.Errorf("%w: %s: %s", ErrAnswer, path, cut(a.Msg, 200))
		}
		if err := json.Unmarshal(a.Obj, out); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrAnswer, path, err)
		}
		return nil
	}
	var inbounds []xuiInbound
	if err := get("/panel/api/inbounds/list", xuiListBytes, &inbounds); err != nil {
		return nil, err
	}
	// The device limits, where the panel lists its clients (3.x); without the list the
	// limit of addresses stands in.
	hwid := map[string]int64{}
	var clients []xuiClient
	if err := get("/panel/api/clients/list", xuiListBytes, &clients); err != nil && !errors.Is(err, ErrAnswer) {
		return nil, err
	}
	for _, c := range clients {
		if c.LimitHwid > 0 {
			hwid[strings.ToLower(c.Email)] = int64(c.LimitHwid)
		}
	}
	return xuiUsers(inbounds, hwid, xuiNow())
}

// xuiLogin signs in with the admin's username and password: hc with the session's cookie.
func xuiLogin(ctx context.Context, hc *http.Client, src Source) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := *hc
	c.Jar = jar
	hc = &c
	// 3x-ui 3.x wants the token of the session it opens here on the login; older versions
	// have no /csrf-token, and whatever they answer there is not a token.
	var csrf string
	req, err := http.NewRequest(http.MethodGet, src.URL+"/csrf-token", nil)
	if err != nil {
		return nil, err
	}
	var a struct {
		Obj string `json:"obj"`
	}
	switch err := getJSON(ctx, hc, req, &a); {
	case err == nil:
		csrf = a.Obj
	case !errors.Is(err, ErrAnswer) && !errors.Is(err, ErrRedirect):
		return nil, err
	}
	form := url.Values{"username": {src.Username}, "password": {src.Password}}
	if req, err = http.NewRequest(http.MethodPost, src.URL+"/login", bytes.NewBufferString(form.Encode())); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	var login xuiAnswer
	if err := getJSON(ctx, hc, req, &login); err != nil {
		return nil, err
	}
	// A wrong password, a two-factor code that was not given, or too many tries.
	if !login.Success {
		return nil, ErrAuth
	}
	return hc, nil
}

// xuiPerson is the clients of one subscription.
type xuiPerson struct {
	clients   []xuiClient
	protocols []string
}

// xuiUsers makes the users out of the inbounds' clients, in the order they come.
func xuiUsers(inbounds []xuiInbound, hwid map[string]int64, now time.Time) ([]User, error) {
	sort.SliceStable(inbounds, func(i, j int) bool { return inbounds[i].ID < inbounds[j].ID })
	// Traffic is per email; 3.x repeats a client's on every inbound it is on.
	stats := map[string]xuiClientStats{}
	for _, in := range inbounds {
		for _, s := range in.ClientStats {
			k := strings.ToLower(strings.TrimSpace(s.Email))
			if old, ok := stats[k]; !ok || s.Up+s.Down > old.Up+old.Down {
				stats[k] = s
			}
		}
	}
	people := map[string]*xuiPerson{}
	var order []string
	clients := 0
	for _, in := range inbounds {
		var settings struct {
			Clients []xuiClient `json:"clients"`
		}
		if strings.TrimSpace(in.Settings) != "" {
			if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
				return nil, fmt.Errorf("%w: inbound %q: settings: %v", ErrAnswer, cut(in.Remark, 100), err)
			}
		}
		proto := strings.ToLower(strings.TrimSpace(in.Protocol))
		for _, c := range settings.Clients {
			if clients++; clients > 4*maxUsers {
				return nil, fmt.Errorf("%w: more than %d clients", ErrAnswer, 4*maxUsers)
			}
			c.Email, c.SubID = strings.TrimSpace(c.Email), strings.TrimSpace(c.SubID)
			key := "sub:" + c.SubID
			if c.SubID == "" {
				key = "email:" + strings.ToLower(c.Email)
			}
			p := people[key]
			if p == nil {
				p = &xuiPerson{}
				people[key] = p
				order = append(order, key)
			}
			p.clients = append(p.clients, c)
			if !slices.Contains(p.protocols, proto) {
				p.protocols = append(p.protocols, proto)
			}
		}
	}
	if len(order) > maxUsers {
		return nil, fmt.Errorf("%w: more than %d users", ErrAnswer, maxUsers)
	}
	out := make([]User, 0, len(order))
	for _, key := range order {
		out = append(out, people[key].user(stats, hwid, now))
	}
	return out, nil
}

func (p *xuiPerson) user(stats map[string]xuiClientStats, hwid map[string]int64, now time.Time) User {
	first := p.clients[0]
	u := User{Name: cut(first.Email, maxNameBytes), Status: StatusDisabled}
	if !slices.ContainsFunc(p.protocols, func(s string) bool { return slices.Contains(xuiProtocols, s) }) {
		u.Skip = "only on " + strings.Join(p.protocols, ", ") + " inbounds, which mikan has not"
	}
	if first.SubID != "" {
		u.Token = cut(first.SubID, 128)
		if !strongToken.MatchString(u.Token) {
			u.Token, u.WeakToken = "", true
		}
	}
	// Each email once: in 3.x a client is on every inbound it is attached to.
	seen := map[string]bool{}
	// On any of the clients: no traffic limit, no term, no device limit.
	unlimited, never, noDevices := false, false, false
	var expires time.Time
	var onHold time.Duration
	var notes []string
	for _, c := range p.clients {
		email := strings.ToLower(c.Email)
		if seen[email] {
			continue
		}
		seen[email] = true
		if c.Enable == nil || bool(*c.Enable) {
			u.Status = StatusActive
		}
		s := stats[email]
		used := max(0, int64(s.Up)) + max(0, int64(s.Down))
		u.Used += used
		u.Lifetime += max(used, int64(s.AllTime))
		if c.TotalGB > 0 {
			u.TrafficLimit += int64(c.TotalGB)
		} else {
			unlimited = true
		}
		switch e := int64(c.ExpiryTime); {
		case e > 0:
			if t := time.UnixMilli(e).UTC(); t.After(expires) {
				expires = t
			}
		case e < 0:
			// Checked before the multiplication, which would wrap a huge number.
			if -e > int64(maxTerm/time.Millisecond) {
				onHold = -1 // out of range: Normalize refuses it
			} else if d := time.Duration(-e) * time.Millisecond; onHold >= 0 && d > onHold {
				onHold = d
			}
		default:
			never = true
		}
		// The device limit of 3.x, else the limit of addresses at once, the nearest 2.x has.
		devices := hwid[email]
		if devices <= 0 {
			devices = int64(c.LimitIP)
		}
		if devices > 0 {
			u.DeviceLimit = max(u.DeviceLimit, devices)
		} else {
			noDevices = true
		}
		if c.TgID > 0 && u.Contact == "" {
			u.Contact = "tg:" + strconv.FormatInt(int64(c.TgID), 10)
		}
		if note := strings.TrimSpace(c.Comment); note != "" && !slices.Contains(notes, note) {
			notes = append(notes, note)
		}
	}
	u.Note = cut(strings.Join(notes, "\n"), maxNoteBytes)
	if unlimited {
		u.TrafficLimit = 0
	}
	if noDevices {
		u.DeviceLimit = 0
	}
	switch {
	case never:
	case !expires.IsZero():
		u.Expires = expires
	case onHold != 0:
		u.OnHold = onHold
		if u.Status == StatusActive {
			u.Status = StatusOnHold
		}
	}
	if u.Status == StatusActive {
		switch {
		case !u.Expires.IsZero() && !u.Expires.After(now):
			u.Status = StatusExpired
		case u.TrafficLimit > 0 && u.Used >= u.TrafficLimit:
			u.Status = StatusLimited
		}
	}
	return u
}
