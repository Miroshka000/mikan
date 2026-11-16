package subsrc

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/proto"
	"mikan/internal/sub/serve"
)

// previewKeys are the keys a preview's profile carries: never a device's or the user's own.
var previewKeys = proto.Slot{Name: "preview", UUID: "00000000-0000-0000-0000-000000000000", Secret: "preview"}

// dryKeys is Keys for a preview: what the device would be told, worked out without binding
// it (domain.Devices.Admit), and the preview's keys in place of any real ones. Without
// binding every device shares the user's keys, yet a banned one still gets none.
func (s *subscription) dryKeys(ctx context.Context, dev serve.Device) (proto.Slot, error) {
	devices, u := s.h.devices, s.u
	if devices == nil {
		return previewKeys, nil
	}
	if !s.cfg.Binding {
		banned, err := devices.Banned(ctx, u.ID, dev.HWID)
		if err != nil {
			return proto.Slot{}, err
		}
		if banned {
			return proto.Slot{}, deviceError(domain.ErrDeviceBanned)
		}
		s.admitted = domain.AdmitShared
		return previewKeys, nil
	}
	verdict, err := devices.Admit(ctx, u, domain.DeviceInfo{HWID: dev.HWID, OS: dev.OS, OSVersion: dev.OSVersion, Model: dev.Model, App: dev.App, IP: dev.IP},
		s.cfg.RequireHWID && !dev.SkipHWID)
	if err != nil {
		return proto.Slot{}, deviceError(err)
	}
	s.admitted = verdict
	return previewKeys, nil
}
