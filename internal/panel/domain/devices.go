package domain

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Devices binds a subscription to the devices that use it, against resale: a device
// that sends its hardware id (x-hwid, sent by Happ, Koala Clash, INCY, v2RayTun,
// FlClashX…) gets keys of its own, so it can be unbound — and cut off — alone. Apps
// without an id share the user's own keys as one device. The user's device limit is
// the number of places.
type Devices struct {
	st      *store.Store
	set     *settings.Settings
	pool    *Pool
	changes Changes
	now     func() time.Time
}

// DeviceInfo is what a client tells about itself when it fetches the subscription.
type DeviceInfo struct {
	HWID, OS, OSVersion, Model, App, IP string
}

var hwidRe = regexp.MustCompile(`^[a-zA-Z0-9=-]{10,64}$`)

// ValidHWID follows Remnawave's rule, which the apps are built for.
func ValidHWID(s string) bool { return hwidRe.MatchString(s) }

// MaxDevices is how many devices a user without a limit may bind. The id is whatever the
// client sends, and every new one takes a slot of the pool for good: holders of a link
// could otherwise use the pool up and make the nodes rebuild their listeners.
const MaxDevices = 50

// DeviceIdle: a device not seen for this long is forgotten, and its keys burn.
const DeviceIdle = 90 * 24 * time.Hour

var (
	ErrDeviceLimit    = errors.New("device_limit")    // the user's places are taken
	ErrNoHWID         = errors.New("no_hwid")         // the app sends no device id and one is required
	ErrUnbindCooldown = errors.New("unbind_cooldown") // the subscriber used up the unbinds the admin's rules allow for now
	ErrDeviceBanned   = errors.New("device_banned")   // the admin banned this device from the subscription
	ErrBanShared      = errors.New("device_no_hwid")  // the shared place of apps without an id cannot be banned
)

func NewDevices(st *store.Store, pool *Pool, changes Changes, now func() time.Time) *Devices {
	return &Devices{st: st, set: settings.New(st.Q), pool: pool, changes: changes, now: now}
}

// same: what the app sent tells nothing new (it did not send it, or it is what is known).
func same(v, old string) bool { return v == "" || v == old }

// clip keeps what a client reports short and printable.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len([]rune(s)) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// Bind returns the slot whose keys a device fetching the subscription gets. A new
// device takes a free place: its own slot with an id, the user's slot without one.
func (d *Devices) Bind(ctx context.Context, u db.User, in DeviceInfo, requireHWID bool) (db.Slot, error) {
	hwid := in.HWID
	if !ValidHWID(hwid) {
		hwid = ""
	}
	if hwid == "" && requireHWID {
		return db.Slot{}, ErrNoHWID
	}
	slot, created, err := d.bind(ctx, u, hwid, in)
	if errors.Is(err, ErrNoSlots) {
		if err := d.pool.Refill(ctx, RefillBatch); err != nil {
			return db.Slot{}, err
		}
		d.changes.SlotsChanged()
		slot, created, err = d.bind(ctx, u, hwid, in)
	}
	if err != nil {
		return db.Slot{}, err
	}
	if created && hwid != "" {
		d.changes.PoliciesChanged() // the new slot is allowed from now on
	}
	return slot, nil
}

// touchEvery: a known device that reports nothing new is written down at most this often.
// Apps fetch the subscription every few minutes; last_seen needs no finer grain.
const touchEvery = 5 * time.Minute

