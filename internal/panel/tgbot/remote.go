package tgbot

import (
	"context"
	"time"

	"mikan/internal/panel/model"
)

// The methods of Remote, one call each (rpc.go). A method whose interface has no error to
// give (Config, Brand, ...) gives its zero value when the panel cannot be reached: the
// screens show the defaults rather than nothing.

func (r *Remote) Config(ctx context.Context) Config {
	var c Config
	if err := r.call(ctx, "Config", nil, &c); err != nil {
		return Default("")
	}
	return c
}

func (r *Remote) Brand(ctx context.Context) string {
	var s string
	_ = r.call(ctx, "Brand", nil, &s)
	return s
}

func (r *Remote) SupportURL(ctx context.Context) string {
	var s string
	_ = r.call(ctx, "SupportURL", nil, &s)
	return s
}

func (r *Remote) SubURL(ctx context.Context, u model.User) string {
	var s string
	_ = r.call(ctx, "SubURL", []any{u}, &s)
	return s
}

func (r *Remote) MiniAppURL(ctx context.Context) string {
	var s string
	_ = r.call(ctx, "MiniAppURL", nil, &s)
	return s
}

func (r *Remote) Member(ctx context.Context, chat string, tgID int64, fresh bool) (bool, error) {
	var in bool
	err := r.call(ctx, "Member", []any{chat, tgID, fresh}, &in)
	return in, err
}

func (r *Remote) Context(ctx context.Context, tgID int64) (Account, error) {
	var a Account
	err := r.call(ctx, "Context", []any{tgID}, &a)
	return a, err
}

func (r *Remote) Chat(ctx context.Context, tgID int64) (model.TgChat, error) {
	var c model.TgChat
	err := r.call(ctx, "Chat", []any{tgID}, &c)
	return c, err
}

func (r *Remote) SetMenu(ctx context.Context, tgID, msgID int64) error {
	return r.call(ctx, "SetMenu", []any{tgID, msgID})
}

func (r *Remote) SetCurrent(ctx context.Context, tgID, userID int64) error {
	return r.call(ctx, "SetCurrent", []any{tgID, userID})
}

func (r *Remote) TagCampaign(ctx context.Context, tgID int64, code string) (bool, error) {
	var tagged bool
	err := r.call(ctx, "TagCampaign", []any{tgID, code}, &tagged)
	return tagged, err
}

func (r *Remote) UserOfCode(ctx context.Context, code string) (int64, error) {
	var id int64
	err := r.call(ctx, "UserOfCode", []any{code}, &id)
	return id, err
}

func (r *Remote) UserOfAddress(ctx context.Context, urlPath string) (int64, bool) {
	var (
		id int64
		ok bool
	)
	if err := r.call(ctx, "UserOfAddress", []any{urlPath}, &id, &ok); err != nil {
		return 0, false
	}
	return id, ok
}

func (r *Remote) Link(ctx context.Context, tgID, userID int64) (Linked, error) {
	var l Linked
	err := r.call(ctx, "Link", []any{tgID, userID}, &l)
	return l, err
}

func (r *Remote) Owner(ctx context.Context, userID int64) (int64, error) {
	var tg int64
	err := r.call(ctx, "Owner", []any{userID}, &tg)
	return tg, err
}

func (r *Remote) Transfer(ctx context.Context, userID, to int64) error {
	return r.call(ctx, "Transfer", []any{userID, to})
}

func (r *Remote) Unlink(ctx context.Context, userID int64) error {
	return r.call(ctx, "Unlink", []any{userID})
}

func (r *Remote) User(ctx context.Context, id int64) (model.User, error) {
	var u model.User
	err := r.call(ctx, "User", []any{id}, &u)
	return u, err
}

func (r *Remote) GrantsLeft(ctx context.Context, userID int64, now time.Time) (model.GrantsLeft, error) {
	var g model.GrantsLeft
	err := r.call(ctx, "GrantsLeft", []any{userID, now}, &g)
	return g, err
}

func (r *Remote) Pools(ctx context.Context, userID int64, now time.Time) ([]Pool, error) {
	var p []Pool
	err := r.call(ctx, "Pools", []any{userID, now}, &p)
	return p, err
}

