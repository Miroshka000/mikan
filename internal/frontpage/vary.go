package frontpage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"text/template"
)

// The built-in pages are templates. With no seed they are the pages as written; with the
// installation's seed (SetSeed) each installation gets its own: other words, sizes, colours
// and names in the markup and the script. A page served the same, byte for byte, by every
// installation is a mark a scanner finds them all by with one hash; a page of one's own is
// only a small site like many. The seed decides everything, so the panel, its nodes and
// their protocols' ports show one page, and it stays the same over restarts.

// look is the choices of one page for one seed.
type look struct {
	kind string // the page's name
	seed string // "": the page as written
}

// pick is the choice of n for key: 0 with no seed. Keys that share the part before a dot
// share the choice ("tick" and "tick.per"), so numbers that belong together stay together.
func (l look) pick(key string, n int) int {
	if l.seed == "" || n <= 1 {
		return 0
	}
	group, _, _ := strings.Cut(key, ".")
	return int(binary.BigEndian.Uint64(l.sum(group)) % uint64(n))
}

func (l look) sum(key string) []byte {
	h := sha256.Sum256([]byte(l.seed + "\x00" + l.kind + "\x00" + key))
	return h[:]
}

// texts are the words of the pages, the page as written first.
var texts = map[string]map[string][]string{
	"cats": {
		"title":  {"Cat Clicker", "Kitty Clicker", "Purr Clicker", "Pet the Cat", "Cat Treats", "Meow Tap", "Paws and Treats", "Tap the Cat", "Treat Counter", "Kitty Treats", "Cozy Cat", "Cat Break"},
		"hint":   {"pet the cat to earn treats", "tap the cat to collect treats", "click the cat for treats", "give the cat a pat"},
		"pet":    {"Pet the cat", "Tap the cat", "Click the cat"},
		"paw":    {"Soft paw", "Gentle paw", "Warm paw", "Careful paw"},
		"yarn":   {"Ball of yarn", "Yarn ball", "Toy mouse", "Feather wand"},
		"kitten": {"Kitten", "Little kitten", "Tiny kitten", "Sleepy kitten"},
		"tower":  {"Cat tower", "Cat tree", "Scratching post", "Window perch"},
		"shelf":  {"Things for the cat", "The cat's shelf", "Spend your treats", "For a happier cat"},
		"foot":   {"Your treats are kept in this browser.", "Progress is saved on this device.", "The cat remembers you in this browser."},
		"store":  {"cat-clicker", "kitty-clicker", "cat-treats", "purr-game", "cat-tap", "treats"},
	},
	"maintenance": {
		"title": {"Scheduled maintenance", "Maintenance in progress", "We'll be back soon", "Under maintenance", "Site maintenance", "Temporarily unavailable", "Be right back", "Planned maintenance"},
		"body": {
			"We are upgrading our servers to serve you better. The site will be back shortly.",
			"We are making a few improvements. Please check back in a little while.",
			"The site is down for planned work and will return soon.",
			"We are updating things behind the scenes. It will not take long.",
		},
		"thanks": {"Thank you for your patience.", "Thanks for bearing with us.", "We appreciate your patience.", "Sorry for the wait."},
		"label":  {"Maintenance in progress", "Work in progress", "Updating"},
		"head":   {"Service status", "System status", "Status", "Status page"},
		"plan":   {"What is being done", "The plan of the work", "Steps of the maintenance"},
		// The three lines of the plan are one set (one group), and so are their states.
		"step.a":  {"Backups made and checked", "Data copied to the new servers", "Traffic moved off the old machines", "New storage prepared"},
		"step.b":  {"Moving data to the new storage", "Upgrading the database", "Installing the new software", "Switching to the new network"},
		"step.c":  {"Checks, then the site opens again", "Final tests and reopening", "Verification and return to service", "Tests, then back online"},
		"state.a": {"Done", "Finished", "Complete"},
		"state.b": {"In progress", "Under way", "Now"},
		"state.c": {"Up next", "Waiting", "Later"},
		"foot":    {"This page updates as the work goes on.", "The site returns when the last step is done.", "Nothing is needed from you while we work."},
	},
}

// numbers are the sizes and timings of the pages, the page as written first. The keys of
// one group (before the dot) have as many values each: they are chosen together.
var numbers = map[string]map[string][]string{
	"cats": {
		"cost.a":       {"15", "10", "20", "12", "25"},
		"cost.b":       {"50", "40", "75", "60", "100"},
		"cost.c":       {"300", "250", "400", "350", "500"},
		"cost.d":       {"1500", "1200", "2000", "1800", "2500"},
		"growth":       {"1.15", "1.12", "1.18", "1.2", "1.14"},
		"tick":         {"250", "200", "500", "100"},
		"tick.per":     {"4", "5", "2", "10"},
		"keep":         {"5000", "3000", "4000", "8000", "10000"},
		"width":        {"880", "840", "920", "860", "960"},
		"radius":       {"24", "16", "20", "28", "12"},
		"radius.small": {"12", "8", "10", "14", "6"},
		"size":         {"260", "240", "250", "270", "280"},
		// The cat's coat: a cat's own colours, in either colour scheme.
		"fur": {"#f4a04a", "#9aa0a8", "#c98a5a", "#e9d3ad", "#b9b2a8", "#e08a34"},
	},
	"maintenance": {
		"width": {"640", "600", "680", "620", "660"},
		"pad":   {"24", "20", "28", "32", "16"},
		"gap":   {"56", "48", "64", "52", "72"},
		"glide": {"2.4", "2", "3", "2.8", "1.8"},
	},
}

