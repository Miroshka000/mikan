package subtest

import "mikan/internal/sub/model"

// WithHostOptions is u with every inbound's default host asking for all of an address's own
// fields: ALPN of TLS over TCP, an IP version, sing-mux with its protocol and padding, the
// TLS hello split into packets. The build gives each proxy those it can carry, so the cores
// see every one of them somewhere.
func WithHostOptions(u User) User {
	return WithHostFragment(u, model.FragmentTCP)
}

// WithHostFragment is WithHostOptions with the TLS hello split the way fragment says.
func WithHostFragment(u User, fragment string) User {
	ins := make([]model.Inbound, len(u.Build.Inbounds))
	for i, in := range u.Build.Inbounds {
		in.Hosts = []model.Host{{ALPN: []string{"h2", "http/1.1"}, IPVersion: "ipv6-prefer", Mux: true, MuxProtocol: "h2mux", MuxPadding: true, Fragment: fragment}}
		ins[i] = in
	}
	u.Build.Inbounds = ins
	return u
}
