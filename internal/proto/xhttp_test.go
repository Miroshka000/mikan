package proto

import (
	"errors"
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
		"GET outside packet-up":     {XHTTPTuning{Mode: "stream-up", UplinkMethod: "GET"}, "xhttp_get_mode", "xhttp-config.uplink-http-method"},
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

// What the apps send of an XHTTP template: the server's settings both ends read and the
// apps' own over them, under the names of both cores; the server-only keys stay at the
// server. The id table is the client's own, with mihomo's default length when it has none.
func TestXHTTPClient(t *testing.T) {
	tpl := xhttpTemplate(t, ", mode: packet-up")
	err := SetXHTTP(tpl, XHTTPTuning{Mode: "packet-up", SessionPlacement: "header", SessionKey: "X-Sid", UplinkMethod: "POST", UplinkDataPlacement: "body",
		StreamUpServerSecs: "5-9", MaxBufferedPosts: "50", NoSSEHeader: true, SessionTable: "Alphabet", ClientUplinkMethod: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	settings, reuse := XHTTPClient(tpl)
	got := map[string]XHTTPSetting{}
	for _, s := range settings {
		if _, twice := got[s.Mihomo]; twice {
			t.Fatalf("%s twice: %v", s.Mihomo, settings)
		}
		got[s.Mihomo] = s
	}
	for mihomo, want := range map[string]XHTTPSetting{
		"session-placement":     {"session-placement", "sessionIDPlacement", "header"},
		"session-key":           {"session-key", "sessionIDKey", "X-Sid"},
		"uplink-http-method":    {"uplink-http-method", "uplinkHTTPMethod", "GET"}, // the apps' own over the server's
		"uplink-data-placement": {"uplink-data-placement", "uplinkDataPlacement", "body"},
		"session-table":         {"session-table", "sessionIDTable", "Alphabet"},
		"session-length":        {"session-length", "sessionIDLength", "16-32"},
	} {
		if got[mihomo] != want {
			t.Errorf("%s: %+v, want %+v", mihomo, got[mihomo], want)
		}
	}
	for _, k := range []string{"sc-stream-up-server-secs", "sc-max-buffered-posts", "no-sse-header", "mode"} {
		if _, has := got[k]; has {
			t.Errorf("the apps got the server's %s", k)
		}
	}
	if reuse != nil {
		t.Fatalf("no reuse of its own: %v", reuse)
	}

	// A reuse of the template's own replaces the cores' default whole, even one with no
	// key the cores know.
	if err := SetXHTTP(tpl, XHTTPTuning{MaxConnections: "2-4"}); err != nil {
		t.Fatal(err)
	}
	if _, reuse := XHTTPClient(tpl); len(reuse) != 1 || reuse[0] != (XHTTPSetting{"max-connections", "maxConnections", "2-4"}) {
		t.Fatalf("own reuse: %v", reuse)
	}
	tpl[extKey] = map[string]any{"client": map[string]any{"xhttp": map[string]any{"xmux": map[string]any{"nope": "1"}}}}
	if _, reuse := XHTTPClient(tpl); reuse == nil || len(reuse) != 0 {
		t.Fatalf("an own reuse of unknown keys: %v", reuse)
	}
	// Nothing set: nothing sent.
	if settings, reuse := XHTTPClient(xhttpTemplate(t, "")); settings != nil || reuse != nil {
		t.Fatalf("defaults: %v %v", settings, reuse)
	}
}

// The apps' own upload over the server's: the node keeps POST with the data in the body,
// the apps send GET (a CDN that passes only GET and HEAD). What the server would not take
// is refused, by field.
func TestXHTTPClientUplink(t *testing.T) {
	tpl := xhttpTemplate(t, ", mode: packet-up")
	x := XHTTPTuning{Mode: "packet-up", UplinkMethod: "POST", UplinkDataPlacement: "body", ClientUplinkMethod: "GET", ClientUplinkPlacement: "body", ClientUplinkChunkSize: "512000"}
	if err := SetXHTTP(tpl, x); err != nil {
		t.Fatal(err)
	}
	if got, _ := XHTTPOf(tpl); got != x {
		t.Fatalf("read back: %+v", got)
	}
	if sec := tpl.section("xhttp-config"); sec["uplink-http-method"] != "POST" || sec["uplink-data-placement"] != "body" {
		t.Fatalf("the node keeps its own: %v", sec)
	}
	if !NeedsOf(tpl).GetUplink {
		t.Fatal("the apps' GET picks the apps that can send it")
	}

	for name, c := range map[string]struct {
		x           XHTTPTuning
		code, field string
	}{
		"GET outside packet-up":     {XHTTPTuning{Mode: "stream-up", ClientUplinkMethod: "GET"}, "xhttp_get_mode", extKey + ".client.xhttp.uplink-http-method"},
		"header the server refuses": {XHTTPTuning{Mode: "packet-up", UplinkDataPlacement: "body", ClientUplinkPlacement: "header", ClientUplinkDataKey: "X-Data"}, "xhttp_client_placement", extKey + ".client.xhttp.uplink-data-placement"},
		"body the server refuses":   {XHTTPTuning{Mode: "packet-up", UplinkDataPlacement: "cookie", UplinkDataKey: "d", ClientUplinkMethod: "GET", ClientUplinkPlacement: "body"}, "xhttp_client_placement", extKey + ".client.xhttp.uplink-data-placement"},
		"another name":              {XHTTPTuning{Mode: "packet-up", UplinkDataPlacement: "header", UplinkDataKey: "X-D", ClientUplinkDataKey: "X-E"}, "xhttp_client_key", extKey + ".client.xhttp.uplink-data-key"},
		"header without a name":     {XHTTPTuning{Mode: "packet-up", ClientUplinkPlacement: "header"}, "xhttp_key_needed", extKey + ".client.xhttp.uplink-data-key"},
		"auto is the server's":      {XHTTPTuning{Mode: "packet-up", ClientUplinkPlacement: "auto"}, "xhttp_value", extKey + ".client.xhttp.uplink-data-placement"},
		"bad chunk":                 {XHTTPTuning{ClientUplinkChunkSize: "0"}, "xhttp_range", extKey + ".client.xhttp.uplink-chunk-size"},
		"header into auto":          {XHTTPTuning{Mode: "packet-up", UplinkDataPlacement: "auto", ClientUplinkMethod: "GET", ClientUplinkPlacement: "header", ClientUplinkDataKey: "X-Data"}, "", ""},
		"cookie by default name":    {XHTTPTuning{Mode: "packet-up", ClientUplinkPlacement: "cookie", ClientUplinkDataKey: "x_data"}, "", ""},
	} {
		err := SetXHTTP(xhttpTemplate(t, ""), c.x)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != c.code || e.Field != c.field {
			t.Errorf("%s: %v, want %s at %s", name, err, c.code, c.field)
		}
	}
}
