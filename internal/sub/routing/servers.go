package routing

import (
	"errors"

	"mikan/internal/sub/model"
)

// Servers is how the apps pick a server. The zero value is the profile as it was: a group
// per country, the fastest picked every five minutes.
type Servers struct {
	NoCountries bool   `json:"no_countries,omitempty"` // no group per node
	Auto        string `json:"auto,omitempty"`         // "" the fastest (url-test), "fallback" the first that works
	Interval    int    `json:"interval,omitempty"`     // seconds between checks; 0: 300
}

// The defaults of the automatic groups, the profile as it always was.
const (
	ProbeURL         = "https://www.gstatic.com/generate_204"
	defaultInterval  = 300
	defaultTolerance = 50
)

// Balance is how the automatic groups (the automatic choice and the country groups) test
// their servers.
type Balance struct {
	Type      model.GroupType // of the automatic choice; the country groups are the fastest
	URL       string
	Interval  int // seconds
	Tolerance int // milliseconds
}

// Balance is the balance of the settings.
func (s Servers) Balance() Balance {
	b := Balance{Type: model.URLTest, URL: ProbeURL, Interval: defaultInterval, Tolerance: defaultTolerance}
	if s.Auto == "fallback" {
		b.Type = model.Fallback
	}
	if s.Interval > 0 {
		b.Interval = s.Interval
	}
	return b
}

// check refuses settings no app would take, with a code for the admin panel.
func (s Servers) check() error {
	if s.Auto != "" && s.Auto != "fallback" || s.Interval != 0 && (s.Interval < 30 || s.Interval > 86400) {
		return errors.New("routes_servers")
	}
	return nil
}
