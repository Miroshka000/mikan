package domain

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Packages is the catalog of traffic packages (GitHub issue #12): extra traffic for the
// main quota or one server group, with prices like tariffs, sold to every tariff they fit
// or to the chosen ones. Archived packages stay for the
// payments and grants that name them.
type Packages struct {
	st  *store.Store
	now func() time.Time
}

func NewPackages(st *store.Store, now func() time.Time) *Packages { return &Packages{st: st, now: now} }

// PackageInput is a package as the admin sets it.
type PackageInput struct {
	Name       string
	Bytes      int64
	PoolID     int64 // 0: the main traffic
	Lifetime   string
	Days       int64 // LifetimeDays
	PriceStars sql.NullInt64
	PriceRub   sql.NullInt64 // kopecks
	OnSale     bool
	Sort       int64
	// Tariffs are the tariffs the package is sold to; none: every tariff it fits.
	Tariffs []int64
}

const maxPackageName = 60

func (in *PackageInput) check() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > maxPackageName {
		return fieldErr("name", "name_blank")
	}
	if err := checkGrant(in.Bytes, in.Lifetime, in.Days); err != nil {
		return err
	}
	if in.Lifetime != LifetimeDays {
		in.Days = 0
	}
	// A package costs what a tariff may cost.
	if err := CheckPrices(in.PriceStars, in.PriceRub); err != nil {
		return err
	}
	if in.OnSale && !in.PriceStars.Valid && !in.PriceRub.Valid {
		return fieldErr("on_sale", "on_sale_no_price")
	}
	return nil
}

func (s *Packages) Create(ctx context.Context, in PackageInput) (db.TrafficPackage, error) {
	if err := in.check(); err != nil {
		return db.TrafficPackage{}, err
	}
	var p db.TrafficPackage
	// No invariant across rows (a pool deleted after the check fails on its foreign key):
	// READ COMMITTED.
	err := s.st.TxRC(ctx, func(q *db.Queries) error {
		if err := PoolExists(ctx, q, in.PoolID); err != nil {
			return err
		}
		var err error
		p, err = q.CreateTrafficPackage(ctx, db.CreateTrafficPackageParams{Name: in.Name, Bytes: in.Bytes, PoolID: poolRef(in.PoolID),
			Lifetime: in.Lifetime, Days: in.Days, PriceStars: in.PriceStars, PriceRub: in.PriceRub, OnSale: Flag(in.OnSale), Sort: in.Sort,
			CreatedAt: s.now().Unix()})
		if err != nil {
			return err
		}
		return setPackageTariffs(ctx, q, p.ID, in.Tariffs)
	})
	return p, err
}

// Update changes a package. Grants already given keep what they were given.
func (s *Packages) Update(ctx context.Context, id int64, in PackageInput) (db.TrafficPackage, error) {
	if err := in.check(); err != nil {
		return db.TrafficPackage{}, err
	}
	var p db.TrafficPackage
	err := s.st.TxRC(ctx, func(q *db.Queries) error {
		if err := PoolExists(ctx, q, in.PoolID); err != nil {
			return err
		}
		var err error
		p, err = q.UpdateTrafficPackage(ctx, db.UpdateTrafficPackageParams{Name: in.Name, Bytes: in.Bytes, PoolID: poolRef(in.PoolID),
			Lifetime: in.Lifetime, Days: in.Days, PriceStars: in.PriceStars, PriceRub: in.PriceRub, OnSale: Flag(in.OnSale), Sort: in.Sort, ID: id})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := setPackageTariffs(ctx, q, p.ID, in.Tariffs); err != nil {
			return err
		}
		// A tariff that gives it must still give a quota it adds to: a package moved to a
		// group the tariff does not give would be promised and never granted.
		return recheckGiftsOf(ctx, q, p.ID)
	})
	return p, err
}

// recheckGiftsOf checks the gifts of every tariff that gives package id (RecheckTariffGifts);
// one that no longer fits is refused as package_gift_not_fit.
func recheckGiftsOf(ctx context.Context, q *db.Queries, id int64) error {
	gifts, err := q.ListAllTariffGifts(ctx)
	if err != nil {
		return err
	}
	for _, g := range gifts {
		if g.PackageID != id {
			continue
		}
		t, err := q.GetTariff(ctx, g.TariffID)
		if err != nil {
			return err
		}
		var fe *FieldError
		if err := RecheckTariffGifts(ctx, q, t); errors.As(err, &fe) && fe.Code == "gift_not_fit" {
			return fieldErr("pool_id", "package_gift_not_fit")
		} else if err != nil {
			return err
		}
	}
	return nil
}

