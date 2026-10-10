package panelimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Limits of POST /users, which an imported user meets too.
const (
	maxName        = domain.UserNameMax
	maxContact     = 100
	maxNote        = 2000
	maxDevices     = 100
	maxTerm        = 20 * 365 * 24 * time.Hour // an on-hold term past this is not a term
	maxTrafficByte = 1 << 60                   // an exabyte: past it a limit is a broken number
)

// Earliest and latest term an imported user may have: outside, the old panel's number is
// broken, not a date.
var (
	minExpire = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	maxExpire = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Normalize checks a user the old panel gave against mikan's limits: names, contacts and
// notes as POST /users takes them, numbers in range. What cannot be taken is an error, and
// the user is left out with it.
func Normalize(u User) (User, error) {
	u.Contact = strings.TrimSpace(u.Contact)
	name, err := domain.CleanUserName(u.Name)
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		switch fe.Code {
		case domain.CodeUserNameEmpty:
			return u, errors.New("no name")
		case domain.CodeUserNameLong:
			return u, fmt.Errorf("the name is longer than %d characters", maxName)
		}
		return u, errors.New("the name has control characters")
	}
	u.Name = name
	if utf8.RuneCountInString(u.Contact) > maxContact || !utf8.ValidString(u.Contact) {
		return u, fmt.Errorf("the contact is longer than %d characters", maxContact)
	}
	if utf8.RuneCountInString(u.Note) > maxNote || !utf8.ValidString(u.Note) {
		return u, fmt.Errorf("the note is longer than %d characters", maxNote)
	}
	if u.TrafficLimit < 0 || u.TrafficLimit > maxTrafficByte {
		return u, fmt.Errorf("traffic limit %d is out of range", u.TrafficLimit)
	}
	u.Used, u.Lifetime = max(0, min(u.Used, maxTrafficByte)), max(0, min(u.Lifetime, maxTrafficByte))
	u.Lifetime = max(u.Lifetime, u.Used)
	if !u.Expires.IsZero() && (u.Expires.Before(minExpire) || u.Expires.After(maxExpire)) {
		return u, fmt.Errorf("term %s is out of range", u.Expires.Format(time.DateOnly))
	}
	if u.OnHold < 0 || u.OnHold > maxTerm {
		return u, errors.New("the on-hold term is out of range")
	}
	if u.DeviceLimit < 0 {
		u.DeviceLimit = 0
	}
	u.DeviceLimit = min(u.DeviceLimit, maxDevices)
	return u, nil
}

// listCap is how many names a report or a preview lists; the rest is counted.
const listCap = 50

// List is the first names of a long list, with how many there are in all.
type List struct {
	Names []string `json:"names" doc:"Первые 50"`
	Count int      `json:"count" doc:"Сколько всего"`
}

func (l *List) add(s string) {
	l.Count++
	if len(l.Names) < listCap {
		l.Names = append(l.Names, s)
	}
}

func newList() List { return List{Names: []string{}} }

// Report is what an import did.
type Report struct {
	Created int  `json:"created"`
	Skipped List `json:"skipped" doc:"Имена, которые уже есть в mikan: эти пользователи не перенесены"`
	Links   int  `json:"links" doc:"Старые ссылки и ключи, заведённые этим импортом"`
	NoLink  List `json:"no_link" doc:"Перенесены без старой ссылки: её токен слишком короткий, чтобы быть секретом"`
	Failed  List `json:"failed" doc:"Имя и причина для тех, кого не удалось создать"`
}

func newReport() Report {
	return Report{Skipped: newList(), NoLink: newList(), Failed: newList()}
}

// Preview is what an import would do, without doing it.
type Preview struct {
	Total    int            `json:"total"`
	New      int            `json:"new" doc:"Будут созданы"`
	Taken    List           `json:"taken" doc:"Имена, которые уже есть в mikan"`
	Invalid  List           `json:"invalid" doc:"Пользователи, которых mikan не примет, с причиной"`
	NoLink   List           `json:"no_link" doc:"Будут перенесены без старой ссылки"`
	Statuses map[string]int `json:"statuses"`
	OnHold   int            `json:"on_hold" doc:"Пользователи «на паузе»: в mikan их срок пойдёт с момента импорта"`
}