func (d *Devices) bind(ctx context.Context, u db.User, hwid string, in DeviceInfo) (slot db.Slot, created bool, err error) {
	now := d.now().Unix()
	os, osv, model, app, ip := clip(in.OS, 40), clip(in.OSVersion, 40), clip(in.Model, 60), clip(in.App, 120), clip(in.IP, 45)
	// The usual request: a known device, nothing new about it. Read, no write.
	if dev, err := d.st.Q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: u.ID, Hwid: hwid}); err == nil &&
		now-dev.LastSeen < int64(touchEvery/time.Second) && now >= dev.LastSeen &&
		same(os, dev.Os) && same(osv, dev.OsVersion) && same(model, dev.Model) && same(app, dev.App) && same(ip, dev.LastIp) {
		slot, err = d.st.Q.GetSlot(ctx, dev.SlotID)
		return slot, false, err
	}
	err = d.st.Tx(ctx, func(q *db.Queries) error {
		// A conflict runs this again: a device an attempt made and rolled back is not made.
		slot, created = db.Slot{}, false
		dev, err := q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: u.ID, Hwid: hwid})
		if err == nil {
			// Apps do not send every header every time: keep what is known.
			keep := func(v, old string) string {
				if v == "" {
					return old
				}
				return v
			}
			if err := q.TouchBoundDevice(ctx, db.TouchBoundDeviceParams{Os: keep(os, dev.Os), OsVersion: keep(osv, dev.OsVersion), Model: keep(model, dev.Model),
				App: keep(app, dev.App), LastIp: keep(ip, dev.LastIp), LastSeen: now, ID: dev.ID}); err != nil {
				return err
			}
			slot, err = q.GetSlot(ctx, dev.SlotID)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// A banned device gets nothing, whatever the user's state: banning unbound it, and
		// it must not take a place again. So does one its subscriber just unbound, until its
		// pause ends. Only a new device is asked: both remove the device's row in the same
		// transaction.
		if hwid != "" {
			until, err := q.ActiveDeviceBan(ctx, db.ActiveDeviceBanParams{UserID: u.ID, Hwid: hwid, Until: sql.NullInt64{Int64: now, Valid: true}})
			switch {
			case err == nil && until.Valid:
				return &UnboundError{Until: time.Unix(until.Int64, 0).UTC()}
			case err == nil:
				return ErrDeviceBanned
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		// A user who cannot connect (turned off, term over) registers no new device and takes
		// no slot: the link itself is enough to ask, and it may be in anyone's hands. The
		// answer is the user's own keys, which the node refuses anyway.
		if u.Status == "disabled" || (u.ExpiresAt.Valid && d.now().Unix() >= u.ExpiresAt.Int64) {
			if !u.SlotID.Valid {
				return errors.New("user has no slot")
			}
			slot, err = q.GetSlot(ctx, u.SlotID.Int64)
			return err
		}
		n, err := q.CountBoundDevices(ctx, u.ID)
		if err != nil {
			return err
		}
		limit := int64(MaxDevices)
		if u.DeviceLimit.Valid {
			limit = u.DeviceLimit.Int64
		}
		if n >= limit {
			return ErrDeviceLimit
		}
		if hwid == "" {
			if !u.SlotID.Valid {
				return errors.New("user has no slot")
			}
			slot, err = q.GetSlot(ctx, u.SlotID.Int64)
		} else {
			slot, err = q.TakeFreeSlot(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSlots
			}
		}
		if err != nil {
			return err
		}
		if _, err := q.CreateBoundDevice(ctx, db.CreateBoundDeviceParams{UserID: u.ID, Hwid: hwid, SlotID: slot.ID, Os: os, OsVersion: osv, Model: model,
			App: app, LastIp: ip, CreatedAt: now, LastSeen: now}); err != nil {
			return err
		}
		created = true
		return nil
	})
	return slot, created, err
}

// Unbind frees a device's place and cuts it off: its own slot burns (the node drops it
// at once). The shared place of apps without an id is the user's own slot: it is replaced
// with a fresh one, the subscription link stays. The subscriber does it within the admin's
// UnbindRules (ErrUnbindCooldown past them), and the device they unbound stays out for the
// rules' pause; the admin unbinds outside them.
func (d *Devices) Unbind(ctx context.Context, userID, deviceID int64, bySubscriber bool) error {
	rules, err := LoadUnbindRules(ctx, d.set)
	if err != nil {
		return err
	}
	err = d.unbind(ctx, rules, userID, deviceID, bySubscriber)
	if errors.Is(err, ErrNoSlots) {
		if err := d.pool.Refill(ctx, RefillBatch); err != nil {
			return err
		}
		d.changes.SlotsChanged()
		err = d.unbind(ctx, rules, userID, deviceID, bySubscriber)
	}
	if err == nil {
		d.changes.PoliciesChanged()
	}
	return err
}

func (d *Devices) unbind(ctx context.Context, rules UnbindRules, userID, deviceID int64, bySubscriber bool) error {
	now := d.now()
	return d.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetUser(ctx, userID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		dev, err := q.GetBoundDeviceByID(ctx, db.GetBoundDeviceByIDParams{ID: deviceID, UserID: userID})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if bySubscriber {
			// One unbind of the subscriber's at a time: two at once would both fit the limit.
			if err := q.LockUser(ctx, userID); err != nil {
				return err
			}
			st, err := unbindState(ctx, q, rules, userID, now)
			if err != nil {
				return err
			}
			if !st.Next.IsZero() {
				return ErrUnbindCooldown
			}
		}
		if dev.Hwid == "" {
			fresh, err := q.TakeFreeSlot(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoSlots
			}
			if err != nil {
				return err
			}
			if err := q.SetUserSlot(ctx, db.SetUserSlotParams{SlotID: sql.NullInt64{Int64: fresh.ID, Valid: true}, UpdatedAt: now.Unix(), ID: userID}); err != nil {
				return err
			}
		}
		if err := q.BurnSlot(ctx, db.BurnSlotParams{BurnedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: dev.SlotID}); err != nil {
			return err
		}
		if err := q.DeleteBoundDevice(ctx, dev.ID); err != nil {
			return err
		}
		if !bySubscriber {
			return nil
		}
		if err := q.AddUnbind(ctx, db.AddUnbindParams{UserID: userID, At: now.Unix()}); err != nil {
			return err
		}
		return holdUnbound(ctx, q, rules, userID, dev, now)
	})
}

