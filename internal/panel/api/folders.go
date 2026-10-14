package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Folders of the users' list: named groups the admin makes to keep users apart (family,
// friends, resellers). A user is in one folder at most. A folder only sorts the list: what
// a user may do never depends on it, and deleting a folder deletes no user.

// maxFolders keeps the row of chips above the list usable.
const maxFolders = 100

// folderColors is the palette of a folder's chip; the web draws each name from its own
// tokens, so no colour value from outside ever reaches a style.
var folderColors = []string{"gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"}

type FolderView struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink"`
	Emoji string `json:"emoji" doc:"Значок перед названием; может быть пустым"`
	Users int    `json:"users" doc:"Сколько пользователей в папке, скрытые тоже"`
}

type foldersOutput struct{ Body []FolderView }
type folderOutput struct{ Body FolderView }

type folderIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createFolderInput struct {
	Body struct {
		Name  string `json:"name" minLength:"1" maxLength:"40"`
		Color string `json:"color,omitempty" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink" doc:"Не задан — gray"`
		Emoji string `json:"emoji,omitempty" maxLength:"16"`
	}
}

type patchFolderInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name  *string `json:"name,omitempty" minLength:"1" maxLength:"40"`
		Color *string `json:"color,omitempty" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink"`
		Emoji *string `json:"emoji,omitempty" maxLength:"16" doc:"Пустая строка убирает значок"`
	}
}

type orderFoldersInput struct {
	Body struct {
		IDs []int64 `json:"ids" minItems:"1" maxItems:"1000" doc:"Все папки, каждая один раз, в том порядке, в каком их показывает список"`
	}
}

func (h *handlers) registerFolders() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "list-folders", Method: http.MethodGet, Path: "/api/v1/folders", Summary: "Папки пользователей", Tags: tags}, h.listFolders)
	huma.Register(h.api, huma.Operation{OperationID: "create-folder", Method: http.MethodPost, Path: "/api/v1/folders", Summary: "Создать папку", Tags: tags, DefaultStatus: http.StatusCreated}, h.createFolder)
	huma.Register(h.api, huma.Operation{OperationID: "update-folder", Method: http.MethodPatch, Path: "/api/v1/folders/{id}", Summary: "Изменить папку", Tags: tags}, h.updateFolder)
	huma.Register(h.api, huma.Operation{OperationID: "order-folders", Method: http.MethodPut, Path: "/api/v1/folders/order", Summary: "Порядок папок", Tags: tags, DefaultStatus: http.StatusNoContent}, h.orderFolders)
	huma.Register(h.api, huma.Operation{OperationID: "delete-folder", Method: http.MethodDelete, Path: "/api/v1/folders/{id}", Summary: "Удалить папку: её пользователи остаются, но вне папок", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteFolder)
}

func folderView(f db.UserFolder, users int) FolderView {
	return FolderView{ID: f.ID, Name: f.Name, Color: f.Color, Emoji: f.Emoji, Users: users}
}

// folderUsers is how many users each folder holds.
func (h *handlers) folderUsers(ctx context.Context) (map[int64]int, error) {
	rows, err := h.d.Store.Q.CountFolderUsers(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]int, len(rows))
	for _, r := range rows {
		m[r.FolderID.Int64] = int(r.N)
	}
	return m, nil
}

func (h *handlers) listFolders(ctx context.Context, _ *struct{}) (*foldersOutput, error) {
	fs, err := h.d.Store.Q.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	n, err := h.folderUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := &foldersOutput{Body: make([]FolderView, 0, len(fs))}
	for _, f := range fs {
		out.Body = append(out.Body, folderView(f, n[f.ID]))
	}
	return out, nil
}

// folderName trims a name and refuses an empty one.
func folderName(s string) (string, error) {
	name := strings.TrimSpace(s)
	if name == "" {
		return "", fieldErr("body.name", "name_blank")
	}
	return name, nil
}

func folderColor(s string) (string, error) {
	if s == "" {
		return "gray", nil
	}
	if !slices.Contains(folderColors, s) {
		return "", fieldErrValue("body.color", "bad_color", s)
	}
	return s, nil
}

