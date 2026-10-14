package proto

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func xhttpTemplate(t *testing.T, xhttp string) Template {
	t.Helper()
	priv, _ := realityKey(t)
	return mustParse(t, "type: vless\nxhttp-config: {path: /x"+xhttp+"}\n"+
		"reality-config:\n  dest: www.microsoft.com:443\n  private-key: "+priv+"\n  short-id: [a1b2]\n  server-names: [www.microsoft.com]\n")
}

// The form writes what it shows: every field, an empty one back to the core's default, and
// what it read from YAML (numbers too) comes back unchanged.
func TestSetXHTTPRoundTrip(t *testing.T) {
	tpl := xhttpTemplate(t, ", x-padding-bytes: 1000, host: cdn.example.com, sc-max-buffered-posts: 30")
	x, ok := XHTTPOf(tpl)
	if !ok || x.PaddingBytes != "1000" {
		t.Fatalf("read: %+v %v", x, ok)
	}
	x.Mode, x.PaddingObfs, x.PaddingPlacement, x.PaddingKey, x.PaddingMethod = "packet-up", true, "cookie", "sid", "tokenish"
	x.UplinkMethod, x.MaxEachPostBytes, x.MaxConnections, x.MinPostsIntervalMs = "PUT", "500000-1000000", "2-4", "10-50"
	if x.MaxBufferedPosts != "30" {
		t.Fatalf("a number typed in YAML shows in the form: %+v", x)
	}
	x.SessionPlacement, x.SessionKey, x.SeqPlacement, x.SeqKey = "cookie", "sess", "header", "X-N"
	x.UplinkDataPlacement, x.UplinkDataKey = "header", "X-Payload"
	x.StreamUpServerSecs, x.MaxBufferedPosts, x.NoSSEHeader = "10-30", "64", true
	x.SessionTable, x.SessionLength = "Base62", "20-24"
	if err := SetXHTTP(tpl, x); err != nil {
		t.Fatal(err)
	}
	sec0 := tpl.section("xhttp-config")
	if sec0["session-placement"] != "cookie" || sec0["seq-key"] != "X-N" || sec0["uplink-data-placement"] != "header" || sec0["sc-stream-up-server-secs"] != "10-30" ||
		sec0["sc-max-buffered-posts"] != "64" || sec0["no-sse-header"] != true {
		t.Fatalf("server keys: %v", sec0)
	}
	if cl := xhttpClient(tpl); cl["session-table"] != "Base62" || cl["session-length"] != "20-24" {
		t.Fatalf("client keys: %v", cl)
	}
	if err := Validate(tpl, Options{AnyDest: true}); err != nil {
		t.Fatalf("written template: %v", err)
	}
	got, _ := XHTTPOf(tpl)
	if got != x {
		t.Fatalf("read back:\n%+v\nwant\n%+v", got, x)
	}
	if sec := tpl.section("xhttp-config"); sec["host"] != "cdn.example.com" || sec["path"] != "/x" {
		t.Fatalf("keys the form does not show stay: %v", sec)
	}

	if err := SetXHTTP(tpl, XHTTPTuning{}); err != nil {
		t.Fatal(err)
	}
	sec := tpl.section("xhttp-config")
	if _, has := sec["x-padding-bytes"]; has || sec["x-padding-obfs-mode"] != nil || sec["no-sse-header"] != nil || sec["session-placement"] != nil ||
		sec["sc-max-buffered-posts"] != nil || tpl[extKey] != nil {
		t.Fatalf("defaults again: %v %v", sec, tpl[extKey])
	}
}

