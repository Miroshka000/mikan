package api

import (
	"context"
	"errors"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A leaked API key must not decide which apps get a subscription: requiring a device ID
// would turn away every app without one. Those fields take the session.
func TestDeviceSettingsTakeTheSession(t *testing.T) {
	key := context.WithValue(context.Background(), keyAPIKey, db.ApiKey{Scope: "full"})
	on := true
	for field, set := range map[string]func(*patchSettingsInput){
		"device_binding":      func(in *patchSettingsInput) { in.Body.DeviceBinding = &on },
		"device_require_hwid": func(in *patchSettingsInput) { in.Body.RequireHWID = &on },
		"device_unbind":       func(in *patchSettingsInput) { in.Body.DeviceUnbind = &domain.UnbindRules{} },
	} {
		in := &patchSettingsInput{}
		set(in)
		_, err := (&handlers{}).updateSettings(key, in)
		var he huma.StatusError
		if !errors.As(err, &he) || he.GetStatus() != 403 {
			t.Errorf("%s through an API key: %v, want 403 session_only", field, err)
		}
	}
}
