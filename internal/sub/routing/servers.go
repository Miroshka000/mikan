package routing

import (
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"

	"mikan/internal/sub/model"
)

// Servers is how the apps pick a server. The zero value is the profile as it was: a group
// per country, the fastest picked every five minutes. Mihomo, SingBox and Xray are each
// core's own settings over the common ones: what its apps can do and the others cannot.
type Servers struct {
	NoCountries bool           `json:"no_countries,omitempty"` // no group per node
	Auto        string         `json:"auto,omitempty"`         // "" the fastest (url-test), "fallback" the first that works
	Interval    int            `json:"interval,omitempty"`     // seconds between checks; 0: 300
	Mihomo      MihomoServers  `json:"mihomo,omitzero"`
	SingBox     SingBoxServers `json:"singbox,omitzero"`
	Xray        XrayServers    `json:"xray,omitzero"`
}

// MihomoServers are the Clash apps' own: the kind of the automatic choice, and how it and
// the country groups test the servers. Zero values keep the common settings.
type MihomoServers struct {
	Type      string `json:"type,omitempty" enum:"url-test,fallback,load-balance" doc:"Автовыбор в приложениях Clash: url-test — самый быстрый, fallback — первый рабочий, load-balance — нагрузка по всем"`
	Interval  int    `json:"interval,omitempty" doc:"Секунды между проверками, 30–86400; 0 — общая настройка"`
	Tolerance int    `json:"tolerance,omitempty" doc:"Миллисекунды: на сервер быстрее меньше чем на столько не переключаться, 0–5000; 0 — 50"`
}

// SingBoxServers are the sing-box apps' own: how the automatic groups test the servers.
type SingBoxServers struct {
	Interval  int `json:"interval,omitempty" doc:"Секунды между проверками, 30–86400; 0 — общая настройка"`
	Tolerance int `json:"tolerance,omitempty" doc:"Миллисекунды, 0–5000; 0 — 50"`
}

// XrayServers are the xray apps' own: the balancer's strategy and its observatory. Sampling,
// Tolerance, Expected and MaxRTT are of leastLoad alone.
type XrayServers struct {
	Strategy  string  `json:"strategy,omitempty" enum:"leastPing,leastLoad,random,roundRobin" doc:"Стратегия балансировщика; пусто — по виду группы (самый быстрый — leastPing)"`
	ProbeURL  string  `json:"probe_url,omitempty" maxLength:"500" doc:"Адрес проверки серверов (http или https); пусто — общий"`
	Interval  int     `json:"interval,omitempty" doc:"Секунды между проверками, 30–86400; 0 — общая настройка"`
	Sampling  int     `json:"sampling,omitempty" doc:"leastLoad: сколько последних проверок учитывать, 1–100"`
	Tolerance float64 `json:"tolerance,omitempty" doc:"leastLoad: доля неудачных проверок, с которой сервер ещё годится, 0–1"`
	Expected  int     `json:"expected,omitempty" doc:"leastLoad: из скольких лучших серверов выбирать, 1–100"`
	MaxRTT    int     `json:"max_rtt,omitempty" doc:"leastLoad: миллисекунды, дольше которых сервер не берётся, 1–60000"`
}

// The defaults of the automatic groups, the profile as it always was.
const (
	ProbeURL         = "https://www.gstatic.com/generate_204"
	defaultInterval  = 300
	defaultTolerance = 50
	maxTolerance     = 5000
)

// The strategies of an xray balancer.
const (
	XrayLeastPing  = "leastPing"
	XrayLeastLoad  = "leastLoad"
	XrayRandom     = "random"
	XrayRoundRobin = "roundRobin"
)

// Balance is how a core's automatic groups (the automatic choice and the country groups)
// test their servers: the common settings with the core's own over them.
type Balance struct {
	Type      model.GroupType // of the automatic choice; the country groups are the fastest
	URL       string
	Interval  int // seconds
	Tolerance int // milliseconds
}

func (s Servers) balance() Balance {
	b := Balance{Type: model.URLTest, URL: ProbeURL, Interval: defaultInterval, Tolerance: defaultTolerance}
	if s.Auto == "fallback" {
		b.Type = model.Fallback
	}
	if s.Interval > 0 {
		b.Interval = s.Interval
	}
	return b
}

// MihomoBalance is the Clash apps' balance.
func (s Servers) MihomoBalance() Balance {
	b := s.balance()
	if t := s.Mihomo.Type; t != "" {
		b.Type = model.GroupType(t)
	}
	if s.Mihomo.Interval > 0 {
		b.Interval = s.Mihomo.Interval
	}
	if s.Mihomo.Tolerance > 0 {
		b.Tolerance = s.Mihomo.Tolerance
	}
	return b
}

// SingBoxBalance is the sing-box apps' balance.
func (s Servers) SingBoxBalance() Balance {
	b := s.balance()
	if s.SingBox.Interval > 0 {
		b.Interval = s.SingBox.Interval
	}
	if s.SingBox.Tolerance > 0 {
		b.Tolerance = s.SingBox.Tolerance
	}
	return b
}

// XrayBalance is the xray apps' balance; the strategy and the leastLoad settings are in
// s.Xray.
func (s Servers) XrayBalance() Balance {
	b := s.balance()
	if s.Xray.ProbeURL != "" {
		b.URL = s.Xray.ProbeURL
	}
	if s.Xray.Interval > 0 {
		b.Interval = s.Xray.Interval
	}
	return b
}

// check refuses settings no app would take: the error is a code for the admin panel, of
// the common settings (routes_servers) or of one core's.
func (s Servers) check() error {
	interval := func(n int) bool { return n == 0 || n >= 30 && n <= 86400 }
	if s.Auto != "" && s.Auto != "fallback" || !interval(s.Interval) {
		return errors.New("routes_servers")
	}
	if m := s.Mihomo; !slices.Contains([]string{"", string(model.URLTest), string(model.Fallback), string(model.LoadBalance)}, m.Type) ||
		!interval(m.Interval) || m.Tolerance < 0 || m.Tolerance > maxTolerance {
		return errors.New("routes_servers_mihomo")
	}
	if b := s.SingBox; !interval(b.Interval) || b.Tolerance < 0 || b.Tolerance > maxTolerance {
		return errors.New("routes_servers_singbox")
	}
	x := s.Xray
	load := x.Strategy == XrayLeastLoad
	switch {
	case !slices.Contains([]string{"", XrayLeastPing, XrayLeastLoad, XrayRandom, XrayRoundRobin}, x.Strategy),
		x.ProbeURL != "" && !probeURL(x.ProbeURL),
		!interval(x.Interval),
		// What only leastLoad reads is refused with another strategy: it would sit in the
		// settings doing nothing.
		!load && (x.Sampling != 0 || x.Tolerance != 0 || x.Expected != 0 || x.MaxRTT != 0),
		x.Sampling < 0 || x.Sampling > 100, x.Expected < 0 || x.Expected > 100, x.MaxRTT < 0 || x.MaxRTT > 60000,
		math.IsNaN(x.Tolerance) || x.Tolerance < 0 || x.Tolerance > 1:
		return errors.New("routes_servers_xray")
	}
	return nil
}

// probeURL: an http or https address the apps can request, with nothing in it that would
// break the config it goes into.
func probeURL(s string) bool {
	if len(s) > 500 || strings.ContainsAny(s, " \t\r\n\"'\\") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}