// folderNameTaken: two folders may not differ by case alone, which the database's unique
// index does not see; self is the folder being renamed.
func folderNameTaken(all []db.UserFolder, name string, self int64) bool {
	return slices.ContainsFunc(all, func(f db.UserFolder) bool { return f.ID != self && strings.EqualFold(f.Name, name) })
}

func nameTakenError() error {
	return huma.Error409Conflict("folder_name_taken", &huma.ErrorDetail{Location: "body.name", Message: "folder_name_taken"})
}

func (h *handlers) createFolder(ctx context.Context, in *createFolderInput) (*folderOutput, error) {
	name, err := folderName(in.Body.Name)
	if err != nil {
		return nil, err
	}
	color, err := folderColor(in.Body.Color)
	if err != nil {
		return nil, err
	}
	var f db.UserFolder
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListFolders(ctx)
		if err != nil {
			return err
		}
		if len(all) >= maxFolders {
			return huma.Error409Conflict("folder_limit")
		}
		if folderNameTaken(all, name, 0) {
			return nameTakenError()
		}
		f, err = q.CreateFolder(ctx, db.CreateFolderParams{Name: name, Color: color, Emoji: strings.TrimSpace(in.Body.Emoji), CreatedAt: h.d.Now().Unix()})
		return err
	})
	if store.IsUnique(err) {
		err = nameTakenError()
	}
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "folder.create", "folder", strconv.FormatInt(f.ID, 10), map[string]any{"name": name})
	return &folderOutput{Body: folderView(f, 0)}, nil
}

func (h *handlers) updateFolder(ctx context.Context, in *patchFolderInput) (*folderOutput, error) {
	var f db.UserFolder
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		var err error
		if f, err = q.GetFolder(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		} else if err != nil {
			return err
		}
		b := in.Body
		if b.Name != nil {
			if f.Name, err = folderName(*b.Name); err != nil {
				return err
			}
			all, err := q.ListFolders(ctx)
			if err != nil {
				return err
			}
			if folderNameTaken(all, f.Name, f.ID) {
				return nameTakenError()
			}
		}
		if b.Color != nil {
			if f.Color, err = folderColor(*b.Color); err != nil {
				return err
			}
		}
		if b.Emoji != nil {
			f.Emoji = strings.TrimSpace(*b.Emoji)
		}
		f, err = q.UpdateFolder(ctx, db.UpdateFolderParams{Name: f.Name, Color: f.Color, Emoji: f.Emoji, ID: f.ID})
		return err
	})
	if store.IsUnique(err) {
		err = nameTakenError()
	}
	if err != nil {
		return nil, err
	}
	n, err := h.folderUsers(ctx)
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "folder.update", "folder", strconv.FormatInt(f.ID, 10), nil)
	return &folderOutput{Body: folderView(f, n[f.ID])}, nil
}

// orderFolders sets the order of the chips: the list must be every folder once, so a
// stale page of the admin cannot silently drop or duplicate one.
func (h *handlers) orderFolders(ctx context.Context, in *orderFoldersInput) (*struct{}, error) {
	ids := in.Body.IDs
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListFolders(ctx)
		if err != nil {
			return err
		}
		have := make(map[int64]bool, len(all))
		for _, f := range all {
			have[f.ID] = true
		}
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if !have[id] || seen[id] {
				return fieldErrValue("body.ids", "folder_order_mismatch", id)
			}
			seen[id] = true
		}
		if len(ids) != len(all) {
			return fieldErr("body.ids", "folder_order_mismatch")
		}
		for i, id := range ids {
			if err := q.SetFolderSort(ctx, db.SetFolderSortParams{Sort: int64(i + 1), ID: id}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "folder.order", "folder", "", map[string]any{"ids": ids})
	return nil, nil
}

func (h *handlers) deleteFolder(ctx context.Context, in *folderIDInput) (*struct{}, error) {
	var freed int
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		// The users are counted for the journal; the foreign key lets them go.
		rows, err := q.CountFolderUsers(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.FolderID.Int64 == in.ID {
				freed = int(r.N)
			}
		}
		if _, err := q.LockFolderUsers(ctx, sql.NullInt64{Int64: in.ID, Valid: true}); err != nil {
			return err
		}
		n, err := q.DeleteFolder(ctx, in.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return huma.Error404NotFound("not_found")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "folder.delete", "folder", strconv.FormatInt(in.ID, 10), map[string]any{"users": freed})
	return nil, nil
}
