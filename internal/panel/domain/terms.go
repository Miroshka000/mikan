package domain

import (
	"context"
	"database/sql"

	"mikan/internal/panel/store/db"
)

// MaxTerms caps the terms one tariff is sold for.
const MaxTerms = 12

// Term is one way a tariff is sold: for so many days (0: no end date; with a billing day,
// months of 30 days each) at these prices.
type Term struct {
	Days       int64
	PriceStars sql.NullInt64 // not for Stars when invalid
	PriceRub   sql.NullInt64 // kopecks; not for rubles when invalid
}

// TariffTerms are the terms tariff t is sold for, in order: its rows in tariff_terms, or,
// without any, the one term in the tariff itself. The first is the tariff's default term.
func TariffTerms(t db.Tariff, rows []db.TariffTerm) []Term {
	var out []Term
	for _, r := range rows {
		if r.TariffID == t.ID {
			out = append(out, Term{Days: r.Days, PriceStars: r.PriceStars, PriceRub: r.PriceRub})
		}
	}
	if len(out) == 0 {
		out = []Term{{Days: t.DurationDays, PriceStars: t.PriceStars, PriceRub: t.PriceRub}}
	}
	return out
}

// TermsOf reads the terms tariff t is sold for.
func TermsOf(ctx context.Context, q *db.Queries, t db.Tariff) ([]Term, error) {
	rows, err := q.ListTariffTerms(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return TariffTerms(t, rows), nil
}

// FindTerm is the term of so many days among terms.
func FindTerm(terms []Term, days int64) (Term, bool) {
	for _, t := range terms {
		if t.Days == days {
			return t, true
		}
	}
	return Term{}, false
}

// SetTariffTerms writes the terms of a tariff whose own columns already hold the first
// one: one term needs no rows, two or more are stored in order.
func SetTariffTerms(ctx context.Context, q *db.Queries, tariffID int64, terms []Term) error {
	if err := q.DeleteTariffTerms(ctx, tariffID); err != nil {
		return err
	}
	if len(terms) < 2 {
		return nil
	}
	for i, t := range terms {
		if err := q.AddTariffTerm(ctx, db.AddTariffTermParams{TariffID: tariffID, Days: t.Days, PriceStars: t.PriceStars, PriceRub: t.PriceRub, Sort: int64(i)}); err != nil {
			return err
		}
	}
	return nil
}

// termDays is the term bought on tariff t: the payment's, or the tariff's own.
func termDays(t db.Tariff, term sql.NullInt64) int64 {
	if term.Valid {
		return term.Int64
	}
	return t.DurationDays
}