func (r *Remote) DeviceBinding(ctx context.Context) bool {
	var on bool
	_ = r.call(ctx, "DeviceBinding", nil, &on)
	return on
}

func (r *Remote) DeviceCount(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := r.call(ctx, "DeviceCount", []any{userID}, &n)
	return n, err
}

func (r *Remote) Devices(ctx context.Context, userID int64) ([]model.BoundDevice, error) {
	var d []model.BoundDevice
	err := r.call(ctx, "Devices", []any{userID}, &d)
	return d, err
}

func (r *Remote) DeviceTraffic(ctx context.Context, userID int64) (map[int64]int64, error) {
	var m map[int64]int64
	err := r.call(ctx, "DeviceTraffic", []any{userID}, &m)
	return m, err
}

func (r *Remote) UnbindState(ctx context.Context, userID int64) (model.UnbindState, error) {
	var s model.UnbindState
	err := r.call(ctx, "UnbindState", []any{userID}, &s)
	return s, err
}

func (r *Remote) Unbind(ctx context.Context, userID, deviceID int64) error {
	return r.call(ctx, "Unbind", []any{userID, deviceID})
}

func (r *Remote) ReissueState(ctx context.Context, userID int64) (model.ReissueState, error) {
	var s model.ReissueState
	err := r.call(ctx, "ReissueState", []any{userID}, &s)
	return s, err
}

func (r *Remote) Reissue(ctx context.Context, userID int64) error {
	return r.call(ctx, "Reissue", []any{userID})
}

func (r *Remote) Shop() Shop { return r.shop }

func (r *Remote) Tariff(ctx context.Context, id int64) (model.Tariff, error) {
	var t model.Tariff
	err := r.call(ctx, "Tariff", []any{id}, &t)
	return t, err
}

// remoteShop is Shop over the Panel API ("Shop.<method>").
type remoteShop struct{ r *Remote }

func (s remoteShop) Config(ctx context.Context) model.Config {
	var c model.Config
	_ = s.r.call(ctx, "Shop.Config", nil, &c)
	return c
}

func (s remoteShop) Offers(ctx context.Context) ([]model.Offer, model.Available, error) {
	var (
		o []model.Offer
		a model.Available
	)
	err := s.r.call(ctx, "Shop.Offers", nil, &o, &a)
	return o, a, err
}

func (s remoteShop) AddonName(ctx context.Context, id, lang string) string {
	var n string
	_ = s.r.call(ctx, "Shop.AddonName", []any{id, lang}, &n)
	return n
}

func (s remoteShop) Invoice(ctx context.Context, req model.InvoiceRequest) (model.Payment, error) {
	var p model.Payment
	err := s.r.call(ctx, "Shop.Invoice", []any{req}, &p)
	return p, err
}

func (s remoteShop) PackageOffers(ctx context.Context, userID int64) ([]model.PackageOffer, model.Available, error) {
	var (
		o []model.PackageOffer
		a model.Available
	)
	err := s.r.call(ctx, "Shop.PackageOffers", []any{userID}, &o, &a)
	return o, a, err
}

func (s remoteShop) DeviceOfferFor(ctx context.Context, userID int64) (model.DeviceOffer, model.Available, bool, error) {
	var (
		o  model.DeviceOffer
		a  model.Available
		ok bool
	)
	err := s.r.call(ctx, "Shop.DeviceOfferFor", []any{userID}, &o, &a, &ok)
	return o, a, ok, err
}

func (s remoteShop) DeviceInvoice(ctx context.Context, req model.DeviceRequest) (model.Payment, error) {
	var p model.Payment
	err := s.r.call(ctx, "Shop.DeviceInvoice", []any{req}, &p)
	return p, err
}

func (s remoteShop) PackageInvoice(ctx context.Context, req model.PackageRequest) (model.Payment, error) {
	var p model.Payment
	err := s.r.call(ctx, "Shop.PackageInvoice", []any{req}, &p)
	return p, err
}

func (s remoteShop) TrialTariff(ctx context.Context) (model.Tariff, bool) {
	var (
		t  model.Tariff
		ok bool
	)
	if err := s.r.call(ctx, "Shop.TrialTariff", nil, &t, &ok); err != nil {
		return model.Tariff{}, false
	}
	return t, ok
}

