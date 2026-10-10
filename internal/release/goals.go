package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Goals are what the project collects money for: each goal is a feature that is taken into
// work once its sum is collected. They live in .github/goals.json, which the goals workflow
// signs into the release index ("goals" next to "releases"); the panel shows them from
// there, the documentation site from the file itself.
//
//	{"donate": "https://…", "items": [{"id": "device-addon", "title": {"ru": "…", "en": "…"},
//	  "about": {"ru": "…", "en": "…"}, "target": 150, "raised": 40, "currency": "USD",
//	  "status": "open", "version": ""}]}
//
// Readers are lenient like with releases: a goal they cannot use is left out, never fatal.
type Goals struct {
	// Donate is where money goes when a goal has no link of its own.
	Donate string `json:"donate"`
	Items  []Goal `json:"items"`
}

type Goal struct {
	ID string `json:"id"`
	// Title and About are by language: ru and en, at least one of them.
	Title    map[string]string `json:"title"`
	About    map[string]string `json:"about,omitempty"`
	Target   int               `json:"target"`
	Raised   int               `json:"raised"`
	Currency string            `json:"currency"`
	Status   string            `json:"status"`
	// Version is the release the goal shipped in, once it is done.
	Version string `json:"version,omitempty"`
	// URL is where to give money for this goal; empty: Goals.Donate.
	URL string `json:"url,omitempty"`
}

// The states of a goal.
const (
	GoalOpen    = "open"    // collecting
	GoalWorking = "working" // collected, in work
	GoalDone    = "done"    // shipped, in Version
)

// MaxGoals is how many goals an index carries; the panel shows the first few. Panels up to
// 0.5.0.4 read only the first 20, so the open goals go first and the done ones last.
const MaxGoals = 40

var (
	goalID       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	goalCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
)

// check says what is wrong with a goal, nil when a reader can show it.
func (g Goal) check() error {
	switch {
	case !goalID.MatchString(g.ID):
		return fmt.Errorf("id %q: lowercase letters, digits and dashes", g.ID)
	case g.Title["ru"] == "" && g.Title["en"] == "":
		return errors.New("no title in ru or en")
	case g.Target <= 0 || g.Target > 10_000_000:
		return fmt.Errorf("target %d", g.Target)
	case g.Raised < 0 || g.Raised > 10_000_000:
		return fmt.Errorf("raised %d", g.Raised)
	case !goalCurrency.MatchString(g.Currency):
		return fmt.Errorf("currency %q: three capital letters, like USD", g.Currency)
	case g.Status != GoalOpen && g.Status != GoalWorking && g.Status != GoalDone:
		return fmt.Errorf("status %q: open, working or done", g.Status)
	case g.Status == GoalDone && versionParts(g.Version) == nil:
		return fmt.Errorf("a done goal names its release: version %q", g.Version)
	case g.Version != "" && versionParts(g.Version) == nil:
		return fmt.Errorf("version %q", g.Version)
	case g.URL != "" && !httpsURL(g.URL):
		return fmt.Errorf("url %q: https only", g.URL)
	}
	for _, texts := range []map[string]string{g.Title, g.About} {
		for lang, s := range texts {
			if lang != "ru" && lang != "en" {
				return fmt.Errorf("language %q: ru or en", lang)
			}
			if !utf8.ValidString(s) || utf8.RuneCountInString(s) > 400 || strings.ContainsAny(s, "<>") {
				return fmt.Errorf("%s text: up to 400 characters, no < or >", lang)
			}
		}
	}
	for lang, s := range g.Title {
		if utf8.RuneCountInString(s) > 80 {
			return fmt.Errorf("%s title: up to 80 characters", lang)
		}
	}
	return nil
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(s) <= 300
}

// ParseGoals reads a goals file strictly, as the workflow that signs it does: every goal
// must be one the readers can show, ids unique, at most MaxGoals.
func ParseGoals(data []byte) (Goals, error) {
	var g Goals
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		return Goals{}, fmt.Errorf("goals: %w", err)
	}
	if !httpsURL(g.Donate) {
		return Goals{}, fmt.Errorf("goals: donate %q: an https link", g.Donate)
	}
	if len(g.Items) > MaxGoals {
		return Goals{}, fmt.Errorf("goals: %d goals, at most %d", len(g.Items), MaxGoals)
	}
	seen := map[string]bool{}
	for _, it := range g.Items {
		if err := it.check(); err != nil {
			return Goals{}, fmt.Errorf("goals: %s: %w", it.ID, err)
		}
		if seen[it.ID] {
			return Goals{}, fmt.Errorf("goals: %s twice", it.ID)
		}
		seen[it.ID] = true
	}
	if g.Items == nil {
		g.Items = []Goal{}
	}
	return g, nil
}

// decodeGoals reads the goals of a signed index leniently: what a reader cannot show is
// left out.
func decodeGoals(raw json.RawMessage) Goals {
	var doc struct {
		Donate string            `json:"donate"`
		Items  []json.RawMessage `json:"items"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil || !httpsURL(doc.Donate) {
		return Goals{}
	}
	g := Goals{Donate: doc.Donate}
	seen := map[string]bool{}
	for _, r := range doc.Items {
		var it Goal
		if json.Unmarshal(r, &it) != nil || it.check() != nil || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		g.Items = append(g.Items, it)
		if len(g.Items) == MaxGoals {
			break
		}
	}
	return g
}
