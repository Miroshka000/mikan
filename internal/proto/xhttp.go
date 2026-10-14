package proto

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// XHTTPTuning is what an XHTTP inbound may change past its defaults, as the inbound form
// shows it: the server's xhttp-config keys both ends read (they reach the apps, see
// xhttpShared) and the client's own connection reuse, which the node cannot tell them.
// "" or false: the default of the core.
type XHTTPTuning struct {
	Mode string `json:"mode" required:"false" enum:",stream-one,stream-up,packet-up" doc:"Режим XHTTP; пусто — по умолчанию"`
	// Padding hides the sizes of requests; obfs mode moves it out of the usual place.
	PaddingBytes     string `json:"x_padding_bytes" required:"false" doc:"Размер паддинга, байт: число или диапазон «100-1000»"`
	PaddingObfs      bool   `json:"x_padding_obfs_mode" required:"false" doc:"Паддинг в своём месте вместо Referer: нужен свежий Xray или mihomo 1.19.31+ у клиентов"`
	PaddingPlacement string `json:"x_padding_placement" required:"false" enum:",queryInHeader,header,cookie,query"`
	PaddingKey       string `json:"x_padding_key" required:"false"`
	PaddingHeader    string `json:"x_padding_header" required:"false"`
	PaddingMethod    string `json:"x_padding_method" required:"false" enum:",repeat-x,tokenish"`
	UplinkMethod     string `json:"uplink_http_method" required:"false" enum:",POST,PUT,PATCH"`
	UplinkChunkSize  string `json:"uplink_chunk_size" required:"false" doc:"Размер куска отправки, байт: число или диапазон"`
	MaxEachPostBytes string `json:"sc_max_each_post_bytes" required:"false" doc:"Наибольший запрос отправки (packet-up), байт: число или диапазон"`
	// Where the session id, the request number and the upload data travel: both ends must
	// agree, so these reach the apps (xhttpShared).
	SessionPlacement    string `json:"session_placement" required:"false" enum:",path,query,header,cookie" doc:"Где едет номер сессии; пусто — path"`
	SessionKey          string `json:"session_key" required:"false" doc:"Имя параметра сессии (не для path)"`
	SeqPlacement        string `json:"seq_placement" required:"false" enum:",path,query,header,cookie" doc:"Где едет номер запроса (packet-up); пусто — path"`
	SeqKey              string `json:"seq_key" required:"false" doc:"Имя параметра номера запроса (не для path)"`
	UplinkDataPlacement string `json:"uplink_data_placement" required:"false" enum:",body,header,cookie,auto" doc:"Где едут данные отправки (packet-up); header и cookie только в режиме packet-up"`
	UplinkDataKey       string `json:"uplink_data_key" required:"false" doc:"Имя заголовка или cookie с данными: нужно для header и cookie"`
	// The server's own: the apps never see them.
	StreamUpServerSecs string `json:"sc_stream_up_server_secs" required:"false" doc:"Как часто сервер шлёт паддинг в stream-up, секунд: число или диапазон; 0 — не слать"`
	MaxBufferedPosts   string `json:"sc_max_buffered_posts" required:"false" doc:"Сколько запросов отправки сервер держит в очереди (packet-up): число от 1"`
	NoSSEHeader        bool   `json:"no_sse_header" required:"false" doc:"Не слать Content-Type: text/event-stream в ответах"`
	// The client's own: how many HTTP/2 connections carry how many streams, and for how long.
	MaxConcurrency     string `json:"xmux_max_concurrency" required:"false" doc:"Потоков на соединение: диапазон «16-32»"`
	MaxConnections     string `json:"xmux_max_connections" required:"false" doc:"Соединений сразу; взаимоисключается с потоками"`
	MaxReuseTimes      string `json:"xmux_c_max_reuse_times" required:"false" doc:"Сколько раз переиспользовать соединение"`
	MaxRequestTimes    string `json:"xmux_h_max_request_times" required:"false" doc:"Запросов на соединение HTTP/2"`
	MaxReusableSecs    string `json:"xmux_h_max_reusable_secs" required:"false" doc:"Сколько секунд соединение живёт"`
	MinPostsIntervalMs string `json:"sc_min_posts_interval_ms" required:"false" doc:"Пауза между запросами отправки (packet-up), мс"`
	// What the app draws its session id from; the node takes any id.
	SessionTable  string `json:"session_table" required:"false" doc:"Из каких ASCII-символов приложение составляет номер сессии: свой набор или имя hex, base36, Base62, number, alphabet, Alphabet"`
	SessionLength string `json:"session_length" required:"false" doc:"Длина номера сессии: число или диапазон; с набором по умолчанию 16-32"`
}