func (s remoteShop) TrialOpen(ctx context.Context, tgID int64) bool {
	var ok bool
	_ = s.r.call(ctx, "Shop.TrialOpen", []any{tgID}, &ok)
	return ok
}

func (s remoteShop) TrialAllowed(ctx context.Context, tgID int64) bool {
	var ok bool
	_ = s.r.call(ctx, "Shop.TrialAllowed", []any{tgID}, &ok)
	return ok
}

func (s remoteShop) Trial(ctx context.Context, tgID int64) (model.User, error) {
	var u model.User
	err := s.r.call(ctx, "Shop.Trial", []any{tgID}, &u)
	return u, err
}

func (s remoteShop) TrialOffer(ctx context.Context, tgID int64) (model.TrialOffer, model.Available, bool) {
	var (
		o  model.TrialOffer
		a  model.Available
		ok bool
	)
	if err := s.r.call(ctx, "Shop.TrialOffer", []any{tgID}, &o, &a, &ok); err != nil {
		return model.TrialOffer{}, model.Available{}, false
	}
	return o, a, ok
}

func (s remoteShop) TrialInvoice(ctx context.Context, tgID int64, provider string) (model.Payment, error) {
	var p model.Payment
	err := s.r.call(ctx, "Shop.TrialInvoice", []any{tgID, provider}, &p)
	return p, err
}

func (s remoteShop) FreeOffer(ctx context.Context, tgID int64) (model.FreeOffer, bool) {
	var (
		o  model.FreeOffer
		ok bool
	)
	if err := s.r.call(ctx, "Shop.FreeOffer", []any{tgID}, &o, &ok); err != nil {
		return model.FreeOffer{}, false
	}
	return o, ok
}

func (s remoteShop) FreePlan(ctx context.Context, tgID int64) (model.User, error) {
	var u model.User
	err := s.r.call(ctx, "Shop.FreePlan", []any{tgID}, &u)
	return u, err
}

func (s remoteShop) BalanceOf(ctx context.Context, tgID int64) (int64, error) {
	var n int64
	err := s.r.call(ctx, "Shop.BalanceOf", []any{tgID}, &n)
	return n, err
}

func (s remoteShop) BalanceHistory(ctx context.Context, tgID int64, n int32) ([]model.BalanceOp, error) {
	var ops []model.BalanceOp
	err := s.r.call(ctx, "Shop.BalanceHistory", []any{tgID, n}, &ops)
	return ops, err
}

func (s remoteShop) StartTopup(ctx context.Context, tgID, amount int64) (model.Topup, error) {
	var t model.Topup
	err := s.r.call(ctx, "Shop.StartTopup", []any{tgID, amount}, &t)
	return t, err
}

func (s remoteShop) SetTopupAmount(ctx context.Context, tgID, amount int64) (model.Topup, error) {
	var t model.Topup
	err := s.r.call(ctx, "Shop.SetTopupAmount", []any{tgID, amount}, &t)
	return t, err
}

func (s remoteShop) TopupDraft(ctx context.Context, tgID int64) (model.Topup, bool) {
	var (
		t  model.Topup
		ok bool
	)
	if err := s.r.call(ctx, "Shop.TopupDraft", []any{tgID}, &t, &ok); err != nil {
		return model.Topup{}, false
	}
	return t, ok
}

func (s remoteShop) CancelTopup(ctx context.Context, tgID int64) error {
	return s.r.call(ctx, "Shop.CancelTopup", []any{tgID})
}

func (s remoteShop) SubmitTopup(ctx context.Context, tgID int64, photo string) (model.Topup, error) {
	var t model.Topup
	err := s.r.call(ctx, "Shop.SubmitTopup", []any{tgID, photo}, &t)
	return t, err
}

// ChatSetup reads both values without masking an unavailable panel as an empty menu.
func (r *Remote) ChatSetup(ctx context.Context) (Config, string, error) {
	var cfg Config
	if err := r.call(ctx, "Config", nil, &cfg); err != nil {
		return cfg, "", err
	}
	var url string
	if cfg.MiniApp {
		if err := r.call(ctx, "MiniAppURL", nil, &url); err != nil {
			return cfg, "", err
		}
	}
	return cfg, url, nil
}