// idWords name the classes, ids and functions of a seeded page. None is a word of the
// script's own (its variables), of JavaScript or of the window, so a function may take any.
var idWords = []string{
	"box", "wrap", "hero", "card", "panel", "view", "tile", "unit", "cell", "slot", "area", "zone", "part", "block",
	"lead", "note", "info", "meta", "stat", "sum", "total", "score", "tally", "meter", "gauge", "badge", "chip", "pill",
	"tag", "mark", "sign", "icon", "pic", "art", "head", "foot", "side", "base", "core", "hub", "dock", "bay", "rack",
	"shelf", "mart", "stall", "booth", "desk", "orb", "dot", "ring", "disc", "knob", "dial", "pad", "tray", "bin", "jar",
}

// ids are the names a page's template asks for (id), in a fixed order: each gets a word of
// its own.
var ids = map[string][]string{
	"cats":        {"cat", "count", "face", "owned", "perPet", "perSec", "pop", "price", "rate", "render", "save", "shop", "up", "stage", "side", "foot", "tail", "wag", "eo", "ec", "bl"},
	"maintenance": {"wrap", "state", "pulse", "lead", "steps", "done", "now", "slide", "thanks"},
}

func (l look) text(key string) (string, error) {
	v := texts[l.kind][key]
	if len(v) == 0 {
		return "", fmt.Errorf("frontpage: %s has no text %q", l.kind, key)
	}
	s := v[l.pick(key, len(v))]
	// The key the script keeps its state under is the page's own too.
	if key == "store" && l.seed != "" {
		s += "-" + hex.EncodeToString(l.sum("store.tail")[:2])
	}
	return s, nil
}

func (l look) number(key string) (string, error) {
	v := numbers[l.kind][key]
	if len(v) == 0 {
		return "", fmt.Errorf("frontpage: %s has no number %q", l.kind, key)
	}
	return v[l.pick(key, len(v))], nil
}

// id is the name the page gives to name: itself with no seed, else a word of idWords that
// no other name of the page has, at times with a digit.
func (l look) id(name string) (string, error) {
	i := slices.Index(ids[l.kind], name)
	if i < 0 {
		return "", fmt.Errorf("frontpage: %s has no name %q", l.kind, name)
	}
	if l.seed == "" {
		return name, nil
	}
	// The words in an order of the seed's: the page's names take the first ones.
	order := make([]int, len(idWords))
	for j := range order {
		order[j] = j
	}
	for j := len(order) - 1; j > 0; j-- {
		k := int(binary.BigEndian.Uint64(l.sum("id."+strconv.Itoa(j))) % uint64(j+1))
		order[j], order[k] = order[k], order[j]
	}
	word := idWords[order[i]]
	if d := l.sum("digit." + name)[0] % 20; d < 10 {
		word += strconv.Itoa(int(d))
	}
	return word, nil
}

// color is the colour hex (#rgb or #rrggbb) turned on the colour wheel by the seed's angle:
// the page keeps its contrast and its harmony, the palette is the installation's own.
func (l look) color(hexColor string) (string, error) {
	if l.seed == "" {
		return hexColor, nil
	}
	h := strings.TrimPrefix(hexColor, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != 3 {
		return "", fmt.Errorf("frontpage: colour %q", hexColor)
	}
	angle := float64(15 + l.pick("hue", 331)) // 15..345: never the palette as written
	r, g, b := turn(float64(raw[0])/255, float64(raw[1])/255, float64(raw[2])/255, angle)
	return fmt.Sprintf("#%02x%02x%02x", channel(r), channel(g), channel(b)), nil
}

func channel(v float64) int {
	return int(math.Round(math.Min(1, math.Max(0, v)) * 255))
}

// turn rotates the hue of an RGB colour (each channel 0..1) by angle degrees, keeping its
// saturation and lightness.
func turn(r, g, b, angle float64) (float64, float64, float64) {
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	light := (hi + lo) / 2
	if hi == lo {
		return r, g, b // a grey has no hue to turn
	}
	d := hi - lo
	sat := d / (1 - math.Abs(2*light-1))
	var hue float64
	switch hi {
	case r:
		hue = math.Mod((g-b)/d, 6)
	case g:
		hue = (b-r)/d + 2
	default:
		hue = (r-g)/d + 4
	}
	hue = math.Mod(hue*60+angle+360, 360)
	c := (1 - math.Abs(2*light-1)) * sat
	x := c * (1 - math.Abs(math.Mod(hue/60, 2)-1))
	m := light - c/2
	switch int(hue / 60) {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return r + m, g + m, b + m
}

// render makes the page kind of its template src for seed.
func render(kind string, src []byte, seed string) ([]byte, error) {
	l := look{kind: kind, seed: seed}
	t, err := template.New(kind).Delims("[[", "]]").Funcs(template.FuncMap{"t": l.text, "n": l.number, "id": l.id, "c": l.color}).Parse(string(src))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, nil); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// notFoundBodies are the bodies of the plain 404, the one as written first: a small site's
// server says it in one of a few ways.
var notFoundBodies = []string{"404 Not Found\n", "404 page not found\n", "Not Found\n", "404 Not Found", "Not Found", "404\n"}

func notFoundBody(seed string) string {
	return notFoundBodies[look{kind: "404", seed: seed}.pick("body", len(notFoundBodies))]
}
