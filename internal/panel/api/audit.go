package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// AuditEntry is a line of the admin's journal.
type AuditEntry struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	AdminID    *int64          `json:"admin_id" doc:"null — система, консоль сервера или неудачный вход"`
	Action     string          `json:"action" doc:"Например user.create, settings.update, auth.login_failed"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	IP         string          `json:"ip"`
	Details    json.RawMessage `json:"details" doc:"Подробности действия; null — нет"`
}

type auditInput struct {
	Before int64 `query:"before" minimum:"0" default:"0" doc:"Записи до этого номера; 0 — самые новые"`
	Limit  int   `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type auditOutput struct {
	Body struct {
		Items []AuditEntry `json:"items" doc:"От новых к старым"`
		Next  int64        `json:"next" doc:"Передайте как before за следующей страницей; 0 — это всё"`
	}
}

func (h *handlers) registerAudit() {
	huma.Register(h.api, huma.Operation{OperationID: "list-audit", Method: http.MethodGet, Path: "/api/v1/audit", Summary: "Журнал действий админа: хранится 180 суток",
		Tags: []string{"settings"}, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listAudit)
}

// listAudit reads the journal the panel writes; it holds the admins' addresses, so it is
// for the session only.
func (h *handlers) listAudit(ctx context.Context, in *auditInput) (*auditOutput, error) {
	before := in.Before
	if before == 0 {
		before = 1 << 62
	}
	rows, err := h.d.Store.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: before, Lim: int64(in.Limit)})
	if err != nil {
		return nil, err
	}
	out := &auditOutput{}
	out.Body.Items = make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		e := AuditEntry{ID: r.ID, At: time.Unix(r.Ts, 0).UTC(), Action: r.Action, TargetType: r.TargetType.String, TargetID: r.TargetID.String,
			IP: r.Ip.String, Details: json.RawMessage("null"), AdminID: store.Int64Ptr(r.AdminID)}
		if r.Details.Valid {
			e.Details = json.RawMessage(r.Details.String)
		}
		out.Body.Items = append(out.Body.Items, e)
	}
	if len(rows) == in.Limit {
		out.Body.Next = rows[len(rows)-1].ID
	}
	return out, nil
}
