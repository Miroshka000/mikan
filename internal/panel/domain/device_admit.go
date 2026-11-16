package domain

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"mikan/internal/panel/store/db"
)

// What a device fetching a subscription gets (admit): Bind does it, Admit only tells it, for
// the admin panel's preview of a subscription, where no device takes a place, no slot is
// handed out and nothing is written.
const (
	AdmitKnown    = "known"    // a device bound already: it keeps its place
	AdmitNew      = "new"      // a new device: it takes a free place of its own
	AdmitShared   = "shared"   // an app that sends no device id: the subscription's shared place
	AdmitInactive = "inactive" // the subscription cannot connect: the user's own keys, no place
)

// admission is what admit decided, with the device bound already (AdmitKnown).
type admission struct {
	verdict string
	dev     db.BoundDevice
}

// admit decides what the device of hwid ("" for an app that sends no valid id) gets of u's
// subscription at now, read on q: one of Admit*, or why it gets no keys (ErrDeviceBanned,
// *UnboundError, ErrDeviceLimit). Bind runs it on the transaction that then does it.
func admit(ctx context.Context, q *db.Queries, u db.User, hwid string, now int64) (admission, error) {
	dev, err := q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: u.ID, Hwid: hwid})
	if err == nil {
		return admission{verdict: AdmitKnown, dev: dev}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return admission{}, err
	}
	// A banned device gets nothing, whatever the user's state: banning unbound it, and it
	// must not take a place again. So does one its subscriber just unbound, until its pause
	// ends. Only a new device is asked: both remove the device's row in the same transaction.
	if hwid != "" {
		until, err := q.ActiveDeviceBan(ctx, db.ActiveDeviceBanParams{UserID: u.ID, Hwid: hwid, Until: sql.NullInt64{Int64: now, Valid: true}})
		switch {
		case err == nil && until.Valid:
			return admission{}, &UnboundError{Until: time.Unix(until.Int64, 0).UTC()}
		case err == nil:
			return admission{}, ErrDeviceBanned
		case !errors.Is(err, sql.ErrNoRows):
			return admission{}, err
		}
	}
	// A user who cannot connect (turned off, term over) registers no new device and takes no
	// slot: the link itself is enough to ask, and it may be in anyone's hands. The answer is
	// the user's own keys, which the node refuses anyway.
	if u.Status == "disabled" || (u.ExpiresAt.Valid && now >= u.ExpiresAt.Int64) {
		return admission{verdict: AdmitInactive}, nil
	}
	n, err := q.CountBoundDevices(ctx, u.ID)
	if err != nil {
		return admission{}, err
	}
	// In the online mode the limit is not about places: any number of devices may bind, and
	// the nodes turn away the ones past the limit that are online at once (their addresses,
	// across all the user's slots). Only the ceiling stays.
	limit := int64(MaxDevices)
	if u.DeviceLimit.Valid && u.DeviceMode != DeviceModeOnline {
		limit = u.DeviceLimit.Int64
	}
	if n >= limit {
		return admission{}, ErrDeviceLimit
	}
	if hwid == "" {
		return admission{verdict: AdmitShared}, nil
	}
	return admission{verdict: AdmitNew}, nil
}

// Admit says what Bind would do with the device in for u now: one of Admit*, or why the
// device would get no keys, as Bind refuses it (ErrNoHWID, ErrDeviceBanned, *UnboundError,
// ErrDeviceLimit). Nothing is written, the device's last visit neither.
func (d *Devices) Admit(ctx context.Context, u db.User, in DeviceInfo, requireHWID bool) (string, error) {
	hwid := in.HWID
	if !ValidHWID(hwid) {
		hwid = ""
	}
	if hwid == "" && requireHWID {
		return "", ErrNoHWID
	}
	a, err := admit(ctx, d.st.Q, u, hwid, d.now().Unix())
	return a.verdict, err
}
