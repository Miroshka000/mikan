package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbackup"
)

// Backups of the database to the admin's Telegram chat (tgbackup). Everything here is the
// session's: a backup holds every secret of the panel, and its password opens it.

type BackupView struct {
	Enabled      bool       `json:"enabled" doc:"Слать базу в чат администратора раз в сутки"`
	Hour         int        `json:"hour" doc:"Час отправки (UTC)"`
	PasswordSet  bool       `json:"password_set" doc:"Пароль шифрования задан; сам пароль не возвращается"`
	AdminChatSet bool       `json:"admin_chat_set" doc:"Чат администратора подключён к боту (вкладка «Инфраструктура»)"`
	LastOK       *time.Time `json:"last_ok,omitempty" doc:"Когда ушёл последний бэкап"`
	LastTry      *time.Time `json:"last_try,omitempty"`
	LastError    string     `json:"last_error,omitempty" doc:"Код ошибки последней попытки"`
	LastSize     int64      `json:"last_size,omitempty" doc:"Размер последнего файла, байт"`
	Sending      bool       `json:"sending" doc:"Бэкап делается прямо сейчас"`
}

type backupOutput struct{ Body BackupView }

type patchBackupInput struct {
	Body struct {
		Enabled  *bool   `json:"enabled,omitempty"`
		Hour     *int    `json:"hour,omitempty" minimum:"0" maximum:"23"`
		Password *string `json:"password,omitempty" maxLength:"256" doc:"Пароль, которым шифруется файл: от 20 символов. Файл остаётся в истории чата навсегда; без пароля его не открыть"`
	}
}

func (h *handlers) registerBackups() {
	tags := []string{"telegram"}
	huma.Register(h.api, huma.Operation{OperationID: "get-telegram-backup", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodGet, Path: "/api/v1/telegram/backup", Summary: "Бэкапы в Telegram", Tags: tags}, h.getBackup)
	huma.Register(h.api, huma.Operation{OperationID: "update-telegram-backup", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/telegram/backup", Summary: "Настроить бэкапы в Telegram", Tags: tags}, h.updateBackup)
	huma.Register(h.api, huma.Operation{OperationID: "send-telegram-backup", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPost, Path: "/api/v1/telegram/backup/send", Summary: "Отправить бэкап сейчас", Description: "Бэкап делается в фоне: ответ 202 сразу, итог — в GET /api/v1/telegram/backup (sending, last_ok, last_error).", Tags: tags, DefaultStatus: http.StatusAccepted}, h.sendBackup)
}

func (h *handlers) backupView(ctx context.Context) (BackupView, error) {
	var v BackupView
	var err error
	if v.Enabled, err = h.d.Settings.On(ctx, tgbackup.Enabled); err != nil {
		return v, err
	}
	hour, ok, err := settings.Get[int](ctx, h.d.Settings, tgbackup.KeyHour)
	if err != nil {
		return v, err
	}
	v.Hour = tgbackup.DefaultHour
	if ok {
		v.Hour = hour
	}
	pw, err := h.d.Settings.String(ctx, tgbackup.KeyPassword)
	if err != nil {
		return v, err
	}
	v.PasswordSet = len(pw) >= tgbackup.MinPassword
	if h.d.Gateway != nil {
		_, v.AdminChatSet, _ = h.d.Gateway.InfrastructureAdminChat(ctx)
	}
	st, _, err := settings.Get[tgbackup.State](ctx, h.d.Settings, tgbackup.KeyState)
	if err != nil {
		return v, err
	}
	at := func(unix int64) *time.Time {
		if unix == 0 {
			return nil
		}
		t := time.Unix(unix, 0).UTC()
		return &t
	}
	v.LastOK, v.LastTry, v.LastError, v.LastSize = at(st.LastOK), at(st.LastTry), st.LastError, st.LastSize
	v.Sending = h.d.Backups != nil && h.d.Backups.Busy()
	return v, nil
}

func (h *handlers) getBackup(ctx context.Context, _ *struct{}) (*backupOutput, error) {
	v, err := h.backupView(ctx)
	if err != nil {
		return nil, err
	}
	return &backupOutput{Body: v}, nil
}

func (h *handlers) updateBackup(ctx context.Context, in *patchBackupInput) (*backupOutput, error) {
	b := in.Body
	wasOn, err := h.d.Settings.On(ctx, tgbackup.Enabled)
	if err != nil {
		return nil, err
	}
	if b.Password != nil && len(*b.Password) < tgbackup.MinPassword {
		return nil, fieldErr("body.password", "backup_password_short")
	}
	if b.Enabled != nil && *b.Enabled && b.Password == nil {
		pw, err := h.d.Settings.String(ctx, tgbackup.KeyPassword)
		if err != nil {
			return nil, err
		}
		if len(pw) < tgbackup.MinPassword {
			return nil, fieldErr("body.password", "no_backup_password")
		}
	}
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		if b.Password != nil {
			if err := settings.Set(ctx, set, tgbackup.KeyPassword, *b.Password); err != nil {
				return err
			}
		}
		if b.Hour != nil {
			if err := settings.Set(ctx, set, tgbackup.KeyHour, *b.Hour); err != nil {
				return err
			}
		}
		if b.Enabled != nil {
			return settings.Set(ctx, set, tgbackup.KeyEnabled, *b.Enabled)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Switched on now: the first backup goes at the chosen hour, not within a minute.
	if b.Enabled != nil && *b.Enabled && !wasOn && h.d.Backups != nil {
		if err := h.d.Backups.Enabling(ctx); err != nil {
			return nil, err
		}
	}
	details := map[string]any{}
	if b.Enabled != nil {
		details["enabled"] = *b.Enabled
	}
	if b.Hour != nil {
		details["hour"] = *b.Hour
	}
	if b.Password != nil {
		details["password"] = "changed"
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.backup.update", "telegram", "", details)
	return h.getBackup(ctx, nil)
}

func (h *handlers) sendBackup(ctx context.Context, _ *struct{}) (*backupOutput, error) {
	if h.d.Backups == nil {
		return nil, huma.Error503ServiceUnavailable("bot_unavailable")
	}
	// What is known at once is said at once; the dump and the upload go on in the background.
	v, err := h.backupView(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case !v.PasswordSet:
		return nil, huma.Error409Conflict(tgbackup.ErrNoPassword.Error())
	case !v.AdminChatSet:
		return nil, huma.Error409Conflict(tgbackup.ErrNoChat.Error())
	}
	if err := h.d.Backups.Start(); errors.Is(err, tgbackup.ErrBusy) {
		return nil, huma.Error409Conflict(err.Error())
	} else if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "telegram.backup.send", "telegram", "", nil)
	v.Sending = true
	return &backupOutput{Body: v}, nil
}