func TestXHTTPTuningCheck(t *testing.T) {
	for name, x := range map[string]XHTTPTuning{
		"auto mode":        {Mode: "auto"},
		"bad range":        {PaddingBytes: "1000-100"},
		"not a number":     {PaddingBytes: "lots"},
		"zero chunk":       {UplinkChunkSize: "0"},
		"bad method":       {UplinkMethod: "GET"},
		"bad placement":    {PaddingPlacement: "body"},
		"bad key":          {PaddingKey: "a b"},
		"streams and conn": {MaxConcurrency: "8", MaxConnections: "2"},
	} {
		var pe *Error
		if err := SetXHTTP(xhttpTemplate(t, ""), x); !errors.As(err, &pe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	priv, _ := realityKey(t)
	ws := mustParse(t, "type: vless\nws-path: /ws\nreality-config:\n  dest: www.microsoft.com:443\n  private-key: "+priv+"\n  short-id: [a1b2]\n  server-names: [www.microsoft.com]\n")
	if err := SetXHTTP(ws, XHTTPTuning{}); err == nil {
		t.Fatal("not XHTTP")
	}
	// The same rules hold for YAML typed by hand.
	if err := Validate(xhttpTemplate(t, ", uplink-http-method: GET"), Options{AnyDest: true}); err == nil {
		t.Fatal("YAML with a bad method passed")
	}
}

// The session, order and data settings: what the cores would refuse or what would leave
// the two ends apart is refused by the form, each with its field.
func TestXHTTPSessionSettingsCheck(t *testing.T) {
	for name, c := range map[string]struct {
		x           XHTTPTuning
		code, field string
	}{
		"session placement":         {XHTTPTuning{SessionPlacement: "body"}, "xhttp_value", "xhttp-config.session-placement"},
		"seq placement":             {XHTTPTuning{SeqPlacement: "auto"}, "xhttp_value", "xhttp-config.seq-placement"},
		"data in the query":         {XHTTPTuning{UplinkDataPlacement: "query"}, "xhttp_value", "xhttp-config.uplink-data-placement"},
		"bad session key":           {XHTTPTuning{SessionPlacement: "header", SessionKey: "X Session"}, "xhttp_value", "xhttp-config.session-key"},
		"bad data key":              {XHTTPTuning{UplinkDataKey: "a;b"}, "xhttp_value", "xhttp-config.uplink-data-key"},
		"data in header, any mode":  {XHTTPTuning{UplinkDataPlacement: "header", UplinkDataKey: "X-D"}, "xhttp_packet_up", "xhttp-config.uplink-data-placement"},
		"data in cookie, stream-up": {XHTTPTuning{Mode: "stream-up", UplinkDataPlacement: "cookie", UplinkDataKey: "d"}, "xhttp_packet_up", "xhttp-config.uplink-data-placement"},
		"data without a key":        {XHTTPTuning{Mode: "packet-up", UplinkDataPlacement: "cookie"}, "xhttp_key_needed", "xhttp-config.uplink-data-key"},
		"same header":               {XHTTPTuning{SessionPlacement: "header", SessionKey: "X-Id", SeqPlacement: "header", SeqKey: "X-Id"}, "xhttp_same_key", "xhttp-config.seq-key"},
		"same named query":          {XHTTPTuning{SessionPlacement: "query", SessionKey: "id", SeqPlacement: "query", SeqKey: "id"}, "xhttp_same_key", "xhttp-config.seq-key"},
		"no GET upload":             {XHTTPTuning{Mode: "packet-up", UplinkMethod: "GET", UplinkDataPlacement: "header", UplinkDataKey: "X-D"}, "xhttp_value", "xhttp-config.uplink-http-method"},
		"buffered range":            {XHTTPTuning{MaxBufferedPosts: "10-30"}, "xhttp_count", "xhttp-config.sc-max-buffered-posts"},
		"buffered zero":             {XHTTPTuning{MaxBufferedPosts: "0"}, "xhttp_count", "xhttp-config.sc-max-buffered-posts"},
		"buffered huge":             {XHTTPTuning{MaxBufferedPosts: "100000"}, "xhttp_count", "xhttp-config.sc-max-buffered-posts"},
		"server secs":               {XHTTPTuning{StreamUpServerSecs: "80-20"}, "xhttp_range", "xhttp-config.sc-stream-up-server-secs"},
		"length alone":              {XHTTPTuning{SessionLength: "16"}, "xhttp_table_needed", extKey + ".client.xhttp.session-table"},
		"non-ASCII table":           {XHTTPTuning{SessionTable: "абвгдежзик"}, "xhttp_value", extKey + ".client.xhttp.session-table"},
		"slash in table":            {XHTTPTuning{SessionTable: "ab/cdef"}, "xhttp_value", extKey + ".client.xhttp.session-table"},
		"mihomo's uuid":             {XHTTPTuning{SessionTable: "uuid"}, "xhttp_value", extKey + ".client.xhttp.session-table"},
		"table too small":           {XHTTPTuning{SessionTable: "abc", SessionLength: "4-8"}, "xhttp_table_small", extKey + ".client.xhttp.session-table"},
		"default length too small":  {XHTTPTuning{SessionTable: "ab"}, "", ""}, // 2^16..2^32 is room enough
		"length zero":               {XHTTPTuning{SessionTable: "hex", SessionLength: "0-8"}, "xhttp_range", extKey + ".client.xhttp.session-length"},
		"length reversed":           {XHTTPTuning{SessionTable: "hex", SessionLength: "16-8"}, "xhttp_range", extKey + ".client.xhttp.session-length"},
		"length too long":           {XHTTPTuning{SessionTable: "hex", SessionLength: "16-100000000"}, "xhttp_range", extKey + ".client.xhttp.session-length"},
	} {
		err := SetXHTTP(xhttpTemplate(t, ""), c.x)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != c.code || pe.Field != c.field {
			t.Errorf("%s: %v, want %s at %s", name, err, c.code, c.field)
		}
	}
	// What the cores take stays allowed, hand-typed YAML included.
	for name, x := range map[string]XHTTPTuning{
		"cookie session, header seq": {SessionPlacement: "cookie", SeqPlacement: "header"},
		"session and seq in a query": {SessionPlacement: "query", SeqPlacement: "query"},
		"path with a key":            {SessionPlacement: "path", SessionKey: "ignored"},
		"data in the body and auto":  {UplinkDataPlacement: "auto"},
		"packet-up header data":      {Mode: "packet-up", UplinkDataPlacement: "header", UplinkDataKey: "X-Data"},
		"named table":                {SessionTable: "base36", SessionLength: "12"},
		"own table":                  {SessionTable: "abcdefghij", SessionLength: "10-20"},
		"no keepalive padding":       {StreamUpServerSecs: "0"},
	} {
		if err := SetXHTTP(xhttpTemplate(t, ""), x); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Validate(xhttpTemplate(t, ", session-placement: body"), Options{AnyDest: true}); err == nil {
		t.Error("YAML with a bad session placement passed")
	}
	if err := Validate(xhttpTemplate(t, ", sc-max-buffered-posts: 0"), Options{AnyDest: true}); err == nil {
		t.Error("YAML with a zero queue passed")
	}
}

// The session, order and data keys reach every format that carries XHTTP: the server's
// value in mihomo's profile and in the Xray link's extra under Xray's own names; the id
// table is the client's own. The server-only keys stay at the server.
func TestXHTTPSessionSettingsReachTheClient(t *testing.T) {
	tpl := xhttpTemplate(t, ", mode: packet-up")
	err := SetXHTTP(tpl, XHTTPTuning{Mode: "packet-up", SessionPlacement: "header", SessionKey: "X-Sid", SeqPlacement: "query", SeqKey: "n",
		UplinkDataPlacement: "cookie", UplinkDataKey: "d", StreamUpServerSecs: "5-9", MaxBufferedPosts: "50", NoSSEHeader: true,
		SessionTable: "hex", SessionLength: "20-24"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ClientConfig(tpl, ClientInput{Name: "X", Host: "203.0.113.7", Port: 443, Slot: slots[0]})
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Mihomo["xhttp-opts"].(map[string]any)
	for k, want := range map[string]string{"session-placement": "header", "session-key": "X-Sid", "seq-placement": "query", "seq-key": "n",
		"uplink-data-placement": "cookie", "uplink-data-key": "d", "session-table": "hex", "session-length": "20-24"} {
		if opts[k] != want {
			t.Errorf("mihomo %s = %v, want %s (%v)", k, opts[k], want, opts)
		}
	}
	for _, k := range []string{"sc-stream-up-server-secs", "sc-max-buffered-posts", "no-sse-header"} {
		if _, has := opts[k]; has {
			t.Errorf("mihomo got the server's %s", k)
		}
	}
	u, _ := url.Parse(c.URI)
	var extra map[string]any
	if err := json.Unmarshal([]byte(u.Query().Get("extra")), &extra); err != nil {
		t.Fatalf("extra: %v", err)
	}
	for k, want := range map[string]string{"sessionIDPlacement": "header", "sessionIDKey": "X-Sid", "seqPlacement": "query", "seqKey": "n",
		"uplinkDataPlacement": "cookie", "uplinkDataKey": "d", "sessionIDTable": "hex", "sessionIDLength": "20-24"} {
		if extra[k] != want {
			t.Errorf("link %s = %v, want %s (%v)", k, extra[k], want, extra)
		}
	}
	for _, k := range []string{"scStreamUpServerSecs", "scMaxBufferedPosts", "noSSEHeader", "sessionPlacement", "sessionKey"} {
		if _, has := extra[k]; has {
			t.Errorf("link has %s", k)
		}
	}

	// A table without a length: Xray would find it too small, so the link says mihomo's default.
	if err := SetXHTTP(tpl, XHTTPTuning{SessionTable: "Alphabet"}); err != nil {
		t.Fatal(err)
	}
	c, _ = ClientConfig(tpl, ClientInput{Name: "X", Host: "203.0.113.7", Port: 443, Slot: slots[0]})
	u, _ = url.Parse(c.URI)
	extra = nil
	_ = json.Unmarshal([]byte(u.Query().Get("extra")), &extra)
	if o := c.Mihomo["xhttp-opts"].(map[string]any); o["session-length"] != "16-32" || extra["sessionIDLength"] != "16-32" || extra["sessionIDTable"] != "Alphabet" {
		t.Fatalf("default length: %v %v", o, extra)
	}

	// Nothing set: nothing sent.
	if err := SetXHTTP(tpl, XHTTPTuning{}); err != nil {
		t.Fatal(err)
	}
	c, _ = ClientConfig(tpl, ClientInput{Name: "X", Host: "203.0.113.7", Port: 443, Slot: slots[0]})
	for _, k := range []string{"session-table", "session-length", "session-placement", "uplink-data-placement"} {
		if _, has := c.Mihomo["xhttp-opts"].(map[string]any)[k]; has {
			t.Errorf("defaults still send %s", k)
		}
	}
	if strings.Contains(c.URI, "sessionID") {
		t.Errorf("defaults in the link: %s", c.URI)
	}
}

// The client's own reuse settings replace the panel's, in mihomo's profile and in the
// Xray link's extra.
func TestXHTTPClientReuse(t *testing.T) {
	tpl := xhttpTemplate(t, "")
	plain, err := ClientConfig(tpl, ClientInput{Name: "X", Host: "203.0.113.7", Port: 443, Slot: slots[0]})
	if err != nil {
		t.Fatal(err)
	}
	if r := plain.Mihomo["xhttp-opts"].(map[string]any)["reuse-settings"].(map[string]any); r["max-concurrency"] != "16-32" {
		t.Fatalf("the panel's default: %v", r)
	}
	if err := SetXHTTP(tpl, XHTTPTuning{MaxConnections: "2-4", MaxReusableSecs: "600", MinPostsIntervalMs: "20"}); err != nil {
		t.Fatal(err)
	}
	c, err := ClientConfig(tpl, ClientInput{Name: "X", Host: "203.0.113.7", Port: 443, Slot: slots[0]})
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Mihomo["xhttp-opts"].(map[string]any)
	r := opts["reuse-settings"].(map[string]any)
	if r["max-connections"] != "2-4" || r["h-max-reusable-secs"] != "600" || r["max-concurrency"] != nil || opts["sc-min-posts-interval-ms"] != "20" {
		t.Fatalf("mihomo: %v", opts)
	}
	u, _ := url.Parse(c.URI)
	var extra map[string]any
	if err := json.Unmarshal([]byte(u.Query().Get("extra")), &extra); err != nil {
		t.Fatalf("extra %q: %v", u.Query().Get("extra"), err)
	}
	xm, _ := extra["xmux"].(map[string]any)
	if xm["maxConnections"] != "2-4" || xm["hMaxReusableSecs"] != "600" || extra["scMinPostsIntervalMs"] != "20" || strings.Contains(c.URI, "maxConcurrency") {
		t.Fatalf("link extra: %v", extra)
	}
}