// rangeKind says whether a key holds a byte or count range ("N" or "N-M"), and whether it
// must be above zero.
type rangeKind int

const (
	noRange   rangeKind = iota
	anyRange            // 0 and up
	someRange           // 1 and up
	oneCount            // one whole number from 1: Xray reads an integer here, not a range
)

// The server keys of XHTTPTuning in xhttp-config; a range key is checked by its kind
// (XHTTPTuning.check), so a key added here is read, written and checked at once.
var xhttpServerKeys = []struct {
	key  string
	get  func(*XHTTPTuning) *string
	kind rangeKind
}{
	{"mode", func(x *XHTTPTuning) *string { return &x.Mode }, noRange},
	{"x-padding-bytes", func(x *XHTTPTuning) *string { return &x.PaddingBytes }, anyRange},
	{"x-padding-placement", func(x *XHTTPTuning) *string { return &x.PaddingPlacement }, noRange},
	{"x-padding-key", func(x *XHTTPTuning) *string { return &x.PaddingKey }, noRange},
	{"x-padding-header", func(x *XHTTPTuning) *string { return &x.PaddingHeader }, noRange},
	{"x-padding-method", func(x *XHTTPTuning) *string { return &x.PaddingMethod }, noRange},
	{"uplink-http-method", func(x *XHTTPTuning) *string { return &x.UplinkMethod }, noRange},
	{"uplink-chunk-size", func(x *XHTTPTuning) *string { return &x.UplinkChunkSize }, someRange},
	{"sc-max-each-post-bytes", func(x *XHTTPTuning) *string { return &x.MaxEachPostBytes }, someRange},
	{"session-placement", func(x *XHTTPTuning) *string { return &x.SessionPlacement }, noRange},
	{"session-key", func(x *XHTTPTuning) *string { return &x.SessionKey }, noRange},
	{"seq-placement", func(x *XHTTPTuning) *string { return &x.SeqPlacement }, noRange},
	{"seq-key", func(x *XHTTPTuning) *string { return &x.SeqKey }, noRange},
	{"uplink-data-placement", func(x *XHTTPTuning) *string { return &x.UplinkDataPlacement }, noRange},
	{"uplink-data-key", func(x *XHTTPTuning) *string { return &x.UplinkDataKey }, noRange},
	{"sc-stream-up-server-secs", func(x *XHTTPTuning) *string { return &x.StreamUpServerSecs }, anyRange},
	{"sc-max-buffered-posts", func(x *XHTTPTuning) *string { return &x.MaxBufferedPosts }, oneCount},
}

// The client keys: mihomo's reuse-settings and the Xray share link's xmux; all ranges.
var xmuxKeys = []struct {
	mihomo, xray string
	get          func(*XHTTPTuning) *string
}{
	{"max-concurrency", "maxConcurrency", func(x *XHTTPTuning) *string { return &x.MaxConcurrency }},
	{"max-connections", "maxConnections", func(x *XHTTPTuning) *string { return &x.MaxConnections }},
	{"c-max-reuse-times", "cMaxReuseTimes", func(x *XHTTPTuning) *string { return &x.MaxReuseTimes }},
	{"h-max-request-times", "hMaxRequestTimes", func(x *XHTTPTuning) *string { return &x.MaxRequestTimes }},
	{"h-max-reusable-secs", "hMaxReusableSecs", func(x *XHTTPTuning) *string { return &x.MaxReusableSecs }},
}