// setPackageTariffs writes the tariffs a package is sold to on q's transaction.
func setPackageTariffs(ctx context.Context, q *db.Queries, packageID int64, tariffs []int64) error {
	if err := q.ClearPackageTariffs(ctx, packageID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range tariffs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := q.GetTariff(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return fieldErr("tariffs", "tariff_not_found")
		} else if err != nil {
			return err
		}
		if err := q.AddPackageTariff(ctx, db.AddPackageTariffParams{PackageID: packageID, TariffID: id}); err != nil {
			return err
		}
	}
	return nil
}

// Archive takes a package off the catalog, and off the tariffs that gave it as a gift;
// invoices already paid for it still apply.
func (s *Packages) Archive(ctx context.Context, id int64) error {
	return s.st.TxRC(ctx, func(q *db.Queries) error {
		n, err := q.ArchiveTrafficPackage(ctx, id)
		if err == nil && n == 0 {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return q.DropPackageGifts(ctx, id)
	})
}

// PackageFits: the package adds to a quota the user has, and is sold to the user's tariff
// (tariffs, the package's own list; none: every tariff). Unlimited traffic never uses
// grants, so nothing is offered for it: a package for the main traffic needs a limited main
// traffic, one for a server group the group's own limit, and a group the user reaches
// (reach, UserGroups): traffic of a group none of whose servers the user may connect to is
// nothing to spend. A group in the main traffic takes the main traffic's packages, and a
// closed group sells nothing.
func PackageFits(u db.User, pools []db.UserPool, p db.TrafficPackage, tariffs []int64, reach map[int64]bool) bool {
	if len(tariffs) > 0 && (!u.TariffID.Valid || !slices.Contains(tariffs, u.TariffID.Int64)) {
		return false
	}
	if !p.PoolID.Valid {
		return u.TrafficLimit.Valid
	}
	_, mode := UserPoolOf(pools, p.PoolID.Int64)
	return mode == PoolOwn && reach[p.PoolID.Int64]
}

// PackageForTariff: the package adds to a quota tariff t gives (its rows of the groups in
// tariffPools, of a group its servers serve: serves, TariffGroups), whoever buys it; the
// tariffs it is sold to are not looked at.
func PackageForTariff(t db.Tariff, tariffPools []db.TariffPool, p db.TrafficPackage, serves map[int64]bool) bool {
	if !p.PoolID.Valid {
		return t.TrafficLimit.Valid
	}
	mode, _ := TariffPoolMode(tariffPools, p.PoolID.Int64)
	return mode == PoolOwn && serves[p.PoolID.Int64]
}

// PackagePoolClosed: the package is for a server group that is closed for the user.
func PackagePoolClosed(pools []db.UserPool, p db.TrafficPackage) bool {
	if !p.PoolID.Valid {
		return false
	}
	_, mode := UserPoolOf(pools, p.PoolID.Int64)
	return mode == PoolClosed
}

// PackageTariffs are the tariffs each package is sold to, by package id.
func PackageTariffs(ctx context.Context, q *db.Queries) (map[int64][]int64, error) {
	rows, err := q.ListPackageTariffs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	for _, r := range rows {
		out[r.PackageID] = append(out[r.PackageID], r.TariffID)
	}
	return out, nil
}

// PackagesFor are the packages on sale that fit the user, in the catalog's order. A
// subscription that cannot connect (off, or its term over) is sold none: what it would buy
// could not be used, and a package for a term runs out meanwhile.
func PackagesFor(ctx context.Context, q *db.Queries, userID int64, now time.Time) ([]db.TrafficPackage, error) {
	u, err := q.GetUser(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if u.Status != "active" || u.ExpiresAt.Valid && u.ExpiresAt.Int64 <= now.Unix() {
		return []db.TrafficPackage{}, nil
	}
	pools, err := q.ListUserPools(ctx, userID)
	if err != nil {
		return nil, err
	}
	reach, err := UserGroups(ctx, q, u)
	if err != nil {
		return nil, err
	}
	all, err := q.ListTrafficPackagesOnSale(ctx)
	if err != nil {
		return nil, err
	}
	sold, err := PackageTariffs(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []db.TrafficPackage{}
	for _, p := range all {
		if PackageFits(u, pools, p, sold[p.ID], reach) {
			out = append(out, p)
		}
	}
	return out, nil
}

func poolRef(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

// Flag is a bool as the database keeps it: 1 or 0.
func Flag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// NullInt is an optional number as the database keeps it: NULL for nil.
func NullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// NullBool is an optional yes or no as the database keeps it: NULL for nil.
func NullBool(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}