// takenNames asks the database once per thousand names which of them mikan has.
func takenNames(ctx context.Context, q *db.Queries, names []string) (map[string]bool, error) {
	taken := map[string]bool{}
	for start := 0; start < len(names); start += 1000 {
		chunk := names[start:min(start+1000, len(names))]
		got, err := q.TakenUserNames(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for _, n := range got {
			taken[n] = true
		}
	}
	return taken, nil
}

// Check counts what Apply would do.
func Check(ctx context.Context, q *db.Queries, list []User) (Preview, error) {
	p := Preview{Total: len(list), Taken: newList(), Invalid: newList(), NoLink: newList(), Statuses: map[string]int{}}
	var valid []User
	var names []string
	for _, in := range list {
		u, err := Normalize(in)
		if err != nil {
			p.Invalid.add(describe(in.Name) + ": " + err.Error())
			continue
		}
		valid = append(valid, u)
		names = append(names, u.Name)
	}
	taken, err := takenNames(ctx, q, names)
	if err != nil {
		return p, err
	}
	seen := map[string]bool{}
	for _, u := range valid {
		if taken[u.Name] || seen[u.Name] {
			p.Taken.add(u.Name)
			continue
		}
		seen[u.Name] = true
		p.New++
		p.Statuses[u.Status]++
		if u.Status == StatusOnHold {
			p.OnHold++
		}
		if u.WeakToken {
			p.NoLink.add(u.Name)
		}
	}
	return p, nil
}

// errTaken: the name is in mikan already.
var errTaken = errors.New("taken")

// afterCreate runs inside a user's transaction once the user is made; tests use it to
// fail the rest of the steps.
var afterCreate func(name string) error

// Apply makes the users on the plan tariffID, with the old panel's limit, term, traffic
// used and device limit, and files the keys their old links resolve to: Remnawave's
// short UUID, or for Marzban and PasarGuard the name and id their signed tokens carry
// (Verifier). Each user is one transaction, so a failure leaves nothing half made; a
// name mikan already has is skipped, never merged into someone else. progress, when not
// nil, is told after each user. The nodes are told once, at the end.
func Apply(ctx context.Context, st *store.Store, users *domain.Users, now time.Time, kind Kind, tariffID int64, list []User, progress func(done int)) (Report, error) {
	r := newReport()
	if _, err := st.Q.GetTariff(ctx, tariffID); errors.Is(err, sql.ErrNoRows) {
		return r, domain.ErrNotFound
	} else if err != nil {
		return r, err
	}
	// Slots for everyone who will be made, at once: one refill, one word to the nodes.
	// Users mikan refuses or already has take none.
	plan, err := Check(ctx, st.Q, list)
	if err != nil {
		return r, err
	}
	if err := users.RefillFor(ctx, plan.New); err != nil {
		return r, err
	}
	created := false
	defer func() {
		if created {
			users.Changed()
		}
	}()
	for i, raw := range list {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		in, err := Normalize(raw)
		if err != nil {
			r.Failed.add(describe(raw.Name) + ": " + err.Error())
		} else {
			links, err := applyOne(ctx, st, users, now, kind, tariffID, in)
			if errors.Is(err, domain.ErrNoSlots) {
				if err = users.RefillSlots(ctx); err == nil {
					links, err = applyOne(ctx, st, users, now, kind, tariffID, in)
				}
			}
			switch {
			case errors.Is(err, errTaken):
				r.Skipped.add(in.Name)
			case err != nil:
				r.Failed.add(in.Name + ": " + failure(err))
			default:
				r.Created++
				r.Links += links
				created = true
				if in.WeakToken {
					r.NoLink.add(in.Name)
				}
			}
		}
		if progress != nil {
			progress(i + 1)
		}
	}
	return r, nil
}

func applyOne(ctx context.Context, st *store.Store, users *domain.Users, now time.Time, kind Kind, tariffID int64, in User) (links int, err error) {
	p := domain.Patch{}
	if in.TrafficLimit > 0 {
		p.TrafficLimit = &in.TrafficLimit
	} else {
		p.ClearTrafficLimit = true
	}
	switch {
	case !in.Expires.IsZero():
		p.ExpiresAt = &in.Expires
	case in.Status == StatusOnHold && in.OnHold > 0:
		// mikan has no term that waits for the first connection: it starts now.
		exp := now.Add(in.OnHold)
		p.ExpiresAt = &exp
	default:
		p.ClearExpiry = true
	}
	if in.DeviceLimit > 0 {
		p.DeviceLimit = &in.DeviceLimit
	}
	if in.Status == StatusDisabled {
		off := true
		p.Disabled = &off
	}
	var keys []string
	switch kind {
	case Remnawave:
		if in.Token != "" {
			keys = append(keys, in.Token)
		}
	case Marzban:
		keys = append(keys, "name:"+in.Name)
	case PasarGuard:
		keys = append(keys, "name:"+in.Name)
		if in.SourceID > 0 {
			keys = append(keys, "id:"+strconv.FormatInt(in.SourceID, 10))
		}
	}
	err = st.Tx(ctx, func(q *db.Queries) error {
		links = 0
		// Two imports at once (or a retry) cannot both find the name free.
		if err := q.LockUserName(ctx, in.Name); err != nil {
			return err
		}
		if taken, err := q.UserNameTaken(ctx, in.Name); err != nil {
			return err
		} else if taken {
			return errTaken
		}
		u, err := users.CreateOn(ctx, q, domain.CreateInput{Name: in.Name, Contact: in.Contact, Note: in.Note, TariffID: tariffID, Source: domain.UserFromImport}, p)
		if err != nil {
			return err
		}
		if afterCreate != nil {
			if err := afterCreate(in.Name); err != nil {
				return err
			}
		}
		if in.Used > 0 || in.Lifetime > 0 {
			if err := q.SetImportedUsage(ctx, db.SetImportedUsageParams{ID: u.ID, UsedDown: in.Used, TotalDown: in.Lifetime, UpdatedAt: now.Unix()}); err != nil {
				return fmt.Errorf("traffic: %w", err)
			}
		}
		var notBefore int64
		if !in.Created.IsZero() {
			notBefore = in.Created.Unix()
		}
		for _, k := range keys {
			n, err := q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: k, UserID: u.ID, Source: string(kind), NotBefore: notBefore})
			if err != nil {
				return fmt.Errorf("old link: %w", err)
			}
			links += int(n)
		}
		return nil
	})
	return links, err
}

// failure says why a user was not made, without the database's own text.
func failure(err error) string {
	switch {
	case errors.Is(err, domain.ErrNoSlots):
		return "no free key slot"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the import was stopped"
	}
	return "could not be saved"
}

// describe names a user in a report even when the name is the problem.
func describe(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "(no name)"
	}
	if utf8.RuneCountInString(name) > 40 {
		return string([]rune(name)[:40]) + "…"
	}
	return name
}
