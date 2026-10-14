package autotune

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// A node like the default one plus gRPC: three TCP inbounds, two UDP.
func nodeInbounds() []db.Inbound {
	return []db.Inbound{
		{ID: 1, Name: "vless-xhttp", Preset: "vless_reality_xhttp", Port: "443", Enabled: 1, UpdatedAt: 100},
		{ID: 2, Name: "hysteria2", Preset: "hysteria2", Port: "443", Enabled: 1, UpdatedAt: 100},
		{ID: 3, Name: "tuic", Preset: "tuic_v5", Port: "8443", Enabled: 1, UpdatedAt: 100},
		{ID: 4, Name: "vless-vision", Preset: "vless_reality_vision", Port: "8443", Enabled: 1, UpdatedAt: 100},
		{ID: 5, Name: "vless-grpc", Preset: "vless_reality_grpc", Port: "2053", Enabled: 1, UpdatedAt: 100},
	}
}

func TestDetect(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fresh := now.Add(-2 * time.Minute).Unix()
	old := now.Add(-2 * time.Hour).Unix() // outside the window
	seen := func(names ...string) map[string]int64 {
		m := map[string]int64{}
		for _, n := range names {
			m[n] = fresh
		}
		return m
	}
	all := []string{"vless-xhttp", "hysteria2", "tuic", "vless-vision", "vless-grpc"}
	without := func(skip string) []string {
		return slices.DeleteFunc(slices.Clone(all), func(n string) bool { return n == skip })
	}
	device := func(slot, ip string, s map[string]int64) nodeapi.ClientActivity {
		return nodeapi.ClientActivity{Slot: slot, IP: ip, Seen: s}
	}
	// Inbounds changed at 100; the devices took their profiles at 200, stale ones at 50.
	fetched := func(at int64) map[string]int64 { return map[string]int64{"203.0.113.1": at, "203.0.113.2": at} }
	users := map[string]User{"s1": {SubFetched: fetched(200)}, "s2": {SubFetched: fetched(200)},
		"stale": {SubFetched: fetched(50)}, "limited": {SubFetched: fetched(200), Inbounds: []int64{1, 4, 5}},
		"phone":  {SubFetched: map[string]int64{"198.51.100.9": 200}},
		"newapp": {SubFetched: fetched(200)},
		// Bound devices with keys of their own: their own profile fetch counts, not their address's.
		"dev": {Fetched: 200}, "devstale": {Fetched: 50, SubFetched: fetched(200)}}
	// Every device has reached every inbound before, except newapp: its app never got to TUIC.
	earlier := now.Add(-3 * 24 * time.Hour).Unix()
	reach := map[string]map[int64]int64{}
	for slot := range users {
		reach[slot] = map[int64]int64{1: earlier, 2: earlier, 3: earlier, 4: earlier, 5: earlier}
	}
	delete(reach["newapp"], 3)

	cases := []struct {
		name    string
		clients []nodeapi.ClientActivity
		want    map[int64]Verdict
	}{
		{
			name:    "a device that reaches everything but one port is cut off from it",
			clients: []nodeapi.ClientActivity{device("s1", "203.0.113.1", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{1: {Blocked: 1, Networks: 1}, 2: {Reached: 1}, 3: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			name: "devices cut off on one network are one network",
			clients: []nodeapi.ClientActivity{device("s1", "203.0.113.1", seen(without("vless-xhttp")...)),
				device("s2", "203.0.113.2", seen(without("vless-xhttp")...))},
			want: map[int64]Verdict{1: {Blocked: 2, Networks: 1}, 2: {Reached: 2}, 3: {Reached: 2}, 4: {Reached: 2}, 5: {Reached: 2}},
		},
		{
			name: "devices cut off on two networks",
			clients: []nodeapi.ClientActivity{device("s1", "203.0.113.1", seen(without("vless-xhttp")...)),
				device("phone", "198.51.100.9", seen(without("vless-xhttp")...))},
			want: map[int64]Verdict{1: {Blocked: 2, Networks: 2}, 2: {Reached: 2}, 3: {Reached: 2}, 4: {Reached: 2}, 5: {Reached: 2}},
		},
		{
			// Its TCP inbounds see only two others each: not enough to judge them either.
			name: "a network without UDP is not a blocked Hysteria2 port",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", seen("vless-xhttp", "vless-vision", "vless-grpc")),
			},
			want: map[int64]Verdict{},
		},
		{
			// TUIC has no other UDP inbound reached next to it, so it is not judged.
			name: "one UDP inbound reached, the other not: that port is blocked",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", seen("vless-xhttp", "vless-vision", "vless-grpc", "tuic")),
			},
			want: map[int64]Verdict{1: {Reached: 1}, 2: {Blocked: 1, Networks: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			name: "a client with one or two chosen links says nothing",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", seen("vless-vision")),
				device("s2", "203.0.113.2", seen("vless-vision", "hysteria2")),
			},
			want: map[int64]Verdict{},
		},
		{
			name: "activity older than the window does not count",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", map[string]int64{"vless-xhttp": old, "hysteria2": fresh, "tuic": fresh, "vless-vision": fresh, "vless-grpc": fresh}),
			},
			want: map[int64]Verdict{1: {Blocked: 1, Networks: 1}, 2: {Reached: 1}, 3: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			name:    "a profile older than the inbound's last change may still use the old port",
			clients: []nodeapi.ClientActivity{device("stale", "203.0.113.1", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{},
		},
		{
			// The user's phone took the new profile; this laptop did not.
			name:    "another device's fresh profile says nothing about this one",
			clients: []nodeapi.ClientActivity{device("phone", "203.0.113.1", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{},
		},
		{
			name:    "inbounds outside the user's profile are not judged by that user",
			clients: []nodeapi.ClientActivity{device("limited", "203.0.113.1", seen("vless-xhttp", "vless-vision", "vless-grpc"))},
			want:    map[int64]Verdict{},
		},
		{
			name:    "devices of unknown slots are ignored",
			clients: []nodeapi.ClientActivity{device("gone", "203.0.113.1", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{},
		},
		{
			// Happ does not speak TUIC: it checks every link it can use, never this one.
			// Hysteria2 is not judged either: no other UDP inbound was reached next to it.
			name:    "a device that never reached an inbound is not cut off from it",
			clients: []nodeapi.ClientActivity{device("newapp", "203.0.113.1", seen(without("tuic")...))},
			want:    map[int64]Verdict{1: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			// A phone next to it uses XHTTP right now: the network does not block the port.
			name: "another device on the same network gets through: the problem is this device's",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", seen(without("vless-xhttp")...)),
				device("s2", "203.0.113.77", seen("vless-xhttp")),
			},
			want: map[int64]Verdict{2: {Reached: 1}, 3: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			name: "a device on another network says nothing about this one",
			clients: []nodeapi.ClientActivity{
				device("s1", "203.0.113.1", seen(without("vless-xhttp")...)),
				device("s2", "198.51.100.5", seen("vless-xhttp")),
			},
			want: map[int64]Verdict{1: {Blocked: 1, Networks: 1}, 2: {Reached: 1}, 3: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			name:    "a bound device counts by its own profile fetch, wherever it is",
			clients: []nodeapi.ClientActivity{device("dev", "192.0.2.50", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{1: {Blocked: 1, Networks: 1}, 2: {Reached: 1}, 3: {Reached: 1}, 4: {Reached: 1}, 5: {Reached: 1}},
		},
		{
			// Another device at the same address took the new profile; this one did not.
			name:    "a bound device with a stale profile says nothing",
			clients: []nodeapi.ClientActivity{device("devstale", "203.0.113.1", seen(without("vless-xhttp")...))},
			want:    map[int64]Verdict{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Detect(Evidence{Inbounds: nodeInbounds(), Users: users, Activity: nodeapi.Activity{Clients: c.clients}, Reach: reach, Since: now.Add(-30 * time.Minute)})
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %v\nwant %v", got, c.want)
			}
		})
	}

	// Two inbounds leave nothing to compare with.
	two := nodeInbounds()[:2]
	if got := Detect(Evidence{Inbounds: two, Users: users, Activity: nodeapi.Activity{Clients: []nodeapi.ClientActivity{device("s1", "a", seen("hysteria2"))}}, Reach: reach, Since: now.Add(-time.Hour)}); len(got) != 0 {
		t.Fatalf("two inbounds: %v", got)
	}
}

func TestCutOff(t *testing.T) {
	for _, c := range []struct {
		v    Verdict
		want bool
	}{
		{Verdict{Blocked: 2, Networks: 2}, true},
		{Verdict{Blocked: 2, Reached: 8, Networks: 2}, true},
		{Verdict{Blocked: 2, Reached: 9, Networks: 2}, false}, // a few devices failing next to many that get through
		{Verdict{Blocked: 1, Networks: 1}, false},             // one device: its second VPN, its router (GitHub issue #67)
		{Verdict{Blocked: 3, Networks: 1}, false},             // one network, however many devices on it
		{Verdict{Reached: 3}, false},
	} {
		if got := c.v.CutOff(); got != c.want {
			t.Errorf("%+v: got %v", c.v, got)
		}
	}
}

// The seeded node 1 plus gRPC on 2053, a disabled Trojan on 2087, the subscription port
// 2096 and a Hysteria2 hopping range over 5000-6500.
func TestFreePorts(t *testing.T) {
	e := setup(t)
	if err := settings.Set(e.ctx, e.set, settings.KeySubPort, 2096); err != nil {
		t.Fatal(err)
	}
	for _, in := range []db.CreateInboundParams{
		{Name: "vless-grpc", Preset: "vless_reality_grpc", Port: "2053"},
		{Name: "off", Preset: "trojan_reality", Port: "2087"},
		{Name: "hopping", Preset: "hysteria2", Port: "5000-6500"},
	} {
		config, err := presets.NewConfig(in.Preset, "")
		if err != nil {
			t.Fatal(err)
		}
		in.NodeID, in.Config = 1, config
		row, err := e.st.Q.CreateInbound(e.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if in.Name == "off" {
			if _, err := e.st.Q.UpdateInbound(e.ctx, db.UpdateInboundParams{Port: row.Port, Config: row.Config, ID: row.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	node, err := e.st.Q.GetNode(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := domain.NodePorts(e.ctx, e.st.Q, node)
	if err != nil {
		t.Fatal(err)
	}
	got := FreePorts(ports, "tcp", map[string]bool{"2443": true}, false)
	want := []string{"2083", "3443", "4443", "5443", "6443", "7443", "9443", "10443", "11443", "12443"} // 2053 gRPC, 2087 a disabled inbound, 2096 subscriptions, 8443 Vision
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tcp: got %v, want %v", got, want)
	}
	// UDP: TUIC's 8443 and the hopping range's 5443 and 6443 are taken there.
	want = []string{"2053", "2083", "2087", "2096", "2443", "3443", "4443", "7443", "9443", "10443", "11443", "12443"}
	if got := FreePorts(ports, "udp", nil, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("udp: got %v, want %v", got, want)
	}
}
