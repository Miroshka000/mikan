package domain

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/model"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

const KeyPaymentAccess = "sub_payment_access"

// PaymentAccessMaxDays is the longest renewal window after an expiry; the days' maximum
// in the API (the tag below) is the same number.
const PaymentAccessMaxDays = 14

type PaymentAccess struct {
	InboundID  int64    `json:"inbound_id" minimum:"0" doc:"0 disables payment access; otherwise an enabled VLESS inbound without a traffic group"`
	Days       int      `json:"days" minimum:"1" maximum:"14"`
	TrafficMiB int      `json:"traffic_mib" minimum:"1" maximum:"200"`
	SpeedMbps  int      `json:"speed_mbps" minimum:"1" maximum:"5"`
	Domains    []string `json:"domains" maxItems:"50" doc:"Additional exact HTTPS hostnames, including any bank redirects"`
}

func LoadPaymentAccess(ctx context.Context, set *settings.Settings) (PaymentAccess, error) {
	c, _, err := settings.GetOver(ctx, set, KeyPaymentAccess, PaymentAccess{Days: 3, TrafficMiB: 150, SpeedMbps: 1, Domains: []string{}})
	return c, err
}

func (c *PaymentAccess) Validate() error {
	if c.InboundID < 0 || c.Days < 1 || c.Days > PaymentAccessMaxDays || c.TrafficMiB < 1 || c.TrafficMiB > 200 || c.SpeedMbps < 1 || c.SpeedMbps > 5 || len(c.Domains) > 50 {
		return fmt.Errorf("payment_access_limits")
	}
	c.Domains = append([]string{}, c.Domains...)
	for i, raw := range c.Domains {
		host, ok := nodeapi.PaymentDomain(raw)
		if !ok {
			return fmt.Errorf("payment_access_domain")
		}
		c.Domains[i] = host
	}
	slices.Sort(c.Domains)
	c.Domains = slices.Compact(c.Domains)
	return nil
}

func (c PaymentAccess) Matches(in db.Inbound) bool {
	if c.InboundID == 0 || c.InboundID != in.ID || in.Enabled == 0 || in.PoolID.Valid {
		return false
	}
	t, err := proto.Parse(in.Config)
	return err == nil && t.Type() == "vless"
}

func (c PaymentAccess) Window(u db.User, now time.Time) (until int64, ok bool) {
	if c.InboundID == 0 || c.Days < 1 || c.Days > PaymentAccessMaxDays || u.Status == StateDisabled || !u.ExpiresAt.Valid || u.ExpiresAt.Int64 <= 0 || u.ExpiresAt.Int64 > model.MaxUnix || u.SubToken == "" {
		return 0, false
	}
	until = u.ExpiresAt.Int64 + int64(c.Days)*86400
	return until, u.ExpiresAt.Int64 <= now.Unix() && now.Unix() < until
}

func (c PaymentAccess) Remaining(used int64) int64 {
	return max(int64(c.TrafficMiB)<<20-used, 0)
}

func (c PaymentAccess) NodeState(inbound, page string) *nodeapi.PaymentAccess {
	state := &nodeapi.PaymentAccess{Inbound: inbound}
	state.Domains = append([]string{"t.me", "telegram.org", "web.telegram.org", "yoomoney.ru", "yookassa.ru", "pay.crypt.bot"}, c.Domains...)
	if u, err := url.Parse(page); err == nil && u.Scheme == "https" {
		port := uint64(443)
		if u.Port() != "" {
			port, _ = strconv.ParseUint(u.Port(), 10, 16)
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
			state.PageHost, state.PagePort = ip.Unmap().String(), uint16(port)
		} else if host, ok := nodeapi.PaymentDomain(u.Hostname()); ok {
			if port == 443 {
				state.Domains = append(state.Domains, host)
			} else {
				state.PageHost, state.PagePort = host, uint16(port)
			}
		}
	}
	slices.Sort(state.Domains)
	state.Domains = slices.Compact(state.Domains)
	return state
}

// Deriving the temporary key requires the base slot's secret as well as the subscription token.
func PaymentSlot(u db.User, secret string) nodeapi.Slot {
	name := fmt.Sprintf("%s%d-%d", nodeapi.PaymentSlotPrefix, u.ID, u.ExpiresAt.Int64)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(name + ":" + u.SubToken))
	key, _ := uuid.FromBytes(mac.Sum(nil)[:16])
	key.SetVersion(uuid.V4)
	key.SetVariant(uuid.VariantRFC9562)
	return nodeapi.Slot{Name: name, UUID: key.String()}
}

func ParsePaymentSlot(name string) (id, expired int64, ok bool) {
	raw, ok := strings.CutPrefix(name, nodeapi.PaymentSlotPrefix)
	if !ok {
		return 0, 0, false
	}
	user, end, _ := strings.Cut(raw, "-")
	id, e1 := strconv.ParseInt(user, 10, 64)
	expired, e2 := strconv.ParseInt(end, 10, 64)
	return id, expired, e1 == nil && e2 == nil && id > 0 && expired > 0
}