// XHTTPOf reads the tuning of an XHTTP template; ok is false for other transports.
func XHTTPOf(t Template) (x XHTTPTuning, ok bool) {
	sec := t.section("xhttp-config")
	if sec == nil {
		return x, false
	}
	for _, k := range xhttpServerKeys {
		*k.get(&x) = scalar(sec[k.key])
	}
	x.PaddingObfs, _ = sec["x-padding-obfs-mode"].(bool)
	x.NoSSEHeader, _ = sec["no-sse-header"].(bool)
	c := xhttpClient(t)
	for _, k := range xmuxKeys {
		if xm, _ := c["xmux"].(map[string]any); xm != nil {
			*k.get(&x) = scalar(xm[k.mihomo])
		}
	}
	x.MinPostsIntervalMs = scalar(c["sc-min-posts-interval-ms"])
	x.SessionTable, x.SessionLength = scalar(c["session-table"]), scalar(c["session-length"])
	return x, true
}

// scalar reads a YAML value typed as a string or a number ("1000" and 1000 alike).
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
	}
	return ""
}

// xhttpClient is mikan.client.xhttp of t (nil when unset).
func xhttpClient(t Template) map[string]any {
	ext, _ := t[extKey].(map[string]any)
	cl, _ := ext["client"].(map[string]any)
	x, _ := cl["xhttp"].(map[string]any)
	return x
}

// SetXHTTP writes x over the template's XHTTP settings: every field, so an empty one goes
// back to the core's default.
func SetXHTTP(t Template, x XHTTPTuning) error {
	sec := t.section("xhttp-config")
	if sec == nil {
		return fail("xhttp_no_xhttp", "xhttp-config")
	}
	if err := x.check(); err != nil {
		return err
	}
	for _, k := range xhttpServerKeys {
		if v := strings.TrimSpace(*k.get(&x)); v != "" {
			sec[k.key] = v
		} else {
			delete(sec, k.key)
		}
	}
	if x.PaddingObfs {
		sec["x-padding-obfs-mode"] = true
	} else {
		delete(sec, "x-padding-obfs-mode")
	}
	if x.NoSSEHeader {
		sec["no-sse-header"] = true
	} else {
		delete(sec, "no-sse-header")
	}
	client := map[string]any{}
	xm := map[string]any{}
	for _, k := range xmuxKeys {
		if v := strings.TrimSpace(*k.get(&x)); v != "" {
			xm[k.mihomo] = v
		}
	}
	if len(xm) > 0 {
		client["xmux"] = xm
	}
	if v := strings.TrimSpace(x.MinPostsIntervalMs); v != "" {
		client["sc-min-posts-interval-ms"] = v
	}
	// The length only means something next to a table (check refuses it alone).
	if v := strings.TrimSpace(x.SessionTable); v != "" {
		client["session-table"] = v
		if l := strings.TrimSpace(x.SessionLength); l != "" {
			client["session-length"] = l
		}
	}
	ext, _ := t[extKey].(map[string]any)
	if ext == nil {
		ext = map[string]any{}
	}
	cl, _ := ext["client"].(map[string]any)
	if cl == nil {
		cl = map[string]any{}
	}
	if len(client) > 0 {
		cl["xhttp"] = client
	} else {
		delete(cl, "xhttp")
	}
	if len(cl) > 0 {
		ext["client"] = cl
	} else {
		delete(ext, "client")
	}
	if len(ext) > 0 {
		t[extKey] = ext
	} else {
		delete(t, extKey)
	}
	return nil
}

var xhttpToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// check refuses what the core would refuse when the node loads it, by field.
func (x XHTTPTuning) check() error {
	enum := func(v, field string, ok ...string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		for _, o := range ok {
			if v == o {
				return nil
			}
		}
		return fail("xhttp_value", field)
	}
	if err := enum(x.Mode, "xhttp-config.mode", "stream-one", "stream-up", "packet-up"); err != nil {
		return fail("config_xhttp_mode", "xhttp-config.mode")
	}
	for _, e := range []struct {
		v, field string
		ok       []string
	}{
		{x.PaddingPlacement, "xhttp-config.x-padding-placement", []string{"queryInHeader", "header", "cookie", "query"}},
		{x.PaddingMethod, "xhttp-config.x-padding-method", []string{"repeat-x", "tokenish"}},
		// GET is not here: mihomo's server answers an upload only to a method other than
		// GET (Xray's takes GET in packet-up with the data outside the body).
		{x.UplinkMethod, "xhttp-config.uplink-http-method", []string{"POST", "PUT", "PATCH"}},
		{x.SessionPlacement, "xhttp-config.session-placement", []string{"path", "query", "header", "cookie"}},
		{x.SeqPlacement, "xhttp-config.seq-placement", []string{"path", "query", "header", "cookie"}},
		// No "query" for the data: mihomo sends nothing in that case.
		{x.UplinkDataPlacement, "xhttp-config.uplink-data-placement", []string{"body", "header", "cookie", "auto"}},
	} {
		if err := enum(e.v, e.field, e.ok...); err != nil {
			return err
		}
	}
	for _, v := range []struct{ v, field string }{
		{x.PaddingKey, "xhttp-config.x-padding-key"}, {x.PaddingHeader, "xhttp-config.x-padding-header"},
		{x.SessionKey, "xhttp-config.session-key"}, {x.SeqKey, "xhttp-config.seq-key"}, {x.UplinkDataKey, "xhttp-config.uplink-data-key"},
	} {
		if s := strings.TrimSpace(v.v); s != "" && !xhttpToken.MatchString(s) {
			return fail("xhttp_value", v.field)
		}
	}
	if err := x.checkSession(); err != nil {
		return err
	}
	type rangeField struct {
		v, field string
		min1     bool
	}
	var ranges []rangeField
	for _, k := range xhttpServerKeys {
		switch s := strings.TrimSpace(*k.get(&x)); k.kind {
		case oneCount:
			if n, err := strconv.Atoi(s); s != "" && (err != nil || n < 1 || n > maxBufferedPosts) {
				return fail("xhttp_count", "xhttp-config."+k.key)
			}
		case anyRange, someRange:
			ranges = append(ranges, rangeField{s, "xhttp-config." + k.key, k.kind == someRange})
		}
	}
	for _, k := range xmuxKeys {
		ranges = append(ranges, rangeField{*k.get(&x), extKey + ".client.xhttp.xmux." + k.mihomo, false})
	}
	ranges = append(ranges, rangeField{x.MinPostsIntervalMs, extKey + ".client.xhttp.sc-min-posts-interval-ms", true})
	for _, r := range ranges {
		if s := strings.TrimSpace(r.v); s != "" && !validRange(s, r.min1) {
			return fail("xhttp_range", r.field)
		}
	}
	// Xray and mihomo take one of the two: streams per connection or connections.
	if strings.TrimSpace(x.MaxConcurrency) != "" && strings.TrimSpace(x.MaxConnections) != "" {
		return fail("xhttp_xmux_both", extKey+".client.xhttp.xmux.max-connections")
	}
	return nil
}

// validRange reads "N" or "N-M" the way the cores do: whole numbers, N ≤ M, above zero
// where the core needs it.
func validRange(s string, min1 bool) bool {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, err1 := strconv.Atoi(lo)
	b, err2 := strconv.Atoi(hi)
	if err1 != nil || err2 != nil || a < 0 || a > b || b > 1<<30 {
		return false
	}
	return !min1 || a > 0
}

// maxBufferedPosts keeps a typo from turning the upload queue into a memory sink: every
// queued post may weigh up to sc-max-each-post-bytes.
const maxBufferedPosts = 10000

