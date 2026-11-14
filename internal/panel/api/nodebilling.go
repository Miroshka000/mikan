package api

import (
	"context"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// NodeBilling is the node's server at its provider, as the admin keeps it: the panel reminds
// of the payment and of the plan's traffic (the "billing" event of the infrastructure alerts),
// it pays and limits nothing. Dates are UTC days.
type NodeBilling struct {
	Provider     string `json:"provider" maxLength:"100" doc:"Хостер сервера, как его назвал админ"`
	PayDate      string `json:"pay_date" pattern:"^([0-9]{4}-[0-9]{2}-[0-9]{2})?$" doc:"Дата оплаты, YYYY-MM-DD (UTC); пусто — не задана. С pay_months — любая оплата из череды, следующую считает панель"`
	PayMonths    int64  `json:"pay_months" minimum:"0" maximum:"36" doc:"Оплата повторяется каждые столько месяцев; 0 — разовая"`
	RemindDays   int64  `json:"remind_days" minimum:"0" maximum:"60" doc:"За сколько дней до оплаты напомнить админу в Telegram; 0 — не напоминать"`
	TrafficLimit int64  `json:"traffic_limit" minimum:"0" doc:"Трафик тарифа сервера за период, байты; 0 — без лимита"`
	ResetDay     int64  `json:"reset_day" minimum:"1" maximum:"31" doc:"День месяца, с которого хостер считает трафик заново; в коротком месяце — его последний день"`
	TrafficAlert int64  `json:"traffic_alert" minimum:"1" maximum:"100" doc:"При скольких процентах лимита сказать админу; раз за период"`
}

// VPSTraffic is what the node has carried in the plan's period so far.
type VPSTraffic struct {
	Used   int64  `json:"used" doc:"Сколько нода унесла с начала периода: трафик пользователей и каскада других нод, вверх и вниз вместе, байты"`
	Since  string `json:"since" doc:"Первый день периода, YYYY-MM-DD (UTC)"`
	Resets string `json:"resets" doc:"День, с которого начнётся следующий период, YYYY-MM-DD (UTC)"`
}

const dateLayout = time.DateOnly

func dayDate(day int64) string { return time.Unix(day*86400, 0).UTC().Format(dateLayout) }

func billingView(b domain.NodeBilling) NodeBilling {
	v := NodeBilling{Provider: b.Provider, PayMonths: b.PayMonths, RemindDays: b.RemindDays, TrafficLimit: b.TrafficLimit, ResetDay: b.ResetDay, TrafficAlert: b.TrafficAlert}
	if b.PayDay != 0 {
		v.PayDate = dayDate(b.PayDay)
	}
	return v
}

// billingIn is the billing a request sends; a date that is no day of the calendar is refused.
func billingIn(v NodeBilling) (domain.NodeBilling, error) {
	b := domain.NodeBilling{Provider: v.Provider, PayMonths: v.PayMonths, RemindDays: v.RemindDays, TrafficLimit: v.TrafficLimit, ResetDay: v.ResetDay, TrafficAlert: v.TrafficAlert}
	if v.PayDate != "" {
		t, err := time.Parse(dateLayout, v.PayDate)
		if err != nil {
			return b, fieldErr("body.billing.pay_date", "bad_billing")
		}
		b.PayDay = domain.Day(t)
	}
	return b, nil
}

// nodeBilling fills the node's billing in, with the next payment.
func (h *handlers) nodeBilling(v *NodeInfo, n db.Node) {
	b := domain.BillingOf(n)
	v.Billing = billingView(b)
	if next, ok := b.NextPayment(domain.Day(h.d.Now())); ok {
		v.NextPayment = dayDate(next)
	}
}

// vpsTraffic fills in what the node has carried in its plan's period, when the plan has a limit.
func (h *handlers) vpsTraffic(ctx context.Context, v *NodeInfo, n db.Node) error {
	b := domain.BillingOf(n)
	if b.TrafficLimit == 0 {
		return nil
	}
	from, next := b.TrafficPeriod(h.d.Now())
	used, err := h.d.Store.Q.NodeTrafficSinceDay(ctx, db.NodeTrafficSinceDayParams{NodeID: n.ID, Day: from})
	if err != nil {
		return err
	}
	v.VPSTraffic = &VPSTraffic{Used: used, Since: dayDate(from), Resets: dayDate(next)}
	return nil
}