// Rename gives a bound device of userID its own name; "" goes back to the name its app
// reports. A device of another user is ErrNotFound. The name is checked by
// CleanDeviceName: the subscriber may set it too.
func (d *Devices) Rename(ctx context.Context, userID, deviceID int64, name string) (string, error) {
	name, err := CleanDeviceName(name)
	if err != nil {
		return "", err
	}
	n, err := d.st.Q.SetBoundDeviceName(ctx, db.SetBoundDeviceNameParams{Name: name, ID: deviceID, UserID: userID})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrNotFound
	}
	return name, nil
}

// DeviceLabel is what to call a bound device in a list: its own name, else what its app
// reported (model, system, the app). "" for a device that reported nothing.
func DeviceLabel(dev db.BoundDevice) string {
	switch {
	case dev.Name != "":
		return dev.Name
	case dev.Model != "":
		return dev.Model
	case dev.Os != "":
		return strings.TrimSpace(dev.Os + " " + dev.OsVersion)
	}
	app, _, _ := strings.Cut(strings.TrimSpace(dev.App), " ")
	return strings.Replace(app, "/", " ", 1)
}

// Ban unbinds a device of userID and keeps it from binding to that user again by its id:
// its keys burn at once, and its next fetch gets the notice instead of keys. Only a device
// with an id can be banned (ErrBanShared): the shared place is anyone's app without one.
// adminID 0 is not an admin of the panel (a script's key).
func (d *Devices) Ban(ctx context.Context, userID, deviceID, adminID int64) (db.DeviceBan, error) {
	var ban db.DeviceBan
	now := d.now().Unix()
	err := d.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetUser(ctx, userID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		dev, err := q.GetBoundDeviceByID(ctx, db.GetBoundDeviceByIDParams{ID: deviceID, UserID: userID})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if dev.Hwid == "" {
			return ErrBanShared
		}
		ban, err = q.CreateDeviceBan(ctx, db.CreateDeviceBanParams{UserID: userID, Hwid: dev.Hwid, Label: DeviceLabel(dev),
			AdminID: sql.NullInt64{Int64: adminID, Valid: adminID > 0}, BannedAt: now})
		if err != nil {
			return err
		}
		if err := q.BurnSlot(ctx, db.BurnSlotParams{BurnedAt: sql.NullInt64{Int64: now, Valid: true}, ID: dev.SlotID}); err != nil {
			return err
		}
		return q.DeleteBoundDevice(ctx, dev.ID)
	})
	if err == nil {
		d.changes.PoliciesChanged() // the burnt slot leaves the nodes
	}
	return ban, err
}

// Unban lets a banned device of userID bind again: on its next fetch it takes a free
// place like a new device. A ban of another user is ErrNotFound.
func (d *Devices) Unban(ctx context.Context, userID, banID int64) (db.DeviceBan, error) {
	ban, err := d.st.Q.DeleteDeviceBan(ctx, db.DeleteDeviceBanParams{ID: banID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return ban, ErrNotFound
	}
	return ban, err
}

// Banned tells whether the admin banned the device with this id from userID; an id the
// apps would not send is never banned. For fetches without binding, which bind nothing:
// the pause after the subscriber's own unbind is about places, and there are none then.
func (d *Devices) Banned(ctx context.Context, userID int64, hwid string) (bool, error) {
	if !ValidHWID(hwid) {
		return false, nil
	}
	until, err := d.st.Q.ActiveDeviceBan(ctx, db.ActiveDeviceBanParams{UserID: userID, Hwid: hwid, Until: sql.NullInt64{Int64: d.now().Unix(), Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && !until.Valid, err
}

// ForgetIdle forgets the devices not seen for DeviceIdle and burns their keys: nobody
// frees the places of devices that were sold, lost or reinstalled, and each took a slot of
// the pool. It returns how many it forgot.
func (d *Devices) ForgetIdle(ctx context.Context) (int, error) {
	now := d.now()
	n := 0
	err := d.st.Tx(ctx, func(q *db.Queries) error {
		n = 0
		devs, err := q.ListIdleBoundDevices(ctx, now.Add(-DeviceIdle).Unix())
		if err != nil {
			return err
		}
		for _, dev := range devs {
			// The shared place is the user's own slot: only the record goes.
			if dev.Hwid != "" {
				if err := q.BurnSlot(ctx, db.BurnSlotParams{BurnedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: dev.SlotID}); err != nil {
					return err
				}
			}
			if err := q.DeleteBoundDevice(ctx, dev.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err == nil && n > 0 {
		d.changes.PoliciesChanged()
	}
	return n, err
}

// burnDevices burns the slots of a user's devices with ids and forgets all devices:
// the user is reissued or deleted. The shared place uses the user's own slot, which the
// caller handles.
func burnDevices(ctx context.Context, q *db.Queries, userID, now int64) error {
	devs, err := q.ListBoundDevices(ctx, userID)
	if err != nil {
		return err
	}
	for _, dev := range devs {
		if dev.Hwid == "" {
			continue
		}
		if err := q.BurnSlot(ctx, db.BurnSlotParams{BurnedAt: sql.NullInt64{Int64: now, Valid: true}, ID: dev.SlotID}); err != nil {
			return err
		}
	}
	return q.DeleteBoundDevicesOf(ctx, userID)
}