// maxSessionLength is far above any useful id; the cores take more, but the room check
// below loops once per length.
const maxSessionLength = 256

// defaultSessionLength is what mihomo draws when a table has no length.
const defaultSessionLength = "16-32"

// sessionTables are the names mihomo and Xray both expand into a set of characters.
var sessionTables = map[string]string{
	"ALPHABET": "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Alphabet": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"BASE36":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Base62":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"HEX":      "0123456789ABCDEF",
	"alphabet": "abcdefghijklmnopqrstuvwxyz",
	"base36":   "0123456789abcdefghijklmnopqrstuvwxyz",
	"hex":      "0123456789abcdef",
	"number":   "0123456789",
}

// sessionTableChars: an id travels in a path, a query, a header or a cookie, so a table
// holds URL-safe characters. The cores ask for ASCII only, which is not enough: a "/" would
// split the id in the path. mihomo's own "uuid" is not here: Xray reads it as four letters.
var sessionTableChars = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// checkSession is what depends on several of the new settings at once, and the client's
// id table.
func (x XHTTPTuning) checkSession() error {
	switch strings.TrimSpace(x.UplinkDataPlacement) {
	case "header", "cookie":
		// Xray refuses these in other modes; without a key mihomo uses an empty name where
		// Xray uses X-Data / x_data, so the two would not meet.
		if strings.TrimSpace(x.Mode) != "packet-up" {
			return fail("xhttp_packet_up", "xhttp-config.uplink-data-placement")
		}
		if strings.TrimSpace(x.UplinkDataKey) == "" {
			return fail("xhttp_key_needed", "xhttp-config.uplink-data-key")
		}
	}
	// One request carries both: the same place and name would overwrite one with the other.
	sp, qp := orPath(x.SessionPlacement), orPath(x.SeqPlacement)
	if sp == qp && sp != "path" && keyOr(x.SessionKey, sp, "session") == keyOr(x.SeqKey, qp, "seq") {
		return fail("xhttp_same_key", "xhttp-config.seq-key")
	}

	table, length := strings.TrimSpace(x.SessionTable), strings.TrimSpace(x.SessionLength)
	const field = extKey + ".client.xhttp.session-"
	if table == "" {
		if length != "" {
			return fail("xhttp_table_needed", field+"table")
		}
		return nil
	}
	chars, named := sessionTables[table]
	if !named {
		if table == "uuid" || !sessionTableChars.MatchString(table) {
			return fail("xhttp_value", field+"table")
		}
		chars = table
	}
	if length == "" {
		length = defaultSessionLength
	}
	if !validRange(length, true) {
		return fail("xhttp_range", field+"length")
	}
	lo, hi, _ := strings.Cut(length, "-")
	lowest, _ := strconv.Atoi(lo)
	highest := lowest
	if hi != "" {
		highest, _ = strconv.Atoi(hi)
	}
	if highest > maxSessionLength {
		return fail("xhttp_range", field+"length")
	}
	// The cores want about 2.1 billion possible ids, or two sessions may get the same one.
	base, room := big.NewInt(int64(len(chars))), new(big.Int)
	for k := lowest; k <= highest; k++ {
		room.Add(room, new(big.Int).Exp(base, big.NewInt(int64(k)), nil))
	}
	if room.Cmp(big.NewInt(2<<30)) < 0 {
		return fail("xhttp_table_small", field+"table")
	}
	return nil
}

func orPath(p string) string {
	if p = strings.TrimSpace(p); p != "" {
		return p
	}
	return "path"
}

// keyOr is the name a core gives the session id or the request number: the admin's, or
// the default both cores share.
func keyOr(key, placement, what string) string {
	if key = strings.TrimSpace(key); key != "" {
		return key
	}
	if placement == "header" {
		return map[string]string{"session": "X-Session", "seq": "X-Seq"}[what]
	}
	return "x_" + what
}
