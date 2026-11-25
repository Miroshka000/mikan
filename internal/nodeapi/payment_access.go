package nodeapi

import "strings"

const PaymentSlotPrefix = "payment-"

// PaymentAccess installs keys only on the named VLESS listener. Keeping them apart from
// Slots means a node that doesn't implement the restrictions never installs the keys.
type PaymentAccess struct {
	Inbound  string   `json:"inbound"`
	Domains  []string `json:"domains"`
	PageHost string   `json:"page_host,omitempty"`
	PagePort uint16   `json:"page_port,omitempty"`
	Slots    []Slot   `json:"slots"`
}

func PaymentDomain(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	host, ok := CanonDomain(raw)
	return host, ok && !strings.HasPrefix(raw, ".") && !strings.Contains(raw, "*") && strings.Contains(host, ".")
}
