package xray

// The keepalive of a gRPC connection in an xray JSON profile (grpcSettings), in seconds: an
// idle connection is pinged after grpcIdleTimeout and given up when no answer comes within
// grpcHealthCheckTimeout, so that the app does not go on sending into one a middlebox cut
// while it was silent. Share links have no place for them.
const (
	grpcIdleTimeout        = 15
	grpcHealthCheckTimeout = 10
)
